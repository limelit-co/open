// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package prnow is a client for the PRNow press release API
// (https://prnow.io/api-docs): save a draft, submit a release for
// distribution, follow it to its live placements, and retract it while it is
// still pending.
//
// It is not an answer-engine provider and is deliberately not in the provider
// registry: it measures nothing. It is the step after the numbers, publishing
// the news article that gives engines something new to cite.
//
// Submitting spends PRNow credits, so every caller in this repository treats
// it the way the run tools treat a pass: check and show first, spend only on
// an explicit second step. Every submit carries an Idempotency-Key, so a
// retried request is charged once.
package prnow

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// KeyEnv is the credential name: the environment variable that wins, and
	// the name the key is sealed under in the settings store.
	KeyEnv = "PRNOW_API_KEY"
	// DefaultBaseURL is PRNow's API root.
	DefaultBaseURL = "https://prnow.io/api/v1"
	// DocsURL is where a user reads the API and finds where keys are made.
	DocsURL = "https://prnow.io/api-docs"
)

// Sentinel errors a caller can branch on with errors.Is. Every *APIError
// matches the one for its status code.
var (
	ErrAuth      = errors.New("prnow: the API key was not accepted")
	ErrCredits   = errors.New("prnow: not enough credits")
	ErrNotFound  = errors.New("prnow: no such release on this account")
	ErrConflict  = errors.New("prnow: conflict")
	ErrIdemReuse = errors.New("prnow: idempotency key reused with a different body")
)

// Conflict codes PRNow documents on a 409.
const (
	CodeDuplicate          = "same_plan_duplicate"
	CodeInProgress         = "idempotency_request_in_progress"
	CodeInterrupted        = "idempotency_request_interrupted"
	CodeIdempotencyReused  = "idempotency_key_reused"
	headerIdempotencyKey   = "Idempotency-Key"
	headerIdempotentReplay = "Idempotent-Replayed"
)

// APIError is a non-2xx answer from PRNow.
type APIError struct {
	StatusCode int
	Code       string // the 409/422 code, when PRNow sent one
	Message    string
	// RetryAfter is set on idempotency_request_in_progress.
	RetryAfter time.Duration
	// Existing is the release a same_plan_duplicate points at.
	Existing *Release
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	if e.Code != "" {
		return fmt.Sprintf("prnow: %d %s: %s", e.StatusCode, e.Code, msg)
	}
	return fmt.Sprintf("prnow: %d: %s", e.StatusCode, msg)
}

// Is maps the documented status codes onto the sentinels.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrAuth:
		return e.StatusCode == http.StatusUnauthorized
	case ErrCredits:
		return e.StatusCode == http.StatusPaymentRequired
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrConflict:
		return e.StatusCode == http.StatusConflict
	case ErrIdemReuse:
		return e.StatusCode == http.StatusUnprocessableEntity || e.Code == CodeIdempotencyReused
	}
	return false
}

// Client talks to one PRNow account.
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

// New returns a client for key against the production API.
func New(key string) *Client {
	return &Client{BaseURL: DefaultBaseURL, Key: strings.TrimSpace(key), HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// Account is GET /test: who the key belongs to and what it can spend.
type Account struct {
	Message string `json:"message"`
	Account struct {
		ID      string `json:"id"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Company string `json:"company"`
	} `json:"account"`
	// Credits is the total across wallets.
	Credits float64 `json:"credits"`
	// MaxSingleWalletCredits is the largest order one wallet can pay for.
	MaxSingleWalletCredits float64 `json:"maxSingleWalletCredits"`
}

// Draft is a press release as PRNow takes it on /draft and /submit. Content
// is HTML. Validate checks the documented limits before anything is sent.
type Draft struct {
	Title           string   `json:"title"`
	Summary         string   `json:"summary,omitempty"`
	Content         string   `json:"content"`
	Categories      []string `json:"categories"`
	Country         string   `json:"country"`
	City            string   `json:"city"`
	Plan            string   `json:"plan,omitempty"`
	MetaTitle       string   `json:"metaTitle,omitempty"`
	MetaDescription string   `json:"metaDescription,omitempty"`
	MetaKeywords    string   `json:"metaKeywords,omitempty"`
}

// Release is a release as PRNow reports it. Only the documented fields are
// decoded; Raw keeps the whole object for anything else.
type Release struct {
	ID             string  `json:"id"`
	PressReleaseID string  `json:"pressReleaseId"`
	SlugID         string  `json:"slugId"`
	ReleaseID      string  `json:"release_id"` // the short submit example's name for it
	Title          string  `json:"title"`
	Status         string  `json:"status"`
	Plan           string  `json:"plan"`
	CreditsUsed    float64 `json:"creditsUsed"`
	CreatedAt      string  `json:"createdAt"`
	PublishedAt    string  `json:"publishedAt"`
	// Packages is per outlet: live with a URL, held or rejected with a
	// reason, or absent while still pending.
	Packages []Package      `json:"-"`
	Raw      map[string]any `json:"-"`
	// Replayed is true when PRNow answered from its idempotency record: the
	// release already existed and nothing new was charged.
	Replayed bool `json:"-"`
}

// Ref is the identifier to pass back to Status or Retract: the short slug
// when PRNow gave one, otherwise the UUID.
func (r Release) Ref() string {
	for _, v := range []string{r.SlugID, r.PressReleaseID, r.ID, r.ReleaseID} {
		if v != "" {
			return v
		}
	}
	return ""
}

// Package is one outlet's state for a release.
type Package struct {
	Outlet          string  `json:"outlet"`
	State           string  `json:"state"`
	LiveURL         string  `json:"liveUrl"`
	Reason          string  `json:"reason"`
	CreditsRefunded float64 `json:"creditsRefunded"`
}

// Retraction is POST /retract/{id}.
type Retraction struct {
	Release
	CreditsRefunded float64 `json:"creditsRefunded"`
	Message         string  `json:"message"`
}

// Test checks the key and returns the account and its credits.
func (c *Client) Test(ctx context.Context) (Account, error) {
	var a Account
	_, err := c.do(ctx, http.MethodGet, "/test", nil, "", &a)
	return a, err
}

// Categories lists the values Draft.Categories accepts (case-sensitive).
func (c *Client) Categories(ctx context.Context) ([]string, error) {
	var raw json.RawMessage
	if _, err := c.do(ctx, http.MethodGet, "/categories", nil, "", &raw); err != nil {
		return nil, err
	}
	return names(raw)
}

// SaveDraft stores d as a draft on the account. Nothing is submitted and
// nothing is charged.
func (c *Client) SaveDraft(ctx context.Context, d Draft, idempotencyKey string) (Release, error) {
	return c.post(ctx, "/draft", d, idempotencyKey)
}

// Submit sends d for review and distribution. This spends credits. An empty
// idempotencyKey gets a fresh one; pass the same key to retry safely.
func (c *Client) Submit(ctx context.Context, d Draft, idempotencyKey string) (Release, error) {
	return c.post(ctx, "/submit", d, idempotencyKey)
}

// Status reports a release by its UUID or slug, with per-outlet packages.
func (c *Client) Status(ctx context.Context, ref string) (Release, error) {
	if err := checkRef(ref); err != nil {
		return Release{}, err
	}
	var raw json.RawMessage
	if _, err := c.do(ctx, http.MethodGet, "/status/"+url.PathEscape(ref), nil, "", &raw); err != nil {
		return Release{}, err
	}
	return decodeRelease(raw)
}

// Retract withdraws a pending release and refunds its credits. PRNow refuses
// once editorial work has started.
func (c *Client) Retract(ctx context.Context, ref string) (Retraction, error) {
	if err := checkRef(ref); err != nil {
		return Retraction{}, err
	}
	var raw json.RawMessage
	if _, err := c.do(ctx, http.MethodPost, "/retract/"+url.PathEscape(ref), nil, "", &raw); err != nil {
		return Retraction{}, err
	}
	var r Retraction
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("prnow: decode retraction: %w", err)
	}
	rel, err := decodeRelease(raw)
	if err != nil {
		return r, err
	}
	r.Release = rel
	return r, nil
}

// NewIdempotencyKey returns a random UUID (v4), the form PRNow suggests.
func NewIdempotencyKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (c *Client) post(ctx context.Context, path string, d Draft, key string) (Release, error) {
	if key == "" {
		key = NewIdempotencyKey()
	}
	var raw json.RawMessage
	h, err := c.do(ctx, http.MethodPost, path, d, key, &raw)
	if err != nil {
		return Release{}, err
	}
	rel, err := decodeRelease(raw)
	rel.Replayed = strings.EqualFold(h.Get(headerIdempotentReplay), "true")
	return rel, err
}

func checkRef(ref string) error {
	if strings.TrimSpace(ref) == "" {
		return errors.New("prnow: a release id or slug is required")
	}
	return nil
}

type envelope struct {
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   json.RawMessage `json:"error"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	// Existing is the release a same_plan_duplicate names.
	Existing json.RawMessage `json:"existingPressRelease"`
}

// do sends one request and decodes the {success, data, error} envelope's
// data into out. The key goes in the Authorization header and nowhere else,
// and is never part of an error.
func (c *Client) do(ctx context.Context, method, path string, body any, idemKey string, out any) (http.Header, error) {
	if c.Key == "" {
		return nil, fmt.Errorf("%w: no key is set (%s)", ErrAuth, KeyEnv)
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idemKey != "" {
		req.Header.Set(headerIdempotencyKey, idemKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prnow: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.Header, fmt.Errorf("prnow: read %s: %w", path, err)
	}

	var env envelope
	jsonErr := json.Unmarshal(raw, &env)
	if resp.StatusCode < 200 || resp.StatusCode > 299 || (env.Success != nil && !*env.Success) {
		return resp.Header, apiError(resp, env, jsonErr == nil)
	}
	if jsonErr != nil {
		return resp.Header, fmt.Errorf("prnow: %s answered with something that is not JSON", path)
	}
	data := env.Data
	if len(data) == 0 || string(data) == "null" {
		data = raw // tolerate an answer with no envelope
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, fmt.Errorf("prnow: decode %s: %w", path, err)
		}
	}
	return resp.Header, nil
}

func apiError(resp *http.Response, env envelope, decoded bool) *APIError {
	e := &APIError{StatusCode: resp.StatusCode, Code: env.Code, Message: env.Message}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		e.StatusCode = http.StatusBadRequest // success:false on a 200
	}
	if decoded && len(env.Error) > 0 {
		var s string
		if json.Unmarshal(env.Error, &s) == nil {
			e.Message = s
		} else {
			var obj struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if json.Unmarshal(env.Error, &obj) == nil {
				if obj.Code != "" {
					e.Code = obj.Code
				}
				if obj.Message != "" {
					e.Message = obj.Message
				}
			}
		}
	}
	if e.Code == "" && strings.Contains(e.Message, "_") {
		// Some answers carry the code as the whole error string.
		for _, code := range []string{CodeDuplicate, CodeInProgress, CodeInterrupted, CodeIdempotencyReused} {
			if strings.Contains(e.Message, code) {
				e.Code = code
			}
		}
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 {
			e.RetryAfter = time.Duration(n) * time.Second
		}
	}
	if len(env.Existing) > 0 && string(env.Existing) != "null" {
		if rel, err := decodeRelease(env.Existing); err == nil {
			e.Existing = &rel
		}
	}
	return e
}

// decodeRelease reads a release object, including packages, which PRNow
// may send as a list or as an object keyed by outlet.
func decodeRelease(raw json.RawMessage) (Release, error) {
	var r Release
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("prnow: decode release: %w", err)
	}
	_ = json.Unmarshal(raw, &r.Raw)
	var p struct {
		Packages json.RawMessage `json:"packages"`
	}
	if json.Unmarshal(raw, &p) == nil && len(p.Packages) > 0 {
		r.Packages = decodePackages(p.Packages)
	}
	return r, nil
}

func decodePackages(raw json.RawMessage) []Package {
	var list []rawPackage
	if json.Unmarshal(raw, &list) == nil {
		out := make([]Package, 0, len(list))
		for _, p := range list {
			out = append(out, p.pkg(""))
		}
		return out
	}
	var byOutlet map[string]rawPackage
	if json.Unmarshal(raw, &byOutlet) == nil {
		out := make([]Package, 0, len(byOutlet))
		for name, p := range byOutlet {
			out = append(out, p.pkg(name))
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Outlet < out[j].Outlet })
		return out
	}
	return nil
}

// rawPackage takes the outlet under any of the names it might arrive as.
type rawPackage struct {
	Package
	Name string `json:"name"`
	Site string `json:"site"`
}

func (p rawPackage) pkg(key string) Package {
	out := p.Package
	for _, v := range []string{out.Outlet, p.Name, p.Site, key} {
		if v != "" {
			out.Outlet = v
			break
		}
	}
	return out
}

// names reads a list of strings, or of objects with a name.
func names(raw json.RawMessage) ([]string, error) {
	var s []string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var objs []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &objs); err != nil {
		var wrapped struct {
			Categories json.RawMessage `json:"categories"`
		}
		if json.Unmarshal(raw, &wrapped) == nil && len(wrapped.Categories) > 0 {
			return names(wrapped.Categories)
		}
		return nil, fmt.Errorf("prnow: decode list: %w", err)
	}
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		if o.Name != "" {
			out = append(out, o.Name)
		} else if o.Value != "" {
			out = append(out, o.Value)
		}
	}
	return out, nil
}

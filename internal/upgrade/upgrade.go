// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package upgrade moves a self-hosted instance to Limelit Cloud.
//
// One command, nothing retyped. It exports everything this instance holds and
// pushes it to a Cloud workspace the user already owns, identified by an API
// key they paste. What they measured on their own machine becomes the same
// numbers in Cloud, and the grid is the same grid.
//
// Two things it deliberately does not do.
//
// It never sends a provider key. The payload is data: prompts, answers,
// mentions, citations, usage. The OpenAI or Anthropic key that produced them
// stays on this machine, because a migration is not a reason to hand a third
// party your credentials.
//
// It never deletes anything locally. An upgrade that wiped the source would
// make a failed import unrecoverable, and the point of an open core is that
// leaving is reversible.
//
// The same import keeps a workspace current when the user opts in to sync
// (sync.go): after every run the new answers go to the workspace the Cloud
// key belongs to, under the same rules.
package upgrade

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/export"
	"github.com/limelit-co/open/internal/store"
)

const (
	// DefaultEndpoint is the Cloud import endpoint.
	DefaultEndpoint = "https://api.limelit.co/v1/import/open"
	// EndpointEnv overrides it, for a self-hosted Cloud or a test.
	EndpointEnv = "LIMELIT_CLOUD_ENDPOINT"
	// KeyEnv supplies the Cloud API key without putting it in a shell
	// history.
	KeyEnv = "LIMELIT_CLOUD_KEY"

	// instanceSetting holds this instance's own id.
	instanceSetting = "instance_id"
)

// Import modes, the payload's "mode". An absent mode is an upgrade, which is
// what every payload sent before modes existed was.
const (
	ModeUpgrade = "upgrade"
	ModeSync    = "sync"
	ModeSyncOff = "sync_off"
)

var (
	// ErrKeyRejected is Cloud refusing the key (401 or 403).
	ErrKeyRejected = errors.New("limelit cloud rejected the API key")
	// ErrAnotherInstanceSyncing is Cloud's 409: a different instance holds
	// the workspace's one sync slot.
	ErrAnotherInstanceSyncing = errors.New("another Limelit Open instance already syncs into this Limelit Cloud workspace")
)

// CloudError is Limelit Cloud refusing an upload. Nothing in it was
// imported: Cloud imports in one transaction.
type CloudError struct {
	Status int
	// Code is Cloud's error code, such as another_instance_syncing.
	Code string
	// Detail is Cloud's own message.
	Detail string
	msg    string
}

func (e *CloudError) Error() string { return e.msg }

// Is makes errors.Is(err, ErrKeyRejected) and ErrAnotherInstanceSyncing hold.
func (e *CloudError) Is(target error) bool {
	switch target {
	case ErrKeyRejected:
		return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
	case ErrAnotherInstanceSyncing:
		return e.Status == http.StatusConflict
	}
	return false
}

// unreachableError is Limelit Cloud not answering at all.
type unreachableError struct{ err error }

func (e *unreachableError) Error() string {
	return fmt.Sprintf("could not reach Limelit Cloud: %v. Nothing was changed here; re-running is safe", e.err)
}

func (e *unreachableError) Unwrap() error { return e.err }

// Result is what Cloud reported back.
type Result struct {
	OK     bool `json:"ok"`
	Import struct {
		ImportID     string `json:"import_id"`
		Prompts      int    `json:"prompts_imported"`
		Competitors  int    `json:"competitors_imported"`
		Chats        int    `json:"chats_imported"`
		Mentions     int    `json:"mentions_imported"`
		Citations    int    `json:"citations_imported"`
		ChatsSkipped int    `json:"chats_skipped"`
		// ChatsRederived counts answers already imported whose mentions or
		// citations a replace_derived sync brought in line.
		ChatsRederived int `json:"chats_rederived"`
	} `json:"import"`
	WorkspaceURL string `json:"workspace_url"`
	MCPURL       string `json:"mcp_url"`
	Next         string `json:"next"`
}

// Options configure one upgrade.
type Options struct {
	// Key is the Cloud API key. Required.
	Key string
	// Endpoint overrides the Cloud import URL.
	Endpoint string
	// Since narrows the export to answers on or after this date.
	Since string
	// Until narrows it to answers before this date (exclusive).
	Until string
	// Mode is the import mode: ModeUpgrade when empty. Run only upgrades;
	// Sync and SyncOff set the others.
	Mode string
	// ReplaceDerived asks Cloud to bring the mentions and citations of
	// answers it already holds in line with this payload.
	ReplaceDerived bool
	// Client is injected by tests.
	Client *http.Client
}

// Run exports this instance and pushes it to Cloud.
//
// A successful upgrade also turns this instance's sync off. Cloud turns it
// off on its side (the user has moved, so Cloud schedules those prompts
// now), and a sync after the next run here would take the slot back.
func Run(ctx context.Context, db *store.DB, opts Options) (Result, error) {
	if opts.Mode != "" && opts.Mode != ModeUpgrade {
		return Result{}, fmt.Errorf("upgrade.Run only upgrades; use Sync for mode %q", opts.Mode)
	}
	opts.Mode = ModeUpgrade
	c, err := connect(opts, 30*time.Minute)
	if err != nil {
		return Result{}, err
	}
	// An upgrade waits out a sync in flight, so the sync cannot turn the
	// workspace's sync back on after the upgrade turned it off.
	if err := acquireSync(ctx); err != nil {
		return Result{}, err
	}
	defer releaseSync()

	instance, err := InstanceID(ctx, db)
	if err != nil {
		return Result{}, err
	}
	result, err := push(ctx, db, c, instance, opts)
	if err != nil {
		return Result{}, err
	}
	if err := endSyncAfterUpgrade(ctx, db); err != nil {
		return result, fmt.Errorf("the upgrade went through, but sync could not be turned off here: %w", err)
	}
	return result, nil
}

// conn is where one upload goes and with what.
type conn struct {
	key      string
	endpoint string
	client   *http.Client
}

// connect resolves the key, the endpoint and the client. timeout bounds one
// request when the caller did not inject a client.
func connect(opts Options, timeout time.Duration) (conn, error) {
	key := strings.TrimSpace(opts.Key)
	if key == "" {
		return conn{}, fmt.Errorf(
			"a Limelit Cloud API key is required. Create one at https://limelit.co/settings and pass it with --key, or set %s", KeyEnv)
	}
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	client := opts.Client
	if client == nil {
		// A large export over a slow link is normal, and a short timeout
		// would abandon a transfer that was going to succeed.
		client = &http.Client{Timeout: timeout}
	}
	return conn{key: key, endpoint: endpoint, client: client}, nil
}

// push exports what opts selects and posts it.
func push(ctx context.Context, db *store.DB, c conn, instance string, opts Options) (Result, error) {
	payload, err := buildPayload(ctx, db, instance, opts)
	if err != nil {
		return Result{}, err
	}
	return post(ctx, c, payload)
}

// post sends one payload and reads Cloud's answer.
func post(ctx context.Context, c conn, payload []byte) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(payload))

	resp, err := c.client.Do(req)
	if err != nil {
		return Result{}, &unreachableError{err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return Result{}, cloudError(resp.StatusCode, body)
	}

	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		return Result{}, fmt.Errorf("limelit cloud accepted the upload but its reply could not be read: %w", err)
	}
	return result, nil
}

// buildPayload exports the instance and stamps it with its own id, which is
// what lets Cloud make a re-run idempotent, and with the import mode.
func buildPayload(ctx context.Context, db *store.DB, instance string, opts Options) ([]byte, error) {
	var buf bytes.Buffer
	if err := export.WriteJSON(ctx, db, &buf, export.Options{
		Since: opts.Since,
		Until: opts.Until,
		Now:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return nil, err
	}

	// The export is a stream, so the id and the mode are spliced into the
	// envelope rather than the whole document being decoded and re-encoded
	// to add a few fields.
	doc := buf.Bytes()
	if !bytes.HasPrefix(doc, []byte("{")) {
		return nil, fmt.Errorf("the export is not a JSON object")
	}
	stamped := append([]byte(`{"source_instance":`), mustJSON(instance)...)
	// An upgrade leaves the mode out: Cloud reads an absent mode as an
	// upgrade, so the payload stays the one every Cloud already takes.
	if opts.Mode != "" && opts.Mode != ModeUpgrade {
		stamped = append(stamped, `,"mode":`...)
		stamped = append(stamped, mustJSON(opts.Mode)...)
	}
	if opts.ReplaceDerived {
		stamped = append(stamped, `,"replace_derived":true`...)
	}
	stamped = append(stamped, ',')
	stamped = append(stamped, doc[1:]...)
	return stamped, nil
}

// InstanceID returns this instance's own id, generating one on first use.
//
// It has to be stable across runs: it is half the key Cloud uses to tell an
// answer it has already imported from one it has not, so an upgrade
// interrupted halfway and re-run does not duplicate anything. A fresh id per
// run would make every retry a full re-import.
func InstanceID(ctx context.Context, db *store.DB) (string, error) {
	existing, err := db.Setting(ctx, instanceSetting)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(existing) != "" {
		return existing, nil
	}

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := "open-" + hex.EncodeToString(raw)
	if err := db.SetSetting(ctx, instanceSetting, id); err != nil {
		return "", err
	}
	return id, nil
}

// cloudError turns a Cloud rejection into something a user can act on.
//
// Every case says what happened to the data and what to do next, because the
// alternative is a status code and a person guessing whether their history is
// now half in two places.
//
// Cloud's envelope is {"error":"<code>","message":"..."}; an older shape
// nests them as {"error":{"code":...,"message":...}}. Both are read.
func cloudError(status int, body []byte) error {
	var payload struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)
	var code, detail string
	if len(payload.Error) > 0 {
		var nested struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(payload.Error, &code) != nil && json.Unmarshal(payload.Error, &nested) == nil {
			code, detail = nested.Code, nested.Message
		}
	}
	if detail == "" {
		detail = payload.Message
	}
	if detail == "" {
		detail = strings.TrimSpace(string(body))
	}

	e := &CloudError{Status: status, Code: code, Detail: detail}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		e.msg = "limelit cloud rejected the API key. Create a new one at https://limelit.co/settings. Nothing was imported"
	case http.StatusConflict:
		e.msg = ErrAnotherInstanceSyncing.Error() + ", so nothing was imported. Only one instance can sync into a workspace: " +
			"turn sync off in that instance's Settings, or this one can take over once that one has gone 30 days without a sync"
	case http.StatusRequestEntityTooLarge:
		e.msg = fmt.Sprintf("the export is too large for one upload. Narrow it with --since, for example --since %s. Nothing was imported: %s",
			time.Now().AddDate(0, -3, 0).Format("2006-01-02"), detail)
	case http.StatusBadRequest:
		e.msg = fmt.Sprintf("limelit cloud could not read this export: %s. Nothing was imported", detail)
	default:
		e.msg = fmt.Sprintf("limelit cloud returned %d: %s. The import is one transaction, so nothing was partially written and re-running is safe",
			status, detail)
	}
	return e
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

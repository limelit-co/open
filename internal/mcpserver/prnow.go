// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

// The PRNow tools: publish a news article as a press release, and follow it.
//
// These are open-core only, with names no Cloud tool uses, so the "same name,
// same shape" rule does not bind them. They follow the run tools' other rule:
// prnow_submit_release spends PRNow credits, so dry_run defaults to true and
// the submit is a second call the agent makes only after the user has seen
// the release and the account's credits and said yes.
//
// The key never passes through a tool. The server holds a PRNowSource, which
// resolves the key itself (the environment, then the sealed store) at the
// moment of each call, the way the runner resolves provider keys.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/prnow"
)

// PRNowSource returns a client for the PRNow account this instance is
// connected to, or an error saying how to connect one.
type PRNowSource func(context.Context) (*prnow.Client, error)

// PRNowFrom builds a PRNowSource over a credential reader, such as
// credentials.Source. The key is read on every call, so a key saved with
// limelit prnow login is in use at once.
func PRNowFrom(creds func(name string) string) PRNowSource {
	return func(context.Context) (*prnow.Client, error) {
		key := ""
		if creds != nil {
			key = creds(prnow.KeyEnv)
		}
		if key == "" {
			return nil, errNoPRNowKey
		}
		return prnow.New(key), nil
	}
}

var errNoPRNowKey = errors.New("no PRNow key is connected. Tell the user to run `limelit prnow login` on this machine " +
	"(or set " + prnow.KeyEnv + "), then call again. Never ask for the key in chat")

type prnowSubmitArgs struct {
	Title           string   `json:"title" jsonschema:"the headline: 5 to 200 characters, at least 5 words"`
	Content         string   `json:"content" jsonschema:"the article body: HTML, or plain text with a blank line between paragraphs. 250 to 1200 words, at most 5 links and 3 inline images. Markdown is not converted"`
	Categories      []string `json:"categories,omitempty" jsonschema:"1 to 5 values from prnow_list_categories, exactly as listed"`
	Country         string   `json:"country,omitempty" jsonschema:"the dateline country, a name or a two-letter code"`
	City            string   `json:"city,omitempty" jsonschema:"the dateline city"`
	Plan            string   `json:"plan,omitempty" jsonschema:"the PRNow distribution plan, as named in the user's PRNow account. Required to submit: the plan sets what is spent. Ask the user; never guess"`
	Summary         string   `json:"summary,omitempty" jsonschema:"optional, up to 45 words"`
	MetaTitle       string   `json:"meta_title,omitempty" jsonschema:"optional SEO title, up to 60 characters"`
	MetaDescription string   `json:"meta_description,omitempty" jsonschema:"optional SEO description, up to 200 characters"`
	MetaKeywords    string   `json:"meta_keywords,omitempty" jsonschema:"optional SEO keywords, up to 250 characters"`
	DryRun          *bool    `json:"dry_run,omitempty" jsonschema:"default true: check the release and show the account's credits, and send nothing. Pass false only after the user has seen the check and confirmed"`
	IdempotencyKey  string   `json:"idempotency_key,omitempty" jsonschema:"to retry a submit that did not answer, pass the idempotency_key the earlier call returned: PRNow charges at most once per key. Omit on a first submit"`
}

type prnowSubmitOut struct {
	DryRun bool `json:"dry_run"`
	// Ready is whether the release passes every documented limit and has a
	// plan, so dry_run=false would be sent.
	Ready    bool     `json:"ready" jsonschema:"true when the release passes PRNow's limits and has a plan"`
	Problems []string `json:"problems,omitempty" jsonschema:"what must change before it can be submitted"`
	Warnings []string `json:"warnings,omitempty" jsonschema:"what PRNow will change on its own, such as cutting a long summary"`
	Words    int      `json:"words" jsonschema:"words in the body as a reader sees them"`

	Account                string  `json:"account,omitempty" jsonschema:"who the key belongs to"`
	Credits                float64 `json:"credits" jsonschema:"PRNow credits across the account's wallets; a count, not money"`
	MaxSingleWalletCredits float64 `json:"max_single_wallet_credits" jsonschema:"the largest order one wallet can pay for"`

	ReleaseID      string `json:"release_id,omitempty" jsonschema:"the submitted release; pass it to prnow_release_status"`
	Status         string `json:"status,omitempty" jsonschema:"pending once submitted"`
	Replayed       bool   `json:"replayed,omitempty" jsonschema:"true when PRNow already had this submit: nothing new was charged"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"the key this submit was sent with; reuse it to retry safely"`

	NextStep string `json:"next_step"`
}

type prnowStatusArgs struct {
	ReleaseID string `json:"release_id" jsonschema:"the release_id prnow_submit_release returned: the short slug or the full UUID"`
}

type prnowOutlet struct {
	Outlet          string  `json:"outlet"`
	State           string  `json:"state" jsonschema:"live, held or rejected; an outlet not listed is still pending"`
	LiveURL         string  `json:"live_url,omitempty" jsonschema:"where the article is live"`
	Reason          string  `json:"reason,omitempty" jsonschema:"why it is held or rejected"`
	CreditsRefunded float64 `json:"credits_refunded,omitempty"`
}

type prnowStatusOut struct {
	ReleaseID   string        `json:"release_id"`
	Title       string        `json:"title,omitempty"`
	Status      string        `json:"status" jsonschema:"draft, pending, in_progress, published, distributed, rejected or refunded"`
	Plan        string        `json:"plan,omitempty"`
	CreditsUsed float64       `json:"credits_used"`
	PublishedAt string        `json:"published_at,omitempty"`
	Outlets     []prnowOutlet `json:"outlets"`
	NextStep    string        `json:"next_step"`
}

type prnowCategoriesOut struct {
	Categories []string `json:"categories" jsonschema:"the values categories accepts, case-sensitive"`
}

const prnowConfirmFirst = " Call with dry_run=true first (the default) and show the user the release, any problems, " +
	"and the account's credits. Submitting spends PRNow credits on the user's plan and puts the article in front of " +
	"news outlets, which cannot be taken back once editorial work starts. Call again with dry_run=false only after the " +
	"user confirms. Then follow it with prnow_release_status."

func registerPRNow(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "prnow_list_categories",
		Description: "List the categories a PRNow press release can use, for prnow_submit_release's categories.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, prnowCategoriesOut, error) {
		c, err := d.PRNow(ctx)
		if err != nil {
			return nil, prnowCategoriesOut{}, err
		}
		cats, err := c.Categories(ctx)
		if err != nil {
			return nil, prnowCategoriesOut{}, prnowError(err, "")
		}
		return nil, prnowCategoriesOut{Categories: cats}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "prnow_submit_release",
		Description: "Publish a news article as a press release through PRNow, so the engines Limelit tracks have " +
			"a new source to cite. Checks the release against PRNow's limits before anything is sent." + prnowConfirmFirst,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in prnowSubmitArgs) (*mcp.CallToolResult, prnowSubmitOut, error) {
		out, err := prnowSubmit(ctx, d, in)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "prnow_release_status",
		Description: "Report a PRNow press release: its status and each outlet, live with its URL or held or " +
			"rejected with the reason. Call it after prnow_submit_release; an article takes time to go live.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in prnowStatusArgs) (*mcp.CallToolResult, prnowStatusOut, error) {
		if strings.TrimSpace(in.ReleaseID) == "" {
			return nil, prnowStatusOut{}, errors.New("release_id is required: the release_id prnow_submit_release returned")
		}
		c, err := d.PRNow(ctx)
		if err != nil {
			return nil, prnowStatusOut{}, err
		}
		rel, err := c.Status(ctx, in.ReleaseID)
		if err != nil {
			return nil, prnowStatusOut{}, prnowError(err, "")
		}
		out := prnowStatusOut{
			ReleaseID: rel.Ref(), Title: rel.Title, Status: rel.Status, Plan: rel.Plan,
			CreditsUsed: rel.CreditsUsed, PublishedAt: rel.PublishedAt, Outlets: []prnowOutlet{},
		}
		live := 0
		for _, p := range rel.Packages {
			out.Outlets = append(out.Outlets, prnowOutlet{
				Outlet: p.Outlet, State: p.State, LiveURL: p.LiveURL, Reason: p.Reason, CreditsRefunded: p.CreditsRefunded,
			})
			if p.LiveURL != "" {
				live++
			}
		}
		switch {
		case live > 0:
			out.NextStep = fmt.Sprintf("Live on %d outlet(s). Each live_url is now a page the engines can cite; the next runs record whether they do.", live)
		case rel.Status == "pending":
			out.NextStep = "Awaiting PRNow review. While it is pending the user can still withdraw it with limelit prnow retract and get the credits back."
		case rel.Status == "rejected" || rel.Status == "refunded":
			out.NextStep = "Not going out; PRNow returned the credits."
		default:
			out.NextStep = "Not live anywhere yet. Check again later."
		}
		return nil, out, nil
	})
}

func prnowSubmit(ctx context.Context, d Deps, in prnowSubmitArgs) (prnowSubmitOut, error) {
	dryRun := in.DryRun == nil || *in.DryRun
	draft := prnow.Draft{
		Title:           strings.TrimSpace(in.Title),
		Summary:         strings.TrimSpace(in.Summary),
		Content:         prnow.HTMLFromText(in.Content),
		Categories:      in.Categories,
		Country:         strings.TrimSpace(in.Country),
		City:            strings.TrimSpace(in.City),
		Plan:            strings.TrimSpace(in.Plan),
		MetaTitle:       strings.TrimSpace(in.MetaTitle),
		MetaDescription: strings.TrimSpace(in.MetaDescription),
		MetaKeywords:    strings.TrimSpace(in.MetaKeywords),
	}
	check := prnow.Validate(draft)
	if draft.Plan == "" {
		check.Errors = append(check.Errors, "plan is required: ask the user which PRNow plan to use (it sets what is spent)")
	}
	out := prnowSubmitOut{
		DryRun: dryRun, Ready: check.OK(), Problems: check.Errors, Warnings: check.Warnings,
		Words: prnow.Words(draft.Content),
	}
	if !dryRun && !check.OK() {
		return out, fmt.Errorf("nothing was sent: %s", strings.Join(check.Errors, "; "))
	}

	c, err := d.PRNow(ctx)
	if err != nil {
		return out, err
	}
	acct, err := c.Test(ctx)
	if err != nil {
		return out, prnowError(err, "")
	}
	out.Account = accountName(acct)
	out.Credits, out.MaxSingleWalletCredits = acct.Credits, acct.MaxSingleWalletCredits

	if dryRun {
		if out.Ready {
			out.NextStep = "Nothing was sent. Show the user the release, the plan and the credits; if they confirm, call again with dry_run=false."
		} else {
			out.NextStep = "Nothing was sent. Fix the problems (ask the user for anything only they know, such as the plan or the dateline), then check again."
		}
		return out, nil
	}

	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" {
		key = prnow.NewIdempotencyKey()
	}
	out.IdempotencyKey = key
	rel, err := c.Submit(ctx, draft, key)
	if err != nil {
		return out, prnowError(err, key)
	}
	out.ReleaseID, out.Status, out.Replayed = rel.Ref(), rel.Status, rel.Replayed
	out.NextStep = "Submitted. Follow it with prnow_release_status and release_id " + rel.Ref() + "."
	if rel.Replayed {
		out.NextStep = "PRNow already had this submit, so nothing new was charged. Follow it with prnow_release_status and release_id " + rel.Ref() + "."
	}
	return out, nil
}

// prnowError turns PRNow's documented failures into what the agent should
// say or do next. key is the submit's idempotency key, when there was one.
func prnowError(err error, key string) error {
	var apiErr *prnow.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case prnow.CodeInProgress:
			return fmt.Errorf("PRNow is still processing this submit. Wait a few seconds and call again with idempotency_key %q; it will not be charged twice", key)
		case prnow.CodeInterrupted:
			return fmt.Errorf("PRNow could not confirm whether the submit went through. Do not submit again; the user should ask PRNow support about idempotency key %q", key)
		case prnow.CodeDuplicate:
			msg := "PRNow already has a live release with this title on this plan, so nothing new was submitted"
			if apiErr.Existing != nil && apiErr.Existing.Ref() != "" {
				msg += ". The existing release_id is " + apiErr.Existing.Ref()
			}
			return errors.New(msg)
		}
	}
	switch {
	case errors.Is(err, prnow.ErrAuth):
		return errors.New("PRNow did not accept the saved key. Tell the user to run `limelit prnow login` with a current key. Never ask for the key in chat")
	case errors.Is(err, prnow.ErrCredits):
		return errors.New("the PRNow account does not have enough credits for this plan, so nothing was charged. The user can add credits in their PRNow account")
	case errors.Is(err, prnow.ErrNotFound):
		return errors.New("PRNow has no release with that release_id on this account")
	}
	return err
}

func accountName(a prnow.Account) string {
	switch {
	case a.Account.Company != "" && a.Account.Name != "":
		return a.Account.Name + ", " + a.Account.Company
	case a.Account.Company != "":
		return a.Account.Company
	case a.Account.Name != "":
		return a.Account.Name
	}
	return a.Account.Email
}

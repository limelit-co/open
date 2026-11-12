// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/credentials"
	"github.com/limelit-co/open/internal/prnow"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/store"
)

const prnowUsage = `Usage: limelit prnow <command> [flags]

Publish a news article as a press release through PRNow (prnow.io), so the
engines you track have something new to cite.

Commands:
  login       save a PRNow API key for this instance
  account     show the account the key belongs to and its credits
  categories  list the categories a release can use
  draft       save a release as a draft on PRNow (free, nothing is published)
  submit      check a release, then submit it with --yes (spends credits)
  status      show a release's status and where it went live
  retract     withdraw a pending release and refund its credits

Run "limelit prnow <command> -h" for the flags of one command.
`

// prnowIO is what the prnow commands touch outside the data directory, so a
// test can stand in for the terminal and for PRNow.
type prnowIO struct {
	in          io.Reader
	out         io.Writer
	interactive bool
	client      func(key string) *prnow.Client
	sleep       func(time.Duration)
}

func cmdPRNow(ctx context.Context, args []string) error {
	return prnowMain(ctx, prnowIO{
		in:          os.Stdin,
		out:         os.Stdout,
		interactive: isTerminal(os.Stdin),
		client:      prnow.New,
		sleep:       time.Sleep,
	}, args)
}

func prnowMain(ctx context.Context, t prnowIO, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(t.out, prnowUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "login":
		return prnowLogin(ctx, t, rest)
	case "account":
		return prnowAccount(ctx, t, rest)
	case "categories":
		return prnowCategories(ctx, t, rest)
	case "draft":
		return prnowRelease(ctx, t, rest, false)
	case "submit":
		return prnowRelease(ctx, t, rest, true)
	case "status":
		return prnowStatus(ctx, t, rest)
	case "retract":
		return prnowRetract(ctx, t, rest)
	case "-h", "--help", "help":
		fmt.Fprint(t.out, prnowUsage)
		return nil
	}
	fmt.Fprint(t.out, prnowUsage)
	return fmt.Errorf("unknown prnow command %q", sub)
}

func prnowLogin(ctx context.Context, t prnowIO, args []string) error {
	fs := flag.NewFlagSet("prnow login", flag.ContinueOnError)
	fs.SetOutput(t.out)
	key := fs.String("key", "", "the PRNow API key, instead of pasting it (it can also be piped on stdin)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: limelit prnow login [flags]

Saves a PRNow API key (it starts prnow_) for this instance, encrypted in the
data directory (%s, or $%s). Make a key in your PRNow account;
the API is described at %s. $%s, when set, wins over the saved key.

Flags:
`, config.DataDir(), config.DataDirEnv, prnow.DocsURL, prnow.KeyEnv)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" {
		if t.interactive {
			fmt.Fprint(t.out, "PRNow API key: ")
		}
		read, err := readKey(t.in, t.interactive)
		if err != nil {
			return err
		}
		*key = read
	}
	k := cleanPRNowKey(*key)
	if k == "" {
		return errors.New("no key given: make one in your PRNow account and paste it here")
	}

	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	acct, checkErr := t.client(k).Test(checkCtx)
	cancel()
	switch {
	case errors.Is(checkErr, prnow.ErrAuth):
		return errors.New("PRNow did not accept that key, so nothing was saved. Check it in your PRNow account and run limelit prnow login again")
	case checkErr != nil:
		fmt.Fprintf(t.out, "Could not reach PRNow to check the key (%v). Saving it anyway; the next command will say if it is wrong.\n", checkErr)
	}

	db, keys, err := openKeyStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := credentials.Save(ctx, db, keys, prnow.KeyEnv, k); err != nil {
		return err
	}
	fmt.Fprintf(t.out, "Saved the PRNow key for the instance in %s.\n", config.DataDir())
	if checkErr == nil {
		printAccount(t.out, acct)
	}
	if env := config.Credential(prnow.KeyEnv); env != "" && env != k {
		fmt.Fprintf(t.out, "Note: %s is set in this shell and wins over the saved key until you unset it.\n", prnow.KeyEnv)
	}
	return nil
}

func prnowAccount(ctx context.Context, t prnowIO, args []string) error {
	fs := flag.NewFlagSet("prnow account", flag.ContinueOnError)
	fs.SetOutput(t.out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := prnowClient(ctx, t)
	if err != nil {
		return err
	}
	acct, err := c.Test(ctx)
	if err != nil {
		return explainPRNow(err)
	}
	printAccount(t.out, acct)
	return nil
}

func prnowCategories(ctx context.Context, t prnowIO, args []string) error {
	fs := flag.NewFlagSet("prnow categories", flag.ContinueOnError)
	fs.SetOutput(t.out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := prnowClient(ctx, t)
	if err != nil {
		return err
	}
	cats, err := c.Categories(ctx)
	if err != nil {
		return explainPRNow(err)
	}
	for _, cat := range cats {
		fmt.Fprintln(t.out, cat)
	}
	return nil
}

// prnowRelease is draft and submit. They take the same release; submit adds
// --yes, without which it checks everything and sends nothing.
func prnowRelease(ctx context.Context, t prnowIO, args []string, submit bool) error {
	name := "draft"
	if submit {
		name = "submit"
	}
	fs := flag.NewFlagSet("prnow "+name, flag.ContinueOnError)
	fs.SetOutput(t.out)
	file := fs.String("file", "", "the article body: HTML, or plain text with blank lines between paragraphs (- reads stdin). A first line \"# Title\" is used as the title")
	title := fs.String("title", "", "the headline: 5 to 200 characters, at least 5 words")
	summary := fs.String("summary", "", "a short summary, up to 45 words")
	var categories []string
	fs.Func("category", "a category from limelit prnow categories (repeat, or separate with commas; 1 to 5)", func(v string) error {
		for _, c := range strings.Split(v, ",") {
			if c = strings.TrimSpace(c); c != "" {
				categories = append(categories, c)
			}
		}
		return nil
	})
	country := fs.String("country", "", "the dateline country, a name or a two-letter code")
	city := fs.String("city", "", "the dateline city")
	planHelp := "the PRNow distribution plan, as named in your PRNow account (optional for a draft)"
	if submit {
		planHelp = "the PRNow distribution plan, as named in your PRNow account (required: the plan sets what is spent)"
	}
	plan := fs.String("plan", "", planHelp)
	metaTitle := fs.String("meta-title", "", "SEO title, up to 60 characters")
	metaDescription := fs.String("meta-description", "", "SEO description, up to 200 characters")
	metaKeywords := fs.String("meta-keywords", "", "SEO keywords, up to 250 characters")
	yes := false
	if submit {
		fs.BoolVar(&yes, "yes", false, "submit for real. Without it nothing is sent: the release is checked and the account's credits are shown")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("--file is required: the article body. See limelit prnow %s -h", name)
	}
	body, err := readArticle(t.in, *file)
	if err != nil {
		return err
	}
	headline, body := splitTitle(body)
	if strings.TrimSpace(*title) != "" {
		headline = strings.TrimSpace(*title)
	}
	d := prnow.Draft{
		Title:           headline,
		Summary:         strings.TrimSpace(*summary),
		Content:         prnow.HTMLFromText(body),
		Categories:      categories,
		Country:         strings.TrimSpace(*country),
		City:            strings.TrimSpace(*city),
		Plan:            strings.TrimSpace(*plan),
		MetaTitle:       strings.TrimSpace(*metaTitle),
		MetaDescription: strings.TrimSpace(*metaDescription),
		MetaKeywords:    strings.TrimSpace(*metaKeywords),
	}

	problems := prnow.Validate(d)
	for _, w := range problems.Warnings {
		fmt.Fprintln(t.out, "Note: "+w)
	}
	if submit && d.Plan == "" {
		problems.Errors = append(problems.Errors, "--plan is required: the plan sets what the submit spends")
	}
	if !problems.OK() {
		if !submit {
			// A draft can be incomplete; PRNow decides what it keeps.
			for _, e := range problems.Errors {
				fmt.Fprintln(t.out, "Before submitting: "+e)
			}
		} else {
			return problems
		}
	}

	c, err := prnowClient(ctx, t)
	if err != nil {
		return err
	}

	if !submit {
		rel, err := c.SaveDraft(ctx, d, "")
		if err != nil {
			return explainPRNow(err)
		}
		fmt.Fprintf(t.out, "Saved as a draft on PRNow: %s. Nothing was submitted or charged.\n", rel.Ref())
		fmt.Fprintln(t.out, "Finish it in your PRNow account, or submit it from here with limelit prnow submit --yes.")
		return nil
	}

	acct, err := c.Test(ctx)
	if err != nil {
		return explainPRNow(err)
	}
	fmt.Fprintf(t.out, "Release: %q\n", d.Title)
	fmt.Fprintf(t.out, "  %d words, categories %s, dateline %s, %s\n", prnow.Words(d.Content), strings.Join(d.Categories, ", "), d.City, d.Country)
	fmt.Fprintf(t.out, "  plan %s\n", d.Plan)
	printAccount(t.out, acct)
	if !yes {
		fmt.Fprintln(t.out, "\nNothing was sent. Run the same command with --yes to submit it; that spends PRNow credits on the plan above.")
		return nil
	}

	rel, err := submitOnce(ctx, t, c, d)
	if err != nil {
		return err
	}
	if rel.Replayed {
		fmt.Fprintf(t.out, "\nPRNow already had this submit (same idempotency key); nothing new was charged. Release %s is %s.\n", rel.Ref(), orUnknown(rel.Status))
	} else {
		fmt.Fprintf(t.out, "\nSubmitted: release %s is %s.\n", rel.Ref(), orUnknown(rel.Status))
	}
	fmt.Fprintf(t.out, "Follow it: limelit prnow status %s\n", rel.Ref())
	fmt.Fprintf(t.out, "While it is pending you can withdraw it and get the credits back: limelit prnow retract %s\n", rel.Ref())
	return nil
}

// submitOnce submits with one idempotency key for every attempt, so a retry
// after a dropped connection or an in-flight answer is charged once.
func submitOnce(ctx context.Context, t prnowIO, c *prnow.Client, d prnow.Draft) (prnow.Release, error) {
	key := prnow.NewIdempotencyKey()
	const attempts = 3
	var last error
	for i := 0; i < attempts; i++ {
		rel, err := c.Submit(ctx, d, key)
		if err == nil {
			return rel, nil
		}
		last = err
		var apiErr *prnow.APIError
		switch {
		case errors.As(err, &apiErr) && apiErr.Code == prnow.CodeInProgress:
			wait := apiErr.RetryAfter
			if wait <= 0 {
				wait = 5 * time.Second
			}
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			fmt.Fprintf(t.out, "PRNow is still working on this submit; asking again in %s.\n", wait)
			t.sleep(wait)
		case errors.As(err, &apiErr):
			return prnow.Release{}, explainSubmit(err, key)
		default:
			// No answer at all: the same key makes asking again safe.
			if ctx.Err() != nil {
				return prnow.Release{}, err
			}
			fmt.Fprintf(t.out, "No answer from PRNow (%v); asking again with the same idempotency key.\n", err)
			t.sleep(2 * time.Second)
		}
	}
	return prnow.Release{}, fmt.Errorf("%w (idempotency key %s: PRNow charges at most once for it)", explainSubmit(last, key), key)
}

func prnowStatus(ctx context.Context, t prnowIO, args []string) error {
	fs := flag.NewFlagSet("prnow status", flag.ContinueOnError)
	fs.SetOutput(t.out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: limelit prnow status <release id or slug>")
	}
	c, err := prnowClient(ctx, t)
	if err != nil {
		return err
	}
	rel, err := c.Status(ctx, fs.Arg(0))
	if err != nil {
		return explainPRNow(err)
	}
	fmt.Fprintf(t.out, "Release %s: %s\n", rel.Ref(), orUnknown(rel.Status))
	if rel.Title != "" {
		fmt.Fprintf(t.out, "  %q\n", rel.Title)
	}
	if rel.Plan != "" {
		fmt.Fprintf(t.out, "  plan %s, %s credits used\n", rel.Plan, credits(rel.CreditsUsed))
	}
	if rel.PublishedAt != "" {
		fmt.Fprintf(t.out, "  published %s\n", rel.PublishedAt)
	}
	if len(rel.Packages) > 0 {
		fmt.Fprintln(t.out, "Outlets:")
		for _, p := range rel.Packages {
			line := "  " + orUnknown(p.Outlet) + ": " + orUnknown(p.State)
			switch {
			case p.LiveURL != "":
				line += " " + p.LiveURL
			case p.Reason != "":
				line += " (" + p.Reason + ")"
			}
			if p.CreditsRefunded > 0 {
				line += ", " + credits(p.CreditsRefunded) + " credits refunded"
			}
			fmt.Fprintln(t.out, line)
		}
	}
	return nil
}

func prnowRetract(ctx context.Context, t prnowIO, args []string) error {
	fs := flag.NewFlagSet("prnow retract", flag.ContinueOnError)
	fs.SetOutput(t.out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: limelit prnow retract <release id or slug>")
	}
	c, err := prnowClient(ctx, t)
	if err != nil {
		return err
	}
	r, err := c.Retract(ctx, fs.Arg(0))
	var apiErr *prnow.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.StatusCode == 400:
		return errors.New("PRNow will not retract it: only a pending release can be withdrawn, and editorial work has started or it is live")
	case errors.As(err, &apiErr) && apiErr.StatusCode == 409:
		return errors.New("that release was already retracted; no further credits were refunded")
	case err != nil:
		return explainPRNow(err)
	}
	fmt.Fprintf(t.out, "Retracted %s: %s credits refunded.\n", orUnknown(r.Ref()), credits(r.CreditsRefunded))
	return nil
}

// prnowClient resolves the key the way every command must: the environment
// first, then the sealed store.
func prnowClient(ctx context.Context, t prnowIO) (*prnow.Client, error) {
	key := config.Credential(prnow.KeyEnv)
	if key == "" {
		db, keys, err := openKeyStore(ctx)
		if err != nil {
			return nil, err
		}
		key = credentials.Source(ctx, db, keys, newLogger())(prnow.KeyEnv)
		db.Close()
	}
	if key == "" {
		return nil, fmt.Errorf("no PRNow key: run limelit prnow login, or set %s", prnow.KeyEnv)
	}
	return t.client(key), nil
}

func openKeyStore(ctx context.Context) (*store.DB, *secrets.Keyring, error) {
	db, err := store.Open(ctx, config.DatabasePath())
	if err != nil {
		return nil, nil, err
	}
	keys, err := secrets.Open(config.DataDir())
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, keys, nil
}

// explainPRNow turns the documented failures into what to do next.
func explainPRNow(err error) error {
	switch {
	case errors.Is(err, prnow.ErrAuth):
		return errors.New("PRNow did not accept the key: run limelit prnow login with a current one")
	case errors.Is(err, prnow.ErrCredits):
		return errors.New("the PRNow account does not have enough credits for this plan: add credits in your PRNow account, then run the command again")
	case errors.Is(err, prnow.ErrNotFound):
		return errors.New("PRNow has no release by that id on this account")
	}
	return err
}

func explainSubmit(err error, key string) error {
	var apiErr *prnow.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Code {
	case prnow.CodeDuplicate:
		msg := "PRNow already has a live release with this title on this plan, so nothing new was submitted"
		if apiErr.Existing != nil && apiErr.Existing.Ref() != "" {
			msg += fmt.Sprintf(": %s (%s). See it with limelit prnow status %s", apiErr.Existing.Ref(), orUnknown(apiErr.Existing.Status), apiErr.Existing.Ref())
		}
		return errors.New(msg)
	case prnow.CodeInterrupted:
		return fmt.Errorf("PRNow could not confirm whether the submit went through. Do not submit again; ask PRNow support about idempotency key %s", key)
	}
	return explainPRNow(err)
}

func printAccount(out io.Writer, a prnow.Account) {
	who := a.Account.Name
	if a.Account.Company != "" {
		if who != "" {
			who += ", "
		}
		who += a.Account.Company
	}
	if who == "" {
		who = a.Account.Email
	}
	if who != "" {
		fmt.Fprintf(out, "PRNow account: %s\n", who)
	}
	fmt.Fprintf(out, "Credits: %s in total, %s payable from one wallet\n", credits(a.Credits), credits(a.MaxSingleWalletCredits))
}

// readArticle reads the body from a file, or stdin for "-".
func readArticle(in io.Reader, path string) (string, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(io.LimitReader(in, 1<<20))
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("read the article: %w", err)
	}
	return string(raw), nil
}

// splitTitle takes a first line "# Headline" as the title.
func splitTitle(body string) (title, rest string) {
	trimmed := strings.TrimLeft(body, "\r\n\t ")
	first, after, _ := strings.Cut(trimmed, "\n")
	if strings.HasPrefix(first, "# ") {
		return strings.TrimSpace(strings.TrimPrefix(first, "# ")), after
	}
	return "", body
}

// cleanPRNowKey accepts the key as pasted, including an export line.
func cleanPRNowKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "export ")
	if _, v, ok := strings.Cut(s, "="); ok && strings.HasPrefix(s, prnow.KeyEnv) {
		s = v
	}
	s = strings.TrimPrefix(strings.TrimSpace(s), "Bearer ")
	return strings.Trim(strings.TrimSpace(s), `"'`)
}

func credits(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

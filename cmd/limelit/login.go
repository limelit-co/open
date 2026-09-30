// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/credentials"
	"github.com/limelit-co/open/internal/engines"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/store"
)

// cmdLogin connects this instance to the free Limelit Cloud allowance: it
// sends the user to the page that makes a key, takes the pasted key, checks
// it, saves it encrypted where `limelit serve` reads it, and tracks every
// engine it reaches. A running server uses the key on its next call.
func cmdLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	key := fs.String("key", "", "the Limelit Cloud key, instead of pasting it (it can also be piped on stdin)")
	noBrowser := fs.Bool("no-browser", false, "print the link without trying to open a browser")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: limelit login [flags]

Gets a free Limelit Cloud key and saves it for this instance, so it can ask
ChatGPT, Gemini, Perplexity, Google AI Overviews and AI Mode without any
provider key of your own. Open the link it prints, sign in with Google, press
Create my key, copy it, and paste it here.

The key is saved in the data directory (%s, or $%s), the same one
limelit serve uses: run both from the same place, with the same
LIMELIT_SECRET if you set one. $%s, when set, wins over the saved key.

Flags:
`, config.DataDir(), config.DataDirEnv, provider.LimelitKeyEnv)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	return login(ctx, loginIO{
		in:          os.Stdin,
		out:         os.Stdout,
		interactive: isTerminal(os.Stdin),
		openURL:     openBrowser,
		allowance:   provider.FetchLimelitAllowance,
	}, *key, *noBrowser)
}

// loginIO is what login touches outside the data directory, so a test can
// stand in for the terminal, the browser and Limelit Cloud.
type loginIO struct {
	in          io.Reader
	out         io.Writer
	interactive bool
	openURL     func(string) error
	allowance   func(context.Context, string) (provider.LimelitAllowance, error)
}

func login(ctx context.Context, t loginIO, key string, noBrowser bool) error {
	dataDir, err := filepath.Abs(config.DataDir())
	if err != nil {
		return err
	}

	if strings.TrimSpace(key) == "" {
		fmt.Fprintf(t.out, "Get a free Limelit Cloud key:\n"+
			"  1. Open %s\n"+
			"     and sign in with Google (a first sign-in creates the account).\n"+
			"  2. Press Create my key, then Copy.\n"+
			"  3. Paste it here.\n\n", provider.LimelitKeyPage)
		if !noBrowser && t.interactive && t.openURL != nil && t.openURL(provider.LimelitKeyPage) == nil {
			fmt.Fprintln(t.out, "Opened the link in your browser.")
		}
		if t.interactive {
			fmt.Fprint(t.out, "Key: ")
		}
		key, err = readKey(t.in, t.interactive)
		if err != nil {
			return err
		}
	}
	key = cleanKey(key)
	if key == "" {
		return errors.New("no key given: paste the key from " + provider.LimelitKeyPage)
	}

	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	allowance, checkErr := t.allowance(checkCtx, key)
	cancel()
	switch {
	case errors.Is(checkErr, provider.ErrAuth):
		return fmt.Errorf("Limelit Cloud did not accept that key, so nothing was saved. Make a new one at %s and run limelit login again", provider.LimelitKeyPage)
	case checkErr != nil:
		fmt.Fprintf(t.out, "Could not reach Limelit Cloud to check the key (%v). Saving it anyway; the first run will say if it is wrong.\n", checkErr)
	}

	db, err := store.Open(ctx, config.DatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()
	keys, err := secrets.Open(config.DataDir())
	if err != nil {
		return err
	}
	if err := credentials.Save(ctx, db, keys, provider.LimelitKeyEnv, key); err != nil {
		return err
	}
	entry, _ := provider.CatalogEntryFor("limelit")
	tracked, err := credentials.Track(ctx, db, provider.Default(), entry)
	if err != nil {
		return err
	}

	fmt.Fprintf(t.out, "\nSaved the key for the instance in %s.\n", dataDir)
	if labels := engineLabels(tracked); len(labels) > 0 {
		fmt.Fprintf(t.out, "Tracking %s through the free allowance.\n", strings.Join(labels, ", "))
	}
	if checkErr == nil && allowance.MonthlyCredits > 0 {
		left := allowance.MonthlyCredits - allowance.UsedThisMonth
		if left < 0 {
			left = 0
		}
		fmt.Fprintf(t.out, "This month: %d of %d free credits left, about one answer each.\n", left, allowance.MonthlyCredits)
	}
	if env := config.Credential(provider.LimelitKeyEnv); env != "" && env != key {
		fmt.Fprintf(t.out, "Note: %s is set in this shell and wins over the saved key until you unset it.\n", provider.LimelitKeyEnv)
	}
	if _, err := db.Property(ctx); errors.Is(err, store.ErrNotFound) {
		fmt.Fprintln(t.out, "Next: finish setup in the dashboard (limelit serve). The key is already in place.")
	} else {
		fmt.Fprintln(t.out, "Next: press Run in the dashboard, or run: limelit run")
	}
	return nil
}

// readKey reads one line from a terminal, or everything piped in.
func readKey(in io.Reader, interactive bool) (string, error) {
	if interactive {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return line, nil
	}
	raw, err := io.ReadAll(io.LimitReader(in, 4096))
	return string(raw), err
}

// cleanKey accepts the key as pasted, including the whole export line the
// key page offers to copy, with or without quotes.
func cleanKey(s string) string { return provider.CleanLimelitKey(s) }

// engineLabels names the engines behind target specs, in order, once each.
func engineLabels(specs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, spec := range specs {
		id, _, _ := strings.Cut(spec, ":")
		label := id
		if e, ok := engines.Lookup(id); ok {
			label = e.Label
		}
		if !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	return out
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// openBrowser is best effort: over SSH or in a container there is no browser,
// and the printed link is the way in.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

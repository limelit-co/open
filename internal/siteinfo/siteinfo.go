// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package siteinfo reads what a website calls itself, so setup can suggest
// the brand name answers actually use: "Kindle to PDF", not
// "kindletopdf.com". An answer names a product the way its own site does,
// and a brand tracked only by its domain is missed every time an engine
// writes the name.
//
// It reads the page head only (og:site_name, application-name, <title>),
// with a short timeout and a size cap, and never follows anything the page
// links to.
package siteinfo

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Info is what a page's head says about the site.
type Info struct {
	SiteName        string // og:site_name
	ApplicationName string // <meta name="application-name">
	Title           string
	Description     string
}

// maxHead bounds how much of a page is read: the head is near the top.
const maxHead = 256 << 10

// Fetch reads rawURL's head. The client should carry a timeout; Fetch adds
// none of its own beyond ctx.
func Fetch(ctx context.Context, client *http.Client, rawURL, userAgent string) (Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Info{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")
	res, err := client.Do(req)
	if err != nil {
		return Info{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return Info{}, fmt.Errorf("siteinfo: %s answered %d", rawURL, res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxHead))
	if err != nil {
		return Info{}, err
	}
	return Parse(string(raw)), nil
}

// Lookup fetches https://domain/ with a short timeout.
func Lookup(ctx context.Context, domain, userAgent string) (Info, error) {
	client := &http.Client{Timeout: 6 * time.Second}
	return Fetch(ctx, client, "https://"+strings.TrimSpace(domain)+"/", userAgent)
}

var (
	metaTag   = regexp.MustCompile(`(?is)<meta\s[^>]*>`)
	attr      = regexp.MustCompile(`(?is)([a-z][a-z0-9:_-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	titleTag  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	whitespce = regexp.MustCompile(`\s+`)
)

// Parse reads the head fields from an HTML document.
func Parse(doc string) Info {
	var info Info
	for _, tag := range metaTag.FindAllString(doc, -1) {
		attrs := map[string]string{}
		for _, m := range attr.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(m[1])] = m[2] + m[3] + m[4]
		}
		key := strings.ToLower(attrs["property"] + attrs["name"])
		content := clean(attrs["content"])
		switch key {
		case "og:site_name":
			if info.SiteName == "" {
				info.SiteName = content
			}
		case "application-name":
			if info.ApplicationName == "" {
				info.ApplicationName = content
			}
		case "description", "og:description":
			if info.Description == "" {
				info.Description = content
			}
		}
	}
	if m := titleTag.FindStringSubmatch(doc); m != nil {
		info.Title = clean(m[1])
	}
	return info
}

func clean(s string) string {
	return strings.TrimSpace(whitespce.ReplaceAllString(html.UnescapeString(s), " "))
}

// titleSeparators split a title into its parts: "Kindle to PDF — Export
// Kindle Books as PDF", "Home | Acme".
var titleSeparators = []string{" | ", " — ", " – ", " - ", " · ", ": ", " :: "}

// BrandName picks the name the site uses for itself, or "" when nothing
// better than the domain is on the page. The site name wins; failing that,
// the title part that spells the domain's name ("Kindle to PDF" for
// kindletopdf); failing that, the shortest title part the domain's name
// contains ("ChatCache" in getchatcache).
func BrandName(info Info, domain string) string {
	label := domainLabel(domain)
	good := func(name string) bool {
		n := key(name)
		return n != "" && n != key(domain) && !strings.Contains(strings.ToLower(name), "."+tld(domain)) && len([]rune(name)) <= 60
	}
	for _, name := range []string{info.SiteName, info.ApplicationName} {
		if good(name) {
			return name
		}
	}
	parts := []string{info.Title}
	for _, sep := range titleSeparators {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}
	var best string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if !good(p) {
			continue
		}
		switch k := key(p); {
		case k == label:
			return p
		case label != "" && strings.Contains(label, k) && len(k) >= 4:
			if best == "" || len(p) < len(best) {
				best = p
			}
		}
	}
	return best
}

// key is a name's letters and digits, lowercased.
func key(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func domainLabel(domain string) string {
	d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), "www.")
	if i := strings.IndexByte(d, '.'); i >= 0 {
		d = d[:i]
	}
	return key(d)
}

func tld(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	if i := strings.LastIndexByte(d, '.'); i >= 0 {
		return d[i+1:]
	}
	return ""
}

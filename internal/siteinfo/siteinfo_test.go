// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package siteinfo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The heads below are from the real sites, September 2026.
func TestBrandNameIsWhatTheSiteCallsItself(t *testing.T) {
	for _, tc := range []struct {
		domain, head, want string
	}{
		{"kindletopdf.com", `<title>Kindle to PDF — Save Kindle Books as PDF | Free Chrome Extension</title>
			<meta property="og:site_name" content="Kindle to PDF"/>`, "Kindle to PDF"},
		// No site name: the title part that spells the domain.
		{"kindletopdf.com", `<title>Kindle to PDF — Save Kindle Books as PDF | Free Chrome Extension</title>`, "Kindle to PDF"},
		{"getchatcache.com", `<title>ChatCache - Fast ChatGPT PDF &amp; Word Export | Free Chrome Extension</title>`, "ChatCache"},
		{"limelit.co", `<meta content="Limelit" property="og:site_name"><title>Limelit · One marketer.</title>`, "Limelit"},
		// Nothing better than the domain: no suggestion.
		{"acme.com", `<title>acme.com</title>`, ""},
		{"acme.com", `<title>Welcome | Home</title>`, ""},
	} {
		if got := BrandName(Parse(tc.head), tc.domain); got != tc.want {
			t.Errorf("%s: BrandName = %q, want %q", tc.domain, got, tc.want)
		}
	}
}

func TestFetchReadsOnlyAnOKPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`<html><head><meta property="og:site_name" content="Acme"></head></html>`))
	}))
	defer srv.Close()
	info, err := Fetch(context.Background(), srv.Client(), srv.URL+"/", "test")
	if err != nil || info.SiteName != "Acme" {
		t.Errorf("Fetch = %+v, %v", info, err)
	}
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL+"/missing", "test"); err == nil {
		t.Error("a 404 was read as a page")
	}
}

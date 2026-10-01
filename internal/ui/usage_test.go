// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/provider/providertest"
)

// TestTheTopBarShowsFreeCreditsAndUpgrade. On the free allowance every page
// says how much of the month is used, and offers the upgrade beside it.
func TestTheTopBarShowsFreeCreditsAndUpgrade(t *testing.T) {
	fakeCloudSetup(t, "kindletopdf.com")
	_, h := kindleApp(t)
	body := get(t, h, "/prompts").Body.String()
	for _, want := range []string{"Free credits", "24 / 1,000", "used this month", "resets Oct 1", `class="upgrade-link" href="/upgrade"`, "Upgrade to Cloud"} {
		if !strings.Contains(body, want) {
			t.Errorf("top bar lacks %q", want)
		}
	}
}

// TestWithoutACloudKeyTheBarShowsTodaysAnswers against the daily limit,
// the limit that actually stops a run on an instance paying its own keys.
func TestWithoutACloudKeyTheBarShowsTodaysAnswers(t *testing.T) {
	t.Setenv("LIMELIT_CLOUD_KEY", "")
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	body := get(t, h, "/prompts").Body.String()
	if !strings.Contains(body, "Answers today") || !strings.Contains(body, "0 / 200") || strings.Contains(body, "Free credits") {
		t.Error("the bar does not show today's answers against the limit")
	}
	if !strings.Contains(body, "Upgrade to Cloud") {
		t.Error("no upgrade in the bar")
	}
}

func TestTheMeterWarnsNearTheLimit(t *testing.T) {
	if v := meter("x", 950, 1000); !v.Warn || v.Width != "95%" {
		t.Errorf("950/1000 = %+v", v)
	}
	if v := meter("x", 1200, 1000); v.Width != "100%" {
		t.Errorf("over the limit = %+v", v)
	}
	if v := meter("x", 24, 1000); v.Warn {
		t.Errorf("24/1000 warns: %+v", v)
	}
}

// TestUpgradeRefusesAnAccountThatTracksAnotherBrand. Cloud imports into the
// key's account, so a key from an account for another domain uploads nothing.
func TestUpgradeRefusesAnAccountThatTracksAnotherBrand(t *testing.T) {
	fakeCloudSetup(t, "rafay.co")
	_, h := kindleApp(t)
	body := post(t, h, "/upgrade", url.Values{}).Body.String()
	if !strings.Contains(body, "which tracks a different brand, so nothing was uploaded") {
		t.Errorf("no refusal: %s", flashOf(body))
	}
}

// TestUpgradeUsesTheSavedFreeKey: the key saved for the free allowance is
// the account the answers came through, so the upload offers to use it.
func TestUpgradeUsesTheSavedFreeKey(t *testing.T) {
	fakeCloudSetup(t, "kindletopdf.com")
	t.Setenv("LIMELIT_CLOUD_KEY", "")
	_, h := kindleApp(t)
	post(t, h, "/settings/keys", url.Values{"provider": {"limelit"}, "cred_LIMELIT_CLOUD_KEY": {"lmlt_test"}})
	body := get(t, h, "/upgrade").Body.String()
	if !strings.Contains(body, "your saved Limelit Cloud key") || !strings.Contains(body, "Leave blank to use the key you saved") {
		t.Error("the upgrade form does not offer the saved key")
	}
}

// TestTheTopBarAsksForAStar, as a plain link: no star count is fetched,
// because nothing leaves this machine except the calls to the engines.
func TestTheTopBarAsksForAStar(t *testing.T) {
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	body := get(t, h, "/prompts").Body.String()
	if !strings.Contains(body, `class="gh-star" href="https://github.com/limelit-co/open"`) || !strings.Contains(body, "<span>Star</span>") {
		t.Error("no star link in the top bar")
	}
	star := strings.Index(body, `class="gh-star"`)
	run := strings.Index(body, `action="/run"`)
	if star < 0 || run < 0 || star > run {
		t.Error("the star link is not right before Run now")
	}
}

// fakeAllowance serves only the allowance, with the star thank-you fields.
func fakeAllowance(t *testing.T, body map[string]any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer lmlt_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LIMELIT_CLOUD_API", srv.URL)
	t.Setenv("LIMELIT_CLOUD_KEY", "lmlt_test")
}

func starOffer(claimed bool) map[string]any {
	return map[string]any{
		"enabled": true, "monthly_credits": 1000, "used_this_month": 1100, "daily_credits": 150, "used_today": 10,
		"bonus_left": 400, "bonus_used_this_month": 100, "account": "Kindle to PDF",
		"star": map[string]any{"enabled": true, "credits": 500, "claimed": claimed, "repo": "limelit-co/open",
			"claim_url": "https://limelit.co/settings/open-key#star"},
	}
}

// TestTheStarSaysWhatItBringsUntilClaimed. On the free credits the star link
// offers the thank-you and goes to the page that checks it; once claimed it
// is the plain repository link again. The meter's total counts the bonus
// left and the bonus already spent this month.
func TestTheStarSaysWhatItBringsUntilClaimed(t *testing.T) {
	fakeAllowance(t, starOffer(false))
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	body := get(t, h, "/prompts").Body.String()
	// html/template writes "+" as &#43;.
	if !strings.Contains(body, `href="https://limelit.co/settings/open-key#star"`) || !strings.Contains(body, "Star · &#43;500 credits") {
		t.Error("the star link does not offer the thank-you")
	}
	if !strings.Contains(body, "1,100 / 1,500") || !strings.Contains(body, "Includes 500 thank-you credits") {
		t.Error("the meter does not count the bonus")
	}
	if strings.Contains(body, "Used up") {
		t.Error("1,100 of 1,500 reads as used up: the check ignores the bonus")
	}
}

func TestAClaimedStarIsThePlainLink(t *testing.T) {
	fakeAllowance(t, starOffer(true))
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	body := get(t, h, "/prompts").Body.String()
	if strings.Contains(body, "500 credits</span>") || !strings.Contains(body, `class="gh-star" href="https://github.com/limelit-co/open"`) {
		t.Error("a claimed star still offers credits")
	}
}

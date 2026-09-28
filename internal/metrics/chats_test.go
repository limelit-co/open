// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package metrics

import (
	"context"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/store"
)

func TestChatsListsNewestFirstWithoutBodies(t *testing.T) {
	f := newFixture(t)
	body := strings.Repeat("Rival leads the field. ", 40)
	if _, err := f.db.RecordChat(context.Background(), store.ChatRecord{
		EvaluationID: f.evalID, PromptID: f.prompts["discovery"], TargetID: f.target,
		Status: store.ChatOK, Text: body, Model: "test",
	}); err != nil {
		t.Fatal(err)
	}
	f.chat(t, "use case", store.ChatOK, 1, 0, cite("acme.com", "own", 1))

	rows, err := f.svc.Chats(context.Background(), ChatFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if len([]rune(r.Preview)) > previewRunes+3 {
			t.Errorf("preview is %d runes, want a clip", len([]rune(r.Preview)))
		}
		if r.Access != "api" || r.Engine != "chatgpt" {
			t.Errorf("row %+v lost its target metadata", r)
		}
	}
	if rows[0].Prompt == "" {
		t.Error("the prompt text must travel with the answer")
	}
}

func TestChatsFilterByMentioned(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 1, 0)
	f.chat(t, "use case", store.ChatOK, 0, 1)
	f.chat(t, "comparison", store.ChatOK, 0, 0)

	yes, no := true, false
	got, err := f.svc.Chats(context.Background(), ChatFilter{Mentioned: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Mentioned {
		t.Fatalf("mentioned=true returned %d rows, want 1", len(got))
	}

	// The answers that left you out are the half worth reading, so the
	// negative filter has to work as well as the positive one.
	missed, err := f.svc.Chats(context.Background(), ChatFilter{Mentioned: &no})
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 2 {
		t.Fatalf("mentioned=false returned %d rows, want 2", len(missed))
	}
	for _, r := range missed {
		if r.Mentioned {
			t.Errorf("row %d is mentioned but came back under mentioned=false", r.ID)
		}
	}
}

func TestChatsFilterByStatusKeepsFailuresReachable(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 1, 0)
	f.chat(t, "use case", store.ChatNoAnswerSurface, 0, 0)

	// Excluded from every metric, still readable: a user needs to see that
	// the surface did not render rather than wonder where the answer went.
	got, err := f.svc.Chats(context.Background(), ChatFilter{Status: store.ChatNoAnswerSurface})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != store.ChatNoAnswerSurface {
		t.Fatalf("got %+v, want the one no_answer_surface row", got)
	}
}

func TestChatReturnsBrandsAndSourcesInOrder(t *testing.T) {
	f := newFixture(t)
	id := f.chat(t, "discovery", store.ChatOK, 2, 1,
		cite("rival.com", "competitor", 1), cite("g2.com", "informational", 2))

	got, err := f.svc.Chat(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text == "" {
		t.Error("the detail view must carry the body")
	}
	if len(got.Brands) != 2 || got.Citations != 2 {
		t.Fatalf("brands=%d citations=%d, want 2 and 2", len(got.Brands), got.Citations)
	}
	if !got.Mentioned {
		t.Error("an answer naming the property must report Mentioned")
	}
	var own *ChatBrand
	for i := range got.Brands {
		if got.Brands[i].IsOwn {
			own = &got.Brands[i]
		}
	}
	if own == nil || own.Position != 2 {
		t.Fatalf("own brand = %+v, want position 2", own)
	}
	if got.Sources[0].Position != 1 || got.Sources[1].Position != 2 {
		t.Errorf("sources out of order: %+v", got.Sources)
	}
}

func TestSourceURLsDrillIntoOneSite(t *testing.T) {
	f := newFixture(t)
	twice := store.Citation{URL: "https://g2.com/best", Host: "g2.com", Site: "g2.com", Title: "Best tools", Position: 1, SourceType: "informational"}
	other := store.Citation{URL: "https://g2.com/compare", Host: "g2.com", Site: "g2.com", Position: 2, SourceType: "informational"}
	f.chat(t, "discovery", store.ChatOK, 0, 0, twice, other)
	f.chat(t, "use case", store.ChatOK, 0, 0, twice, cite("reddit.com", "social", 2))

	// Both branches: the windowed query mixes ?1 with bare ? placeholders,
	// so its argument numbering is worth pinning.
	for _, w := range []Window{{}, {Days: 30}} {
		urls, err := f.svc.SourceURLs(context.Background(), "g2.com", w, 10)
		if err != nil {
			t.Fatalf("window %+v: %v", w, err)
		}
		if len(urls) != 2 || urls[0].URL != "https://g2.com/best" || urls[0].Citations != 2 {
			t.Fatalf("window %+v: urls = %+v", w, urls)
		}
	}

	urls, err := f.svc.SourceURLs(context.Background(), "g2.com", Window{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 2 {
		t.Fatalf("urls = %d, want 2", len(urls))
	}
	if urls[0].URL != "https://g2.com/best" || urls[0].Citations != 2 || urls[0].Answers != 2 {
		t.Fatalf("top url = %+v, want /best cited twice across two answers", urls[0])
	}
	if urls[0].Title != "Best tools" {
		t.Errorf("title = %q, want the recorded one", urls[0].Title)
	}
}

func TestPreviewClipsOnRunesNotBytes(t *testing.T) {
	// A byte cut through a multi-byte character renders as a replacement
	// glyph, which looks like data corruption rather than a clip.
	text := strings.Repeat("café ", 200)
	got := preview(text)
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("preview did not clip: %q", got)
	}
	if strings.ContainsRune(got, '�') {
		t.Error("preview cut through a rune")
	}
	if n := len([]rune(got)); n > previewRunes+3 {
		t.Errorf("preview is %d runes, want at most %d", n, previewRunes+3)
	}
}

func TestPreviewLeavesShortAnswersAlone(t *testing.T) {
	if got := preview("Acme  leads\nthe field."); got != "Acme leads the field." {
		t.Errorf("preview = %q, want whitespace collapsed and no ellipsis", got)
	}
}

func TestCountChatsCountsPastThePageSize(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 7; i++ {
		f.chat(t, "discovery", store.ChatOK, 1, 0)
	}
	f.chat(t, "use case", store.ChatOK, 0, 0)

	page, err := f.svc.Chats(context.Background(), ChatFilter{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 3 {
		t.Fatalf("page = %d, want 3", len(page))
	}
	n, err := f.svc.CountChats(context.Background(), ChatFilter{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Fatalf("count = %d, want 8: paging off a capped count hides rows", n)
	}

	// The count has to honour the same filter the page did.
	yes := true
	n, err = f.svc.CountChats(context.Background(), ChatFilter{Mentioned: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("mentioned count = %d, want 7", n)
	}
}

func TestChatsOffsetWalksThePages(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		f.chat(t, "discovery", store.ChatOK, 1, 0)
	}
	seen := map[int64]bool{}
	for off := 0; off < 5; off += 2 {
		page, err := f.svc.Chats(context.Background(), ChatFilter{Limit: 2, Offset: off})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page {
			if seen[r.ID] {
				t.Fatalf("chat %d appeared on two pages", r.ID)
			}
			seen[r.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("walked %d rows, want 5", len(seen))
	}
}

func TestChatCarriesTheFanOut(t *testing.T) {
	f := newFixture(t)
	id, err := f.db.RecordChat(context.Background(), store.ChatRecord{
		EvaluationID: f.evalID, PromptID: f.prompts["discovery"], TargetID: f.target,
		Status: store.ChatOK, Text: "Rival leads.",
		FanOut: []string{"best widget tools 2026", "widget tools comparison"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Chat(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FanOut) != 2 {
		t.Fatalf("fanout = %v, want 2 queries in order", got.FanOut)
	}
	if got.FanOut[0] != "best widget tools 2026" {
		t.Errorf("fanout order lost: %v", got.FanOut)
	}
}

// TestFanOutIsAbsenceNotZero: a provider that does not report its searches
// leaves this empty, and that must not read as "the engine searched nothing".
func TestFanOutAbsentWhenProviderDoesNotReport(t *testing.T) {
	f := newFixture(t)
	id := f.chat(t, "discovery", store.ChatOK, 1, 0)
	got, err := f.svc.Chat(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.FanOut != nil {
		t.Fatalf("fanout = %v, want nil", got.FanOut)
	}
}

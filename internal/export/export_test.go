// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/metrics"
	"github.com/limelit-co/open/internal/store"
)

// seeded builds an instance with one of everything an export has to carry.
func seeded(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "limelit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.SaveProperty(ctx, store.Property{
		Name: "Acme", Domain: "acme.com", Aliases: []string{"Acme Corp"},
	}); err != nil {
		t.Fatal(err)
	}
	rival, err := db.AddCompetitor(ctx, store.Competitor{Name: "Rival", Domain: "rival.com"})
	if err != nil {
		t.Fatal(err)
	}
	promptID, err := db.AddPrompt(ctx, store.Prompt{
		Text: "best widget tools", Category: "discovery", Active: true, Tags: []string{"core"},
	})
	if err != nil {
		t.Fatal(err)
	}
	brandedID, err := db.AddPrompt(ctx, store.Prompt{Text: "is acme good", Branded: true, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := db.AddTarget(ctx, store.Target{
		Spec: "chatgpt:openai:online", Engine: "chatgpt", Provider: "openai", Online: true, Access: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	evalID, err := db.CreateEvaluation(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}

	rank := 2
	for _, p := range []int64{promptID, brandedID} {
		if _, err := db.RecordChat(ctx, store.ChatRecord{
			EvaluationID: evalID, PromptID: p, TargetID: targetID,
			Status: store.ChatOK, Text: "Rival leads, Acme is second.", Model: "test",
			InputTokens: 10, OutputTokens: 20, Calls: 1,
			Mentions: []store.Mention{
				{BrandName: "Acme", BrandKey: "acme", ListRank: &rank, OffsetStart: 13, OffsetEnd: 17},
				{CompetitorID: &rival, BrandName: "Rival", BrandKey: "rival"},
			},
			Citations: []store.Citation{{URL: "https://g2.com/x", Host: "g2.com", Site: "g2.com", Position: 1, SourceType: "informational"}},
			FanOut:    []string{"best widget tools 2026"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.FinishEvaluation(ctx, evalID, store.EvaluationDone); err != nil {
		t.Fatal(err)
	}
	return db
}

func exportJSON(t *testing.T, db *store.DB, opts Options) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteJSON(context.Background(), db, &buf, opts); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("the export is not valid JSON: %v\n%s", err, buf.String())
	}
	return doc
}

func TestExportCarriesEveryTable(t *testing.T) {
	doc := exportJSON(t, seeded(t), Options{Now: "2026-09-12T00:00:00Z"})

	if v, _ := doc["schema_version"].(float64); int(v) != SchemaVersion {
		t.Errorf("schema_version = %v, want %d", doc["schema_version"], SchemaVersion)
	}
	for _, table := range tables {
		if _, ok := doc[table]; !ok {
			t.Errorf("the export is missing %q", table)
		}
	}
	for table, want := range map[string]int{
		"competitors": 1, "prompts": 2, "targets": 1, "evaluations": 1,
		"chats": 2, "mentions": 4, "citations": 2, "fanout": 2, "usage": 1,
	} {
		rows, _ := doc[table].([]any)
		if len(rows) != want {
			t.Errorf("%s = %d rows, want %d", table, len(rows), want)
		}
	}
	// The property is a singleton, so an object. A one-element array would
	// make every consumer write an index.
	prop, ok := doc["property"].(map[string]any)
	if !ok {
		t.Fatalf("property = %T, want an object", doc["property"])
	}
	if prop["name"] != "Acme" || prop["domain"] != "acme.com" {
		t.Errorf("property = %v", prop)
	}
	if aliases, _ := prop["aliases"].([]any); len(aliases) != 1 {
		t.Errorf("aliases = %v", prop["aliases"])
	}
}

// TestExportTypesAreNotAllStrings. SQLite is loosely typed and the driver
// hands back bytes, so without coercion an id exports as "1" and a boolean as
// "1", and every consumer has to guess.
func TestExportTypesAreNotAllStrings(t *testing.T) {
	doc := exportJSON(t, seeded(t), Options{})

	prompts, _ := doc["prompts"].([]any)
	var branded, plain map[string]any
	for _, p := range prompts {
		row := p.(map[string]any)
		if row["branded"] == true {
			branded = row
		} else {
			plain = row
		}
	}
	if branded == nil || plain == nil {
		t.Fatalf("branded flag did not survive as a boolean: %v", prompts)
	}
	if _, ok := plain["id"].(float64); !ok {
		t.Errorf("id = %T, want a number", plain["id"])
	}
	if _, ok := plain["tags"].([]any); !ok {
		t.Errorf("tags = %T, want an array, not a JSON string inside JSON", plain["tags"])
	}

	chats, _ := doc["chats"].([]any)
	chat := chats[0].(map[string]any)
	for _, numeric := range []string{"id", "prompt_id", "target_id", "input_tokens", "output_tokens", "calls"} {
		if _, ok := chat[numeric].(float64); !ok {
			t.Errorf("chat.%s = %T, want a number", numeric, chat[numeric])
		}
	}

	// A null stays null rather than becoming the string "NULL".
	mentions, _ := doc["mentions"].([]any)
	var sawNull bool
	for _, m := range mentions {
		if m.(map[string]any)["competitor_id"] == nil {
			sawNull = true
		}
	}
	if !sawNull {
		t.Error("the property's own mentions should carry a null competitor_id")
	}
}

// TestSinceCascadesToEverythingHangingOffAChat. Exporting a mention whose
// chat was filtered out would produce a file that does not describe anything.
func TestSinceCascadesToEverythingHangingOffAChat(t *testing.T) {
	doc := exportJSON(t, seeded(t), Options{Since: "2099-01-01"})

	for _, table := range []string{"chats", "mentions", "citations", "fanout", "usage", "evaluations"} {
		rows, _ := doc[table].([]any)
		if len(rows) != 0 {
			t.Errorf("%s = %d rows past the since date, want 0", table, len(rows))
		}
	}
	// Configuration is not time series and survives the filter, otherwise an
	// export scoped to last week would describe an instance that tracks
	// nothing.
	for _, table := range []string{"competitors", "prompts", "targets"} {
		rows, _ := doc[table].([]any)
		if len(rows) == 0 {
			t.Errorf("%s was filtered away; configuration is not time series", table)
		}
	}
}

// TestExportRoundTripsEveryMetric is the acceptance criterion: export, load
// into a fresh instance, and every number is identical.
func TestExportRoundTripsEveryMetric(t *testing.T) {
	ctx := context.Background()
	source := seeded(t)

	var buf bytes.Buffer
	if err := WriteJSON(ctx, source, &buf, Options{}); err != nil {
		t.Fatal(err)
	}

	fresh, err := store.Open(ctx, filepath.Join(t.TempDir(), "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := importJSON(ctx, fresh, buf.Bytes()); err != nil {
		t.Fatalf("the export could not be loaded back: %v", err)
	}

	before, err := metrics.New(source).Overview(ctx, metrics.Window{})
	if err != nil {
		t.Fatal(err)
	}
	after, err := metrics.New(fresh).Overview(ctx, metrics.Window{})
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]float64{
		"visibility":     {before.Visibility, after.Visibility},
		"share of voice": {before.ShareOfVoice, after.ShareOfVoice},
		"citation share": {before.CitationShare, after.CitationShare},
		"mean position":  {before.MeanPosition, after.MeanPosition},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: %v before, %v after the round trip", name, pair[0], pair[1])
		}
	}
	if before.Answers != after.Answers || before.AnswersWithMentions != after.AnswersWithMentions {
		t.Errorf("denominators moved: %d/%d before, %d/%d after",
			before.AnswersWithMentions, before.Answers, after.AnswersWithMentions, after.Answers)
	}
}

func TestWriteCSVProducesOneFilePerTable(t *testing.T) {
	dir := t.TempDir()
	files, err := WriteCSV(context.Background(), seeded(t), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(tables) {
		t.Fatalf("wrote %d files, want %d", len(files), len(tables))
	}

	f, err := os.Open(filepath.Join(dir, "chats.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("chats.csv is not valid CSV: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("chats.csv has %d lines, want a header and 2 rows", len(records))
	}
	if records[0][0] != "id" {
		t.Errorf("header = %v", records[0])
	}
	// An answer containing a comma, a quote or a newline must survive.
	if !strings.Contains(records[1][5], "Rival leads") {
		t.Errorf("the answer body did not survive: %q", records[1][5])
	}
}

func TestCSVRendersNullAsAnEmptyCell(t *testing.T) {
	// "null" in a spreadsheet cell reads as the word null.
	if got := csvCell(nil); got != "" {
		t.Errorf("csvCell(nil) = %q, want empty", got)
	}
	if got := csvCell(true); got != "true" {
		t.Errorf("csvCell(true) = %q", got)
	}
	if got := csvCell([]string{"a", "b"}); got != "a|b" {
		t.Errorf("csvCell(list) = %q", got)
	}
}

// TestExportStreamsWithoutBuffering guards the property the acceptance
// criterion names: the document is written as it is read, not assembled first.
func TestExportStreamsWithoutBuffering(t *testing.T) {
	db := seeded(t)
	seen := &chunkCounter{}
	if err := WriteJSON(context.Background(), db, seen, Options{}); err != nil {
		t.Fatal(err)
	}
	// One Write per fragment and per row, so a large export never exists in
	// memory as one buffer.
	if seen.writes < len(tables) {
		t.Errorf("the document was written in %d chunks, which suggests it was assembled first", seen.writes)
	}
}

type chunkCounter struct{ writes int }

func (c *chunkCounter) Write(p []byte) (int, error) {
	c.writes++
	return len(p), nil
}

// importJSON loads an exported document into an empty instance. It lives in
// the test rather than the package because the open core does not import:
// this is here to prove the export is complete enough to round trip.
func importJSON(ctx context.Context, db *store.DB, payload []byte) error {
	var doc struct {
		Property struct {
			Name    string   `json:"name"`
			Domain  string   `json:"domain"`
			Aliases []string `json:"aliases"`
		} `json:"property"`
		Competitors []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Domain string `json:"domain"`
		} `json:"competitors"`
		Prompts []struct {
			ID       int64  `json:"id"`
			Text     string `json:"text"`
			Category string `json:"category"`
			Branded  bool   `json:"branded"`
			Active   bool   `json:"active"`
		} `json:"prompts"`
		Targets []struct {
			ID       int64  `json:"id"`
			Spec     string `json:"spec"`
			Engine   string `json:"engine"`
			Provider string `json:"provider"`
			Online   bool   `json:"online"`
			Access   string `json:"access"`
		} `json:"targets"`
		Chats []struct {
			ID           int64  `json:"id"`
			PromptID     int64  `json:"prompt_id"`
			TargetID     int64  `json:"target_id"`
			Status       string `json:"status"`
			Text         string `json:"text"`
			Model        string `json:"model"`
			InputTokens  int    `json:"input_tokens"`
			OutputTokens int    `json:"output_tokens"`
			Calls        int    `json:"calls"`
		} `json:"chats"`
		Mentions []struct {
			ChatID       int64  `json:"chat_id"`
			CompetitorID *int64 `json:"competitor_id"`
			BrandKey     string `json:"brand_key"`
			BrandName    string `json:"brand_name"`
			OffsetStart  int    `json:"offset_start"`
			OffsetEnd    int    `json:"offset_end"`
			ListRank     *int   `json:"list_rank"`
		} `json:"mentions"`
		Citations []struct {
			ChatID     int64  `json:"chat_id"`
			URL        string `json:"url"`
			Host       string `json:"host"`
			Site       string `json:"site"`
			Position   int    `json:"position"`
			SourceType string `json:"source_type"`
		} `json:"citations"`
		FanOut []struct {
			ChatID int64  `json:"chat_id"`
			Query  string `json:"query"`
		} `json:"fanout"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return err
	}

	if err := db.SaveProperty(ctx, store.Property{
		Name: doc.Property.Name, Domain: doc.Property.Domain, Aliases: doc.Property.Aliases,
	}); err != nil {
		return err
	}
	competitorIDs := map[int64]int64{}
	for _, c := range doc.Competitors {
		id, err := db.AddCompetitor(ctx, store.Competitor{Name: c.Name, Domain: c.Domain})
		if err != nil {
			return err
		}
		competitorIDs[c.ID] = id
	}
	promptIDs := map[int64]int64{}
	for _, p := range doc.Prompts {
		id, err := db.AddPrompt(ctx, store.Prompt{
			Text: p.Text, Category: p.Category, Branded: p.Branded, Active: p.Active,
		})
		if err != nil {
			return err
		}
		promptIDs[p.ID] = id
	}
	targetIDs := map[int64]int64{}
	for _, tg := range doc.Targets {
		id, err := db.AddTarget(ctx, store.Target{
			Spec: tg.Spec, Engine: tg.Engine, Provider: tg.Provider, Online: tg.Online, Access: tg.Access,
		})
		if err != nil {
			return err
		}
		targetIDs[tg.ID] = id
	}

	byChat := map[int64]*store.ChatRecord{}
	var order []int64
	for _, c := range doc.Chats {
		rec := &store.ChatRecord{
			PromptID: promptIDs[c.PromptID], TargetID: targetIDs[c.TargetID],
			Status: c.Status, Text: c.Text, Model: c.Model,
			InputTokens: c.InputTokens, OutputTokens: c.OutputTokens, Calls: c.Calls,
		}
		byChat[c.ID] = rec
		order = append(order, c.ID)
	}
	for _, m := range doc.Mentions {
		rec, ok := byChat[m.ChatID]
		if !ok {
			continue
		}
		var comp *int64
		if m.CompetitorID != nil {
			mapped := competitorIDs[*m.CompetitorID]
			comp = &mapped
		}
		rec.Mentions = append(rec.Mentions, store.Mention{
			CompetitorID: comp, BrandKey: m.BrandKey, BrandName: m.BrandName,
			OffsetStart: m.OffsetStart, OffsetEnd: m.OffsetEnd, ListRank: m.ListRank,
		})
	}
	for _, c := range doc.Citations {
		if rec, ok := byChat[c.ChatID]; ok {
			rec.Citations = append(rec.Citations, store.Citation{
				URL: c.URL, Host: c.Host, Site: c.Site, Position: c.Position, SourceType: c.SourceType,
			})
		}
	}
	for _, f := range doc.FanOut {
		if rec, ok := byChat[f.ChatID]; ok {
			rec.FanOut = append(rec.FanOut, f.Query)
		}
	}
	for _, id := range order {
		if _, err := db.RecordChat(ctx, *byChat[id]); err != nil {
			return err
		}
	}
	return nil
}

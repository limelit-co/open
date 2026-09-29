// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// recompute reads a `limelit export` and recomputes the Overview's numbers
// from the exported rows alone.
//
// It follows the definitions in docs/methodology.md and does not import
// internal/metrics, so a number it agrees with is a number the export can
// prove, and a number it disagrees with is a bug in one of the two.
//
//	limelit export > export.json
//	go run ./tools/recompute -in export.json -days 30
//
// The window ends at the export's own timestamp, so run the export right
// after reading the screen.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

type export struct {
	ExportedAt time.Time `json:"exported_at"`
	Property   struct {
		Name string `json:"name"`
	} `json:"property"`
	Competitors []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"competitors"`
	Prompts []struct {
		ID       int64  `json:"id"`
		Text     string `json:"text"`
		Category string `json:"category"`
		Branded  bool   `json:"branded"`
		Active   bool   `json:"active"`
	} `json:"prompts"`
	Targets []struct {
		ID     int64  `json:"id"`
		Engine string `json:"engine"`
		Access string `json:"access"`
	} `json:"targets"`
	Chats []struct {
		ID        int64  `json:"id"`
		CreatedAt string `json:"created_at"`
		Status    string `json:"status"`
		PromptID  int64  `json:"prompt_id"`
		TargetID  int64  `json:"target_id"`
	} `json:"chats"`
	Mentions []struct {
		ChatID       int64  `json:"chat_id"`
		CompetitorID *int64 `json:"competitor_id"`
		ListRank     *int   `json:"list_rank"`
	} `json:"mentions"`
	Citations []struct {
		ChatID     int64  `json:"chat_id"`
		Site       string `json:"site"`
		SourceType string `json:"source_type"`
	} `json:"citations"`
}

// own is the brand key for the property; competitors are keyed by id.
const own = int64(0)

type chat struct {
	id       int64
	at       time.Time
	day      string
	prompt   int64
	target   int64
	branded  bool
	category string
	mentions []mention
	cites    []cite
}

type mention struct {
	brand int64
	rank  *int
}

type cite struct{ site, sourceType string }

func main() {
	in := flag.String("in", "-", "export JSON, or - for stdin")
	days := flag.Int("days", 30, "window in days; 0 for everything")
	flag.Parse()

	var r io.Reader = os.Stdin
	if *in != "-" {
		f, err := os.Open(*in)
		if err != nil {
			fail(err)
		}
		defer f.Close()
		r = f
	}
	var ex export
	if err := json.NewDecoder(r).Decode(&ex); err != nil {
		fail(fmt.Errorf("decode export: %w", err))
	}
	report(os.Stdout, &ex, *days)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "recompute:", err)
	os.Exit(1)
}

func report(w io.Writer, ex *export, days int) {
	names := map[int64]string{own: ex.Property.Name}
	for _, c := range ex.Competitors {
		names[c.ID] = c.Name
	}
	type promptInfo struct {
		text, category  string
		branded, active bool
	}
	prompts := map[int64]promptInfo{}
	var promptOrder []int64
	for _, p := range ex.Prompts {
		prompts[p.ID] = promptInfo{p.Text, p.Category, p.Branded, p.Active}
		promptOrder = append(promptOrder, p.ID)
	}
	engineOf := map[int64]string{}
	var targetOrder []int64
	for _, t := range ex.Targets {
		engineOf[t.ID] = t.Engine + "/" + t.Access
		targetOrder = append(targetOrder, t.ID)
	}

	// Every ok answer, with what hangs off it.
	byID := map[int64]*chat{}
	var all []*chat
	for _, c := range ex.Chats {
		if c.Status != "ok" {
			continue
		}
		at, err := time.Parse("2006-01-02 15:04:05", c.CreatedAt)
		if err != nil {
			fail(fmt.Errorf("chat %d: %w", c.ID, err))
		}
		p := prompts[c.PromptID]
		ch := &chat{id: c.ID, at: at, day: at.Format("2006-01-02"), prompt: c.PromptID, target: c.TargetID, branded: p.branded, category: p.category}
		byID[c.ID] = ch
		all = append(all, ch)
	}
	for _, m := range ex.Mentions {
		if ch := byID[m.ChatID]; ch != nil {
			b := own
			if m.CompetitorID != nil {
				b = *m.CompetitorID
			}
			ch.mentions = append(ch.mentions, mention{b, m.ListRank})
		}
	}
	for _, c := range ex.Citations {
		if ch := byID[c.ChatID]; ch != nil {
			ch.cites = append(ch.cites, cite{c.Site, c.SourceType})
		}
	}

	now := ex.ExportedAt.UTC()
	inWindow := func(from, to time.Time, withBranded bool) []*chat {
		var out []*chat
		for _, c := range all {
			if c.branded && !withBranded {
				continue
			}
			if days > 0 && (c.at.Before(from) || !c.at.Before(to)) {
				continue
			}
			out = append(out, c)
		}
		return out
	}
	from := now.AddDate(0, 0, -days)
	far := now.AddDate(1, 0, 0)
	window := inWindow(from, far, false)

	fmt.Fprintf(w, "window: %d days ending %s, %d answers (branded prompts excluded)\n\n", days, now.Format(time.RFC3339), len(window))

	// Headline.
	h := headline(window)
	fmt.Fprintln(w, "HEADLINE")
	fmt.Fprintf(w, "  visibility      %s  (%d of %d answers)\n", pct(h.named, h.answers), h.named, h.answers)
	if days > 0 {
		prev := headline(inWindow(now.AddDate(0, 0, -2*days), from, false))
		if prev.answers > 0 {
			fmt.Fprintf(w, "  vs previous     %+.2f pts  (previous window %s of %d)\n",
				ratio(h.named, h.answers)-ratio(prev.named, prev.answers), pct(prev.named, prev.answers), prev.answers)
		}
	}
	fmt.Fprintf(w, "  share of voice  %s  (%d of %d mentions)\n", pct(h.ownMentions, h.allMentions), h.ownMentions, h.allMentions)
	fmt.Fprintf(w, "  avg position    %s  (%d ranked mentions)\n", mean(h.rankSum, h.ranked), h.ranked)
	fmt.Fprintf(w, "  citation share  %s  (%d of %d citations)\n", pct(h.ownCites, h.allCites), h.ownCites, h.allCites)

	// The trend under the visibility tile.
	perDay := map[string][2]int{}
	for _, c := range window {
		v := perDay[c.day]
		v[1]++
		if names1(c)[own] {
			v[0]++
		}
		perDay[c.day] = v
	}
	dayKeys := sortedKeys(perDay)
	lo, hi := 101.0, -1.0
	for _, d := range dayKeys {
		v := ratio(perDay[d][0], perDay[d][1])
		lo, hi = min(lo, v), max(hi, v)
	}
	if len(dayKeys) > 0 {
		fmt.Fprintf(w, "  trend           %d measured days, %.2f%% to %.2f%%\n", len(dayKeys), lo, hi)
	}

	// Standings, ranked by mentions.
	type standing struct {
		brand                          int64
		answers, mentions, ranked, sum int
	}
	st := map[int64]*standing{own: {brand: own}}
	for _, c := range window {
		for b := range names1(c) {
			if st[b] == nil {
				st[b] = &standing{brand: b}
			}
			st[b].answers++
		}
		for _, m := range c.mentions {
			s := st[m.brand]
			s.mentions++
			if m.rank != nil {
				s.ranked++
				s.sum += *m.rank
			}
		}
	}
	var rows []*standing
	for _, s := range st {
		rows = append(rows, s)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].mentions != rows[j].mentions {
			return rows[i].mentions > rows[j].mentions
		}
		return names[rows[i].brand] < names[rows[j].brand]
	})
	fmt.Fprintln(w, "\nSTANDINGS (by mentions)")
	fmt.Fprintf(w, "  %-4s %-12s %8s %8s %11s %8s %8s\n", "#", "brand", "mentions", "answers", "visibility", "share", "avg pos")
	for i, s := range rows {
		fmt.Fprintf(w, "  %-4d %-12s %8d %8d %11s %8s %8s\n", i+1, names[s.brand], s.mentions, s.answers,
			pct(s.answers, h.answers), pct(s.mentions, h.allMentions), mean(s.sum, s.ranked))
	}

	// The race: each brand's answers over the day's answers, every measured
	// day. The last line is what the right edge of the chart shows.
	race := map[string]map[int64]int{}
	for _, c := range window {
		if race[c.day] == nil {
			race[c.day] = map[int64]int{}
		}
		for b := range names1(c) {
			race[c.day][b]++
		}
	}
	fmt.Fprintln(w, "\nRACE (visibility per brand per measured day)")
	for _, d := range dayKeys {
		fmt.Fprintf(w, "  %s %3d answers ", d, perDay[d][1])
		for _, s := range rows {
			fmt.Fprintf(w, " %s %s", names[s.brand], pct(race[d][s.brand], perDay[d][1]))
		}
		fmt.Fprintln(w)
	}

	// Engines: every brand's visibility within each engine's own answers.
	type group struct {
		answers int
		named   map[int64]int
	}
	groups := map[string]*group{}
	for _, c := range window {
		g := groups[engineOf[c.target]]
		if g == nil {
			g = &group{named: map[int64]int{}}
			groups[engineOf[c.target]] = g
		}
		g.answers++
		for b := range names1(c) {
			g.named[b]++
		}
	}
	fmt.Fprintln(w, "\nBY ENGINE")
	for _, key := range sortedKeys(groups) {
		g := groups[key]
		leader, place := own, 1
		for b, n := range g.named {
			if n > g.named[leader] || (n == g.named[leader] && names[b] < names[leader]) {
				leader = b
			}
			if n > g.named[own] {
				place++
			}
		}
		fmt.Fprintf(w, "  %-20s %4d answers  you %s, place %d of %d, leader %s %s\n",
			key, g.answers, pct(g.named[own], g.answers), place, len(st), names[leader], pct(g.named[leader], g.answers))
		fmt.Fprintf(w, "  %-20s ", "")
		for _, s := range rows {
			fmt.Fprintf(w, " %s %s", names[s.brand], pct(g.named[s.brand], g.answers))
		}
		fmt.Fprintln(w)
	}

	// Question type.
	cats := map[string][2]int{}
	for _, c := range window {
		v := cats[c.category]
		v[1]++
		if names1(c)[own] {
			v[0]++
		}
		cats[c.category] = v
	}
	fmt.Fprintln(w, "\nBY QUESTION TYPE")
	for _, k := range sortedKeys(cats) {
		fmt.Fprintf(w, "  %-12s %s  (%d of %d answers)\n", k, pct(cats[k][0], cats[k][1]), cats[k][0], cats[k][1])
	}

	// Citations.
	mix := map[string]int{}
	mixByDay := map[string]map[string]int{}
	sites := map[string]int{}
	siteType := map[string]string{}
	for _, c := range window {
		for _, ct := range c.cites {
			mix[ct.sourceType]++
			if mixByDay[c.day] == nil {
				mixByDay[c.day] = map[string]int{}
			}
			mixByDay[c.day][ct.sourceType]++
			sites[ct.site]++
			siteType[ct.site] = ct.sourceType
		}
	}
	types := sortedKeys(mix)
	fmt.Fprintln(w, "\nCITATIONS BY SOURCE TYPE")
	for _, k := range types {
		fmt.Fprintf(w, "  %-14s %5d  %s\n", k, mix[k], pct(mix[k], h.allCites))
	}
	fmt.Fprintln(w, "\nCITATIONS BY DAY")
	for _, d := range sortedKeys(mixByDay) {
		total := 0
		for _, n := range mixByDay[d] {
			total += n
		}
		fmt.Fprintf(w, "  %s %3d citations ", d, total)
		for _, k := range types {
			fmt.Fprintf(w, " %s %d", k, mixByDay[d][k])
		}
		fmt.Fprintln(w)
	}
	siteKeys := sortedKeys(sites)
	sort.SliceStable(siteKeys, func(i, j int) bool { return sites[siteKeys[i]] > sites[siteKeys[j]] })
	fmt.Fprintln(w, "\nTOP CITED SITES")
	for i, s := range siteKeys {
		if i == 8 {
			break
		}
		fmt.Fprintf(w, "  %-20s %-14s %5d\n", s, siteType[s], sites[s])
	}

	// The grid keeps branded prompts, tagged.
	grid := inWindow(from, far, true)
	type cell struct{ answers, named, ranked, sum int }
	cells := map[[2]int64]*cell{}
	for _, c := range grid {
		k := [2]int64{c.prompt, c.target}
		if cells[k] == nil {
			cells[k] = &cell{}
		}
		cl := cells[k]
		cl.answers++
		if names1(c)[own] {
			cl.named++
		}
		for _, m := range c.mentions {
			if m.brand == own && m.rank != nil {
				cl.ranked++
				cl.sum += *m.rank
			}
		}
	}
	fmt.Fprintln(w, "\nGRID (named/asked, mean position; branded prompts included)")
	fmt.Fprintf(w, "  %-44s", "prompt")
	for _, t := range targetOrder {
		fmt.Fprintf(w, " %-18s", engineOf[t])
	}
	fmt.Fprintln(w, " all")
	colNamed, colAsked := map[int64]int{}, map[int64]int{}
	for _, p := range promptOrder {
		info := prompts[p]
		if !info.active {
			continue
		}
		label := info.text
		if info.branded {
			label += " [branded]"
		}
		fmt.Fprintf(w, "  %-44s", truncate(label, 44))
		rn, ra := 0, 0
		for _, t := range targetOrder {
			cl := cells[[2]int64{p, t}]
			if cl == nil {
				fmt.Fprintf(w, " %-18s", "-")
				continue
			}
			fmt.Fprintf(w, " %-18s", fmt.Sprintf("%d/%d #%s", cl.named, cl.answers, mean(cl.sum, cl.ranked)))
			rn, ra = rn+cl.named, ra+cl.answers
			colNamed[t] += cl.named
			colAsked[t] += cl.answers
		}
		fmt.Fprintf(w, " %d/%d\n", rn, ra)
	}
	fmt.Fprintf(w, "  %-44s", "every prompt")
	for _, t := range targetOrder {
		fmt.Fprintf(w, " %-18s", fmt.Sprintf("%d/%d", colNamed[t], colAsked[t]))
	}
	fmt.Fprintln(w)
}

type head struct {
	answers, named           int
	ownMentions, allMentions int
	ranked, rankSum          int
	ownCites, allCites       int
}

func headline(chats []*chat) head {
	var h head
	for _, c := range chats {
		h.answers++
		if names1(c)[own] {
			h.named++
		}
		for _, m := range c.mentions {
			h.allMentions++
			if m.brand == own {
				h.ownMentions++
				if m.rank != nil {
					h.ranked++
					h.rankSum += *m.rank
				}
			}
		}
		for _, ct := range c.cites {
			h.allCites++
			if ct.sourceType == "own" {
				h.ownCites++
			}
		}
	}
	return h
}

// names1 is the set of brands an answer names: an answer that names a brand
// twice is still one answer for it.
func names1(c *chat) map[int64]bool {
	out := map[int64]bool{}
	for _, m := range c.mentions {
		out[m.brand] = true
	}
	return out
}

func ratio(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}

func pct(part, whole int) string { return fmt.Sprintf("%.2f%%", ratio(part, whole)) }

func mean(sum, n int) string {
	if n == 0 {
		return "none"
	}
	return fmt.Sprintf("%.2f", float64(sum)/float64(n))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

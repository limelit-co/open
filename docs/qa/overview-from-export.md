# Every Overview number, from the export

Item 2 of [#36](https://github.com/limelit-co/open/issues/36): every metric on
the Overview screen is reproducible from `limelit export`.

**Status: holds on a seeded instance. Not yet run on the recorded first-ten-
minutes instance;** rerun it there with the two commands at the end once that
walk exists.

## What was run

`main` at `311fa97`, seeded with `tools/seed` from a synthetic file:

- 537 answers over 24 days: 19 days inside the last 30, five more 35 to 70
  days back, so the windows cut through history.
- Four engines: ChatGPT, Claude and Perplexity through their APIs, Google AI
  Overview scraped.
- The property and four competitors; seven prompts in three categories, one
  of them branded. `tools/seed` does not tag branded prompts, so that one was
  tagged with a single `UPDATE prompt SET branded = 1` after seeding, which is
  what adding it by hand in the dashboard does.
- 1,843 mentions, 206 of them outside any list and so without a rank; answers
  that name the property twice (name and domain) and answers that do not name
  it at all.
- 1,083 citations covering all five source types.

For each window the Overview's text was captured from `/overview?days=N`,
`limelit export` ran straight after, and `tools/recompute` rebuilt every
section from the exported rows. Each recomputed value was then rendered with
the Overview's own rounding and compared with the screen, string for string.

| Window  | Values compared | Mismatches |
| ------- | --------------: | ---------: |
| 7 days  |             253 |          0 |
| 30 days |             573 |          0 |
| 90 days |             672 |          0 |

Covered: the four headline tiles and their counts, the move against the
previous window, the trend's range, every point of the race (each brand on
each measured day), share of voice per brand, every brand on every engine
with your place and the leader, the standings, every cell of the grid with its
mean position and both sets of totals, citations by type and by day and type,
the question types, and the top cited sites. Five deliberate changes to the
recomputed values were each caught by the comparison.

Not covered: the "What the engines searched for" words, because the seed has
no engine searches in it.

The captured screen text and the recompute output for each window are in
[overview-from-export/](overview-from-export/). The comparison itself was a
script kept out of the repository, which has no toolchain but Go; the two
files per window are enough to repeat it by hand or with any script.

## What this proves, and what it does not

It proves that every number on the Overview follows from the rows the export
writes, by the definitions in [methodology.md](../methodology.md).
`tools/recompute` does not import `internal/metrics`; it was written from the
methodology, so agreement is two implementations agreeing, not one agreeing
with itself.

It does not prove the mentions are right. The recompute reads each mention and
its `list_rank` from the export, so it checks the arithmetic on top of the
matcher, not the matcher.

The answers are synthetic. The shapes are the ones real answers take, but
this is not engine output.

## Findings

1. **Prose after a list inherits the last item's rank.** A list item's span
   runs to the start of the next item, and the last item's runs to the end of
   the answer, so "1. Rival 2. Acme", a blank line, then "Orbit is also
   good" gives Orbit rank 2. methodology.md says a mention in prose carries
   no rank, and average position averages it in. The seed above has this
   shape: its 206 unranked mentions all come from answers with no list at
   all, and every prose mention that follows a list carries a rank. The
   Overview and the export agree on those ranks, which is why this is a
   finding about the matcher and not a mismatch above. A fix with tests is
   on the branch `fix/prose-rank-after-list`, with two neighbours found
   while writing it: sub-bullets under an item were counted as items, so
   the next item ranked 4 instead of 2, and a paragraph indented under an
   item restarted the count.
2. **Tied brands have no order in the standings.** Over 90 days Orbit and
   Zenith both have 305 mentions; the screen lists Zenith third and Orbit
   fourth because the query orders by mentions alone and SQLite returns ties
   in whatever order it reaches them. Every value is right; which of the two
   is called third is not defined.

## Rerun it

Read the Overview, then at once:

```bash
limelit export > export.json
go run ./tools/recompute -in export.json -days 30
```

The window ends at the export's timestamp, so an export taken long after the
screen was read can move an answer across the window's edge.

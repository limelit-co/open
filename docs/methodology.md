# Methodology

What Limelit Open counts, how each number is computed, and what it leaves
out. Everything here is a query over rows the runner wrote, so any figure on
any screen can be re-derived from the stored answers and checked by hand.
Where a rule is enforced by a test, the test is named, so the document and
the code cannot drift apart quietly.

In one line: **visibility is the share of stored answers, to prompts that do
not name you, in which your brand string appears.**

## The evidence

One pass asks every active prompt of every enabled target. Each ask produces
one row in `chat` with one of three statuses:

| status | meaning | in metrics? |
|---|---|---|
| `ok` | the engine answered; the text and its citations are stored | yes |
| `failed` | the call did not complete (auth, quota, timeout); the error is stored | no |
| `no_answer_surface` | the surface did not render at all (no AI Overview for that query) | no |

Only `ok` rows enter any metric. A `failed` row is a fact about your setup,
not about the market, and it is kept so the Answers screen and the target's
health line can show it. A `no_answer_surface` row is excluded from every
denominator: the surface said nothing about anyone, and counting it as an
answer that ignored you would drag every number toward zero for reasons that
have nothing to do with you.
Test: `TestNoAnswerSurfaceIsNotABrandMiss`.

For every `ok` row the analyzer writes the derived rows: `mention` (one per
occurrence of a tracked brand, with its list rank where it has one) and
`citation` (one per source the engine attributed, classified). The analyzer
is built once per pass from the property and competitors as they stand when
the pass starts, so every answer in one pass is read by the same rules.

## Finding brands: a text search, on purpose

Mentions come from a deterministic search for the strings you gave us, in the
text we stored. No model reads the answer. That is the design choice the rest
of the method rests on, and it is worth being explicit about both sides.

What it buys: a number that is reproducible (the same answer gives the same
mentions every time), auditable (every mention row points at byte offsets in
the stored text), and free per answer. What it costs: discovery. A brand
nobody listed is invisible, and a sentence such as "I could not find Acme"
counts as a mention, because deciding that it is not one means reading the
sentence, which needs a model. The open core never invents a mention; it
sometimes counts a literal one you would have discounted.
Tests: `TestFindIsDeterministic`, `TestAnAbsenceStatementIsStillAMention`.

The rules of the search:

- **What is searched for.** The brand's name, its aliases, its domain, and,
  for a plain `name.tld` domain, the name part on its own ("KindleToPDF" or
  "Epubor" without the ".com"). A link to a brand's site has put that brand
  in front of the reader, and a domain written in prose counts too. The name
  part is not searched for a subdomain: the first label of
  `marketplace.microsoft.com` is a word, not a brand.
  Tests: `TestDomainInProseCounts`, `TestTheDomainNameCountsWithoutItsDotCom`.
- **A name that is also a description counts only where it reads as a
  name.** "Kindle to PDF" (kindletopdf.com) is a product, and it is also how
  every answer about the category describes the task. A name of that shape,
  two or more words with a connecting word ("to", "for", "and", ...) that run
  together to spell the domain's name part, does not count right after a
  task word ("convert Kindle to PDF"), right before a generic noun ("a Kindle
  to PDF converter") or glued to another capitalized name ("BitRecover Kindle
  to PDF Converter"). Measured on one brand's 200 answers, the plain search
  counted 122 where 44 named the product. The domain and its name part are
  never restricted, and names like "Athena HQ", with no connecting word, are
  searched as before. The same rule decides whether a prompt is branded, so
  "convert Kindle to PDF" is a category question and stays in the headline.
  The cases are in `internal/mentions/testdata/phrase_names.json`, which
  Limelit Cloud's matcher reads too.
  Tests: `TestPhraseNamesFromRealAnswers`, `TestIsBrandedReadsAPhraseNameAsTheCategory`.
- **Whole words only.** "Acme" does not match inside "Acmeify". The boundary
  test is on the neighbouring characters (neither may be a letter or digit),
  so "acme.com" matches even though a dot is not a word character.
  Tests: `TestPartialWordsAreNotMentions`, `TestPunctuationIsAValidBoundary`.
- **Three characters minimum.** A one or two character term matches inside
  ordinary words ("Go" in "good"). Such terms are dropped from the search; a
  wrong mention silently inflates the number the whole product is about, and
  a missing one does not.
  Test: `TestShortBrandNamesAreNotSearched`.
- **Longest match wins.** With "Acme Analytics" and "Acme" both tracked, the
  longer one is reported at that position and the shorter is not.
  Test: `TestLongestBrandWins`.
- **Every occurrence is kept.** The count is evidence; the metrics decide how
  to use it (see distinct answers, below).
  Test: `TestEveryOccurrenceIsKept`.
- **Spellings collapse to one key.** Brand strings are lowercased and
  stripped of everything that is not a letter or digit, so "Run:AI", "RunAI"
  and "run ai" are one brand.
  Test: `TestNormalizeKeyCollapsesSpellings`.
- **Rank comes from lists.** A mention inside a numbered or bulleted list item
  carries that item's 1-based position; a mention in prose carries none, and
  that includes the paragraph after a list. A paragraph or a sub-list indented
  under an item is part of that item, so sub-bullets do not push the next item
  down. A second list in the same answer restarts the count.
  Tests: `TestNumberedListGivesEachBrandItsRank`, `TestBulletedListAlsoRanks`,
  `TestProseMentionHasNoRank`, `TestProseAfterAListHasNoRank`,
  `TestAnIndentedParagraphStaysInItsItem`, `TestNestedBulletsAreNotItems`,
  `TestASecondListRestartsTheRanking`, `TestAnIndentedListAfterProseIsItsOwnList`.

**Stored answers are read again when the rules or the brands change.** The
mentions, classified citations and branded flags derived from an answer are
rebuilt from the stored answer text and the citations the engine gave,
whenever the matching rules change in a new release or you add, remove or
rename a brand. A competitor added today is counted in last month's answers,
and a rule fix corrects the history instead of starting a break in the trend.
It runs when the server starts, after an edit, and at the start and end of a
pass, never at the same time as one, and it rewrites everything in one
transaction. The answer text itself is never changed.
Tests: `TestStoredAnswersAreReadAgainWhenTheRulesOrBrandsChange`,
`TestReadingAgainWaitsForAPassInFlight`.

## Classifying citations

Each source the engine attributed becomes one `citation` row with the URL as
given, a normalised host (leading `www.` dropped), a site (the registrable
domain, so `help.acme.com` and `www.acme.com` group under one company), its
1-based position in the engine's list, and a type:

| type | rule |
|---|---|
| `own` | the site is the property's domain |
| `competitor` | the site is a tracked competitor's domain |
| `social` | the site is on a short list of social and review networks |
| `informational` | the site is on a short list of reference sources, or its host ends in a public-institution suffix such as `.gov` or `.edu` |
| `other` | everything else, which is honest rather than a guess |

`own` wins over `competitor` when a competitor is misconfigured with your own
domain: counting your site as a rival's would be the worst possible error.
Tests: `TestClassifyEveryType`, `TestSiteGroupsSubdomains`,
`TestOwnDomainWinsOverAMisconfiguredCompetitor`.

## The window

Every metric is computed over a window of days: 7, 30 or 90 on the screens,
any number of days through the tools, and 0 for everything ever. The filter
every query starts from is the same:

```
chat.status = 'ok'
AND chat.created_at >= now - N days        (when N > 0)
AND prompt.branded = 0                      (except where noted)
```

## Denominators

Two rules run through every number, and they are where visibility metrics
usually go wrong.

**Branded prompts are excluded from the headline.** A prompt is tagged
`branded` when it names your own brand, by the same matcher that reads
answers. Asking an engine about yourself and counting the answer measures the
question, not the market. Branded prompts stay in the grid, tagged, because
what an engine says when asked directly is worth reading; they never enter
the overview, the standings, the race, the engine breakdown, the series or
the citation numbers.
Tests: `TestVisibilityExcludesBrandedPrompts`, `TestMatrixKeepsBrandedPromptsTagged`,
`TestIsBranded`, `TestPromptAddedByHandIsTaggedBranded`.

**Answers are counted as distinct answers.** Joining `mention` onto `chat`
fans one answer out into one row per mention. The property usually has
several searchable strings, so "Acme (acme.com) leads" is two own mentions in
one answer; a plain count would report two answers of which one named you,
which is 50% visibility on an answer that named you. Every count of answers
across that join is `COUNT(DISTINCT chat.id)`.
Tests: `TestAnswerCountsSurviveMultipleOwnMentions`,
`TestBrandSeriesCountsAnswersNotMentions`.

## The metrics

Every ratio is rounded to two decimals of a percentage. A window with nothing
in it returns 0, never NaN, which is the classic way a dashboard prints
"NaN%" on its first day.
Test: `TestEmptyWindowIsZeroNotNaN`.

**Answers (n).** The count of `ok`, unbranded answers in the window. It is
computed once and every other headline number divides by it, so two figures
on one screen cannot disagree about the denominator. Every result carries it.

**Visibility.** Answers with at least one own mention, over answers.

```
COUNT(DISTINCT chat.id WHERE an own mention exists) / answers
```

**Share of voice.** Own mentions over every tracked brand's mentions in the
same answers. Mentions, not answers: an answer that names you once and a
rival five times is one answer for each of you but a 1:5 share.
Test: `TestShareOfVoiceSplitsMentionsNotAnswers`.

**Position.** The mean list rank of own mentions that have one. Mentions in
prose do not enter it, and a brand that never appears in a ranked list has no
position rather than a position of zero.
Test: `TestMeanPositionIgnoresUnrankedMentions`.

**Citation share.** Citations of type `own`, over all citations in the
window's answers.

**Visibility by prompt category.** The same visibility, grouped by the
prompt's category. Discovery prompts ("best tools for X") are the only shape
that measures whether an engine reaches for you unprompted, so a low headline
with healthy discovery means something different from the reverse, and the
two are never blended into one number without the breakdown beside it.
Test: `TestCategoryVisibilitySeparatesDiscoveryFromComparison`.

**Standings.** Every brand that was mentioned, with its answers, mentions,
visibility (its answers over the window's answers), share of voice and mean
position, ranked by mentions. The property is always present, even at zero:
"you are not in this conversation" is the most useful thing this tool can
say, and hiding an empty row would bury it. Competitors with no mentions are
left out; their zero is not worth the space, yours is.
Test: `TestStandingsAlwaysIncludeTheProperty`.

**The race (visibility per brand per day).** Each brand's distinct answers
over that day's answers, one point per measured day. The property is always
present.
Test: `TestBrandSeriesAlwaysIncludesTheProperty`.

**The engine breakdown.** Every tracked brand's visibility on every engine,
grouped by engine and access mode, so the same engine reached through the
API and through its consumer product is two rows. The denominator is that
group's own answers in the window.
Test: `TestEngineBreakdownCoversEveryBrandOnEveryEngine`.

**The series.** Visibility per day, oldest first, with each day's answers
and mentions. A day with no `ok` answers has no point; the chart draws a gap,
not a zero.
Test: `TestSeriesHandlesASinglePoint`.

**The grid.** Every active prompt against every target: answers, answers
that named you, visibility and mean position per cell. Branded prompts are
included here and tagged. A cell with no answers is an absence (that prompt
was never asked of that target in the window) and is drawn differently from a
measured zero.
Tests: `TestMatrixKeepsBrandedPromptsTagged`,
`TestGridDistinguishesAMeasuredZeroFromAnUnaskedCell`.

**Sources.** Cited sites ranked by citations, with the distinct answers each
appears in and its type, and per site the individual pages.
Tests: `TestSourcesRankBySite`, `TestSourceURLsDrillIntoOneSite`.

**Usage.** Calls and tokens per target per day. There is no currency
anywhere: this project counts what was spent in requests, because it has no
pricing table and inventing one would be worse than omitting it.

## Two ways to reach one engine are two targets

A target is `engine:provider[:model][:online]`, and every target carries an
access mode: `api` (the vendor's model, called directly, usually with web
search on) or `scraped` (the answer a person sees in the product, collected
by a third party). They measure different things. The model behind the API
and the model behind the consumer product are often not the same build, the
consumer product adds retrieval and personalisation the API does not, and a
scraped surface can fail to render where an API call cannot.

So the two are never averaged. Every answer is stored against its target, the
engine breakdown groups by engine and access mode, and the grid gives each
target its own column. Reports never carry the words `api` or `scraped`: the
access mode is a property of the target, shown where targets are configured,
not a label on a number.
Tests: `TestAddTargetStoresAccessMode`, `TestEngineBreakdownCoversEveryBrandOnEveryEngine`.

## Small samples say so

**Under 20 answers, a result is an anecdote.** Every headline carries `n`,
and below `LowNThreshold` (20) it is captioned as a small sample; on the
charts a day resting on fewer than 20 answers is drawn hollow. It is a
caption, not a warning: a new instance is legitimately here on its first day.
Test: `TestLowNFlipsAtTheThreshold`.

**A grid cell cannot reach the top of the heat ramp on fewer than 5
answers.** One answer that named you is 100%, and painting it like five of
five would let a single lucky answer look like dominance. Below `CellMinN`
the fill is capped one step short, and the cell still prints its own
fraction so the reader sees exactly what it rests on. A measured zero is its
own bin, drawn at full strength: greying it is the most common way a real
finding reads as a broken widget.
Test: `TestGridCapsTheTopBinOnATinySample`.

**A trend needs two measured days.** The overview opens on 30 days. When
that window holds fewer than two measured days and no window was chosen, it
widens to the narrowest choice that has two, so an install whose last run
was five weeks ago shows its history rather than "No answers yet". The window
switch shows which view is open, and an explicit choice is never overridden.
Test: `TestOverviewWidensAnEmptyDefaultWindow`.

**Measured days, not calendar days.** Time axes are proportional to the
calendar, so a fortnight's gap looks like a fortnight, and a gap much longer
than the usual cadence is drawn as a bridge rather than a line pretending to
know what happened in between.
Tests: `TestDateScaleSpacesDaysByCalendarTime`,
`TestDateScaleBridgesALongGapNextToAShortOne`, `TestRaceNeverCurvesAcrossAGap`.

## What the open core does not compute, and why

- **Discovery of brands you did not list.** Needs a model to read each answer
  and name what it sees. A model can invent a brand, miss one, and answer
  differently on Tuesday, so its output cannot be re-derived from the stored
  text the way every number here can.
- **Sentiment and framing.** How a brand is described, not whether. Needs a
  model for the same reason, and its result is a judgement, not a count.
- **Hallucination checks and correction drafts.** Whether what the engine
  said about you is true. Needs a model and your own sources.
- **Query fan-out analysis.** The searches an engine ran on the way to its
  answer are stored when the provider reports them, and their words are
  counted; reading what they mean about demand is hosted work.

Each of these is a Limelit Cloud feature; the full hosted-only list lives in
the README and nowhere else. The line is drawn at "needs a model in the loop
to produce the number", because on that side of the line the number stops
being auditable against the stored evidence.

## Checking a number yourself

Everything above is SQL over one SQLite file. `limelit export` (or the
`export_data` tool) writes every table as JSON or CSV, and the database at
`data/limelit.db` opens in any SQLite client. Visibility over the last 30
days, by hand:

```sql
SELECT
  COUNT(DISTINCT CASE WHEN m.id IS NOT NULL THEN c.id END) * 100.0
    / COUNT(DISTINCT c.id) AS visibility,
  COUNT(DISTINCT c.id) AS answers
FROM chat c
JOIN prompt p ON p.id = c.prompt_id
LEFT JOIN mention m ON m.chat_id = c.id AND m.competitor_id IS NULL
WHERE c.status = 'ok'
  AND p.branded = 0
  AND c.created_at >= datetime('now', '-30 days');
```

It should match the overview to two decimals. If it does not, that is a bug,
and the repository wants to hear about it.

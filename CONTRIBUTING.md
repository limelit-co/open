# Contributing

Thanks for looking. This file is the short version of what a pull request
needs; the reasoning behind each rule is in the code comments next to the
rule, and the numbers themselves are explained in
[docs/methodology.md](docs/methodology.md).

## Ground rules

**Original code only.** Nothing copied in from another project, including
other open-source AI visibility trackers, however similar the problem. If you
have read another implementation of the thing you are building, write yours
from the problem, not from memory of theirs. Adapters to a vendor's public
API are fine; a vendor's own SDK is fine when its license permits it.

**Sign off every commit.** `git commit -s` adds the
[Developer Certificate of Origin](https://developercertificate.org/) line,
which states that you wrote the patch or have the right to submit it under
Apache-2.0. There is no CLA and no copyright assignment: you keep the
copyright in what you write. Every `.go` file opens with the two-line
license header; `internal/licensecheck` fails the build on one that does not.

**Tests run offline.** `make test` has to pass on a machine with no network
and no keys. A provider ships with recorded fixtures under `testdata/` and a
unit test against them; a live test goes behind the `live` build tag
(`//go:build live`) so the default suite never touches a vendor.

**No em dashes in anything a user can read.** Labels, hints, empty states,
error text, tool descriptions, docs. Use a colon, a comma, parentheses or a
full stop. `TestNoEmDashesInUserFacingCopy` greps every page for both the
character and `&mdash;`, and code comments are the one place they are allowed.

**No colour from Go.** Templates and chart code emit class names and CSS
custom properties, never a hex value, an `rgb()`, or a `url()`. Colour lives
in `internal/ui/static/app.css` where the light and dark themes can each
decide; `TestNoColourIsEmittedFromGo` and `TestRichChartsNeverEmitAColour`
enforce it.

**No pricing.** The open core counts calls and tokens and never converts
either to money. There is no pricing table to keep current, and a wrong
number about cost is worse than none.

**No credentials in a response.** Keys are sealed at rest and never returned
by a page, the API or a tool, including `list_targets`; a bearer token is
shown once when generated and never again. `TestKeysNeverReachTheMCPTools`
and `TestSavedKeyIsEncryptedAtRest` guard it. If you add a surface, add it to
those.

**The words `api` and `scraped` do not appear on reports.** How an engine was
reached is a property of the target, shown where targets are configured. Two
targets on one engine are two columns, never an average.

## Picking up an issue

- **Comment to ask, and a maintainer assigns you.** The issue is yours once
  you are assigned, not when you comment.
- **One open issue per person** until your first PR merges. After that, ask
  for the next one.
- **No draft PR after 7 days and the issue goes back to open**, so someone
  else can take it. Say so in the issue if you need longer.

## Before you push

Run the same checks CI runs:

```bash
make lint   # gofmt + go vet
make test   # go test ./... -count=1, offline
```

Both green, then push. Every pull request gets a `build` check from GitHub
Actions (gofmt, vet, build, test). Until you have had a pull request merged
here, GitHub holds those runs until a maintainer has read the change and
approves them. If the check fails, its log names the step, and
`make lint && make test` reproduces it.

## Adding a provider

A provider is one file in `internal/provider/`, a fixture, a test, and a
registration. The contract is in [docs/providers.md](docs/providers.md);
the four open adapter issues (Cloro, Bright Data, Oxylabs, Olostep) are the
natural first ones to pick up, and `searchapi.go` is a good model for a
scraped adapter, `perplexity.go` for an API one.

1. **Implement `provider.Provider`** in `internal/provider/<name>.go`:
   `Name`, `Access`, `Engines`, `Run`, `Test`. Stateless; credentials arrive
   at construction. Return the typed errors (`ErrAuth`, `ErrRateLimited`,
   `ErrQuota`, `ErrUnsupportedEngine`, `ErrNoAnswerSurface`) so the runner and
   the settings screen can say what happened. Do not retry: the runner owns
   retries and the run ceiling.
2. **Return citations as the engine gave them.** Normalisation and
   classification happen once, afterwards, for every provider alike.
3. **Record a fixture** in `internal/provider/testdata/<name>_<engine>.json`
   and write the offline test against it in `<name>_test.go`. Strip anything
   personal from the recording.
4. **Register it** in `internal/provider/registry_default.go` with its
   credential variable names. The catalog in `catalog.go` already lists it;
   registration is what turns the "not built yet" line in Settings into a
   button, and `TestCatalogMatchesDocs` keeps the catalog and
   `docs/providers.md` in step.
5. **Run the gate.** `make lint test`.

## Adding a metric or changing a number

Read [docs/methodology.md](docs/methodology.md) first. Every metric there
names the test that enforces it; a change to a denominator, an exclusion or
a rounding rule updates the test and the document in the same pull request.
Count answers as `COUNT(DISTINCT chat.id)` across any join onto `mention`,
exclude `no_answer_surface` rows everywhere, and keep branded prompts out of
the headline.

## Anything user-facing

The dashboard is server-rendered Go templates with one small hover script;
there is no build step and no framework. Keep it that way. When a screen
changes shape, retake the screenshots in `docs/screenshots` that show it
(see the Development section of the README).

## Reporting a bug

Open an issue with the version (`limelit version`), what you expected, what
you saw, and, for a wrong number, the SQL from the end of
docs/methodology.md against your own database. A number that disagrees with
that query is the bug report this project most wants.

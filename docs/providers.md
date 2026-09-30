# Targets and providers

A **target** is one way of asking one engine. A **provider** is the service
that does the asking. The same engine can be reached through several
providers, and the two kinds of provider measure different things, so the
distinction is kept all the way to the number on the screen.

## Target format

```
engine:provider[:model][:online]
```

| Part | Meaning | Examples |
|---|---|---|
| `engine` | The answer surface being measured. Ids are Limelit Cloud's | `chatgpt`, `claude`, `perplexity`, `gemini`, `ai_overview`, `ai_mode`, `bing_copilot` |
| `provider` | Who reaches it | `openai`, `anthropic`, `perplexity`, `google`, `openrouter`, `dataforseo`, `searchapi`, `cloro`, `brightdata`, `oxylabs`, `olostep`, `limelit`, `limelitapi` |
| `model` | Optional pin. Omitted means the provider's default for that engine | `gpt-5.5`, `sonar`, `gemini-2.5-flash` |
| `online` | Web search on, for providers where it is a switch. Scraped surfaces are always online | |

Examples:

```
chatgpt:openai:gpt-5.5:online     # ChatGPT's model through OpenAI's API, web search on
chatgpt:dataforseo:online         # chatgpt.com as a user sees it, through a scraper
claude:anthropic:online
perplexity:perplexity:sonar
gemini:google:online
ai_overview:dataforseo
ai_mode:dataforseo
bing_copilot:searchapi
```

Two targets can point at the same engine. They are tracked and reported
separately, each with its access badge. Nothing averages an `api` target with
a `scraped` one.

Targets live in `limelit.yaml` and in Settings. Credentials live in
environment variables or the settings store, never in the target string.

Most people never write one. Settings lists every engine with a button per
provider that can reach it, and builds the string for you; the raw field is
kept behind an Advanced disclosure, because pinning a model is the one thing
the buttons cannot express.

## Access modes

| Mode | What it measures | Typical cost shape | Providers |
|---|---|---|---|
| `api` | The vendor's model with web search enabled, called directly | Per token | `openai`, `anthropic`, `perplexity`, `google`, `openrouter`, `limelitapi` |
| `scraped` | The consumer surface a user actually sees, fetched by a third-party scraping service | Per request | `dataforseo`, `searchapi`, `cloro`, `brightdata`, `oxylabs`, `olostep`, `limelit` |

An API answer with web search on and the answer a person sees in the
consumer product are different surfaces. Limelit treats them as such, and
this project shows the mode on every metric rather than in a docs page.

Some engines exist in only one mode:

- `ai_overview`, `ai_mode`, `bing_copilot`: scraped only. There is no API
- `claude`: api only. There is no anonymous consumer surface to scrape

## Provider interface

```go
// Access separates vendor APIs from consumer surfaces reached by scraping.
type Access string

const (
    AccessAPI     Access = "api"
    AccessScraped Access = "scraped"
)

type Request struct {
    Engine          string // limelit engine id
    Model           string // optional pin from the target
    Online          bool
    Prompt          string
    LocationCountry string // ISO 3166 alpha-2, optional
    LanguageCode    string // BCP 47 base language, optional
}

type Citation struct {
    URL      string
    Title    string
    Position int // 1-based, as the engine ordered them
}

type Response struct {
    Text         string
    Model        string     // what actually answered, as reported
    Citations    []Citation // sources the engine attributed
    InputTokens  int        // zero for scraped
    OutputTokens int        // zero for scraped
    Calls        int        // 1 for api; scrapers may bill several
}

type Provider interface {
    // Name is the provider segment of the target string.
    Name() string
    // Access is fixed per provider.
    Access() Access
    // Engines lists the engine ids this provider can reach, with the
    // default model for each where a pin is meaningful.
    Engines() map[string]string
    // Run asks one engine one prompt. It returns the answer text and the
    // citations the engine attributed. It must not retry indefinitely;
    // the runner owns retries and the runs_per_day guard.
    Run(ctx context.Context, req Request) (Response, error)
    // Test checks credentials with the cheapest possible call. Settings
    // calls it when a key is saved.
    Test(ctx context.Context) error
}
```

Rules every provider follows:

- **Stateless.** Credentials are passed at construction. No provider reads
  the database.
- **No pricing.** Providers report tokens and calls. Nothing in this
  repository converts either to currency.
- **Citations as the engine gave them.** No normalisation inside the
  provider. `urlnorm` and the citation classifier run afterwards, once, for
  every provider alike.
- **Errors are typed.** `ErrAuth`, `ErrRateLimited`, `ErrQuota`,
  `ErrUnsupportedEngine`, `ErrNoAnswerSurface`. `ErrQuota` is an account with
  no credit left; vendors send it with the same 429 as a rate limit, and it is
  named apart because it is not retried and the fix is billing, not patience.
  The last one matters for scraped Google surfaces: an AI Overview that did
  not appear for a query is not a miss for the brand, it is an absent surface,
  and the metrics exclude it from the denominator.
- **Testable offline.** Every provider ships with recorded fixtures under
  `testdata/` and a unit test that runs without network or keys. A live test
  behind a build tag is welcome but not required.

## Providers in v0.1

Build order. The first group ships before the second is started.

### API

| Provider | Engines | Notes |
|---|---|---|
| `openai` | `chatgpt` | Responses API with the `web_search` tool. Citations from `url_citation` annotations. Key: <https://platform.openai.com/api-keys> |
| `anthropic` | `claude` | Messages API with the web search tool. Citations from `web_search_result_location` blocks. Key: <https://console.anthropic.com/settings/keys> |
| `perplexity` | `perplexity` | Sonar models. Citations from the `citations` array. Key: <https://www.perplexity.ai/account/api/keys> |
| `google` | `gemini` | Gemini API with Google Search grounding. Citations from `groundingChunks`. Key: <https://aistudio.google.com/apikey> |
| `openrouter` | `chatgpt`, `claude`, `gemini`, `perplexity` | One key, four engines, so it is what Settings recommends first. Citations only where the upstream model returns them. Key: <https://openrouter.ai/keys> |
| `limelitapi` | `perplexity` | The Perplexity half of the free allowance (below): Limelit Cloud asks Perplexity's API with Limelit's key. Key: a Limelit Cloud API key, <https://limelit.co/settings> |

### Scraped, with existing Go adapters to draw from

| Provider | Engines | Notes |
|---|---|---|
| `dataforseo` | `ai_overview`, `ai_mode`, `chatgpt`, `gemini`, `perplexity` | SERP endpoints for the two Google surfaces. LLM-scraper endpoints for chatgpt.com and gemini.google.com are a v0.2 candidate. Key: <https://app.dataforseo.com/api-access> |
| `searchapi` | `ai_overview`, `ai_mode`, `bing_copilot` | Key: <https://www.searchapi.io/> |

### Scraped, new adapters to the same interface

| Provider | Engines | Notes |
|---|---|---|
| `cloro` | `chatgpt`, `ai_mode`, `perplexity`, `gemini` | Always online. Key: <https://cloro.ai/> |
| `brightdata` | `chatgpt`, `ai_mode`, `ai_overview`, `perplexity`, `gemini`, `bing_copilot` | Key: <https://brightdata.com/> |
| `oxylabs` | `chatgpt`, `ai_mode`, `ai_overview`, `perplexity` | Key: <https://oxylabs.io/> |
| `olostep` | `chatgpt`, `ai_mode`, `perplexity` | Key: <https://www.olostep.com/> |

### Scraped, through the free Limelit Cloud allowance

| Provider | Engines | Notes |
|---|---|---|
| `limelit` | `chatgpt`, `gemini`, `ai_overview`, `ai_mode` | Limelit Cloud collects the consumer surfaces with Limelit's scraping-service accounts. Key: a Limelit Cloud API key, <https://limelit.co/settings> |

These four are about 150 lines each: an HTTP call, a response parse, a
fixture. They are the intended first contribution for anyone who wants one,
and each has its own issue. Engine lists above are from public provider
documentation and will be corrected against fixtures as each adapter lands.

`internal/provider/catalog.go` carries the same table in code, including each
vendor's key page, which is what Settings renders. A test reads this file to
keep the two in step.

## The free allowance

A user with no vendor key can still measure. They sign up for Limelit Cloud
(free), create an API key in its Settings, and paste it into Limelit Open,
either at the setup wizard's provider step or in Settings. That one key
enables two providers: `limelit` (ChatGPT, Gemini, Google AI Overviews and AI
Mode, the consumer surfaces) and `limelitapi` (Perplexity, through its API).
Claude is not in the allowance; it needs the user's own Anthropic or
OpenRouter key.

Each prompt goes to Limelit Cloud (`POST https://api.limelit.co/v1/relay/answer`),
which asks the engine with Limelit's keys and returns the answer, its
citations and the searches it ran. The answer is stored here like any other.
Limelit Cloud keeps which engine was asked and what it cost, not the prompt or
the answer.

The allowance is monthly and shared fairly: a monthly credit limit per
account, a daily one, and a daily limit across every account. When one is
used up the relay refuses, the answer is recorded as failed with the reason
(`provider: no credit or quota left: This month's free Limelit allowance is
used up...`), and the fix is to add your own key for that engine. Both kinds
of target can run side by side; they are reported separately, each with its
access badge.

## Configuration

```yaml
# limelit.yaml
property:
  name: Acme
  domain: acme.com
  aliases: [Acme Inc, acme.io]

competitors:
  - { name: Globex, domain: globex.com }
  - { name: Initech, domain: initech.com }

targets:
  - chatgpt:openai:gpt-5.5:online
  - claude:anthropic:online
  - perplexity:perplexity:sonar
  - ai_overview:dataforseo

limits:
  runs_per_day: 200

schedule: daily   # daily, hourly, or off
```

Credentials:

```
OPENAI_API_KEY, ANTHROPIC_API_KEY, PERPLEXITY_API_KEY, GOOGLE_API_KEY,
OPENROUTER_API_KEY, DATAFORSEO_LOGIN, DATAFORSEO_PASSWORD, SEARCHAPI_KEY,
CLORO_API_KEY, BRIGHTDATA_API_TOKEN, OXYLABS_USERNAME, OXYLABS_PASSWORD,
OLOSTEP_API_KEY, LIMELIT_CLOUD_KEY
```

The Settings screen writes the same values to the settings store, encrypted
at rest with a key derived from `LIMELIT_SECRET`. Environment wins when both
are set, and the field says so. Saving a key tests it at once with the
provider's `Test` call and reports the provider's own error under the field;
a rejected key stays saved so an account problem can be fixed on the vendor's
side, and Forget removes it. `schedule` and `runs_per_day` can be changed in
Settings too; a stored value wins over the file and the scheduler re-reads it
within a minute.

## The `runs_per_day` guard

One chat is one run. Before an evaluation starts, the runner counts today's
runs plus the runs the evaluation would add. If the total exceeds
`limits.runs_per_day`, the evaluation is refused with the number it would
have needed, and nothing is spent. The guard is per instance, not per target,
because the point is a ceiling on surprise, not a budget.

## What a provider must not do

- Store or log the prompt or the answer. The runner does that, once
- Parse mentions or classify citations. Those are downstream and identical
  for every provider
- Retry on its own beyond a single transient retry. The runner owns backoff
  and the daily guard
- Convert anything to currency

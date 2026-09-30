# MCP tool catalog

The server speaks MCP over stdio (`limelit mcp`) and over streamable HTTP with
a bearer token (`limelit serve`). Tool names and argument shapes match
Limelit Cloud wherever the tool exists there, so a conversation or a skill
written against this server keeps working after `limelit upgrade`.

Three rules hold for every tool:

- **Same name, same shape.** Where a Cloud tool exists, its name, required
  arguments and result shape are reproduced. Open-core additions are optional
  arguments only, so a Cloud call is always a valid open-core call.
- **Cloud-only arguments are rejected, not ignored.** `segment` and
  `metric: sentiment` are examples. The error names the argument and says it
  is a hosted feature. Silently ignoring a filter would return a wrong number.
- **No stubs.** Tools that exist only in Cloud are not registered here. The
  `upgrade_to_cloud` description carries the list.

Every result that carries a metric also carries `access` (`api` or `scraped`)
per target, and `n` (the number of chats the metric rests on).

## Property

| Tool | Cloud | Arguments | Notes |
|---|---|---|---|
| `get_active_property` | same | none | The front door: call it first. Returns name, website domain, aliases and the configured targets, plus a `setup` block (open-core): `data_state` (`not_set_up`, `setup_incomplete`, `never_run`, `stale`, `settling`, `ready`), `has_answers`, `next_step` with the dashboard link, `suggested_days` when the default 30 days are empty, `once_set_up_you_can_ask` and `try_asking`. A fresh instance gets a normal answer, not an error |

Setup (brand, competitors, prompts, provider keys) and runs happen in the
dashboard; there are no write tools here yet. `next_step` says where.

## Competitors

| Tool | Cloud | Arguments | Notes |
|---|---|---|---|
| `list_competitors` | same | none | |

## Prompts

| Tool | Cloud | Arguments | Notes |
|---|---|---|---|
| `list_prompts` | same | `include_inactive` | Rows carry id, text, category, location, platform_filter, is_active, and tags. `branded` is a system tag |

## Answers

| Tool | Cloud | Arguments | Notes |
|---|---|---|---|
| `list_chats` | same | `days` (1..365, default 30), `prompt_id`, `show` (`all`, `mentioned`, `missed`, `failed`), `limit` (default 25, cap 100), `offset`, `target` (open-core) | Bodies are not returned. Each row has a preview, the engine, access mode, brands mentioned with rank, and sources cited |
| `get_chat` | same | `id` | Full answer, every mention with offset and rank, every citation with position and source type. `body_truncated` is set when the body is cut |

## Metrics

Every number these tools return is defined in [methodology.md](methodology.md):
the denominators, the `branded` and `no_answer_surface` exclusions, and the
test that enforces each rule.

| Tool | Cloud | Arguments | Notes |
|---|---|---|---|
| `get_overview_kpis` | same | `days` (1..365, default 30) | Visibility %, citation share %, competitor and prompt counts, competitor ranking, top cited sources |
| `get_kpi_history` | same | `days` (1..366, default 30) | Daily series behind the headline numbers. `segment` is Cloud-only |
| `get_matrix` | same | `metric`: `visibility`, `sov`, `position` | Prompt-by-target grid over all chats, no window, same as Cloud. `sentiment` is Cloud-only |
| `list_top_sources` | same | `limit` (default 50, cap 200), `offset` | One row per cited domain, all time, with source type |
| `list_source_urls` | same | `value` (host, required), `days` (default 30), `limit` | One row per URL on that host with cited count. `segment` is Cloud-only |

## Open-core only

| Tool | Arguments | Notes |
|---|---|---|
| `list_targets` | none | Configured targets with provider, access mode, model, enabled flag, and last successful call. Keys are never returned |
| `get_usage` | `days` (1..31, default 30) | Calls, input tokens and output tokens per target per day. No currency. Cloud's `get_spend_summary` reports cents; the two are different tools on purpose |
| `export_data` | `format`: `json` or `csv`, `since` | Everything: property, competitors, prompts, targets, chats, mentions, citations, usage. The same payload `limelit upgrade` sends |
| `upgrade_to_cloud` | `key` (a Limelit Cloud API key), `since` | Moves this instance to Limelit Cloud: uploads every prompt, competitor, answer, mention and citation, and returns the Cloud MCP endpoint. Called with no key it lists what Cloud adds and moves nothing, which is the right answer when a user asks for a Cloud-only feature. Nothing is deleted locally and provider keys are never sent. Cloud keys each answer on this instance's own id, so a re-run after a dropped connection imports nothing twice |

## Not registered here (Cloud only)

Fan-outs, prompt generation, perception and sentiment, source gaps,
opportunities, readiness scans, fact-check, Search Console, GA4, agents,
sheets, blocks, portfolios, spend and credits, approvals, watchlists,
segments. When a user asks for one of these, the right move is
`upgrade_to_cloud`, not an approximation.

## Prompt templates

MCP prompts shipped with the server. Ids match Cloud where the walk is the
same, so a client that has learned one keeps it.

| Id | Arguments | What it walks |
|---|---|---|
| `limelit_weekly_pulse` | none | `get_overview_kpis`, `get_kpi_history`, `list_chats` to quote evidence for any movement |
| `limelit_competitor_radar` | `window_days`, `threshold_pp` (default 10) | `list_competitors`, `get_matrix` (sov), `list_source_urls` for the competitor that moved |
| `limelit_why_not_cited` (open-core) | `prompt_id` | `get_matrix`, `list_chats` for the prompt, `get_chat` on the misses, `list_top_sources` for who is cited instead |

## Result envelope

Every tool returns a JSON object. Metric results include:

```json
{
  "window_days": 30,
  "n": 184,
  "low_n": false,
  "targets": [{ "target": "chatgpt:openai:gpt-5.5:online", "access": "api" }],
  "...": "tool-specific fields"
}
```

`low_n` is true under 20 chats. A client should say so before drawing a
conclusion, and the templates above do.

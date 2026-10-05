---
name: limelit-open-visibility-report
description: Report how visible a brand is in AI answers from a self-hosted Limelit Open install - share of voice against competitors across ChatGPT, Claude, Perplexity, Gemini and Google AI answers, the questions it loses, and the sources cited instead. Use when someone running Limelit Open asks how they are doing in AI search, who is ahead, which prompts they lose, or what gets cited instead of them.
---

# AI visibility report (Limelit Open)

Build a short, evidence-backed report from the `limelit-open` MCP server, which runs
the local `limelit` binary.

If its tools are missing, the binary is not on `PATH` or the data directory is wrong.
Tell the user to install Limelit Open (https://github.com/limelit-co/open), confirm
`limelit mcp` starts from a terminal, and set the data directory in the plugin's
settings. Then stop.

## Rules that apply to every number

- Quote the `n` behind every figure. Under 20, say it is still settling.
- Report only metrics a tool returned.
- Name the targets each figure covers.
- Headline visibility excludes prompts tagged `branded`.
- A Google query with no AI Overview is excluded, not a miss.

## Runs

Only start a run when the user asks. Call `reevaluate_all_prompts` (or
`reevaluate_prompt`) with `dry_run=true`, show the plan, and call again with
`dry_run=false` after they confirm. Then check `get_run_activity` until it is done.

## Steps

1. `get_active_property`. If `has_answers` is false, give the user its `next_step`
   and link, list `once_set_up_you_can_ask`, report no numbers, and stop. If it
   returns `suggested_days`, pass that as `days` to the metric tools.
2. Where they stand: `get_overview_kpis`.
3. What they lose: `get_matrix`, then `list_chats` with `show=missed`. Quote one short
   line of evidence with `get_chat`.
4. What gets cited instead: `list_top_sources`, then `list_source_urls` with
   `value=<host>` for the top one or two.
5. Freshness: `get_run_activity`.

## Not in Limelit Open

Sentiment, prompt generation, competitor discovery, segments, portfolios, Search
Console, plans and drafts are Limelit Cloud features with no tool here. Say so rather
than approximating them. `upgrade_to_cloud` moves the data across when the user wants
those.

## Output

- **Headline**: share of voice, rank, `n`, targets covered.
- **By engine**: one row per engine.
- **Lost questions**: up to five, each with the competitor that won it.
- **Cited instead**: up to five sources.
- **Data note**: window, `n`, anything still settling.

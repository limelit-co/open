---
name: limelit-visibility-report
description: Report how visible a brand is in AI answers using the connected Limelit MCP server - share of voice against competitors across ChatGPT, Claude, Perplexity, Gemini, Google AI Overviews and Google AI Mode, the buyer questions it loses, the sources the engines cite instead, and the next fixes. Use when someone asks how they are doing in AI search, who is ahead in ChatGPT or Perplexity, which prompts they lose, what gets cited instead of them, or what to write or fix next for GEO or AEO.
---

# AI visibility report (Limelit Cloud)

Build a short, evidence-backed visibility report from the `limelit` MCP server this
plugin connects. If its tools are missing or every call returns 401, the API key is
unset or wrong: tell the user to create one at https://limelit.co/settings and
re-enable the plugin, then stop.

## Rules that apply to every number

- Quote the `n` (answers measured) behind every figure.
- Under 20 answers, say the figure is still settling.
- Report only metrics a tool returned. Never estimate a missing one.
- Headline visibility excludes prompts tagged `branded`; say so if the user asks
  why a branded question is missing.
- A Google query with no AI Overview is excluded, not a miss.

## Runs draw on the account's allowance

Do not call `reevaluate_prompt` or `reevaluate_all_prompts` unless the user asks for a
fresh run. When they do, call with `dry_run=true` first, show the runs and the
estimate it returns, and call again with `dry_run=false` only after they confirm. Then
check `get_run_activity` until it reports done.

## Steps

1. `get_active_property`. Name the property and its competitors.
2. `analyze_my_visibility`. If `brand_visibility_basis_n` is 0, nothing has run yet:
   say so, point to https://limelit.co, report no numbers, and stop.
3. Where they stand: share of voice and rank against each competitor, per engine.
   Use `get_overview_kpis` instead when the user names a date window.
4. What they lose: `get_matrix`, then `list_chats` for the prompts where a competitor
   is named and they are not. Quote one short line from an answer as evidence
   (`get_chat`).
5. What gets cited instead: `list_top_sources`, then `list_source_urls` for the top
   one or two hosts.
6. What to do next: `find_easy_wins` (or `list_opportunities`). If the user wants
   content, `suggest_my_next_blog_post`.
7. Freshness: `get_run_activity`, so the user knows how current the numbers are.

## Output

- **Headline**: one sentence with share of voice, rank, and `n`.
- **By engine**: a small table, one row per engine.
- **Lost questions**: up to five, each with the competitor that won it.
- **Cited instead**: up to five sources, each with what it is.
- **Next three moves**: from step 6, each one line.
- **Data note**: date window, `n`, and anything still settling.

Keep it short enough to read on a phone. Offer to dig into any one section.

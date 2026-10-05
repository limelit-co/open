# Limelit

Connects Claude to Limelit Cloud, which tracks how ChatGPT, Claude, Perplexity,
Gemini, Google AI Overviews and Google AI Mode mention and cite your brand against
your competitors.

## Setup

1. Create a free account at https://limelit.co and add your site.
2. Create an API key at https://limelit.co/settings.
3. Install this plugin and paste the key when Claude asks for it. It is stored in
   your system's secure storage and sent only to `https://api.limelit.co/mcp`.

## Skill

- **`limelit-visibility-report`**: share of voice and rank per engine, the buyer questions
  you lose and to whom, the sources the engines cite instead, and the next three
  fixes. Every figure carries the number of answers behind it.

The connection also gives Claude the full Limelit tool set (around 200 tools): prompts,
competitors, sources, drafts, Cross Check and more. See https://limelit.co/mcp-docs.

## Runs

Reading your data starts nothing. A fresh run draws on your Limelit Cloud
allowance, so the skill always does a dry run first, shows you the plan and its
estimate, and waits for your yes before it starts.

Apache-2.0.

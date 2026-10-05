# Limelit Open

Connects Claude to a self-hosted [Limelit Open](https://github.com/limelit-co/open)
install: one binary that tracks how ChatGPT, Claude, Perplexity, Gemini and Google AI
answers mention and cite your brand, with your own keys and your own data.

## Setup

1. Install Limelit Open and run it once, following the
   [quick start](../../README.md#quick-start).
2. Start it with `limelit serve`. At startup it prints how to connect Claude,
   including the absolute path of your binary and your data directory (the folder
   that holds `limelit.db`).
3. Install this plugin and give Claude those two paths when it asks.
4. Claude starts `limelit mcp` over stdio. Nothing leaves your machine except the
   engine calls your own install already makes.

## Skill

- **`limelit-open-visibility-report`**: share of voice and rank per engine, the questions you
  lose and to whom, and the sources cited instead. Every figure carries the number of
  answers behind it.

Apache-2.0.

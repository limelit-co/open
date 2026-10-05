# Limelit AI Search Audit

Free AI search audits for any website, with no account and no API key.

## Skills

- **`ai-crawler-check`**: reads the site's robots.txt and reports which of about 46
  AI crawlers it allows or blocks: search and answer bots (OAI-SearchBot,
  Claude-SearchBot, PerplexityBot), user-triggered fetchers (ChatGPT-User,
  Claude-User) and training crawlers (GPTBot, Google-Extended, CCBot). It explains
  which blocks remove the site from live AI answers and drafts robots.txt fixes. It
  uses Limelit's free checker and falls back to evaluating robots.txt locally.
- **`llms-txt-check`**: fetches `/llms.txt`, checks it against the llms.txt format,
  tests its links, and drafts a better file from the site's sitemap.

## Try

- "Which AI bots does acme.com block?"
- "Audit the llms.txt on acme.com."

## Data

The skills read public files on the site you name. `ai-crawler-check` sends the
domain to `https://api.limelit.co/rest/v1/public/tools/crawler-check`, which fetches
robots.txt and returns the evaluation; it stores no account data. Privacy policy:
https://limelit.co/privacy.

Apache-2.0.

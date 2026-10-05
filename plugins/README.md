<h1 align="center">Limelit for Claude</h1>

<p align="center">
  <strong>Skills and plugins for AI search visibility (GEO and AEO), from <a href="https://limelit.co">Limelit</a>.</strong>
</p>

<p align="center">
  Check whether AI crawlers can read your site, fix your llms.txt, and see how ChatGPT,
  <br />
  Claude, Perplexity, Gemini and Google AI answers rank and cite your brand.
</p>

## Plugins

| Plugin | What it does | Needs |
|---|---|---|
| [`limelit-audit`](limelit-audit) | Free audits: which AI crawlers your robots.txt allows (`ai-crawler-check`), and whether your llms.txt is present and valid (`llms-txt-check`) | Nothing. No account. |
| [`limelit`](limelit) | Connects Limelit Cloud: share of voice, lost questions, sources cited instead, next fixes (`limelit-visibility-report`) | A free [Limelit](https://limelit.co) account and API key |
| [`limelit-open`](limelit-open) | Connects a self-hosted [Limelit Open](../README.md) install (`limelit-open-visibility-report`) | Limelit Open installed on your machine |

## Install

### Claude Code

```
/plugin marketplace add limelit-co/open
/plugin install limelit-audit@limelit
```

Swap in `limelit` or `limelit-open` for the other plugins.

### Claude desktop app and Cowork

1. Open **Plugins**, then **+ Add**, then **Add marketplace**.
2. Enter `limelit-co/open`.
3. Install the plugins you want from the list.

### claude.ai (one skill at a time)

Each skill folder can be zipped and uploaded under **Customize > Skills**. Skills
uploaded this way do not bring the Limelit connection with them. This route has not
been tested yet: the audit skills need network access to fetch the site's public
files, which the claude.ai skill environment may restrict.

## Try it

- "Can ChatGPT and Perplexity crawl acme.com?"
- "Check the llms.txt on acme.com and draft a better one."
- "How visible are we in AI answers, and who is ahead?" (with `limelit` connected)

## Privacy

The audit skills read only public files on the site you name (robots.txt, llms.txt,
sitemap) and may send that domain to Limelit's free crawler checker. Limelit's privacy
policy: https://limelit.co/privacy.

## License

Apache-2.0. See [LICENSE](../LICENSE).

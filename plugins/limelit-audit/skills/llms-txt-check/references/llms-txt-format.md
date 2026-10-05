# llms.txt format

The convention proposed at https://llmstxt.org. A file at the site root, in markdown,
in this order:

```markdown
# Example Co

> Example Co makes invoicing software for small agencies, with time tracking and
> client portals.

Example Co is used by agencies of 2 to 50 people. Pricing is per seat.

## Product

- [Features](https://example.com/features): what the product does, by module
- [Pricing](https://example.com/pricing): plans, seat prices, what each includes

## Docs

- [Getting started](https://example.com/docs/start): set up an account and first invoice
- [API](https://example.com/docs/api): REST endpoints and authentication

## Optional

- [Changelog](https://example.com/changelog): release notes by date
```

Rules:

- The H1 is the only required part.
- The blockquote is a short summary with the key facts a model needs.
- Free text before the first `##` holds context; it must not contain headings.
- Each `##` section is a list of markdown links, each optionally followed by `: note`.
- A section titled `Optional` marks links that can be skipped when context is short.
- `llms-full.txt` is an optional companion that inlines the full text of the pages.

# Publishing a press release through PRNow

Tracking tells you which sources the engines trust. A press release is one way
to put a new source in front of them: a news article about you, on outlets the
engines read. `limelit prnow` sends one through [PRNow](https://prnow.io),
using its public REST API ([prnow.io/api-docs](https://prnow.io/api-docs)).

You need a PRNow account and an API key from it. Submitting a release spends
PRNow credits; Limelit Open shows the credit counts PRNow reports and never
converts them to money.

## Save the key

```bash
limelit prnow login
```

Paste the key (it starts `prnow_`). It is checked against PRNow, then sealed
in the data directory the same way provider keys are. `PRNOW_API_KEY`, when
set, wins over the saved key.

```bash
limelit prnow account      # who the key belongs to, and its credits
limelit prnow categories   # the categories a release can use
```

## Write the release

The body is a file: HTML, or plain text with a blank line between
paragraphs (it is turned into escaped `<p>` paragraphs). A first line of the
form `# Headline` is used as the title, or pass `--title`.

PRNow's limits, which are checked before anything is sent, with every problem
listed at once:

| Field | Limit |
|---|---|
| title | 5 to 200 characters, at least 5 words |
| body | 250 to 1,200 words, at most 5 links and 3 inline images |
| categories | 1 to 5, from `limelit prnow categories` (case-sensitive) |
| country, city | both required (the dateline) |
| summary | up to 45 words (PRNow cuts a longer one) |
| meta title, description, keywords | 60, 200 and 250 characters |

PRNow has the final say; these exist so a release that cannot pass fails here
rather than after a round trip.

## Draft, then submit

```bash
limelit prnow draft  --file release.md --category Business --country Germany --city Berlin
```

A draft is saved on your PRNow account and nothing is charged. Finish it
there, or submit from here.

```bash
limelit prnow submit --file release.md --category Business --country Germany --city Berlin --plan standard
```

Without `--yes`, `submit` sends nothing: it checks the release, shows the
account's credits, and tells you to run it again with `--yes`. `--plan` is
required, because the plan decides what is spent; use a plan name from your
PRNow account.

```bash
limelit prnow submit ... --plan standard --yes
```

Every submit carries an `Idempotency-Key`. If PRNow is still working on it or
the connection drops, the command asks again with the same key, so a retry is
charged once. A release whose title is already live on the same plan is
refused, and the error names the existing one.

## Follow it

```bash
limelit prnow status G9E1HJK
```

Shows the release's status (`pending`, `in_progress`, `published`,
`distributed`, `rejected` or `refunded`) and each outlet: live with its URL,
held or rejected with the reason, and any credits refunded for it. The id is
the short slug the submit printed, or the full UUID.

Once an outlet is live, the article is a page engines can find and cite.
Every run already records each cited URL, so the next runs show whether the
engines pick it up.

## Withdraw it

```bash
limelit prnow retract G9E1HJK
```

Works only while the release is `pending`; PRNow refunds its credits. Once
editorial work has started it cannot be withdrawn.

## What is not here yet

- No MCP tool: publishing is a command-line step for now.
- PRNow's report, links, events and press release tools endpoints are not
  wrapped.

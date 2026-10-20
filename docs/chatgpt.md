# Use Limelit Open in ChatGPT

ChatGPT can chart what your Limelit Open instance measures: visibility by
engine, share of voice, who gets cited. It does that through the Limelit app
at `https://limelit.co/mcp/apps`.

ChatGPT cannot reach a self-hosted instance. A ChatGPT connector has to be a
public HTTPS address with sign-in, and your instance runs on your machine. So
the route goes through Limelit Cloud: you turn on sync, your instance copies
its data to your Cloud workspace after every run, and ChatGPT charts it from
there.

## Four steps

1. **Sign up for Limelit Cloud.** It is free. Open
   [limelit.co/settings/open-key](https://limelit.co/settings/open-key) and
   sign in with Google. A first sign-in creates the account. Press **Create
   my key** and copy it.

2. **Save the key in your instance.** Run `limelit login` and paste it, or
   paste it into the **Limelit Cloud free allowance** card in **Settings**
   and press **Save and test**. If you already use the free allowance, the
   key is already saved.

3. **Turn on sync.** In **Settings**, in the same card, press **Turn sync
   on**. The first copy goes at once, and the card says how many answers
   arrived. After that, every run copies its new answers when it finishes.

4. **Add the connector in ChatGPT.** Turn on developer mode in ChatGPT's
   settings, then add a connector with the address
   `https://limelit.co/mcp/apps`. Sign in with the same Limelit account when
   ChatGPT asks. Then ask ChatGPT, for example, "chart my share of voice in AI
   answers this month".

## What leaves your machine

- **Copied:** your brand, competitors and prompts, and every answer with its
  mentions, citations and the searches behind it.
- **Never copied:** provider keys. The OpenAI or Anthropic key that produced
  an answer stays on this machine.
- **Where:** the Limelit Cloud workspace your key belongs to, and nowhere
  else.

## Things worth knowing

- **One instance per workspace.** If a second instance tries to sync into the
  same workspace, Cloud refuses it and Settings says so, because two copies of
  the same prompts would count your brand twice. Turn sync off on the first
  one, or the second takes over once the first has gone 30 days without a
  sync.
- **The key has to belong to this brand.** If the Cloud account behind the
  key already tracks a different domain, sync is not turned on. Create a key
  in the Cloud account for this brand.
- **Your instance stays in charge.** Cloud does not run synced prompts on a
  schedule of its own, and a prompt you pause here is paused there.
- **Edits travel too.** When you change your brand's names or competitors,
  your instance reads its stored answers again, and the next sync brings the
  copies in Cloud in line.
- **`limelit run` copies before it exits.** `limelit serve` and `limelit mcp`
  copy in the background, so a run never waits for Cloud. A copy that fails
  is retried after the next run, and Settings shows the last error.
- **Turning sync off** stops the copying and tells Cloud, which then runs
  those prompts on its own schedule again. What was already copied stays in
  Limelit Cloud.
- **Moving to Cloud for good** with `limelit upgrade` turns sync off here,
  because Cloud takes over the prompts.

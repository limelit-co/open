# Deploying Limelit Open

The image in [`../Dockerfile`](../Dockerfile) is the same binary `go install`
produces, plus [Litestream](https://litestream.io) so SQLite survives a
container restart. It runs anywhere a container runs. This file records how
the public demo at [demo.limelit.co](https://demo.limelit.co) is deployed,
because a demo that cannot be rebuilt from its repository is a demo you have
to trust.

## What the image does on boot

[`entrypoint.sh`](entrypoint.sh):

1. If `LIMELIT_SCHEDULE` is set, writes a `limelit.yaml` carrying it and
   `LIMELIT_RUNS_PER_DAY`, so cadence and the spend ceiling are deployment
   configuration rather than a rebuild.
2. If `LITESTREAM_BUCKET` is set and there is no local database, restores one
   from the bucket. A new container starts where the last one stopped.
3. Runs `limelit serve` under `litestream replicate`, which streams every
   change back to the bucket within ten seconds.

Without `LITESTREAM_BUCKET` it says so on stderr and runs unreplicated, which
is right for trying it locally and wrong for anything public.

## Environment

| Variable | Purpose |
|---|---|
| `LIMELIT_DEMO=1` | Read-only public mode. See the README. |
| `LITESTREAM_BUCKET`, `LITESTREAM_PATH` | Where the database replica lives. |
| `LIMELIT_SCHEDULE` | `daily`, `hourly` or `off`. |
| `LIMELIT_RUNS_PER_DAY` | The spend ceiling, checked before any work. |
| `OPENAI_API_KEY` … `SEARCHAPI_KEY` | Provider credentials, from the platform's secret store. Never baked into the image. |
| `PORT` | Set by Cloud Run. Defaults to 1515, the same port `limelit serve` uses. |

## The demo, on Cloud Run

Built by Cloud Build from the pushed commit, stamped with that commit:

```bash
SHA=$(git rev-parse --short=12 HEAD)
gcloud builds submit --config deploy/cloudbuild.yaml --substitutions _VERSION=$SHA .
```

Deployed with one instance, always on, because SQLite has one writer and the
scheduler needs the CPU between requests:

```bash
gcloud run deploy limelit-open-demo \
  --image us-central1-docker.pkg.dev/$PROJECT/limelit/limelit-open:$SHA \
  --service-account limelit-open-demo@$PROJECT.iam.gserviceaccount.com \
  --allow-unauthenticated --min-instances 1 --max-instances 1 --no-cpu-throttling \
  --memory 512Mi --execution-environment gen2 \
  --set-env-vars "LIMELIT_DEMO=1,LITESTREAM_BUCKET=limelit-open-demo,LITESTREAM_PATH=limelit-v5,LIMELIT_SCHEDULE=off,LIMELIT_RUNS_PER_DAY=300" \
  --set-secrets "OPENAI_API_KEY=openai-key:latest,ANTHROPIC_API_KEY=anthropic-key:latest,PERPLEXITY_API_KEY=perplexity-key:latest,GOOGLE_API_KEY=gemini-key:latest,SEARCHAPI_KEY=searchapi-key:latest"
```

The service account holds exactly two grants: `objectAdmin` on its own bucket
and `secretAccessor` on the five secrets it uses. Nothing else.

`max-instances 1` is not a cost setting. Two instances would be two writers on
one SQLite file through Litestream, which is corruption.

## Seeding history

A demo is more useful with history than without, so
[`../tools/seed`](../tools/seed) loads a set of already-answered prompts on
day one. The answers come from the file; the mentions and citations are
produced by this build's own matcher and classifier over that text, so the
demo shows what the open core computes and nothing the source system decided.
Every answer keeps its original timestamp.

**The public demo's history starts on 2026-07-19** (owner decision: the
first week ran daily at a different cadence and read as a dip rather than as
history) and is refreshed from the source by re-running the seed, most
recently with the 2026-09-14 pass.

**The public demo's schedule is `off`, on purpose.** Its history is a real
company's data with the brand renamed, and a renamed brand is one the engines
cannot name: the one live pass that ran measured it at 0 of 234 answers, which
is not a finding about the market. A demo that runs live needs a brand that
exists. Until it tracks one, the banner says the instance is read-only and
does not claim to be measuring (`Base.DemoLive`).

The env var becomes `limelit.yaml`, and a schedule saved in Settings wins
over the file. That cannot happen on the demo because every Settings write is
refused in demo mode, but a seeded database must not carry a `schedule` row
in its `settings` table either, or it would override the deploy's `off`.
Check with `sqlite3 limelit.db "SELECT key FROM settings"` before pushing a
replica.

**Editing the replicated database.** Restore it locally, change it, and push
to a NEW `LITESTREAM_PATH`, then redeploy pointing at that path. Pushing to
the path a running instance replicates to loses the race: its generation
syncs every ten seconds and the next restore picks it, not yours.

Push the seeded database to the bucket once, before the first boot, and the
entrypoint restores it:

```bash
go run ./tools/seed -in seed.json -data ./seed-data
docker run --rm -v ./seed-data:/seed -v litestream.yml:/etc/litestream.yml:ro \
  -v ~/.config/gcloud/application_default_credentials.json:/adc.json:ro \
  -e GOOGLE_APPLICATION_CREDENTIALS=/adc.json litestream/litestream:0.3 \
  replicate -config /etc/litestream.yml -exec "sleep 25"
```

## Two things worth knowing

**`/healthz` does not reach the container on Cloud Run.** Cloud Run's own
frontend answers it with a Google 404 page. The app's health endpoint works
everywhere else; on Cloud Run, use any application path for a liveness check.

**The domain rides the existing load balancer.** `demo.limelit.co` is a host
rule on the `limelit.co` URL map pointing at a serverless NEG for this
service, ahead of the default that serves everything else. The wildcard
certificate already covered it. A backend service for a serverless NEG must
not carry a port name: the first attempt used `--protocol HTTPS`, which set
one, and the NEG refused to attach.

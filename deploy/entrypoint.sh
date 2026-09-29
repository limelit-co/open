#!/bin/sh
# Boot: restore the database from the bucket if this container has none, then
# run the server under Litestream so every change streams back out.
#
# The restore is what makes a Cloud Run instance durable. Containers here are
# replaced without warning, and each new one starts with an empty /data. If
# the bucket holds a replica, this pulls it before the server opens the file;
# if not (first boot ever), the server creates a fresh database and Litestream
# starts replicating it.
set -eu

DB=/data/limelit.db
: "${PORT:=1515}"

# Schedule and the spend ceiling arrive as environment variables and become
# the config file the binary reads, so one image serves any deployment and
# the operator changes cadence with a redeploy rather than a rebuild.
CFG=""
if [ -n "${LIMELIT_SCHEDULE:-}" ]; then
  printf 'schedule: %s\nlimits:\n  runs_per_day: %s\n' \
    "$LIMELIT_SCHEDULE" "${LIMELIT_RUNS_PER_DAY:-200}" > /tmp/limelit.yaml
  CFG="--config /tmp/limelit.yaml"
fi

if [ -z "${LITESTREAM_BUCKET:-}" ]; then
  echo "entrypoint: LITESTREAM_BUCKET is not set; running without replication, so the database lives only in /data (mount a volume there to keep it)" >&2
  exec limelit serve --addr ":${PORT}" $CFG
fi

if [ ! -f "$DB" ]; then
  echo "entrypoint: no local database, restoring from gs://${LITESTREAM_BUCKET}/${LITESTREAM_PATH:-limelit}" >&2
  if litestream restore -if-replica-exists -config /etc/litestream.yml "$DB"; then
    echo "entrypoint: restored" >&2
  else
    echo "entrypoint: nothing to restore, starting fresh" >&2
  fi
fi

exec litestream replicate -config /etc/litestream.yml -exec "limelit serve --addr :${PORT} $CFG"

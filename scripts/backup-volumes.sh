#!/bin/sh
# Cold backup of the stack's named volumes: stops the stack, tars each volume, starts it again.
#   scripts/backup-volumes.sh /var/backups/provenance        (run from the repo root, where .env is)
#   cron: 0 3 * * * cd /srv/provenance && scripts/backup-volumes.sh /var/backups/provenance
# Restore one volume (stack stopped):
#   docker run --rm -v <project>_<volume>:/v -v <backup-dir>:/b alpine sh -c 'rm -rf /v/* && tar -xzf /b/<volume>.tar.gz -C /v'
# shortcut: a cold copy costs a minute or so of downtime and keeps every backup; move to managed databases or
# per-store dumps (pg_dump, mongodump, ...) and add pruning if that matters.
set -eu
export COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.yml:docker-compose.prod.yml}"
project=$(docker compose config | sed -n 's/^name: //p' | head -1)
volumes=$(docker compose config --volumes)
[ -n "$volumes" ] || { echo "no named volumes in $COMPOSE_FILE" >&2; exit 1; }
dir="${1:-./backups}/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)

docker compose stop
trap 'docker compose start' EXIT # restart even if a copy fails
for v in $volumes; do
  docker run --rm -v "${project}_$v:/v:ro" -v "$dir:/b" alpine tar -czf "/b/$v.tar.gz" -C /v .
  echo "backed up $v"
done
echo "backups in $dir"

#!/bin/sh
# Deletes every sandbox, then stops Online Boutique and BranchBox (Linux).
# Pass --purge to also delete BranchBox's data volume.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
project="$root/projects/online-boutique"

# Sandbox containers are outside the compose project; remove them first so
# the shared network can be torn down.
leftovers=$(docker ps -aq --filter label=branchbox.managed=true)
[ -n "$leftovers" ] && docker rm -f $leftovers >/dev/null

volumes=""
[ "${1:-}" = "--purge" ] && volumes="--volumes"
docker compose -f "$project/docker-compose.yml" -f "$project/docker-compose.branchbox.yml" down --remove-orphans $volumes

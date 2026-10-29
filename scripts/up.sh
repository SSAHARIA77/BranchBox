#!/bin/sh
# Starts Online Boutique with BranchBox attached (Linux / cloud VM).
# Deployment settings come from projects/online-boutique/.env (see .env.example).
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
project="$root/projects/online-boutique"

if [ -f "$project/.env" ] && grep -q '^BB_BIND_ADDRESS=0\.0\.0\.0' "$project/.env" \
   && ! grep -q '^BB_API_TOKEN=..' "$project/.env"; then
    echo "error: BB_BIND_ADDRESS=0.0.0.0 requires BB_API_TOKEN in $project/.env" >&2
    exit 1
fi

docker compose -f "$project/docker-compose.yml" -f "$project/docker-compose.branchbox.yml" up -d --build

if command -v go >/dev/null 2>&1; then
    (cd "$root" && go build -o bin/branchbox ./cmd/branchbox)
fi
domain=$(grep -s '^BB_EDGE_DOMAIN=' "$project/.env" | cut -d= -f2 || true)
echo
echo "BranchBox is running. Shop: http://${domain:-localhost}:8080   Controller: port 8090"

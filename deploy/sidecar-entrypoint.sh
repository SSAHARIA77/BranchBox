#!/bin/sh
# Entrypoint of the branchbox/proxy image.
#
#   sidecar  install transparent-interception rules, then run the proxy as
#            the unprivileged user 1337
#   edge     run the edge proxy as user 1337 (no iptables needed)
set -eu

PROXY_UID=1337
PROXY_PORT="${BB_LISTEN_PORT:-15001}"
CHAIN=BRANCHBOX_OUTPUT

if [ "${1:-}" = "sidecar" ]; then
    # Idempotent: the chain is rebuilt from scratch if the sidecar restarts
    # inside an existing network namespace.
    iptables -t nat -D OUTPUT -p tcp -j "$CHAIN" 2>/dev/null || true
    iptables -t nat -F "$CHAIN" 2>/dev/null || iptables -t nat -N "$CHAIN"

    # 1. The proxy's own connections (to upstreams, the controller) are made
    #    as uid 1337 and must never be redirected back into the proxy. The
    #    application can run as any other user — including root.
    iptables -t nat -A "$CHAIN" -m owner --uid-owner "$PROXY_UID" -j RETURN
    # 2. Loopback traffic (including Docker's embedded DNS at 127.0.0.11).
    iptables -t nat -A "$CHAIN" -d 127.0.0.0/8 -j RETURN
    # 3. Optional opt-outs, e.g. BB_EXCLUDE_OUTBOUND_PORTS="3306,5432".
    for port in $(echo "${BB_EXCLUDE_OUTBOUND_PORTS:-}" | tr ',' ' '); do
        iptables -t nat -A "$CHAIN" -p tcp --dport "$port" -j RETURN
    done
    # 4. Every other outbound TCP connection goes to the proxy.
    iptables -t nat -A "$CHAIN" -p tcp -j REDIRECT --to-ports "$PROXY_PORT"
    iptables -t nat -A OUTPUT -p tcp -j "$CHAIN"

    echo "[entrypoint] interception rules installed for ${BB_SERVICE:-?}:"
    iptables -t nat -S "$CHAIN"
fi

exec su-exec "$PROXY_UID:$PROXY_UID" /usr/local/bin/branchbox-proxy "$@"

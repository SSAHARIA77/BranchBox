# BranchBox

**Deploy only what changed. Test against everything else, live.**

BranchBox is a lightweight, self-hostable platform for testing a change to one
microservice without duplicating the whole application. A developer builds
*only* the changed service from their branch as an isolated **sandbox**;
every other service keeps running from one shared, stable **baseline**.
Requests tagged with the sandbox's environment ID are diverted to the sandbox
at exactly the hop where it replaces the baseline — every other request, and
every other hop, is untouched.

This repository contains the Major Project-I (7th semester) deliverable: the
request-routing and context-propagation mechanism, a CLI-driven sandbox
orchestrator, and automated validation — running on
[Google Online Boutique](https://github.com/GoogleCloudPlatform/microservices-demo)
(10 services, 5 languages, gRPC + HTTP) **without any change to its code**.

---

## Contents

1. [How it works](#how-it-works)
2. [Quick start (Windows)](#quick-start-windows)
3. [Using sandboxes](#using-sandboxes)
4. [Validation (MP-I success criteria)](#validation-mp-i-success-criteria)
5. [CLI reference](#cli-reference)
6. [Repository layout](#repository-layout)
7. [Onboarding a different application](#onboarding-a-different-application)
8. [Design decisions](#design-decisions)
9. [Known limitations](#known-limitations)
10. [Troubleshooting](#troubleshooting)

---

## How it works

```
                 browser / curl
                       │   http://diwali.localhost:8080   (or header  baggage: branchbox-env=diwali)
                       ▼
               ┌───────────────┐   tags the request (W3C baggage), starts a trace,
               │ branchbox-edge │   routes the entry service
               └───────┬───────┘
                       ▼
 ┌──────────── pod: frontend ────────────┐      ┌────────────── branchbox-controller ──────────────┐
 │ frontend (unchanged Google image)     │      │ REST API · route table (long-poll) · event log    │
 │   │ outbound call                     │      │ builds sandboxes: git branch → docker build → run │
 │   ▼ iptables REDIRECT                 │◄────►│                                                   │
 │ sidecar: is there a sandbox for       │      └───────────────────────────────────────────────────┘
 │  (productcatalogservice, "diwali")?   │
 └──────┬───────────────────────┬────────┘
   no   │                       │ yes
        ▼                       ▼
 productcatalogservice     bb-diwali-productcatalogservice
 (baseline)                (sandbox: built from feature/diwali-sale)
```

* **Transparent interception.** Every service runs in a two-container "pod"
  with a BranchBox sidecar that owns the network namespace. The sidecar
  installs `iptables` rules that redirect all of the service's *outbound*
  TCP connections to itself, and recovers the original destination with
  `SO_ORIGINAL_DST` — the same primitive Istio uses. The application is not
  modified and does not know BranchBox exists.
* **Per-request L7 routing.** The sidecar speaks HTTP/1.1 and HTTP/2
  (gRPC) and decides *per request* — not per connection, because one gRPC
  connection carries requests from many environments. It identifies the
  destination service from the `:authority`/`Host` the app dialled, and
  forwards to the sandbox if one exists for *(service, env ID)*, otherwise to
  the original destination. Non-HTTP traffic (Redis, databases) is relayed
  untouched.
* **Context propagation.** The environment ID travels as a W3C Baggage
  entry (`baggage: branchbox-env=<id>`). Online Boutique's services are
  OpenTelemetry-instrumented and forward baggage on every call, so the ID
  survives every hop — Go, Python, Node.js, C# and Java services alike —
  with zero code changes.
* **Sandbox lifecycle.** The controller fetches the branch, exports *only*
  that service's directory (`git archive`), builds it with its own
  Dockerfile, starts it with the **same configuration as the baseline
  container it replaces** (copied from the running container), waits until
  it listens, and only then publishes the route. Deleting unpublishes the
  route first, so traffic falls back to the baseline before anything is
  removed.
* **Observability.** Every proxy reports each routing decision (trace ID,
  env, source → destination, baseline/sandbox, status, latency) to the
  controller. `branchbox trace <id>` shows the complete path of one request.

---

## Quick start (Windows)

Prerequisites: Docker Desktop (WSL2 backend, running), Git, Go 1.25+.
About 4 GB of RAM for Docker and ~10 GB of disk.

Expected folder layout (the controller builds sandboxes from the sibling
Online Boutique repository):

```
C:\BranchBox\
├── branchbox\           this repository
└── microservices-demo\  Online Boutique (team fork; demo branches live here)
```

```powershell
cd C:\BranchBox\branchbox
.\scripts\up.ps1            # pulls Google's images, builds BranchBox, starts everything
.\bin\branchbox.exe status
```

Open **http://localhost:8080** — the unmodified shop, served by the baseline.

To stop: `.\scripts\down.ps1` (deletes sandboxes first). `-Purge` also wipes
BranchBox's state volume. To run plain Online Boutique without BranchBox:
`.\scripts\up.ps1 -BaselineOnly` (shop on http://localhost:8081).

Equivalent raw commands (also work on Linux):

```bash
cd projects/online-boutique
docker compose -f docker-compose.yml -f docker-compose.branchbox.yml up -d --build
go build -o bin/branchbox ./cmd/branchbox
```

---

## Using sandboxes

The `microservices-demo` repository has four demo branches on top of `main`
(= release v0.10.7):

| Branch | Service | Visible effect |
|---|---|---|
| `feature/diwali-sale` | productcatalogservice | 20% off, "(Diwali Sale -20%)" in product names |
| `feature/clearance-50` | productcatalogservice | 50% off, "(Clearance -50%)" — a *second* branch of the same service |
| `feature/express-shipping` | shippingservice | flat $4.99 shipping, tracking IDs start with `EXP-` |
| `fix/payment-validation` | paymentservice | orders above 500 USD are declined for fraud review |

```powershell
.\bin\branchbox.exe sandbox create --service productcatalogservice --branch feature/diwali-sale --id diwali
```

```
Creating sandbox diwali: productcatalogservice from branch "feature/diwali-sale"
  [  0.0s] building: fetching branch and building the image
  [ 70.5s] starting: launching sidecar and service containers
  [ 71.5s] ready

Sandbox diwali is ready in 71.1s
  service   productcatalogservice (branch feature/diwali-sale, commit e967825)
  address   bb-diwali-productcatalogservice:3550

Send traffic through it:
  browser   http://diwali.localhost:8080
  curl      curl -H "baggage: branchbox-env=diwali" http://localhost:8080/
Untagged traffic keeps using the baseline.
```

The first build of a service also downloads its base images; later builds
reuse Docker's layer cache (the payment sandbox took 46.8s).

Open `http://localhost:8080` and `http://diwali.localhost:8080` side by side:
same shop, same cart service, same everything — except the product catalog.
Every response from the edge carries an `X-Branchbox-Trace` header; inspect
the request's full path with:

```powershell
.\bin\branchbox.exe trace <trace-id>
```

```
TIME  ENV     FROM      TO                     ROUTED TO  TARGET                                STATUS
+0ms  diwali  edge      frontend               baseline   frontend:8080                         200
+3ms  diwali  frontend  productcatalogservice  SANDBOX    bb-diwali-productcatalogservice:3550  200/grpc 0
+5ms  diwali  frontend  cartservice            baseline   172.22.0.12:7070                      200/grpc 0
...
14 hops, 1 served by a sandbox, 13 by the baseline
```

Other useful commands: `sandbox list`, `sandbox logs diwali [--component sidecar|build] [--follow]`,
`events --env diwali`, `sandbox delete diwali`.

**Choosing a sandbox in a browser.** Besides `http://<id>.localhost:8080`
(which needs wildcard DNS), any browser can switch itself into a sandbox
with `http://<server>:8080/__branchbox/env/<id>`. The edge remembers the
choice in a cookie, so only that browser is affected, and shows a banner on
every page naming the environment. `http://<server>:8080/__branchbox` lists
the active sandboxes; `/__branchbox/exit` returns to production. This works
on any network with no DNS setup.

**Shared server.** To let teammates create and use sandboxes from their own
laptops, copy `projects/online-boutique/.env.example` to `.env` and set
`BB_BIND_ADDRESS`, `BB_EDGE_DOMAIN`, `BB_API_TOKEN` and `BB_REPOSITORY_URL`.
Teammates only need Git and `branchbox.exe`, with `BRANCHBOX_CONTROLLER` and
`BRANCHBOX_TOKEN` set. The full three-laptop walkthrough is in
[docs/team-demo.md](docs/team-demo.md); a single-laptop script is in
[docs/demo-script.md](docs/demo-script.md).

---

## Validation (MP-I success criteria)

```powershell
go run ./test/e2e
```

The suite creates three sandboxes (`e2e-sale` and `e2e-clear` — two branches
of the **same** service — plus `e2e-ship`), runs complete shopper journeys
(home page → add to cart → checkout), each under its own trace ID, and
checks every routing decision the proxies recorded:

```
================ BranchBox MP-I validation ================
  PASS  C1  Untagged requests are served entirely by the baseline  (48 hops checked)
  PASS  C2  Tagged requests are routed to the sandbox only at the correct hop  (144 hops checked)
  PASS  C3  The environment ID survives every intermediate call  (144 hops checked)
  PASS  C4  Concurrent sandboxes never interfere (incl. two of the same service)  (1677 hops checked)
  PASS  C5  After teardown, traffic falls back to the baseline  (192 hops checked)
===========================================================
```

C3 specifically requires sandbox decisions at *second-level* hops
(`checkoutservice → productcatalogservice`, `recommendationservice →
productcatalogservice`, `checkoutservice → shippingservice`), proving the ID
crossed an intermediate service. Flags: `-rounds N` (concurrency rounds),
`-keep` (skip teardown). Unit tests: `go test ./...`.

---

## CLI reference

```
branchbox [--controller URL] [--token TOKEN] <command>
           (or BRANCHBOX_CONTROLLER / BRANCHBOX_TOKEN environment variables)

  status                           controller, project and active routes
  services                         services that can be sandboxed
  sandbox create --service S --branch B [--id ID] [--no-wait]
  sandbox list
  sandbox get ID
  sandbox logs ID [--component app|sidecar|build] [--tail N] [--follow]
  sandbox delete ID... | --all
  trace TRACE_ID                   every routing decision for one request
  events [--env ID] [--limit N]    recent routing decisions
```

Controller REST API (the MP-II dashboard will use the same endpoints):

| Method & path | Purpose |
|---|---|
| `GET /v1/project` | project configuration |
| `GET /v1/routes[?wait=<version>]` | route table; long-polls until the version changes |
| `GET/POST /v1/sandboxes` | list / create (`{"service","branch","id"}`) |
| `GET/DELETE /v1/sandboxes/{id}` | inspect / delete |
| `GET /v1/sandboxes/{id}/logs?component=app\|sidecar\|build&follow=1` | logs |
| `POST /v1/events`, `GET /v1/events?env=&trace=&limit=` | routing events |
| `GET /v1/traces/{traceId}` | all hops of one request |

---

## Repository layout

```
cmd/
  branchbox/      developer CLI
  controller/     control plane
  proxy/          data plane: "sidecar" and "edge" modes
internal/
  envid/          environment-ID extraction/injection (W3C baggage, traceparent)
  routing/        proxy-side route table, synced by long-polling the controller
  proxy/          interception (SO_ORIGINAL_DST), protocol sniffing, L7 forwarding
  controller/     sandbox lifecycle manager, route hub, event log, REST API
  runtime/        Runtime interface + Docker implementation (Kubernetes later)
  gitsrc/         branch fetch and per-service source export
  project/        branchbox.yaml loader
deploy/           Dockerfiles and the sidecar entrypoint (iptables rules)
projects/online-boutique/
  docker-compose.yml            baseline: faithful translation of Google's release manifest
  docker-compose.branchbox.yml  BranchBox layer (controller, edge, sidecars)
  branchbox.yaml                project definition (the only app-specific input)
scripts/          up.ps1 / down.ps1
test/e2e/         automated validation of the MP-I success criteria
```

---

## Onboarding a different application

Nothing in `cmd/` or `internal/` refers to Online Boutique. To onboard
another application:

1. Put its baseline compose file in `projects/<name>/docker-compose.yml`
   (unchanged; give its network a fixed `name`).
2. Write `projects/<name>/branchbox.yaml`: repository URL, compose project,
   network, entry service, and for each service its DNS name, port and
   build context.
3. Write `docker-compose.branchbox.yml` following the same mechanical
   pattern (one `<svc>-sidecar` with the service's DNS alias, and the service
   joined to it via `network_mode`).

Requirement on the application: services must propagate W3C trace context /
baggage (standard OpenTelemetry instrumentation does this). In Major
Project-II, steps 2–3 are generated by the repository onboarding and
discovery modules.

To build from GitHub instead of the local folder, push the fork and set
`repository.url` in `branchbox.yaml` to `https://github.com/<org>/microservices-demo.git`.

---

## Design decisions

| Decision | Why |
|---|---|
| Transparent sidecar (iptables + `SO_ORIGINAL_DST`) instead of an SDK | the application stays unmodified; generic across languages |
| Sidecar owns the network namespace ("pod" pattern) | interception is in place before the app opens its first connection (gRPC dials eagerly) |
| Proxy runs as uid 1337, excluded from redirection | avoids redirect loops even when the app runs as root (Online Boutique's frontend does) |
| Per-request routing at L7 | gRPC multiplexes many environments over one connection |
| Destination identified by `:authority`/`Host`, IP as fallback | payment and shipping both listen on 50051; the port alone is ambiguous |
| W3C Baggage for the env ID | already propagated by OpenTelemetry instrumentation — no app changes |
| Sandbox inherits the baseline container's environment | zero duplicated configuration; the sandbox differs only in its code |
| Route published only when the sandbox listens; removed before teardown | no request is ever routed to a sandbox that is not serving |
| Long-poll route table | changes reach every proxy within milliseconds, no constant polling |
| `Runtime` interface | Docker now; Kubernetes can be added without touching the controller |

---

## Known limitations

* **Shared data stores.** Sandboxes use the baseline's databases (e.g. the
  cart's Redis). Data isolation is out of scope for MP-I.
* **Propagation is the application's job.** The ID only survives services
  that forward W3C context. Online Boutique does on every hop.
* **HTTP and gRPC only.** Message queues are planned for MP-II.
* **Single host.** Docker Compose on one machine; the `Runtime` interface is
  the extension point for Kubernetes.
* **Fork build fix.** `main` of the fork adds one build-time line to the two
  Node.js Dockerfiles (`NODE_OPTIONS=--network-family-autoselection-attempt-timeout=5000`)
  because `node-gyp` header downloads time out on high-latency networks. It
  has no runtime effect; the baseline still runs Google's v0.10.7 images.
* **Sandbox restarts.** If a sandbox's sidecar container is restarted, the
  sandbox loses networking; delete and recreate it.

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `cannot reach the BranchBox controller` | `docker ps` — is `branchbox-controller` up? Run `.\scripts\up.ps1`. |
| Sandbox `failed` | `branchbox sandbox logs <id> --component build` shows the full build log. |
| `baseline container for "x" is not running` | the baseline must be up before sandboxes are created. |
| `*.localhost` does not open in the browser | use Chrome/Edge, or send `baggage: branchbox-env=<id>` with curl. |
| Port 8080/8081/8090 already in use | stop the other process, or change the host ports in `docker-compose.branchbox.yml`. |
| `down.ps1` reports the network is in use | `docker ps -a --filter label=branchbox.managed=true` and remove leftovers. |

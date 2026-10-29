# BranchBox — Demonstration Script (Major Project-I)

Runtime: about 12 minutes. Every step maps to one of the Major Project-I
success criteria in the mid-semester report (Section 4.4.1).

## Before the demo (10 minutes earlier)

```powershell
cd C:\BranchBox\branchbox
.\scripts\down.ps1                 # clean slate (keeps the build cache)
.\scripts\up.ps1
.\bin\branchbox.exe status         # 10 services, 0 routes
```

Pre-build the sandboxes so nobody waits for `docker build` during the demo
(each takes ~45-70 s the first time):

```powershell
.\bin\branchbox.exe sandbox create --service productcatalogservice --branch feature/diwali-sale --id diwali
.\bin\branchbox.exe sandbox create --service shippingservice --branch feature/express-shipping --id express
.\bin\branchbox.exe sandbox create --service paymentservice --branch fix/payment-validation --id payfix
```

Keep one sandbox (e.g. `clearance`) to create live, so the build is shown once.
Open three browser windows (Chrome or Edge): `http://localhost:8080`,
`http://diwali.localhost:8080`, `http://express.localhost:8080`.

## 1. The baseline (1 min)

* Show `projects/online-boutique/docker-compose.yml`: Google's Online
  Boutique v0.10.7, Google's own images, **no BranchBox code inside**.
* `docker ps` — 11 baseline containers, each with a `-sidecar` next to it.
* Browse `http://localhost:8080`: Sunglasses cost $19.99.

## 2. Creating a sandbox from a branch (2 min)

```powershell
cd C:\BranchBox\microservices-demo
git log --oneline main..feature/clearance-50     # one commit: 50% off
cd C:\BranchBox\branchbox
.\bin\branchbox.exe sandbox create --service productcatalogservice --branch feature/clearance-50 --id clearance
```

Talking point: *only* `productcatalogservice` was built — from the branch's
own Dockerfile — and started with the exact configuration of the baseline
container it replaces. Nothing else was duplicated.

```powershell
.\bin\branchbox.exe sandbox list
```

## 3. Untagged traffic is untouched (criterion 1)

Refresh `http://localhost:8080` — still $19.99, no sale names.

## 4. Tagged traffic is diverted at exactly one hop (criterion 2)

* `http://diwali.localhost:8080` — "(Diwali Sale -20%)".
* `http://clearance.localhost:8080` — "(Clearance -50%)".
* Same cart service, same currency service, same everything else.

Show the proof for one request (copy `X-Branchbox-Trace` from the browser
dev tools, or):

```powershell
curl.exe -s -D - -o NUL -H "baggage: branchbox-env=diwali" http://localhost:8080/ | findstr /i x-branchbox-trace
.\bin\branchbox.exe trace <trace-id>
```

Exactly one line says `SANDBOX` (`frontend → productcatalogservice`); all
other hops say `baseline`.

## 5. The ID survives every intermediate hop (criterion 3)

In `http://express.localhost:8080`: add an item, open the cart — shipping is
$4.99 (frontend → shippingservice, hop 2). Place the order — tracking ID
starts with `EXP-` (frontend → checkoutservice → shippingservice, hop 3).

Payment fix (hop 3, gRPC, Node.js service): in `http://payfix.localhost:8080`
order 5 × Watch ($545) → *"Payment declined: orders above 500 USD require
manual fraud review."* The same order on `http://localhost:8080` succeeds.

```powershell
.\bin\branchbox.exe events --env payfix --limit 10
.\bin\branchbox.exe sandbox logs payfix --tail 5
```

Talking point: the frontend, checkout and payment services are written in
Go and Node.js by Google; none were modified. The ID travels as W3C
baggage, which their OpenTelemetry instrumentation forwards.

## 6. Concurrent sandboxes never interfere (criterion 4)

Four windows open at once (baseline, diwali, clearance, express): two
sandboxes of the *same* service plus one of another, all live together.
Then run the automated proof:

```powershell
go run ./test/e2e
```

It runs full shopper journeys for every environment concurrently and checks
every recorded routing decision — over 2,000 hops — then tears its own
sandboxes down (C5).

## 7. Teardown (1 min)

```powershell
.\bin\branchbox.exe sandbox delete diwali
```

Refresh `http://diwali.localhost:8080` — back to $19.99: the tag remains,
but with no sandbox the request falls back to the baseline. The baseline was
never restarted (`docker ps` uptime).

## Questions to expect

| Question | Answer |
|---|---|
| Why not just run docker compose per developer? | 10 services per developer vs. 1. The infrastructure grows by one container pair per sandbox, not one application per developer (quantified in MP-II). |
| How does the app forward the ID without changes? | W3C Baggage via OpenTelemetry instrumentation that Online Boutique already ships with. Apps without it need standard OTel instrumentation — the same requirement Istio documents. |
| How is traffic intercepted? | iptables REDIRECT in the pod's network namespace + `SO_ORIGINAL_DST`, as in Istio. |
| What about databases? | Shared with the baseline in MP-I — explicitly listed as a limitation. |
| Can it run on Kubernetes? | The sandbox runtime is an interface; a Kubernetes implementation is MP-II extension work. |
| Is it tied to Online Boutique? | No — the only app-specific inputs are `branchbox.yaml` and the compose overlay; MP-II adds automatic onboarding and a second application. |

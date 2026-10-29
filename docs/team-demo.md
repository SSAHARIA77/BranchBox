# Three-laptop demonstration

**Goal:** Online Boutique runs on one shared "production" server. Two
developers, each on their own laptop, push a branch and see their change on
production — in their own browser only — without deploying anything. The
production screen never changes.

| Laptop | Role | Needs |
|---|---|---|
| **A** (server) | runs production: Online Boutique + BranchBox; shows the production shop | Docker Desktop, this repo |
| **B** (developer 1) | changes one service on a branch, tests it on production | Git, `branchbox.exe`, a browser |
| **C** (developer 2) | changes a different service, tests it independently | Git, `branchbox.exe`, a browser |

Developers need **no Docker and no copy of the application** — that is the point.

```
 Laptop B ──git push──► GitHub fork ◄──git fetch── controller ──docker build/run──► sandbox
    │                                                  ▲
    └── branchbox sandbox create ─────────────────────┘        Laptop A: production
    └── browser: http://<A>:8080/__branchbox/env/<id> ──► edge ──► baseline + sandbox
```

---

## 1. Choose where "production" runs

**Option 1 — Laptop A on a private hotspot (recommended for the guide demo).**
Free, works offline from college infrastructure. College Wi-Fi usually blocks
laptop-to-laptop traffic, and NITK's DNS breaks wildcard names, so do not
use NITK-NET for the three laptops. Instead:

* turn on **Mobile Hotspot** on laptop A (Windows Settings → Network → Mobile
  hotspot) or use a phone hotspot, and connect B and C to it;
* A's address on its own hotspot is usually `192.168.137.1`
  (`ipconfig` → "Local Area Connection* N").

**Option 2 — a cloud VM** (Azure for Students / GitHub Student Pack credit;
x86, 2 vCPU, 8 GB, Ubuntu). More "production"-like and reachable from
anywhere, but costs setup time and credit, and the college network may block
ports 8080/8090 — check before relying on it. Use `scripts/up.sh`.

Everything below is identical for both options; only the address differs.

## 2. One-time: the shared Git repository

Sandboxes are built from GitHub, so B and C can push branches.

1. On GitHub, **fork** `GoogleCloudPlatform/microservices-demo` (keep it
   public so the controller can clone it without credentials) and add B and C
   as collaborators.
2. On laptop A, publish the prepared `main` (= v0.10.7, the version the
   production images run) and the demo branches:

   ```powershell
   cd C:\BranchBox\microservices-demo
   git remote rename origin upstream
   git remote add origin https://github.com/<you>/microservices-demo.git
   git push --force origin main      # replaces the fork's copy of Google's newer main
   git push origin feature/diwali-sale feature/clearance-50 feature/express-shipping fix/payment-validation
   ```

   If the push says *shallow update not allowed*, run
   `git fetch --unshallow upstream` first.

## 3. One-time: configure laptop A

Create `C:\BranchBox\branchbox\projects\online-boutique\.env`
(template: `.env.example`):

```ini
BB_BIND_ADDRESS=0.0.0.0
BB_EDGE_DOMAIN=192.168.137.1          # A's address on the hotspot (or the VM's public IP)
BB_API_TOKEN=<random 32 characters>    # see .env.example for a one-line generator
BB_REPOSITORY_URL=https://github.com/<you>/microservices-demo.git
```

Allow the two ports through Windows Firewall (PowerShell **as
Administrator**; delete the rule after the demo):

```powershell
New-NetFirewallRule -DisplayName "BranchBox demo" -Direction Inbound -Protocol TCP -LocalPort 8080,8090 -Action Allow
# afterwards: Remove-NetFirewallRule -DisplayName "BranchBox demo"
```

Ports 8080/8090 must be free on A (`docker ps` — stop other projects using
them, or set `BB_EDGE_PORT` / `BB_DIRECT_PORT` in `.env`).

Start production:

```powershell
cd C:\BranchBox\branchbox
.\scripts\down.ps1; .\scripts\up.ps1
```

**Security:** while `BB_BIND_ADDRESS=0.0.0.0`, anyone on the same network can
reach port 8090; the token is what protects it (the controller can run
containers). Share the token only with B and C, and switch back to
`127.0.0.1` (or delete `.env`) after the demo.

## 4. One-time: laptops B and C

1. Install Git. Copy `C:\BranchBox\branchbox\bin\branchbox.exe` from A to
   e.g. `C:\branchbox\` (no Go or Docker needed).
2. Clone the fork: `git clone https://github.com/<you>/microservices-demo.git`
3. Point the CLI at A (new terminals pick these up):

   ```powershell
   setx BRANCHBOX_CONTROLLER http://192.168.137.1:8090
   setx BRANCHBOX_TOKEN <the token>
   ```

4. Check: `C:\branchbox\branchbox.exe status` → `Controller ... (reachable)`.

## 5. Rehearsal (30 minutes before)

* Connect to the hotspot, run `status` on B and C.
* **Warm the build cache** — create and delete one sandbox per service you
  will use, so builds during the demo take ~10-20 s instead of ~70 s:
  `branchbox sandbox create --service productcatalogservice --branch feature/diwali-sale --id warm1`
  (same for `shippingservice` / `feature/express-shipping`), then
  `branchbox sandbox delete warm1 warm2`.
* Prepare the live edits below in an editor but do not commit them yet.

## 6. The demonstration (~10 minutes)

**A — production.** Open `http://192.168.137.1:8080` on A (and on the
projector). Sunglasses cost $19.99. Show `branchbox sandbox list` (empty) and
`docker ps` (the production services).

**B — developer 1 changes the product catalog, live.**

```powershell
cd microservices-demo
git switch -c feature/anshi-sale
# edit src/productcatalogservice/products.json: e.g. rename "Sunglasses" to "Sunglasses - Anshi's sale", price 9
git commit -am "catalog: weekend sale"
git push -u origin feature/anshi-sale
branchbox sandbox create --service productcatalogservice --branch feature/anshi-sale --id anshi
```

Open the printed link `http://192.168.137.1:8080/__branchbox/env/anshi` →
the shop shows the change, with a green banner *"BranchBox environment
anshi — sandboxed: productcatalogservice · everything else from production"*.

**C — developer 2 changes shipping, at the same time.**

```powershell
git switch -c feature/lavanya-shipping main
# edit src/shippingservice/quote.go: CreateQuoteFromFloat(8.99) -> CreateQuoteFromFloat(1.99)
git commit -am "shipping: promotional rate"
git push -u origin feature/lavanya-shipping
branchbox sandbox create --service shippingservice --branch feature/lavanya-shipping --id lavanya
```

Open `http://192.168.137.1:8080/__branchbox/env/lavanya`, add an item, view the
cart → shipping $1.99. The catalog is **not** B's sale.

**Point to A.** Refresh production: still $19.99 and $8.99 shipping. Open
`http://192.168.137.1:8080/__branchbox` → both sandboxes listed; production
is untouched. Run `branchbox sandbox list` on A: two sandboxes, each one
container pair — not two copies of the 10-service application.

**Prove the routing.** On A: `branchbox events --env anshi --limit 15` — only
`productcatalogservice` hops say `SANDBOX`; every other hop is `baseline`.
Checkout on B or C and show `branchbox trace <id>` (the `X-Branchbox-Trace`
response header, visible in the browser's developer tools → Network).

**Clean up.** B: `branchbox sandbox delete anshi`, refresh → banner says
*no sandbox with this ID — every service from production*; click
*back to production*. The production deployment was never rebuilt or
restarted.

If a live build fails, fall back to the prepared branches
(`feature/diwali-sale`, `feature/express-shipping`, `fix/payment-validation`).

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `status` on B/C: cannot reach the controller | not on the same hotspot, wrong IP, or firewall rule missing on A |
| `missing or invalid API token` | `BRANCHBOX_TOKEN` not set in *this* terminal (open a new one after `setx`) |
| Sandbox `failed` at "branch not found" | the branch was not pushed (`git push -u origin <branch>`) |
| Build failed | `branchbox sandbox logs <id> --component build` |
| Browser shows production after the link | cookies blocked, or the sandbox was deleted — open `/__branchbox` |
| Everything stopped after A rebooted / Docker restarted | run `.\scripts\up.ps1` again and recreate the sandboxes |

# BranchBox demo video — script (2½ minutes)

**Cast:** Anshi (Developer 1) and Lavanya (Developer 2).
**Story:** both are working on the **same microservice** (the product
catalog) at the same time. Normally they would fight over one shared staging
environment. With BranchBox, each pushes a branch, gets a private sandbox,
and tests on the **live production website** — without disturbing each other
or anyone else. At the end they delete their sandboxes and production is
exactly as it was.

| | Anshi | Lavanya |
|---|---|---|
| Feature | "Diwali Special" on **Sunglasses**, price $19.99 → **$9.99** | "Clearance" on **Watch**, price $109.99 → **$49.99** |
| Branch | `feature/anshi-diwali` | `feature/lavanya-clearance` |
| Sandbox ID | `anshi` | `lavanya` |
| Service | `productcatalogservice` | `productcatalogservice` (same service!) |

---

## Part A — Preparation (do this the day before; not recorded)

**Sankalp (Codespace owner):**

1. Start the Codespace, then in its terminal:
   ```bash
   ./scripts/up.sh
   export BRANCHBOX_TOKEN=$(grep '^BB_API_TOKEN=' projects/online-boutique/.env | cut -d= -f2)
   ./bin/branchbox production          # must say: Status in sync
   ./bin/branchbox sandbox delete --all # start clean
   ```
2. **PORTS** tab → ports **8080** and **8090** → *Port Visibility* → **Public**.
   Check the shop opens in an incognito window.
3. Build the CLI for Windows and give it to Anshi and Lavanya:
   ```bash
   GOOS=windows go build -o branchbox.exe ./cmd/branchbox
   ```
   In the Codespace file explorer, right-click `branchbox.exe` → *Download*.
4. Send them privately (not in a group chat):
   * production link: `https://<codespace>-8080.app.github.dev`
   * controller link: `https://<codespace>-8090.app.github.dev`
   * the token (`grep BB_API_TOKEN projects/online-boutique/.env`)
5. Make sure both are collaborators on `SSAHARIA77/GoogleOnlineBoutique`
   (GitHub → repo → Settings → Collaborators).
6. **Warm the build cache** so builds on camera take ~10–20 s, not ~1–2 min:
   ```bash
   ./bin/branchbox sandbox create --service productcatalogservice --branch feature/diwali-sale --id warmup
   ./bin/branchbox sandbox delete warmup
   ```

**Anshi and Lavanya (each on own Windows laptop):**

1. Install Git; put `branchbox.exe` in `C:\branchbox\`.
2. In PowerShell (then **close and reopen** PowerShell):
   ```powershell
   setx BRANCHBOX_CONTROLLER https://<codespace>-8090.app.github.dev
   setx BRANCHBOX_TOKEN <token>
   ```
3. Clone the application and open it in VS Code:
   ```powershell
   cd C:\
   git clone https://github.com/SSAHARIA77/GoogleOnlineBoutique.git
   code GoogleOnlineBoutique
   ```
4. Test: `C:\branchbox\branchbox.exe status` → `Controller ... (reachable)`.

**Recording setup:** screen recorder (OBS / Xbox Game Bar `Win+Alt+R`),
browser zoom 125 %, terminal font large, close notifications. Record each
scene as a separate clip; the editor joins them and speeds up the build
waits (scene 4).

---

## Part B — The script

Timings are approximate. **Narration** is spoken (or added as voice-over);
**Do** is what appears on screen.

### Scene 1 — Title (0:00–0:07)

**Do:** title card: *"BranchBox — Deploy only what changed. Test against
everything else, live."*

### Scene 2 — Production (0:07–0:22) · *Anshi's screen*

**Do:** open the production link. Scroll the home page; hover on
**Sunglasses ($19.99)** and **Watch ($109.99)**.

**Anshi:** "This is our company's online store running in production — ten
microservices working together: catalog, cart, checkout, payment, shipping
and more."

### Scene 3 — The problem (0:22–0:37) · *Lavanya's screen*

**Do:** show VS Code with `src/productcatalogservice/products.json` open.

**Lavanya:** "Anshi and I are both changing the same service — the product
catalog. Normally we would have to share one staging environment and take
turns, or each run all ten services ourselves. With BranchBox, we just push
our branches."

### Scene 4 — Make the change and push (0:37–1:12) · *both screens*

**Anshi — Do:** in VS Code terminal:
```powershell
git switch -c feature/anshi-diwali
```
Edit `src/productcatalogservice/products.json`, **Sunglasses** entry:
* `"name": "Sunglasses"` → `"name": "Sunglasses - Diwali Special"`
* `"units": 19` → `"units": 9`

Save, then:
```powershell
git commit -am "Diwali special on sunglasses"
git push -u origin feature/anshi-diwali
```

**Anshi:** "I've put a Diwali special on sunglasses and pushed my branch."

**Lavanya — Do:**
```powershell
git switch -c feature/lavanya-clearance
```
Edit the **Watch** entry:
* `"name": "Watch"` → `"name": "Watch - Clearance"`
* `"units": 109` → `"units": 49`

```powershell
git commit -am "Clearance price on watch"
git push -u origin feature/lavanya-clearance
```

**Lavanya:** "And I've added a clearance price on the watch — same file,
same service."

> Optional: show both branches on GitHub (repo → *branches*) for 2 seconds.

### Scene 5 — Create the sandboxes (1:12–1:40) · *both screens*

**Anshi — Do:**
```powershell
C:\branchbox\branchbox.exe sandbox create --service productcatalogservice --branch feature/anshi-diwali --id anshi
```
**Lavanya — Do:**
```powershell
C:\branchbox\branchbox.exe sandbox create --service productcatalogservice --branch feature/lavanya-clearance --id lavanya
```
(Editor: speed up the "building" wait; keep the final *"Sandbox … is ready
in …s"* line on screen.)

**Anshi:** "BranchBox builds only the one service we changed, from our
branch, and plugs it into production — just for us."

### Scene 6 — Both features, same website, no conflict (1:40–2:15)

Record this on **one laptop** with three browser windows side by side
(each window keeps its own sandbox choice):

| Window | Open | Shows |
|---|---|---|
| Chrome (normal) | the `…/__branchbox/env/anshi` link printed by Anshi's command | **Sunglasses - Diwali Special $9.99**, Watch $109.99, green banner *anshi* |
| Edge (or another browser) | the `…/__branchbox/env/lavanya` link | Sunglasses $19.99, **Watch - Clearance $49.99**, green banner *lavanya* |
| Chrome **incognito** | the plain production link | Sunglasses $19.99, Watch $109.99, no banner |

**Do:** refresh each window in turn; in Anshi's window add Sunglasses to the
cart to show the new price flows through cart and checkout.

> Online Boutique's currency service sometimes rounds a new price down by a
> cent (e.g. $9.99 shows as $9.98). That is the original app's behaviour, not
> BranchBox — the name change makes the difference obvious anyway.

**Lavanya:** "Same website, same moment. Anshi sees her Diwali special, I
see my clearance price, and real customers still see normal production.
Only the catalog service is ours — the cart, checkout and payment are the
real production services."

> Optional (5 s): on Anshi's laptop run
> `C:\branchbox\branchbox.exe events --env anshi --limit 8` — only the
> `productcatalogservice` lines say **SANDBOX**.

### Scene 7 — Clean up (2:15–2:35) · *both screens*

**Anshi — Do:** `C:\branchbox\branchbox.exe sandbox delete anshi`
**Lavanya — Do:** `C:\branchbox\branchbox.exe sandbox delete lavanya`

**Do:** refresh both sandbox windows — the banner says *no sandbox with this
ID*; click **back to production** → normal prices again.

**Anshi:** "When we're done we delete our sandboxes. Nothing in production
was ever redeployed or restarted."

### Scene 8 — Closing (2:35–2:45)

**Do:** closing card: *"BranchBox — one stable production, a lightweight
sandbox per developer. Anshi Sachan · Lavanya Rathi · Sankalp Saharia ·
Guide: Dr. Geetha V. · NIT Karnataka"*

---

## If something goes wrong while recording

| Problem | Fix |
|---|---|
| `missing or invalid API token` | reopen PowerShell after `setx`, or run the command with `--token <token>` |
| `cannot reach the BranchBox controller` | Codespace stopped, or port 8090 not Public |
| Sandbox `failed` | `branchbox.exe sandbox logs <id> --component build`; usually a JSON typo (missing comma/quote) — fix, commit, push, `sandbox delete <id>`, create again |
| `sandbox "anshi" already exists` | `branchbox.exe sandbox delete anshi`, then create again |
| Shop link shows a GitHub page | port 8080 is Private — set it to Public |
| Browser shows production after the sandbox link | cookies blocked, or open `…/__branchbox` and click the sandbox |

## After recording

* Delete the demo branches if you want a clean repo:
  `git push origin --delete feature/anshi-diwali` (and the Lavanya one).
* Do **not** merge them into `main` — production follows `main` and would
  change for everyone.

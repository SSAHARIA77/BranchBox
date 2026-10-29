# Running BranchBox in GitHub Codespaces

GitHub Codespaces gives a free x86 cloud machine (personal accounts include
monthly free core-hours; the GitHub Student Developer Pack adds more) with
public HTTPS links — no credit card, and it works on networks that block
unusual ports because everything goes over port 443.

The three-laptop demo in [team-demo.md](team-demo.md) is unchanged except
that "laptop A / the server" is the Codespace and the addresses are
`https://<codespace>-8080.app.github.dev` (shop) and
`https://<codespace>-8090.app.github.dev` (controller).

## 1. One-time GitHub settings

* GitHub → **Settings → Codespaces → Default idle timeout**: set the maximum
  (240 minutes), so the codespace does not stop during a demo.
* On `SSAHARIA77/GoogleOnlineBoutique` → **Settings → Collaborators**: add
  both teammates (they push their branches there).

## 2. Create the codespace

On https://github.com/SSAHARIA77/BranchBox-GoogleOnlineBoutique:
**Code → Codespaces → ⋯ → New with options** → Machine type **4-core**
(the config requests 4 cores / 16 GB) → **Create codespace**.

Wait for the editor to open (2–4 minutes the first time).

## 3. Start BranchBox

In the codespace terminal:

```bash
./scripts/codespace-setup.sh
```

The first run pulls Online Boutique and builds BranchBox (5–10 minutes). At
the end it prints the production URL and two `setx` lines for teammates.

Check the **PORTS** tab: 8080 and 8090 must show visibility **Public**
(right-click → Port Visibility → Public if the script could not set it).

Open the printed shop URL — that is production.

## 4. Teammates

On each teammate's Windows laptop (Git and `branchbox.exe` only):

```powershell
setx BRANCHBOX_CONTROLLER https://<codespace>-8090.app.github.dev
setx BRANCHBOX_TOKEN <token printed by the script>
# open a NEW terminal, then:
branchbox status
```

`branchbox.exe` can be built on any machine with Go:
`go build -o branchbox.exe ./cmd/branchbox` (or copy it from a laptop that has it).

Then follow section 6 of [team-demo.md](team-demo.md): push a branch, run
`branchbox sandbox create ...`, open the printed link.

## Day-to-day

| Task | How |
|---|---|
| Stop (save free hours) | github.com/codespaces → ⋯ → Stop codespace |
| Restart later | open the codespace again, then `./scripts/up.sh` (the `.env` and token are kept) |
| Sandboxes after a restart | recreate them (`branchbox sandbox create ...`) |
| Check usage | GitHub → Settings → Billing → Codespaces |

**Security:** while ports are public, anyone with the 8090 URL could reach the
controller — the token protects it. Share the token only with teammates and
set the ports back to Private (or stop the codespace) after the demo.

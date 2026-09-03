# Ferrule

> One local panel: every key held once, every model — local or cloud — visible,
> switchable, and accountable for what leaves your machine.

![Ferrule's board — every model, local and cloud, in one place](marketing/hero-x.png)

Ferrule is a **local key vault first, a model router second**. You mint a provider key
once, paste it into Ferrule, and Ferrule becomes the one encrypted local place that key
lives. The unified OpenAI-compatible endpoint, the model board, the alias ladders, and
the spend and egress views all fall out of the vault.

One statically-linked Go binary. No account, no telemetry, no server database, and
nothing of ours in the loop. Ferrule makes exactly one request on its own behalf — a
background fetch of the public model-capability catalog, carrying nothing about you,
which you can turn off.

*A ferrule is the fitting that binds many strands into a single clean termination.*

---

## What it does that the alternatives don't

**Zero-config discovery — probe, don't declare.** Every comparable tool asks you to
hand-write a config listing models and endpoints. Ferrule opens already knowing: it scans
localhost for running runtimes and adopts them, and adding a cloud provider is *paste a
key*, not *edit a file*. No config file, no restart.

For OpenAI-compatible sources that means everything the endpoint lists. Replicate is the
exception: it has no comparable listing, so Ferrule shows a curated slice and you reach
anything else by naming it — the passthrough lane routes by explicit model id anyway.

The panel is usable in **377 ms** cold. Getting a *routable* model takes as long as the
runtime takes to serve one real request — 0.17 s against a warm Ollama, a few seconds
with the model unloaded, up to a minute on a genuinely cold one. Ferrule ends every
adoption with a real request rather than trusting a listing, and both the CLI and the
board say so while they wait.

**Egress visibility — what actually left the machine.** Cost dashboards are everywhere; a
data-egress dashboard is not. Because Ferrule knows which requests stayed on-device and
which went to a provider, it shows you *where your prompts went*, not just what they
cost. Metadata only; content logging is off by default and, when on, stays local.

## Install

```
go install ferrule/cmd/ferrule@latest      # or: make build
./ferrule serve
```

Then open <http://localhost:8899>.

Prebuilt binaries for macOS (arm64/amd64), Linux (arm64/amd64), and Windows (amd64) are
attached to each release. No runtime dependencies.

## Use it

```
ferrule serve                     # the daemon: endpoints + the control panel
ferrule add                       # scan localhost and adopt what is running
ferrule add anthropic             # paste a key (read from the terminal, not from argv)
ferrule ls models --local         # every model on this machine
ferrule alias fast <src>/qwen3:8b <src>/llama-3.3-70b   # a ladder, tried in order
ferrule key my-app                # mint an app token, shown once
ferrule usage --egress            # what left the machine
```

Point any OpenAI-compatible client at Ferrule with that app token:

```
OPENAI_BASE_URL=http://localhost:8899/v1
OPENAI_API_KEY=frl_…              # a Ferrule app token, never a provider key
```

```python
from openai import OpenAI
c = OpenAI(base_url="http://localhost:8899/v1", api_key="frl_…")
c.chat.completions.create(model="fast", messages=[...])   # an alias
c.chat.completions.create(model="gpt-4o", messages=[...]) # remapped to whatever you chose
```

The `model` field may be an alias, a real model id, `source/model`, or an id you have
remapped — which is how Ferrule helps an app that will only ever send `gpt-4o`.

## The two lanes

The fault line is **normalization cost**, not text versus media.

- **Raw tokens — one unified endpoint.** `/v1/chat/completions`, `/v1/completions`,
  `/v1/embeddings`, `/v1/images/generations`. Point anything at it and call any chat or
  embeddings model, local or cloud, through one URL.
- **Media — native shape, key-managed.** Prediction-based providers (Replicate) are
  reached at `/p/<source>/…` with the provider's own request and response shape left
  byte-identical. Ferrule injects the stored key and logs the egress; it does not pretend
  Replicate is OpenAI-compatible. Unified media is a later, additive lane — never the
  thing that defines v1.

Both lanes share the vault, the app tokens, and the egress view. Cost and token counts
are token-lane only: a prediction API reports neither in any shape Ferrule could read
without normalising it, which is the treadmill this split exists to avoid. Passthrough
requests are still recorded — volume, latency, bytes, status, and where they went.

## Where your keys live

Keys are encrypted at rest with [age](https://age-encryption.org) in your config
directory (`~/.config/ferrule`, or `FERRULE_CONFIG_DIR`). A plaintext key never touches
SQLite, the logs, or the ledger — only an opaque vault reference does, and a test asserts
it. Everything else — sources, classified models, aliases, app tokens, the usage ledger —
is SQLite in the same directory. No Postgres. No server database.

The house rule elsewhere is *BYOK, never persisted*. Ferrule is the deliberate exception:
being the single encrypted local store for your keys **is** the value. Persisted locally,
encrypted, is not the same as persisted on someone's server. Nothing phones home, there
is no account, and a key only ever travels from your disk to the provider you gave it
for.

Two ways to hold the vault open, and they defend against different things:

- **Identity file** (default) — `vault.identity`, mode 0600, beside the store. The daemon
  starts unattended. This stops another account on the machine, and it stops the everyday
  leaks that put keys in a dozen `.env` files: a project-wide search, a screen share, a
  pasted diff. It does **not** stop anyone who copies the whole config directory — a
  backup or a cloud-drive sync takes both files, and two files is one decryption.
- **Passphrase** — `ferrule serve --passphrase`, or `FERRULE_PASSPHRASE`. Nothing that can
  open the vault is written to disk at all, so a copy of the directory is useless without
  you. The cost is that the daemon cannot start unattended.

Neither stops code already running as you against a live daemon; no local single-user
secret store can, because the daemon has to read the key to make the request. What
Ferrule offers there is the ledger: every use of every key is recorded, so a key used
behind your back is a key you can see was used.

An exported configuration is sealed under its own passphrase, so the file that leaves
this machine is not protected by anything left on it.

The daemon binds to localhost only. The control plane refuses cross-origin requests
unless you turn on the developer setting; the inference endpoints authenticate with an
app token instead.

## The agent face

Ferrule publishes its control operations as an MCP manifest at `/mcp` — list sources,
list models, read an alias, read usage. An agent orchestrating other tools can ask
Ferrule *"cheapest model with vision?"* and route itself.

Mutating operations **stage** rather than land: the agent proposes, you apply. A
provider key is withheld from the staged payload entirely and supplied by you at the
moment you apply. Minting a credential is person-only and is marked as such in the
manifest rather than hidden from it. Inference does not go through this face.

The manifest is generated from the same command bus the panel and the CLI dispatch
through, so `manifest ⊇ command bus` holds by construction — and a test asserts it.

## Try it without any keys

```
make demo
```

Stands up a whole Ferrule with fake providers — a detected local runtime, three cloud
sources, one that deliberately fails — mints app tokens, replays traffic so Usage and
Egress have something to show, and prints the URL. Nothing you own is involved and
nothing leaves the machine. Ctrl-C to stop; it tells you the scratch directory to delete.

If you have Ollama or LM Studio running, `ferrule add` with no arguments adopts it with
no key and no config file at all.

## Verify it yourself

```
make check     # gofmt, go vet, and the checkpoint harnesses
make dist      # all five targets, CGO off
```

`make check` refuses a skipped test as well as a failing one: two checkpoints need a
non-loopback address, and a machine that cannot run a gate has to say so rather than
quietly accept the package.

The harnesses are the gates, not self-reports: the add-a-source pipeline reaching `live`
for every seed provider and `failed` *with a visible reason* for a bad key; the OpenAI
SDK completing calls routed to a cloud and a local model with correct per-app ledger
attribution; a 100-request replay reproducing exact per-app, per-model, and per-egress
counts against golden totals; passthrough byte-identity against a canned fixture; the MCP
face staging its mutations. Design verification for the panel is recorded in
[design/VERIFICATION.md](design/VERIFICATION.md).

## Not doing

No accounts, no auth, no multi-user, no team key pool, no hosted backend — any of those
would make this a different product. No provider breadth for its own sake: a curated seed
set, widened only when a real request arrives. No telemetry, ever. And Ferrule does not
run models — llama.cpp, Ollama, and the providers do inference; Ferrule routes.

## License

MIT — see [LICENSE](LICENSE). The embedded typefaces (JetBrains Mono, IBM Plex Sans) are
OFL 1.1; see [NOTICE](NOTICE).

Vision, roadmap, and the full agent handoff: [FERRULE.md](FERRULE.md).
The agent face, for a coding agent pointed at this repo: [llms.txt](llms.txt).

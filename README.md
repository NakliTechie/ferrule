# Ferrule

> One local panel: every key held once, every model — local or cloud — visible,
> switchable, and accountable for what leaves your machine.

Ferrule is a **local key vault first, a model router second**. You mint a provider key
once, paste it into Ferrule, and Ferrule becomes the one encrypted local place that key
lives. The unified OpenAI-compatible endpoint, the model board, the alias ladders, and
the spend and egress views all fall out of the vault.

One statically-linked Go binary. No account, no telemetry, no server database, and
nothing of ours in the loop.

*A ferrule is the fitting that binds many strands into a single clean termination.*

---

## What it does that the alternatives don't

**Zero-config discovery — probe, don't declare.** Every comparable tool asks you to
hand-write a config listing models and endpoints. Ferrule opens already knowing: it scans
localhost for running runtimes and adopts them, and adding a cloud provider is *paste a
key*, not *edit a file*. On a first run against an empty config directory, with an Ollama
running, the board shows a live routable model in **267 ms** — no file, no restart.

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

Both lanes share the vault, the app tokens, the cost tracking, and the egress view.

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

Two ways to hold the vault open:

- **Identity file** (default) — `vault.identity`, mode 0600, next to the store. The
  daemon starts unattended.
- **Passphrase** — `ferrule serve --passphrase`, or `FERRULE_PASSPHRASE`. Nothing that
  can open the vault is written to disk at all.

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

## Verify it yourself

```
make check     # gofmt, go vet, and the checkpoint harnesses
make dist      # all five targets, CGO off
```

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

Vision, roadmap, and the full agent handoff: [FERRULE.md](FERRULE.md).

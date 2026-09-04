# FERRULE
### A local key vault and model router — vision, roadmap, and agent handoff in one file

> **This is the founding document, not the changelog.** It is kept as written so the
> reasoning is legible, which means it describes intentions that were later changed or
> dropped — §4.7's browser publication path, for one. What actually shipped is in
> `SPEC.md`, and what you can do with it is in `README.md`. Where they disagree, they win.

**Tier: Tool.** Single user, local, one self-contained binary. This document *is* the
handoff — there is no separate UX/UI reference; surfaces are drawn inline in §4.6.
Re-tier to Product only when a second human role, shared state, or a server appears
(see §3, explicitly out of scope). Strings are externalized for i18n from the first
commit; no user-facing copy is hardcoded.

*Grounding (verified this cycle, do not re-litigate): LiteLLM normalizes chat and
image generation behind one OpenAI-compatible surface but pays for it with a 100+
provider maintenance treadmill — Replicate image is still unsupported and video
output is a stuck request; its per-app virtual keys require Postgres, and some
features sit behind an enterprise license. llama-swap is one zero-dependency Go
binary that hot-swaps local model **processes** to manage VRAM and ships a web
playground and an `/api/mcp` endpoint — but it is not a cloud-key manager. Ferrule is
neither: it is the local encrypted home for your provider keys, with a router that
falls out of the vault.*

---

## 1 · Vision

### 1.1 The frame — a key vault first, a router second

The pain is key juggling: a dozen keys across half a dozen providers (Replicate,
DeepSeek, Claude, Groq, …) pasted into every app, script, and `.env` you own, with no
single place that knows what you hold, what it costs, or where your prompts go. Ferrule
is the answer to that, and the order matters: **it is a key vault first and a router
second.** You mint a key once, paste it into Ferrule, and Ferrule becomes the one
encrypted local place that key lives. Everything else — the unified endpoint, the
model picker, the spend and egress views — falls out of the vault. Frame every design
decision against that: the keys are the center of gravity; the routing is a
consequence. The name encodes the shape — a ferrule is the fitting that binds many
strands into a single clean termination: many keys and providers, bound into one
connection out.

### 1.2 The one line

*One local panel: every key held once, every model — local or cloud — visible,
switchable, and accountable for what leaves your machine.*

### 1.3 Who it's for

A developer or power user who runs many apps against many models and wants an
instrument they own, not a YAML file to maintain or an enterprise gateway to operate.
The person who asks *"why is this key in fourteen places?"* and *"which of my scripts
just spent forty dollars on Claude?"* Single user, single machine.

### 1.4 The two differentiators that justify existing

The unified-local-endpoint shape is table stakes (LiteLLM, llama-swap, aichat, LocalAI
all have it). Ferrule earns its place on two things the incumbents structurally don't
do, and these are the **spearhead** — if they are not load-bearing, Ferrule is
redundant:

1. **Zero-config discovery — probe, don't declare.** Every incumbent makes you
   hand-write config listing models and endpoints. Ferrule opens already knowing:
   it scans the machine for running local runtimes and adopts them, and adding a
   cloud provider is *paste a key*, not *edit a file*. This is also how Ferrule clears
   the five-second cold-load bar — a config-first tool fails that by construction.
2. **Egress visibility — what left the machine.** Cost dashboards are everywhere; a
   data-egress dashboard is not. Because Ferrule knows which requests stayed on-device
   and which went to a provider, it shows you *where your prompts went*, not just what
   they cost. This is the sovereign inversion of observability: not "we log your
   traffic on our server," but "you see, on your own disk, exactly what left your
   machine." For this audience that is the headline.

Grants-without-a-database and the control-surface UI (§2) are the substance that makes
Ferrule a tool rather than a demo — necessary, but not the pitch.

### 1.5 Doctrine posture

- **Sidecar does not apply.** Ferrule is plumbing that *routes* AI; it is not a
  tool-with-an-AI-passenger. Do not spend effort on removability — there is no core to
  keep standing without a model, because the model is what Ferrule routes *to*.
- **Edge-First is embodied here.** Ferrule is the runtime form of the inference ladder:
  the alias-with-fallback (§2.4) *is* the ladder expressed as user policy, and
  detection-over-configuration is the L1/L2 auto-detect made concrete.
- **Sovereignty inverts on one point, deliberately.** The house rule is "BYOK, never
  persisted." Ferrule is the exception, on purpose: being the single **encrypted local
  store** for keys *is* the value. Say this out loud in the copy — *persisted locally,
  encrypted, is not persisted on someone's server.* Nothing phones home; no account;
  the key only ever travels from your disk to the provider you gave it for.

---

## 2 · The shape

### 2.1 Form

One statically-linked **Go** binary. (Go over Rust: llama-swap proves Go carries a
proxy + process concerns + a web UI as a single zero-dependency binary, and proxy
overhead is a rounding error beside inference. Choose Rust only if a shared native core
with another tool later demands it — not the case here.) The thin HTML control surface
is **embedded** in the binary (`go:embed`) and served on localhost, so "one binary"
stays literally true. A CLI drives the same core. Data lives in the user's own config
directory and OS keychain. No framework, no build step for the UI, no external service.

### 2.2 The spine — five parts, in dependency order

1. **Vault** — the encrypted home for provider keys. OS keychain where available, an
   encrypted file store otherwise; export/import as an encrypted, portable file the
   user owns. This is the security-critical surface (§4.5).
2. **Grant** — a local token minted per calling app. The token is the app's *identity*
   to Ferrule; it is what unlocks per-app overrides, attribution, and per-app spend and
   egress. Backed by SQLite — **never** a server database.
3. **Discovery** — the "add a source" pipeline (§2.3), with two entry points: detected
   (local) and pasted (cloud). One mechanism, two doors.
4. **Control surface** — the thin HTML panel (§4.6): every model in one filterable
   board, aliases as first-class objects, live re-point with no restart, model
   remapping.
5. **Observability** — a metadata-only local ledger: spend, volume, latency, errors —
   **per app and per model** — plus the egress view. Content logging is off by default
   and, when enabled, stays local.

### 2.3 The add-a-source pipeline — configuration by probing

The single most important interaction, and the anti-LiteLLM. LiteLLM says *declare your
setup in YAML*; Ferrule says *add a source and I'll work out the rest*. One pipeline,
both entry points:

```
detected (local runtime)  ─┐
                           ├─►  probe  ─►  classify  ─►  test  ─►  live (aliasable)
pasted (key [+ endpoint])  ─┘                                      └─ or FAIL LOUD, with a reason
```

- **detected** — scan localhost for known local runtimes (Ollama :11434, LM Studio
  :1234, llama.cpp servers, and any OpenAI-compatible server the user points at) and
  adopt them with zero input.
- **pasted** — for a cloud provider: paste a key. For a *known* provider (a curated
  seed set) the endpoint and shape are prefilled, so the user types nothing but the
  key; for an unknown OpenAI-compatible endpoint, paste key + base URL.
- **probe** — hit `GET /v1/models` where the source is OpenAI-compatible; for a
  known non-compatible provider (Replicate), use that provider's own listing.
- **classify** — "basic iterative intelligence on completion types": tag each model
  with capability `{chat, completion, embeddings, rerank, image, audio, video}`,
  sync-vs-async, context length, and modalities. Source order: a bundled-then-remote
  capability catalog keyed by model id (models.dev-style, cached, background-refreshed —
  **never hardcoded**, per the date-parameterized-lookup rule); fall back to a cheap
  live probe (a tiny chat call; an embeddings call) where the catalog is silent.
- **test** — fire one minimal real request. Green → the source goes live and its models
  become aliasable. Fail → the source is saved as `failed` with a visible reason. This
  is loud failure applied at config time: **a dead key is never silently stored.**

### 2.4 Aliases and remapping — the ladder as user policy

- **Aliases are first-class objects**, not config strings. `fast`, `smart`, `vision`,
  `cheap`, `local` each resolve to a model *or* an ordered fallback ladder (try local;
  if unavailable or over budget, a cloud model). Apps point at the alias; you re-point
  the alias in the UI and every app follows with no restart. This ladder is exactly
  Edge-First's L1→C2 escalation, authored by the user.
- **Model remapping** intercepts a hardcoded model id — an app that only ever sends
  `gpt-4o` — and serves it from whatever you choose, including a local model. This is
  how Ferrule helps apps that don't let you change the model: you change it at the router.

### 2.5 The two lanes — the load-bearing architectural decision

The fault line is **normalization cost**, not text-vs-media. Chat / completions /
embeddings cost almost nothing to unify today (everyone is OpenAI-compatible or a thin
adapter away). Media costs real, per-provider, forever work *and* drags in async
prediction shapes and file outputs — the exact treadmill that made LiteLLM heavy and
still left holes. So:

- **Raw-tokens lane — unified endpoint.** Ferrule serves the OpenAI-compatible surface
  (`/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`). This is the magic:
  point any app or script at Ferrule and call any chat/embeddings model through one
  endpoint. Anything already in the OpenAI *image/audio* shape (e.g. `gpt-image-1` via
  `/v1/images/generations`) also rides here, because the normalization is free.
- **Media / passthrough lane — native shape, key-managed.** Everything whose shape
  isn't free to normalize (Replicate and other prediction-based providers, async
  video) is reached through a per-source passthrough mount. Ferrule injects the stored
  key, logs egress and cost, and **leaves the request and response shape untouched** —
  you call it with the provider's native SDK pointed at Ferrule. The unification for
  media is at the vault + observability layer, *not* the request-shape layer.

Both lanes share the vault, grants, egress dashboard, and cost tracking — so Replicate
still solves the key-juggling pain (paste once; get per-app spend and egress) without
Ferrule pretending it is OpenAI-compatible. **Unified media is a later, additive lane
(§3), never the thing that defines v1.**

---

## 3 · Roadmap (inline)

### v1.0 — the dead-simple local vault + raw-tokens router
The full spine (§2.2), the add-a-source pipeline (§2.3), aliases + remapping (§2.4), the
raw-tokens unified endpoint and the media passthrough lane (§2.5), the control surface
(§4.6), metadata-only observability with the egress dashboard, and the MCP control face
(§4.7). Curated seed provider set (§4.4). This is the whole product; it ships when it is
simple and honest, not when it is broad.

### v1.x — additive, non-breaking
- Harden the passthrough lane across more media providers **on demand only**.
- Widen the curated provider set as real requests arrive (breadth is demand-pulled).
- A guided *elevation* flow (Edge-First's optional "level up": help a user install a
  local runtime and pull a model), offered, never required.
- Encrypted config sync **across the user's own machines over a transport they own**
  (their drive, their file) — never a coordination server Ferrule runs.

### v2 / horizon — only if demand proves it
- A **unified media endpoint** that normalizes prediction-based providers behind one
  surface. This is the expensive normalization; take it on only when enough people
  need it that the treadmill is worth walking. Additive; it must not change what v1
  guarantees.

### Explicitly out of scope (would re-tier to Product)
Multi-user, accounts, auth, a shared team key pool, RBAC, per-seat caps, an admin
console, a hosted backend. The moment any of these is real, Ferrule has become a
Product on a separate commercial track — stop and re-tier; do not let it drift in.

---

## 4 · Agent Handoff

### 4.1 Repo, build, deploy, CLI

- **Repo:** `ferrule` (Go module `ferrule`). No framework. UI assets embedded via
  `go:embed`.
- **Build:** `go build` → one static binary. Cross-compile targets: macOS
  (arm64/amd64), Linux (arm64/amd64), Windows (amd64). Ship via GitHub Releases and a
  Homebrew tap. Zero runtime dependencies.
- **Run:** `ferrule serve` starts the long-lived daemon serving the endpoints and the
  embedded UI on `http://localhost:8899` (configurable via flag and env).
- **CLI verbs (same core as the UI):** `serve`, `add` (a source), `ls` (sources /
  models), `alias`, `key` (mint / revoke an app token), `usage`, `version`. The binary
  is `ferrule` — clear of standard binaries and common dev-tool CLIs, so no
  daemon-name workaround is needed.

### 4.2 Data, persistence, closure

- **Storage façade (the daemon owns persistence — this is not a browser tool, so no
  File System Access / OPFS / IndexedDB and no `localStorage` for anything):**
  - **Keys → OS keychain** (Keychain / Credential Manager / Secret Service) where
    present; an **encrypted file store** (age / libsodium-class) as the portable
    fallback. Keys are never written in plaintext, anywhere, ever.
  - **Everything else → SQLite** in the config dir: sources and their classified
    models, aliases, app-token grants, and the usage/egress ledger. **No Postgres, no
    server database** — this is a hard line (it is precisely LiteLLM's complexity you
    are refusing).
- **Config dir:** XDG-respecting (`~/.config/ferrule`, platform equivalents),
  overridable via a `FERRULE_CONFIG_DIR` env var.
- **Closure / export:** the user can export their whole configuration — sources,
  aliases, grants, with keys in the encrypted store — as one portable file they own,
  and re-import it on another machine. Open format (JSON + the encrypted key blob).
  Filenames pinned on first write.

### 4.3 The three faces (keep them distinct)

Ferrule has three seams; do not conflate them:
1. **Inference — raw-tokens lane:** OpenAI-compatible HTTP (`/v1/chat/completions`,
   `/v1/completions`, `/v1/embeddings`; `/v1/images/generations` for OpenAI-shaped
   image). Auth = a per-app **Ferrule token** (Bearer), *not* a provider key. The
   `model` field may be an alias, a real model id, or a remapped id.
2. **Inference — media passthrough lane:** a per-source mount that preserves the
   provider's native shape; Ferrule injects the stored provider key and logs.
3. **Control — MCP agent face (§4.7):** managing Ferrule itself (list models, set an
   alias, read usage). This is *not* an inference path; it is the control plane.

### 4.4 Discovery and the capability classifier

- **Local detection** on startup and on demand: probe the known local runtime ports;
  adopt any OpenAI-compatible server found; never surprise-download anything (if a
  runtime has no model, say so — do not fetch gigabytes uninvited).
- **Curated seed cloud set** (v1): the providers that actually matter — Anthropic
  (Claude), DeepSeek, Groq, an OpenAI-compatible generic, and Replicate (as the
  passthrough exemplar). Breadth is the **anti-goal**; widen only on demand.
- **Capability catalog:** bundled snapshot, refreshed in the background from a
  maintained remote source, cache-first render. Never hardcode model capabilities or
  prices inline — they change; look them up by id with a date-parameterized source.

### 4.5 Security and key custody — the critical surface

Ferrule holds every provider key, so it is the highest-value target in the user's kit
and the worst thing to corrupt. Treat this as the surface a forward-pass may never
defer:
- Keys encrypted at rest (keychain / encrypted store); plaintext keys never touch
  SQLite, logs, or the ledger.
- The daemon and UI **bind to localhost only** by default; the local API carries
  origin/CSRF protection so a random web page cannot drive it.
- The cross-origin / cross-tab control channel is **opt-in behind a developer setting**
  (Build Doctrine: the in-tab agent is the person's; the channel is anyone's).
- The egress ledger records metadata only unless the user explicitly turns on local
  content logging.
- State the sovereignty inversion honestly in the UI copy (§1.5).

### 4.6 Surfaces and design

- **Direction: Dense** (DIRECTIONS.md) — Berkeley-adjacent, instruments-you-own. This
  is a workbench a person operates for hours, not a reading surface.
- **Unity sentence (score every screen against this):** *One local panel: every key
  held once, every model — local or cloud — visible, switchable, and accountable for
  what leaves your machine.*
- **Surfaces:** (a) the **board** — every source and model in one filterable, sortable
  table (filters: capability, local/cloud, context length, cost; tabular figures);
  (b) **aliases** — create/edit an alias and its fallback ladder, live; (c)
  **add-a-source** — the paste/detect flow with the loud test result; (d)
  **observability** — per-app / per-model spend and volume, and the **egress** view
  (local vs off-machine); (e) **grants** — mint / revoke an app token, see what each
  spent.
- **Empty state:** "Detecting local runtimes…" then a prompt to add a source — never a
  blank or a spinner at the 5 s frame. If nothing is detected, the board still shows the
  add-a-source affordance and `?` help.
- **Error state:** the add-a-source test fails loud with the reason; a provider going
  down mid-use surfaces per-request, and the alias ladder degrades to the next rung
  rather than erroring.
- **Tokens (starting values — an alpha ladder of one cool ink over a near-black canvas;
  derive then verify with the tint/line checks, do not eyeball; dark canvas → fills
  ~1.5× alpha):**
  ```css
  --canvas: #0d0f11;
  --ink-100: rgba(233,238,242,0.92);  /* primary text        */
  --ink-70:  rgba(233,238,242,0.62);  /* secondary           */
  --ink-50:  rgba(233,238,242,0.42);  /* tertiary / labels   */
  --ink-30:  rgba(233,238,242,0.24);  /* disabled            */
  --ink-15:  rgba(233,238,242,0.12);  /* dividers            */
  --ink-08:  rgba(233,238,242,0.06);  /* hairlines / structure */
  --accent:  #3fb950;                 /* the one accent: active model, live request, focus */
  --warn:    #d29922;                 /* semantic state only  */
  --error:   #f85149;                 /* semantic state only  */
  ```
  One accent, working on state/focus/primary; `--warn`/`--error` are functional state
  signals, not a second accent. Neutrals are a pure alpha ladder (channel spread < 5).
- **The distinctiveness pass (record it in this doc):** the type choice. Dense permits
  a mono-adjacent family; the deliberate move here is **mono numerals for the datum
  board** (model ids, token counts, latency, cost are tabular data — Tufte) paired with
  a humanist grotesque for chrome. Pick the exact families in the pass (free candidates:
  Commit Mono / JetBrains Mono / IBM Plex Mono + Sans; flag any commercial pick's
  license). Do not inherit a scaffold default.
- **Motion:** near none (Dense). ≤200 ms, easing over bounce, hover behind
  `(hover:hover) and (pointer:fine)`, `prefers-reduced-motion` a first-class path.
- **Design verification (Tool tier):** floor viewport 1280×800 (must also survive a
  narrower window). Run the timeline capture (0 s / 5 s / 30 s / failure) throughout the
  UI chunk; run the four checks (lines / tints / density / bands) at Dense budgets (up
  to 6 type styles, higher line budget); one fresh-context rubric pass at ship with zero
  open findings.

### 4.7 The agent face (MCP control ops)

- **One manifest, two doors:** declare the control ops once as a tool manifest, publish
  via `navigator.modelContext` (WebMCP) where the surface offers it and the house
  `window.ferrule` API otherwise. (llama-swap already ships an `/api/mcp`; a control
  face is table stakes.)
- **Ops:** read/query ops — `list_sources`, `list_models`, `get_alias`, `usage_summary`
  — registered on load, no setting. Mutating ops — `add_source`, `set_alias`,
  `revoke_grant` — **stage before they land** (the person applies), per the
  mutating/irreversible rule.
- **Why it matters:** an agent orchestrating other tools can ask Ferrule "cheapest model
  with vision?" and route itself. Inference for that agent still goes through the
  OpenAI-compatible endpoint (§4.3.1); the MCP face is only for managing Ferrule.
- **Parity + attribution:** lint `manifest ⊇ command bus` (the UI dispatches nothing the
  manifest omits). Record every control call with its door and, where available, the
  caller. Mark any non-delegable act person-only rather than omitting it.
- **The verifier drives this face:** headless tests call the manifest, not the DOM.

### 4.8 Hard NOT-to-do list

- **Do not require a config file to reach first value.** Probe, don't declare (§2.3).
- **Do not chase provider breadth.** Curated seed set; breadth is demand-pulled (§4.4).
- **Do not normalize media into the unified endpoint in v1.** Passthrough only (§2.5).
- **Do not add a server database.** SQLite + keychain only; no Postgres.
- **Do not run models.** Ferrule routes; llama.cpp / Ollama / providers do inference.
- **Do not persist keys unencrypted, log them, or send them anywhere but the provider.**
- **Do not add accounts, auth, or multi-user.** That re-tiers to Product (§3).
- **Do not add telemetry or phone home.** Ever.
- **Do not let the agent face auto-commit a mutating op** without staging.
- **Do not build a second inference path** — the UI, the CLI, scripts, and agents are
  all clients of the one command bus / one endpoint.

### 4.9 Escalation and loop discipline

- **Three standing interrupts — stop and ask, otherwise run continuously:** (1) a
  conflict with a locked decision in this doc; (2) a genuinely new dependency; (3) real
  scope ambiguity that would change what the product *is*. Everything else, decide and
  proceed.
- **No-progress exit:** the same failure ~3× at one root → stop, write the tried-trail
  to the state file, escalate. Escalating with a readable trail is success behaviour.
- **Done is the verifier's word.** Every checkpoint below has a machine-checkable
  termination condition; a self-report of "works" advances nothing. Verification runs in
  a fresh context, separate from the context that wrote the code; its first question is
  whether the goal was gamed (a skipped test greening CI is the classic hack).
- **Every run writes the state file** (what was tried, what passed, what's open), and a
  `READY.md` park wall is left at any genuine blocker.

### 4.10 Continuous-run chunk sequence (riskiest assumption first)

Large autonomous chunks; the riskiest premise leads so it can invalidate the project
before infrastructure is built on it. Each checkpoint is a deterministic gate.

1. **Vault + add-a-source pipeline (spine + riskiest).** The whole "paste a key and it
   just works" premise. Build the encrypted vault and the probe→classify→test pipeline
   for both entry points.
   *Checkpoint:* a headless harness adds each seed source (local Ollama + LM Studio
   detected; cloud Claude / DeepSeek / Groq via test keys or a mocked provider) and every
   one reaches `live` with ≥1 classified model; a deliberately-bad key reaches `failed`
   with a visible reason; harness exits 0.
2. **Raw-tokens proxy + grants.** The OpenAI-compatible endpoint, per-app tokens, alias
   and model-id resolution, remapping.
   *Checkpoint:* the OpenAI SDK pointed at localhost with a Ferrule app-token completes a
   chat call routed to (a) a cloud model and (b) a local model; a ledger row is written
   per call with correct app-token attribution and a local/cloud egress flag; a revoked
   token is rejected 401. Asserted in test.
3. **Control surface.** The board, filters, alias editor, live re-point, add-source UI —
   built against the tokens and Unity sentence in §4.6.
   *Checkpoint:* timeline captures pass the 5 s bar at 1280×800; the four checks pass at
   Dense budgets; screenshots captured; one fresh-context rubric pass filed with zero
   open findings.
4. **Observability + egress dashboard.** The per-app / per-model ledger and the egress
   view.
   *Checkpoint:* replaying 100 synthetic requests across 2 apps and 3 models (mixed
   local/cloud) reproduces exact per-app and per-model counts, and the egress view
   classifies local vs off-machine with zero misattribution, against golden totals.
5. **Media passthrough lane.** Native-shape passthrough for Replicate (and the mocked
   async provider) with key injection and egress/cost logging.
   *Checkpoint:* a prediction runs end-to-end through the passthrough mount; the stored
   key is injected; request and response bytes are unaltered versus a canned fixture
   (minus the injected auth header); egress is logged as cloud, `provider=replicate`.
   Asserted.
6. **MCP control face.** Control ops as a manifest, both doors, staging on mutations.
   *Checkpoint:* an MCP client calls `list_models`, `set_alias`, `usage_summary` through
   the manifest; `set_alias` stages and requires an explicit apply; every call is
   recorded with door + caller; the parity lint (`manifest ⊇ command bus`) passes.
   Exercised via the manifest, not the DOM.

### 4.11 Open decisions (resolve with the human)

- **Canvas** — dark is the starting assumption (§4.6); confirm dark vs light for a
  workbench the user stares at for hours.
- **Type families** — pick in the distinctiveness pass and record here.
- **Default port** — `:8899` proposed; confirm it doesn't clash with the user's kit.
- **Encrypted-store library** — age vs a libsodium binding for the non-keychain
  fallback.

---

**Ship the fully-working simple version: the vault, discovery, the raw-tokens endpoint,
the control surface, and the egress view — dead simple, on one machine, nothing of
ours in the loop. The keys live once; everything else falls out of that.**

---

## 5 · Resolutions (2026-09-03 — recorded from the build, per §4.11)

The open decisions of §4.11, closed. Each is recorded here because §4.11 asked for it;
each is reversible, and the reason is stated so reversing it is a decision and not a
drift.

- **Canvas — dark, confirmed.** The starting assumption held. Verified at the floor
  viewport with the timeline capture and the four checks; see `design/VERIFICATION.md`.
- **Type families — JetBrains Mono (datum) + IBM Plex Sans (chrome).** Both OFL 1.1, both
  **embedded in the binary** as woff2 (~229 KB), licences shipped alongside. Embedded
  rather than linked: a panel that fetched a webfont from a CDN on every load would put a
  request on the network on behalf of a tool whose whole claim is that nothing leaves the
  machine uninvited. The page's own CSP (`font-src 'self'`) makes that structural.
  A six-style scale is fixed and machine-checked; see `design/VERIFICATION.md`.
- **Default port — `:8899`, confirmed.** No clash observed; overridable by `--port` and
  `FERRULE_PORT`.
- **Encrypted-store library — `filippo.io/age`.** Pure Go, no cgo, which is what keeps
  the five cross-compile targets of §4.1 buildable as single static binaries. A libsodium
  binding needs cgo and would have cost that.

### One token changed from §4.6, deliberately

§4.6 proposes `rgba(233,238,242, …)` for the ink ladder and, in the same paragraph,
requires neutrals with a channel spread under 5. Those are not compatible: 242 − 233 = 9.
The shipped ladder is **`rgba(236,238,240, …)`** — spread 4, visually the same cool
near-white, and a ladder that satisfies the rule it was given. Every other token is as
specified.

### Two things §4.2 specified that the build resolved differently

- **Key custody is the age-encrypted store on every platform; the OS keychain backend is
  not built.** §2.2 and §4.2 say "OS keychain where available, an encrypted file store
  otherwise". Both routes to the macOS keychain are worse than the fallback: the
  `security(1)` CLI takes the secret in `argv`, where any process on the machine can read
  it, and the Security.framework route needs cgo, which breaks the single-static-binary
  promise of §4.1 for all five targets. The `vault.Vault` interface is the seam a keychain
  backend slots into unchanged if either constraint lifts. The threat model that is
  actually delivered is written out at the top of `internal/vault/vault.go`, and
  passphrase mode (`serve --passphrase`) narrows it further by writing nothing to disk
  that can open the store.
- **A configuration export carries grant token hashes.** §4.2 asks for closure — export
  everything, re-import on another machine. Closure that silently invalidated every app's
  token would not be closure, so the export carries the one-way hash that recognises a
  token. No token travels; a hash cannot be reversed into one.

### The agent contract (§4.7's pass, run)

The DRIVER pass has been run and its output is **`SPEC.md` §0**. It added five things the
handoff did not specify and the product needed: a single bounded perception act
(`ferrule status --json` / the `brief` op), a closed reason vocabulary where every code
carries a remedy, exit codes that map one-to-one to next actions, a rendered trajectory
(ledger + control log + staged ops, all in the brief), and one real accretion mechanism —
a live probe's classification is kept, so the request it cost is never spent twice. Each
clause of §0 names the test that fails if it stops being true.

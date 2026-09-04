# Ferrule

> Every LLM key held once, encrypted, on your own machine — with one OpenAI-compatible
> endpoint on top that your whole house can use.

![Ferrule's household view — the address and key you hand out, and where the models come from](marketing/hero-x.png)

Ferrule is a **key vault first, a model router second**. Paste a provider key once and
Ferrule becomes the one encrypted local place it lives. Apps get a Ferrule token instead —
revoke one, the rest keep working. The unified endpoint, the model board, the alias
ladders and the spend and egress views all fall out of the vault.

One static Go binary. No account, no telemetry, no server. Ferrule makes exactly one
request on its own behalf — a background fetch of the public model-capability catalog,
carrying nothing about you, which you can turn off.

*A ferrule is the fitting that binds many strands into a single clean termination.*

---

## Install

```
go install github.com/NakliTechie/ferrule/cmd/ferrule@latest
ferrule serve
```

Then open <http://localhost:8899>.

Or take a prebuilt binary from the [latest release](../../releases/latest) — no runtime
dependencies, checksums in `SHA256SUMS`:

| | |
|---|---|
| **macOS** | `Ferrule-macos.zip` → unzip, drag `Ferrule.app` to Applications, double-click. One app for Apple Silicon and Intel. |
| **Linux** | `ferrule-linux-arm64` / `-amd64` → `chmod +x`, then `ferrule serve`. |
| **Windows** | `ferrule-windows-amd64.exe` → run it, then open the panel. |

## Use it

```
ferrule serve                     # the daemon: endpoints + the control panel
ferrule add                       # scan localhost and adopt what is running
ferrule add anthropic             # paste a key (read from the terminal, never from argv)
ferrule ls models --local         # every model on this machine
ferrule refresh anthropic         # re-check a source with the key it already holds
ferrule alias fast <src>/qwen3:8b <src>/llama-3.3-70b   # a ladder, tried in order
ferrule key om                    # a token for one person or app, shown once
ferrule usage --egress            # what left the machine
ferrule startup on                # start Ferrule when you log in
```

Point any OpenAI-compatible client at it:

```python
from openai import OpenAI
c = OpenAI(base_url="http://localhost:8899/v1", api_key="frl_…")  # never a provider key
c.chat.completions.create(model="fast", messages=[...])    # an alias you defined
c.chat.completions.create(model="gpt-4o", messages=[...])  # remapped to whatever you chose
```

`model` may be an alias, a real id, `source/model`, or an id you have remapped — which is
how you handle an app that will only ever send `gpt-4o`.

## What it does that the alternatives don't

**Probe, don't declare.** Comparable tools ask you to hand-write a config listing models
and endpoints. Ferrule scans localhost for running runtimes and adopts them, and adding a
cloud provider is *paste a key*, not *edit a file*. No config file, no restart.

**A dead key is never quietly stored.** Adding a source probes the endpoint, classifies
what it finds, and fires one real request before calling it live. If that fails the source
is kept, visibly dead, with the provider's own words and a specific next action — a 402
from DeepSeek means the account is empty and the key is fine; a 404 from NVIDIA means that
one model is outside your tier and the others may work.

**Egress visibility.** Cost dashboards are everywhere; a data-egress dashboard is not.
Ferrule knows which requests stayed on the machine and which went to a provider, so it
shows you *where your prompts went*, not only what they cost. Metadata only — content
logging is off by default and, when on, stays local.

The panel answers in **0.11 s** cold. A routable model takes as long as the runtime takes
to serve one real request: about **5 s** for a local model already pulled, longer on a
genuinely cold one. Both the CLI and the panel say what they are waiting for.

## Share it with the house

```
ferrule serve
```

Sharing is on out of the box, with a switch in the panel. One **household key** works for
everybody from the first start; give people their own with `ferrule key <name>` when you
want the usage list to say who, or to cut one person off without cutting off the house.

**Inference** is served to your network, and only with a valid Ferrule token — no token,
401. **Everything else** — the panel, the vault, minting tokens, the ledger, config
export, `/mcp` — answers only this machine, enforced on the peer address of the accepted
TCP connection rather than a header, so a caller cannot claim to be local. Your provider
keys never cross the network; what does is a token you can revoke.

Turn sharing off and the network gets 403 on the next request, no restart.
`ferrule serve --host 127.0.0.1` closes the port outright and no setting reopens it.

**One thing to know:** tokens cross your LAN in the clear. On your own wifi that is the
same trust boundary every other device already sits behind — but a token spends money, so
mint one per person rather than sharing. For something airtight, run Ferrule on a
[Tailscale](https://tailscale.com) address instead of your LAN.

## Where your keys live

Encrypted at rest with [age](https://age-encryption.org) in `~/.config/ferrule`. A
plaintext key never touches SQLite, the logs, or the ledger — only an opaque vault
reference does, and a test asserts it. Everything else is SQLite in the same directory.

Two ways to hold the vault open, defending against different things:

- **Identity file** (default) — mode 0600, beside the store; the daemon starts unattended.
  Stops another account on the machine, and the everyday leaks that put keys in a dozen
  `.env` files. Does **not** stop someone who copies the whole directory: a backup takes
  both files, and two files is one decryption.
- **Passphrase** — `serve --passphrase`. Nothing that can open the vault is written to
  disk, so a copy of the directory is useless without you. The daemon cannot then start
  unattended.

Neither stops code already running as you against a live daemon; no local single-user
secret store can, because the daemon has to read the key to make the request. What Ferrule
offers there is the ledger: every use of every key is recorded, so a key used behind your
back is one you can see was used.

## The two lanes

The fault line is **normalization cost**, not text versus media.

- **Raw tokens — one endpoint.** `/v1/chat/completions`, `/v1/completions`,
  `/v1/embeddings`, `/v1/images/generations`. Any chat or embeddings model, local or
  cloud, through one URL.
- **Media — native shape.** Prediction-based providers (Replicate) are reached at
  `/p/<source>/…` with their own request and response shape left byte-identical. Ferrule
  injects the key and logs the egress; it does not pretend Replicate is OpenAI-compatible,
  and it lends the key only to that provider's inference routes.

Both share the vault, the tokens and the egress view. Cost and token counts are token-lane
only — a prediction API reports neither in any shape Ferrule could read without
normalising it, which is the treadmill this split avoids.

## The agent face

Control operations are published as an MCP manifest at `/mcp`, generated from the same
command bus the panel and the CLI dispatch through — so `manifest ⊇ command bus` holds by
construction, and a test asserts it. Mutating operations **stage** rather than land: the
agent proposes, you apply. A provider key is withheld from the staged payload entirely.
Minting a credential is person-only and is marked as such rather than hidden. Inference
does not go through this face.

## Try it without any keys

```
make demo
```

A whole Ferrule with fake providers, app tokens, and replayed traffic so Usage and Egress
have something to show. Nothing you own is involved. If Ollama or LM Studio is running,
`ferrule add` with no arguments adopts it with no key and no config file at all.

## Verify it yourself

```
make check     # gofmt, go vet, and the checkpoint harnesses
make dist      # all five targets, CGO off
```

`make check` refuses a skipped test as well as a failing one — a machine that cannot run a
gate has to say so rather than quietly accept the package. The harnesses are the gates:
the add pipeline reaching `live` for every seed provider and `failed` *with a visible
reason* for a bad key; the OpenAI SDK completing calls to a cloud and a local model with
correct per-token ledger attribution; a 100-request replay reproducing exact per-app,
per-model and per-egress counts; passthrough byte-identity; the MCP face staging its
mutations; and the control plane refusing the network from a real non-loopback bind.

## Status

Verified end to end on macOS and Linux. The Windows login-item registration compiles and
its task arguments are tested, but has not been run on Windows. Nothing is signed or
notarised, so macOS warns on first open of `Ferrule.app` — right-click and choose Open.

## Not doing

No accounts, no auth, no multi-user, no team key pool, no hosted backend. No provider
breadth for its own sake: a curated seed set — Anthropic, DeepSeek, Groq, NVIDIA, OpenAI,
Replicate, and a generic OpenAI-compatible endpoint for everything else — widened when a
real request arrives. No telemetry, ever. And Ferrule does not run models: llama.cpp,
Ollama and the providers do inference; Ferrule routes.

## License

MIT — see [LICENSE](LICENSE). The embedded typefaces (JetBrains Mono, IBM Plex Sans) are
OFL 1.1; see [NOTICE](NOTICE).

Founding document: [FERRULE.md](FERRULE.md) · what shipped: [SPEC.md](SPEC.md) · for a
coding agent: [llms.txt](llms.txt) · panel design verification:
[design/VERIFICATION.md](design/VERIFICATION.md).

<h1 align="center">Ferrule</h1>

<p align="center">
  <strong>Every LLM key held once, encrypted, on your own machine —<br>
  with one OpenAI-compatible endpoint on top that your whole house can use.</strong>
</p>

<p align="center">
  One binary. macOS, Linux, Windows. No account, no server, no telemetry.
</p>

<p align="center">
  <a href="../../releases/latest"><img alt="latest release" src="https://img.shields.io/github/v/release/NakliTechie/ferrule?style=flat-square&color=3fb950"></a>
  <a href="LICENSE"><img alt="MIT" src="https://img.shields.io/badge/license-MIT-3fb950?style=flat-square"></a>
  <img alt="one binary" src="https://img.shields.io/badge/install-one%20binary-3fb950?style=flat-square">
  <img alt="no account" src="https://img.shields.io/badge/account-none-3fb950?style=flat-square">
</p>

![The household view — the address and key you hand out, and where the models come from](marketing/hero-x.png)

## Install

| | |
|---|---|
| **macOS** | `brew install --cask NakliTechie/tap/ferrule` — or download **[Ferrule-macos.zip](../../releases/latest)** and drag it to Applications. Not notarised: first open is right-click → Open. |
| **CLI** (macOS, Linux) | `brew install NakliTechie/tap/ferrule` — or `curl -fsSL https://raw.githubusercontent.com/NakliTechie/ferrule/main/install.sh \| sh` |
| **Windows** | `ferrule-windows-amd64.exe` from the [latest release](../../releases/latest). |
| **Go** | `go install github.com/NakliTechie/ferrule/cmd/ferrule@latest` |

Then `ferrule serve` (or open the app). The panel is at **<http://localhost:8899>**. If
Ollama or LM Studio is running, Ferrule has already found it; otherwise **Add a provider**
and paste a key. Point anything that speaks OpenAI at it:

```python
from openai import OpenAI
c = OpenAI(base_url="http://localhost:8899/v1", api_key="frl_…")  # never a provider key
c.chat.completions.create(model="everyday", messages=[...])
```

No config file, no account, no restart. No keys to hand? `make demo` runs a whole Ferrule
on fake providers. Every screen and command, with what it is for: **[the guide](https://naklitechie.github.io/ferrule/)** —
also served by your own Ferrule at `/guide/`.

## Why

Your provider key is in six `.env` files and you are not sure which. Rotating it means
finding all of them. Nothing can tell you what a key spent last month or whether a prompt
left your machine, because nothing was in a position to know.

Ferrule is the one place the key lives. Apps get a Ferrule token instead — revoke one, the
rest keep working — and because every request goes through one door, that door records
what it cost and where it went. It scans localhost and adopts running runtimes; adding a
cloud provider is *paste a key*, not *edit a file*. A dead key is never quietly stored, and
a refusal carries the provider's own words.

**Use [LiteLLM](https://github.com/BerriAI/litellm)** for a team gateway with a config file
you version-control. **Use [Ollama](https://ollama.com)** to run models — Ferrule does not
run anything, it routes to Ollama and the cloud through one endpoint. **Use a `.env`** if
you have one key and one project.

## Share it with the house

Sharing is on out of the box. One **household key** works for everyone; `ferrule key <name>`
gives a person their own, so usage says who and you can cut one off without the rest.

Inference is served to the network, only with a valid token. Everything else — the panel,
the vault, tokens, the ledger, `/mcp` — answers only this machine, enforced on the peer
address of the TCP connection, not a header. Provider keys never cross the network.

Tokens cross your LAN in the clear. On your own wifi that is the trust boundary every
other device already sits behind; for airtight, run it on a [Tailscale](https://tailscale.com)
address. `ferrule serve --host 127.0.0.1` closes the port outright.

## Commands

```
ferrule serve                     # the daemon: endpoints + the control panel
ferrule open                      # make sure it is running, then show the panel
ferrule add                       # scan localhost and adopt what is running
ferrule add anthropic             # paste a key (read from the terminal, never from argv)
ferrule ls models --local         # every model on this machine
ferrule refresh anthropic         # re-check a source with the key it already holds
ferrule rm anthropic              # remove a source, its models, and its key
ferrule alias fast <src>/qwen3:8b <src>/llama-3.3-70b   # a ladder, tried in order
ferrule remap gpt-4o fast         # serve a hardcoded id from a model you chose
ferrule key om                    # a token for one person or app, shown once
ferrule usage --egress            # what left the machine
ferrule startup on                # start Ferrule when you log in
ferrule export / import           # a portable encrypted configuration
```

Control operations are also published as an MCP manifest at `/mcp`; mutations stage,
you apply.

## Where your keys live

Encrypted with [age](https://age-encryption.org) in `~/.config/ferrule`. A plaintext key
never touches SQLite, the logs or the ledger — a test asserts it. The identity file
(default, 0600) lets the daemon start unattended; `serve --passphrase` writes nothing to
disk that can open the vault, at the cost of unattended start. Neither stops code already
running as you — no local secret store can — which is what the ledger is for: every use of
every key is recorded.

## Verify it yourself

```
make check     # gofmt, go vet, the checkpoint harnesses, and the demo boots
make dist      # all five targets, CGO off
```

`make check` refuses a skipped test as well as a failing one. Verified end to end on
macOS, Linux and Windows. Nothing is signed or notarised; Intel Macs get the
`ferrule-darwin-amd64` binary rather than the app.

## License

MIT — see [LICENSE](LICENSE). Embedded typefaces are OFL 1.1; see [NOTICE](NOTICE).

[The guide](https://naklitechie.github.io/ferrule/) · founding document: [FERRULE.md](FERRULE.md) ·
what shipped: [SPEC.md](SPEC.md) · changes: [CHANGELOG.md](CHANGELOG.md) · for a coding agent: [llms.txt](llms.txt)

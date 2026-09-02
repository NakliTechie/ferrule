# Ferrule — specification

Vision, roadmap, and the original handoff live in [FERRULE.md](FERRULE.md). This file is
the built system's contract, and it opens where the driver does.

---

## §0 · Agent contract

*Written by the DRIVER pass (ntkit `DRIVER.md` v0.1), and the section every later design
decision answers to. It is a contract because it is asserted: each clause below names the
test that would fail if it stopped being true.*

An agent driving Ferrule is context-poor, can be killed mid-turn, and will circle if the
system lets it. Ferrule is built for that driver.

### 0.1 One perception act — `brief`

`ferrule status --json`, or the `brief` control op, renders the **whole situation** in one
bounded read: every source with its status, code, message and remedy; a *summary* of what
the router can serve, by capability and by where; every alias with the rung actually
serving it; token counts; what is staged and by whom; the last 24 hours of egress; the
last five failures; the last five control calls; the closed reason vocabulary; and the
short list of moves that would change any of it.

An agent's first act is never exploration. *Asserted:*
`TestBriefRendersTheWholeSituationInOneRead`.

### 0.2 Machine-decidable, not merely machine-readable

Every source outcome is a `{code, message, remedy}` triple. The **code** is a closed
vocabulary an agent branches on; the **message** is for a person; the **remedy** is the
exact next move. Nothing requires parsing prose.

```
ok · unreachable · bad_key · bad_status · no_models
local_no_models · test_failed · unknown_provider · needs_key · needs_base_url
```

The vocabulary is an exhaustive switch in `internal/discovery/reason.go`, published in
the brief, and every non-`ok` code carries a remedy. *Asserted:*
`TestReasonVocabularyIsClosedAndPublished`.

### 0.3 One verdict per distinct next action

CLI exit codes map one-to-one onto what the caller does next:

| Code | Means | The caller's next move |
|---|---|---|
| 0 | it worked | continue |
| 1 | it did not, and the reason says why | fix the cause, re-run |
| 2 | the command itself was wrong | fix the invocation |

`unknown_provider`, `needs_key`, and `needs_base_url` are invocation faults and exit 2;
every other failure is a state fault and exits 1.

### 0.4 Bounded output

The brief grows with the number of **sources and aliases a person configured** — never
with the model catalog, the ledger, or the control log. Models are summarised as counts,
not listed; history windows are fixed at five. `list_models` is the unbounded read, and
you have to ask for it by name. *Asserted:* the brief must not carry a model list.

### 0.5 Every failure names its remedy

A dark source keeps its remedy in the database, renders it in the brief, prints it under
`ferrule add`, and shows it on the board. The documentation for a failure is delivered
where the failure is read, not in a manual.

### 0.6 Crash-safe and idempotent

The vault and the capability cache are written to a temporary file and renamed, so an
interrupted write leaves the old state, never a hybrid. SQLite runs in WAL. `add` is
idempotent by source name — running it again re-probes and updates rather than
duplicating. `detect` re-probes what it already adopted. A staged operation applies once
and is stamped.

### 0.7 The tool holds the memory

The agent's context is ephemeral; Ferrule keeps the trajectory. The ledger holds every
routed request; `control_log` holds every control call with its **door** and its
**caller**; `staged_ops` holds what an agent proposed and a person has not yet applied.
The brief renders all three. Nothing an agent needs to remember lives only in its head.

### 0.8 Accretive by mechanism

Classification is catalog-first. When the catalog is silent, Ferrule fires a live probe —
a real, billable request — and **keeps the answer** in `learned`. The next probe of that
id is free, on every later refresh and after a restart. Nobody has to remember to make
this happen; it is the mechanism. *Asserted:* `TestLiveProbeClassificationIsLearnedOnce`.

The second ratchet is the failure record: a source that failed keeps its typed reason, so
a fix is measured against a stated cause rather than a memory of one.

### 0.9 A tower, not a toolbox

Each layer consumes only the one below, and an agent enters at the altitude its task
needs.

```
doors        panel (HTML)  ·  CLI  ·  MCP manifest         ← all clients, none privileged
command bus  internal/api                                  ← the ops; the manifest is generated from it
lanes        router (raw tokens)  ·  passthrough (media)   ← the two inference paths, and only two
discovery    probe → classify → test                       ← the only way a source becomes routable
catalog      dated snapshot + background refresh           ← what a model can do and costs
store        SQLite: sources, models, aliases, grants, ledger
vault        age-encrypted keys                            ← the bottom, and the reason for the rest
```

Nothing above the lanes may reach into the store directly; nothing below the vault
exists. The panel dispatches only ops the bus defines, which is why `manifest ⊇ command
bus` is structural rather than remembered. *Asserted:*
`TestParityManifestSupersetsCommandBus`.

### 0.10 The evaluator stays outside the loop — fail closed

- The agent door cannot land a mutation. Mutating ops **stage**; a person applies.
  *Asserted:* `TestCheckpointMCPControlFace`.
- Issuing a credential and reading logged content are **person-only**, marked as such in
  the manifest rather than hidden from it, and refused with the refusal recorded.
  *Asserted:* `TestPersonOnlyOpsAreNamedAndRefused`, `TestAgentCannotReadTheContentLog`.
- A provider key is stripped from a staged payload entirely, and supplied by the person at
  the moment they apply. *Asserted:* `TestStagedAddSourceWithholdsTheKey`.
- The pipeline fails closed: a source becomes `live` only after a real request succeeded.
  Anything else — unreachable, rejected, empty, untested — is `failed`, never a hopeful
  `live`. *Asserted:* `TestCheckpointAddASourcePipeline`.
- `make check` is the judge, and nothing the router or the agent face can write reaches
  it.

---

## §1 · Interfaces

### The three faces (they do not overlap)

| Face | Path | Auth | For |
|---|---|---|---|
| Raw-tokens inference | `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/images/generations`, `/v1/models` | Ferrule app token (Bearer) | any OpenAI-compatible client |
| Media passthrough | `/p/<source>/…` | Ferrule app token | the provider's own SDK, native shape |
| Control | `/api/op/<name>`, `/mcp` | localhost + origin guard | the panel, the CLI, an agent |

The control face is not an inference path. Inference for an agent goes through the
OpenAI-compatible endpoint like anything else.

### Model resolution

The `model` field of a raw-tokens request is resolved in this order, first hit wins:

1. an **alias** → its ordered ladder, dark rungs skipped
2. a **remap** → an alias, or `source/model`
3. `source/model` → that exact pair (source id or source name)
4. a bare **model id** → the first live source serving it, local preferred

A rung that fails with a transport error, a 5xx, or a 429 falls through to the next. A
4xx does not: a bad request is the client's to fix, and retrying it elsewhere would turn
a clear error into a cascade. Every attempt that actually left the machine gets its own
ledger row, including the ones that failed.

### Storage

| What | Where | Why |
|---|---|---|
| provider keys | `vault.age`, age-encrypted, 0600 | the reason the product exists |
| vault identity | `vault.identity`, 0600 — or nothing at all in passphrase mode | |
| sources, models, aliases, grants, ledger, learned | `ferrule.db`, SQLite, WAL | no server database, ever |
| capability catalog | `catalog.json`, dated, background-refreshed | never hardcode a price |
| logged content | `content_log` table, off by default | kept apart so the ledger holds no content |

A plaintext key never reaches SQLite, the logs, the ledger, or an export. *Asserted:*
`TestKeyNeverReachesSQLite`.

## §2 · What the gates prove

`make check` runs `gofmt`, `go vet`, and the harnesses. Each chunk's gate from
FERRULE.md §4.10, and what asserts it:

| Gate | Test |
|---|---|
| Every seed source reaches `live`; a bad key reaches `failed` with a visible reason | `TestCheckpointAddASourcePipeline` |
| OpenAI-shaped calls routed to a cloud and a local model; per-call ledger attribution; 401 on revoke | `TestCheckpointRawTokensProxyAndGrants` |
| 100 requests across 2 apps and 3 models reproduce exact per-app, per-model, per-egress counts against golden totals | `TestCheckpointObservabilityAndEgress` |
| Passthrough bytes unaltered versus a canned fixture, key injected, egress logged as cloud | `TestCheckpointMediaPassthroughLane` |
| MCP calls through the manifest; `set_alias` stages; door and caller recorded; parity lint | `TestCheckpointMCPControlFace` |
| The control surface at the floor viewport | [design/VERIFICATION.md](design/VERIFICATION.md) |

Not covered by a gate, and said plainly: no test drives a real cloud provider with a real
key. Every cloud path is exercised against a mock speaking the same dialect. The local
path has been driven against a real Ollama by hand, through the OpenAI Python SDK.

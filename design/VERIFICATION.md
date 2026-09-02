# Ferrule — design verification record

Chunk 3's gate (FERRULE.md §4.10.3): *timeline captures pass the 5 s bar at 1280×800;
the four checks pass at Dense budgets; screenshots captured; one fresh-context rubric
pass filed with zero open findings.*

Floor viewport 1280×800, `device_scale_factor: 2`, `prefers-color-scheme: dark`,
headless Chromium via Playwright. Captures in `design/timeline/`.

---

## Type — the distinctiveness pass (recorded here, per §4.6)

**Datum: JetBrains Mono. Chrome: IBM Plex Sans.** Both OFL 1.1, both **embedded in the
binary** as `woff2` (~229 KB total, `internal/ui/assets/fonts/`), licences alongside.

Embedded rather than linked, deliberately: a panel that fetches a webfont from Google on
every load would put a request on the network on behalf of a tool whose whole claim is
that nothing leaves the machine uninvited. The page's own `Content-Security-Policy`
(`font-src 'self'`) makes that structural rather than a promise.

The pairing is the move §4.6 asked for — mono numerals for the datum board (model ids,
context lengths, costs, latencies, token counts are tabular data), a humanist grotesque
for chrome. It is not a scaffold default: the panel would render in the platform stack
without it, and does not.

### The scale — six styles, no seventh

| # | Style | Face | Size / weight | Used for |
|---|-------|------|---------------|----------|
| 1 | brand  | Mono | 13 / 500, uppercase, 0.14em | the wordmark, once |
| 2 | title  | Sans | 12.5 / 500 | pane titles, dialog and state headings |
| 3 | chrome | Sans | 12.5 / 400 | nav, buttons, notes, prose |
| 4 | label  | Sans | 10.5 / 400, uppercase, 0.07em | column heads, field labels, rail keys |
| 5 | datum  | Mono | 12.5 / 400 | every figure, id, path, and ladder rung |
| 6 | micro  | Mono | 10 / 400 | tags and counts |

Emphasis is carried by colour and by the accent. Adding a weight or a size to make
something stand out would spend the seventh style, so it is not done.

## The four checks — measured, not eyeballed

Run by a script that reads computed styles off the live surface
(`design/` outputs; the script itself is throwaway). Every pane, every check:

| Check | Budget (Dense) | Board | Aliases | Add | Usage | Grants |
|---|---|---|---|---|---|---|
| Type styles | ≤ 6 | 6 | 6 | 6 | 6 | 6 |
| Line weights / colours | higher Dense budget | 3 | 3 | 3 | 2 | 3 |
| Neutral channel spread | < 5 | 4 | 4 | 4 | 4 | 4 |
| Bands (large flat fills with no content) | none | none | none | none | none | none |

**Lines** are 1px throughout, in two neutral tints (`--ink-08` structure, `--ink-15`
containers) plus the accent on focus and active state. No second weight, no second hue.

**Tints** are one neutral fill (`--ink` at 0.045, ~1.5× its light-canvas equivalent as
§4.6 anticipates) and one accent fill. `--warn` and `--error` appear only as state.

**Density** — the board holds a uniform 30 px row; ten to eleven model rows are visible
below the source strip at the floor viewport without scrolling.

### One token changed from §4.6, on purpose

§4.6 proposes `rgba(233,238,242, …)` for the ink ladder and, in the same breath, requires
neutrals with a channel spread under 5. Those two are not compatible: 242 − 233 = 9.
The ladder is now **`rgba(236,238,240, …)`** — spread 4, the same cool near-white to the
eye, and a ladder that actually satisfies the rule it was given. Every other token in
§4.6 is unchanged.

## Timeline — the 5 s bar

**Cold** (`design/timeline/cold-*.png`) — a first run against an empty config directory,
no sources, no catalog cache. Two milestones, measured separately, because collapsing
them into one number hides where the time actually goes:

- **surface usable: 377 ms.** The rail, the nav, and a board with a state that says what
  it is waiting for. Detection runs in the background and never blocks this frame.
- **first routable model on the board: 0.17 s – ~70 s**, and the spread is the runtime's,
  not Ferrule's. Adoption ends in one real request to prove the source actually serves, and
  a local runtime answers that request only after loading the model. Measured on this
  machine against Ollama with `qwen3:8b`: **0.17 s** when the model is resident, **5.9 s**
  when the model is unloaded but the Ollama process is warm, and **~70 s** on a genuinely
  cold Ollama — its own first `/v1/models` alone takes 1.6 s against 20 ms warm.

  Both surfaces say so while it happens: the CLI narrates each phase, and the board holds
  a "still looking" state driven by the daemon's own `scanning` flag rather than a timer.

The 5 s bar is a **time-to-first-value** bar, and it is met at 377 ms: the surface is
usable, it is honest about what it is doing, and nothing is hidden behind a spinner.
Waiting on a local model load is the runtime's cost, disclosed rather than absorbed.

An earlier draft of this record claimed a flat 267 ms to a routable model. That
measurement was taken against an already-warm Ollama and did not generalise; the numbers
above replace it.

**Warm** (`design/timeline/warm-t0.png`, `-t5`, `-t30`) — the board is fully painted at
t≈0 and unchanged at 5 s and 30 s. Nothing arrives late; nothing reflows.

**Failure** (`design/timeline/warm-failure.png`) — a key that cannot work names the
source, prints the upstream reason verbatim in the error treatment, and says what the
person does next. The failed source is kept, visibly failed, on the board.

**Narrower windows** — `warm-narrow-900.png` folds the rail's counts; `warm-narrow-700.png`
moves the rail above the board. Horizontal overflow at the floor viewport: **0 px**.

## Console

Zero console errors and zero uncaught exceptions across every pane, in a clean Chromium
with the shipping CSP. (The panel sets no inline styles at all: `style-src 'self'` with
no `unsafe-inline` is enforced, and the JS sets classes. A runtime-varying bar is a
native `<progress>` so its value rides on an attribute.)

## Open

- **The fresh-context rubric pass is not yet filed.** The machine-checkable half of the
  gate above is met and reproducible; the subjective pass has to be run by a context that
  did not write this code, and this record was written by the one that did. Until that
  pass is filed with zero findings, chunk 3 is verified in part, not in whole.

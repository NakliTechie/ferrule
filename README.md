# Ferrule

> One local panel: every key held once, every model — local or cloud — visible,
> switchable, and accountable for what leaves your machine.

Ferrule is a **local key vault first, a model router second**. You mint a provider key
once, paste it into Ferrule, and Ferrule becomes the one encrypted local place that key
lives. The unified OpenAI-compatible endpoint, the model board, the alias ladders, and
the spend/egress views all fall out of the vault.

- **Zero-config discovery** — probe, don't declare. Ferrule scans localhost for running
  runtimes (Ollama, LM Studio, llama.cpp) and adopts them; adding a cloud provider is
  *paste a key*, not *edit a file*.
- **Egress visibility** — a local, metadata-only ledger of what actually left the
  machine, per app and per model.

One statically-linked Go binary. No account, no telemetry, no server database.

Full vision, roadmap, and agent handoff: [FERRULE.md](FERRULE.md).

## Status

Scaffolded. See FERRULE.md §4.10 for the chunk sequence.

## Build

```
go build -o ferrule ./cmd/ferrule
./ferrule serve
```

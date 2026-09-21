# Capture run — 2026-09-21 21:58:18

**Summary**: 29/29 routes ok · 0 errors

## terminal

| # | Route | Status | ms | Errors |
|---|---|---|---|---|
| 01 | `ferrule --help` | ok | 15 | — |
| 02 | `ferrule status` | ok | 22 | — |
| 03 | `ferrule ls models` | ok | 17 | — |
| 04 | `ferrule ls models --local` | ok | 19 | — |
| 05 | `ferrule alias` | ok | 19 | — |
| 06 | `ferrule usage` | ok | 19 | — |
| 07 | `ferrule usage --egress` | ok | 20 | — |
| 08 | `ferrule key kitchen-ipad` | ok | 20 | — |

## household

| # | Route | Status | ms | Errors |
|---|---|---|---|---|
| 01 | `curl -s http://127.0.0.1:8892/v1/models -H "Authorization: Bearer $FERRULE_KEY" | python3 -m json.tool | head -24` | ok | 140 | — |
| 02 | `curl -s http://127.0.0.1:8892/v1/chat/completions \` | ok | 133 | — |
| 03 | `curl -s -i http://127.0.0.1:8892/v1/models | head -12` | ok | 23 | — |
| 04 | `curl -s http://127.0.0.1:8892/v1/chat/completions \` | ok | 73 | — |

## agent

| # | Route | Status | ms | Errors |
|---|---|---|---|---|
| 01 | `curl -s http://127.0.0.1:8892/llms.txt | head -30` | ok | 18 | — |
| 02 | `curl -s http://127.0.0.1:8892/mcp | python3 -c 'import json,sys; m=json.load(sys.stdin); print(m["instructions"]); print(); [print("  "+t["name"].ljust(18), t["description"][:80]) for t in m["tools"]]'` | ok | 52 | — |
| 03 | `curl -s -X POST http://127.0.0.1:8892/mcp -H 'Content-Type: application/json' -d '{` | ok | 45 | — |

## owner · first run

| # | Route | Status | ms | Errors |
|---|---|---|---|---|
| 01 | `simple/home` | ok | 1854 | — |
| 02 | `advanced/add` | ok | 1438 | — |

## owner · the panel

| # | Route | Status | ms | Errors |
|---|---|---|---|---|
| 03 | `simple/home` | ok | 1814 | — |
| 04 | `simple/home` | ok | 1912 | — |
| 04b | `simple/home` | ok | 1861 | — |
| 05 | `advanced/board` | ok | 1558 | — |
| 06 | `advanced/board` | ok | 1833 | — |
| 07 | `advanced/aliases` | ok | 1422 | — |
| 08 | `advanced/add` | ok | 1363 | — |
| 09 | `advanced/usage` | ok | 2132 | — |
| 10 | `advanced/usage` | ok | 2522 | — |
| 11 | `advanced/grants` | ok | 1288 | — |
| 12 | `advanced/staged` | ok | 1305 | — |
| 13 | `simple/home` | ok | 1970 | — |

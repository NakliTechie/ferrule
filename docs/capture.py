#!/usr/bin/env python3
"""
Capture every screen and command the guide shows. Run by regenerate.sh, which boots the
two daemons this needs and passes their coordinates in the environment:

    FERRULE_BIN     the built binary
    FIRSTRUN_URL    a daemon on an empty config dir (the first-run screens)
    DEMO_URL        the demo daemon (fake providers, replayed traffic)
    DEMO_DIR        the demo's config dir, so the CLI drives the same state
    DEMO_KEY        a household token the demo minted

Outputs:
    screenshots/<role>/NN-<slug>.png     browser routes, retina
    transcripts/<role>/NN-<slug>.txt     cli routes, raw, with the exit code on the last line
    CAPTURE-LOG.md

A route that renders blank or exits with the wrong code is marked, not hidden: it is a bug
for /walkthrough-nt, and the guide should not paper over it.
"""

import os
import re
import subprocess
import sys
import time
from datetime import datetime
from pathlib import Path

from playwright.sync_api import sync_playwright

ROOT = Path(__file__).resolve().parent
SHOTS = ROOT / "screenshots"
TXTS = ROOT / "transcripts"
LOG = ROOT / "CAPTURE-LOG.md"
VIEWPORT = {"width": 1280, "height": 860}

BIN = os.environ["FERRULE_BIN"]
FIRSTRUN_URL = os.environ["FIRSTRUN_URL"].rstrip("/")
DEMO_URL = os.environ["DEMO_URL"].rstrip("/")
DEMO_DIR = os.environ["DEMO_DIR"]
DEMO_KEY = os.environ["DEMO_KEY"]

# ---------------------------------------------------------------------------------------
# Route plans. Browser rows: (NN, slug, base, mode, view, wait_ms, post_js).
# `post_js` runs after the view is up and before the shot — open a <details>, scroll to a
# section, open a modal. CLI rows: (NN, slug, argv-or-shell, expected_exit, must_match) —
# must_match is a regex the output has to contain. Exit 0 plus non-empty output is not an
# oracle: a 401 body is non-empty and exits 0 through a pipe. This is what caught that.
# ---------------------------------------------------------------------------------------

OPEN_DETAILS = "() => document.querySelectorAll('.pane[data-active] details').forEach(d => d.open = true)"
OPEN_VERBATIM = "() => document.querySelectorAll('.pane[data-active] details.verbatim').forEach(d => d.open = true)"
SCROLL_TO_ERRORS = """() => {
  const h = [...document.querySelectorAll('.pane[data-active] h2, .pane[data-active] h3')]
    .find(e => /went wrong/i.test(e.textContent));
  if (h) h.scrollIntoView({ block: 'start' });
  return !!h;
}"""
OPEN_SOVEREIGNTY = "() => { sovereigntyModal(); return true; }"
SHOW_KEY = """() => {
  const b = [...document.querySelectorAll('.pane[data-active] button')].find(b => /household key/i.test(b.textContent));
  if (b) b.click();
  return !!b;
}"""

OWNER_FIRSTRUN = [
    ("01", "first-run-home",   FIRSTRUN_URL, "simple",   "home",    900, None),
    ("02", "first-run-add",    FIRSTRUN_URL, "advanced", "add",     700, None),
]

OWNER_PANEL = [
    ("03", "home",             DEMO_URL, "simple",   "home",    900, None),
    ("04", "home-key",         DEMO_URL, "simple",   "home",    900, SHOW_KEY),
    ("04b", "home-models",     DEMO_URL, "simple",   "home",    900, OPEN_DETAILS),
    ("05", "board",            DEMO_URL, "advanced", "board",   800, None),
    ("06", "board-failed",     DEMO_URL, "advanced", "board",   800, OPEN_VERBATIM),
    ("07", "aliases",          DEMO_URL, "advanced", "aliases", 700, None),
    ("08", "add",              DEMO_URL, "advanced", "add",     700, None),
    ("09", "usage",            DEMO_URL, "advanced", "usage",  1400, None),
    ("10", "usage-errors",     DEMO_URL, "advanced", "usage",  1400, SCROLL_TO_ERRORS),
    ("11", "grants",           DEMO_URL, "advanced", "grants",  700, None),
    ("12", "staged",           DEMO_URL, "advanced", "staged",  700, None),
    ("13", "sovereignty",      DEMO_URL, "simple",   "home",    900, OPEN_SOVEREIGNTY),
]

CLI_ENV = {"FERRULE_CONFIG_DIR": DEMO_DIR, "FERRULE_PORT": DEMO_URL.rsplit(":", 1)[1], "NO_COLOR": ""}
TERMINAL = [
    ("01", "help",           [BIN, "--help"], 0, r"Verbs:"),
    ("02", "status",         [BIN, "status"], 0, r"SOURCE +WHERE"),
    ("03", "ls-models",      [BIN, "ls", "models"], 0, r"MODEL +SOURCE"),
    ("04", "ls-local",       [BIN, "ls", "models", "--local"], 0, r"local"),
    ("05", "alias",          [BIN, "alias"], 0, r"ALIAS +LADDER"),
    ("06", "usage",          [BIN, "usage"], 0, r"REQUESTS"),
    ("07", "usage-egress",   [BIN, "usage", "--egress"], 0, r"off-machine"),
    ("08", "key",            [BIN, "key", "kitchen-ipad"], 0, r"frl_"),
]

# Anyone on the wifi with a token: a client, not the panel.
HOUSEHOLD = [
    ("01", "models", f'curl -s {DEMO_URL}/v1/models -H "Authorization: Bearer $FERRULE_KEY" | python3 -m json.tool | head -24', 0, r'"object": "model"'),
    ("02", "chat", f"""curl -s {DEMO_URL}/v1/chat/completions \\
  -H "Authorization: Bearer $FERRULE_KEY" -H 'Content-Type: application/json' \\
  -d '{{"model":"fast","messages":[{{"role":"user","content":"Say hello in five words."}}]}}' | python3 -m json.tool""", 0, r'"content":'),
    ("03", "no-token", f"curl -s -i {DEMO_URL}/v1/models | head -12", 0, r"401 Unauthorized"),
    # Asks the source the demo deliberately misconfigured, so the guide shows a refusal
    # that names itself, and the Usage screen has something in "What went wrong".
    ("04", "failed-request", f"""curl -s {DEMO_URL}/v1/chat/completions \\
  -H "Authorization: Bearer $FERRULE_KEY" -H 'Content-Type: application/json' \\
  -d '{{"model":"groq-typo/llama-3.3-70b-versatile","messages":[{{"role":"user","content":"hi"}}]}}' | python3 -m json.tool""", 0, r"source groq-typo is failed"),
]

# A coding agent: the docs' agent face, the manifest, one staged proposal.
AGENT = [
    ("01", "llms-txt", f"curl -s {DEMO_URL}/llms.txt | head -30", 0, r"# Ferrule"),
    ("02", "mcp-manifest", f"curl -s {DEMO_URL}/mcp | python3 -c 'import json,sys; m=json.load(sys.stdin); print(m[\"instructions\"]); print(); [print(\"  \"+t[\"name\"].ljust(18), t[\"description\"][:80]) for t in m[\"tools\"]]'", 0, r"add_source"),
    ("03", "mcp-stage", f"""curl -s -X POST {DEMO_URL}/mcp -H 'Content-Type: application/json' -d '{{
  "jsonrpc":"2.0","id":1,"method":"tools/call",
  "params":{{"name":"set_alias","arguments":{{"name":"everyday","ladder":["ollama/qwen3:8b","deepseek/deepseek-chat"]}}}}
}}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["content"][0]["text"])'""", 0, r'"staged": true'),
]


def sh_display(cmd):
    """What the transcript shows as the command line: argv joined, the binary as `ferrule`."""
    if isinstance(cmd, list):
        return " ".join(["ferrule"] + cmd[1:])
    return cmd


def run_cli(role, num, slug, cmd, expect, must_match):
    env = dict(os.environ, **CLI_ENV, FERRULE_KEY=DEMO_KEY)
    t0 = time.time()
    if isinstance(cmd, list):
        p = subprocess.run(cmd, capture_output=True, text=True, env=env, timeout=60)
    else:
        p = subprocess.run(cmd, shell=True, capture_output=True, text=True, env=env, timeout=60)
    out = p.stdout + (("\n" + p.stderr) if p.stderr.strip() else "")
    # The household token is real for this demo run only; the transcript shows the shape.
    out = out.replace(DEMO_KEY, "frl_" + "…" * 3 + DEMO_KEY[-4:])
    body = f"$ {sh_display(cmd)}\n{out.rstrip()}\n"
    path = TXTS / role / f"{num}-{slug}.txt"
    path.write_text(body)
    errors = []
    if p.returncode != expect:
        errors.append({"type": "exit", "text": f"exit {p.returncode}, expected {expect}"})
    if not re.search(must_match, out):
        errors.append({"type": "oracle", "text": f"output does not match /{must_match}/"})
    return {"num": num, "slug": slug, "route": sh_display(cmd).splitlines()[0], "status": "ok" if not errors else "fail",
            "ms": int((time.time() - t0) * 1000), "errors": errors,
            "path": str(path.relative_to(ROOT))}


def run():
    for role in ("owner", "household", "agent", "terminal"):
        (SHOTS / role).mkdir(parents=True, exist_ok=True) if role == "owner" else (TXTS / role).mkdir(parents=True, exist_ok=True)

    sections = {}
    for name, role, plan in (("terminal", "terminal", TERMINAL), ("household", "household", HOUSEHOLD), ("agent", "agent", AGENT)):
        print(f"[{name}]", flush=True)
        rows = []
        for num, slug, cmd, expect, must_match in plan:
            r = run_cli(role, num, slug, cmd, expect, must_match)
            print(f"  [{r['status']:5}] {r['num']} {r['slug']} ({r['ms']}ms)" + (f"  {r['errors'][0]['text']}" if r["errors"] else ""), flush=True)
            rows.append(r)
        sections[name] = rows

    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        ctx = browser.new_context(viewport=VIEWPORT, device_scale_factor=2)
        page = ctx.new_page()
        console = []
        page.on("console", lambda m: console.append({"type": m.type, "text": m.text}) if m.type in ("error", "warning") else None)

        def capture(num, slug, base, mode, view, wait_ms, post_js):
            console.clear()
            t0 = time.time()
            try:
                page.goto(base + "/", wait_until="load")
                page.evaluate("m => { localStorage.setItem('ferrule.mode', m); }", mode)
                page.reload(wait_until="load")
                page.evaluate("() => document.fonts.ready")
                page.wait_for_timeout(400)
                page.evaluate("v => { if (typeof go === 'function') go(v); }", view)
                page.wait_for_timeout(wait_ms)
                if post_js:
                    page.evaluate(post_js)
                    page.wait_for_timeout(350)
                rendered = page.evaluate("() => { const p = document.querySelector('.pane[data-active]'); return p ? p.innerHTML.length : 0; }")
                path = SHOTS / "owner" / f"{num}-{slug}.png"
                page.screenshot(path=str(path))
                errs = [e for e in console if e["type"] == "error"]
                return {"num": num, "slug": slug, "route": f"{mode}/{view}", "status": "ok" if rendered > 50 else "empty",
                        "ms": int((time.time() - t0) * 1000), "errors": errs, "path": str(path.relative_to(ROOT))}
            except Exception as e:  # noqa: BLE001 — a failed route is a row, not a crash
                return {"num": num, "slug": slug, "route": f"{mode}/{view}", "status": "fail",
                        "ms": int((time.time() - t0) * 1000), "errors": [{"type": "exception", "text": str(e)}], "path": None}

        for name, plan in (("owner · first run", OWNER_FIRSTRUN), ("owner · the panel", OWNER_PANEL)):
            print(f"[{name}]", flush=True)
            rows = []
            for row in plan:
                r = capture(*row)
                print(f"  [{r['status']:5}] {r['num']} {r['slug']} ({r['ms']}ms)" + (f"  ERRORS: {len(r['errors'])}" if r["errors"] else ""), flush=True)
                rows.append(r)
            sections[name] = rows
        browser.close()

    ok = sum(1 for rows in sections.values() for r in rows if r["status"] == "ok")
    total = sum(len(rows) for rows in sections.values())
    nerr = sum(len(r["errors"]) for rows in sections.values() for r in rows)
    lines = [f"# Capture run — {datetime.now():%Y-%m-%d %H:%M:%S}", "", f"**Summary**: {ok}/{total} routes ok · {nerr} errors", ""]
    for name, rows in sections.items():
        lines += [f"## {name}", "", "| # | Route | Status | ms | Errors |", "|---|---|---|---|---|"]
        for r in rows:
            err = "; ".join(e["text"][:90].replace("|", "\\|").replace("\n", " ") for e in r["errors"][:2]) or "—"
            lines.append(f"| {r['num']} | `{r['route']}` | {r['status']} | {r['ms']} | {err} |")
        lines.append("")
    LOG.write_text("\n".join(lines))
    print(f"\n{ok}/{total} ok · {nerr} errors · log: {LOG.relative_to(ROOT)}")
    return 0 if ok == total else 1


if __name__ == "__main__":
    sys.exit(run())

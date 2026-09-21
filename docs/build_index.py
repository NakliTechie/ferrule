#!/usr/bin/env python3
"""
Assemble docs/index.html from the captures and the prose below. The prose is data here —
CAPTIONS and SECTIONS — so a regeneration re-shoots every screen and never loses a
sentence. Edit this file and run regenerate.sh; never edit index.html.

The page is one self-contained file plus screenshots/, transcripts/ and fonts/, so it
works opened from disk, served by GitHub Pages, and served by the daemon at /guide/.
"""

import html
import re
import shutil
from pathlib import Path

ROOT = Path(__file__).resolve().parent
REPO = ROOT.parent
OUT = ROOT / "index.html"
SHOTS = ROOT / "screenshots"
TXTS = ROOT / "transcripts"
FONTS = ROOT / "fonts"

# ---------------------------------------------------------------------------------------
# Prose. A caption is (title, one line on what the reader is looking at).
# ---------------------------------------------------------------------------------------

CAPTIONS = {
    # owner
    "owner/01-first-run-home": ("The first screen",
        "A fresh start: nothing routed yet. The card at the top is the address and household key you hand out; the three numbered steps below are the whole setup."),
    "owner/02-first-run-add": ("Add a source",
        "Pick a provider and paste its key. Ferrule tries the key against the provider before it keeps it — a dead key is never quietly stored."),
    "owner/03-home": ("Your household",
        "The simple view. Sharing is on, the address is the one to give people, and every source Ferrule found or was given is listed with its models."),
    "owner/04-home-key": ("The household key",
        "One key everyone can use, shown on request. Give people their own key only when you want the usage list to say who, or to cut one person off."),
    "owner/04b-home-models": ("What each source serves",
        "Each source opens to its models. Local ones are on this machine; cloud ones go to a provider and are counted as such in Usage."),
    "owner/05-board": ("The board",
        "The advanced view. Every model Ferrule can serve, with where it runs, what it can do, its context length and its cost per million tokens; filter by any of them."),
    "owner/06-board-failed": ("A failure that names itself",
        "The source the demo deliberately misconfigured. The red line is the one clear next action; under it, what the provider said, verbatim."),
    "owner/07-aliases": ("Aliases",
        "A plain name for a ladder of models, tried in order. Apps ask for `fast`; you decide what fast means, and change it without telling anyone."),
    "owner/08-add": ("Add a source, in full",
        "The full form: provider, a name, the key, an optional test model for accounts whose tier excludes the usual ones, and a base URL for anything OpenAI-compatible."),
    "owner/09-usage": ("Usage and egress",
        "Every request, by app and model — count, tokens, latency, cost — and above it the split that no cost dashboard shows: what stayed on this machine and what left."),
    "owner/10-usage-errors": ("What went wrong",
        "Failed requests with the provider's full words. An app on the network gets a shorter version; the reason stays here, on the machine the key lives on."),
    "owner/11-grants": ("Grants",
        "Every app token, who it was minted for, and when it was last used. Revoke one and the others keep working; the provider key is never the thing an app holds."),
    "owner/12-staged": ("Staged by an agent",
        "A coding agent proposed an alias through the MCP face. Nothing has changed yet — it lands when you apply it here, and a provider key is never in the proposal."),
    "owner/13-sovereignty": ("Where your keys live",
        "The statement, and the one knob that can contradict it, together: content logging is off, and this is where it would be turned on."),
    # household
    "household/01-models": ("List the models",
        "Anything that speaks OpenAI works. The token is a Ferrule key, never a provider key; `ferrule` in each entry says where the model runs."),
    "household/02-chat": ("A chat completion",
        "Ask for `fast` and the first reachable rung serves it — here a local model, so the request never left the machine."),
    "household/03-no-token": ("Without a token",
        "No token, 401. Inference is served to the network only with a valid key; the panel, the vault and the ledger answer only this machine."),
    "household/04-failed-request": ("When a source is down",
        "The refusal says which source failed and why, so the person can act. The full provider text is on the Usage screen."),
    # terminal
    "terminal/01-help": ("The verbs",
        "Every command, one line each. Exit codes mean something: 0 it worked, 1 it did not and the reason says why, 2 the command was wrong."),
    "terminal/02-status": ("The whole situation in one read",
        "Sources and their state, what is servable, the aliases, tokens, last-day egress, and a `next:` list of anything that needs you. `--json` for an agent."),
    "terminal/03-ls-models": ("Every model",
        "The board, as a table: source, where it runs, capabilities, context, cost."),
    "terminal/04-ls-local": ("Only what runs here",
        "`--local` narrows to models on this machine — the ones whose requests never leave it."),
    "terminal/05-alias": ("The ladders",
        "Each alias and its rungs in order. `ferrule alias <name> <src>/<model> …` sets one."),
    "terminal/06-usage": ("Spend by app and model",
        "The same ledger the panel reads, grouped for a terminal."),
    "terminal/07-usage-egress": ("What left the machine",
        "Requests and bytes, local against off-machine, and the share."),
    "terminal/08-key": ("Mint a token for one person",
        "Shown once. The app gets the address and this key; you can revoke it later without touching anyone else's."),
    # agent
    "agent/01-llms-txt": ("The docs' agent face",
        "The running daemon serves `llms.txt`: what Ferrule is and how to drive it, so an agent pointed at a live Ferrule gets the surface without the repo."),
    "agent/02-mcp-manifest": ("The MCP manifest",
        "Every control operation, generated from the same command bus the panel and the CLI use. Read ops answer; mutating ops stage."),
    "agent/03-mcp-stage": ("A staged proposal",
        "The agent asked for an alias. It is staged, not applied; the person lands it from the panel. A key argument would have been withheld from the payload."),
}

# (section title, intro, [captures], role) — a capture is "role/NN-slug" and the
# card type follows the file that exists for it: a PNG is an image card, a TXT a terminal.
SECTIONS = [
    ("You, at the panel", "The control surface at http://localhost:8899 — the simple view for the household, the advanced view for you.", [
        ("First run", "What `ferrule serve` shows before anything is routed.",
            ["owner/01-first-run-home", "owner/02-first-run-add"]),
        ("Your household", "The simple view: what to hand out, and what Ferrule found.",
            ["owner/03-home", "owner/04-home-key", "owner/04b-home-models", "owner/13-sovereignty"]),
        ("The board", "The advanced view: every model, and the sources behind them.",
            ["owner/05-board", "owner/06-board-failed"]),
        ("Aliases and sources", "Names apps can ask for, and how a source is added.",
            ["owner/07-aliases", "owner/08-add"]),
        ("Usage", "What it cost, where it went, what went wrong.",
            ["owner/09-usage", "owner/10-usage-errors"]),
        ("Grants and staged changes", "Who holds a token; what an agent has proposed.",
            ["owner/11-grants", "owner/12-staged"]),
    ]),
    ("Someone in the house", "A person on the wifi with the address and a key. They never see the panel; their app talks to the endpoint.", [
        ("Using the endpoint", "The OpenAI-compatible lane, with a Ferrule token.",
            ["household/01-models", "household/02-chat", "household/03-no-token", "household/04-failed-request"]),
    ]),
    ("You, in the terminal", "Every panel operation is also a verb. The CLI reads and writes the same state the daemon serves.", [
        ("Reading the state", "Status, models, aliases, usage.",
            ["terminal/01-help", "terminal/02-status", "terminal/03-ls-models", "terminal/04-ls-local", "terminal/05-alias", "terminal/06-usage", "terminal/07-usage-egress"]),
        ("Minting a key", "One token per person or app.",
            ["terminal/08-key"]),
    ]),
    ("A coding agent", "The agent face: `llms.txt` and an MCP manifest served by the daemon. Proposals stage; you apply.", [
        ("Driving Ferrule from an agent", "Read the surface, then propose.",
            ["agent/01-llms-txt", "agent/02-mcp-manifest", "agent/03-mcp-stage"]),
    ]),
]

# ---------------------------------------------------------------------------------------
# The page. Chrome from the panel's own tokens (internal/ui/assets/app.css).
# ---------------------------------------------------------------------------------------

CSS = """
@font-face{font-family:"Ferrule Sans";src:url(fonts/IBMPlexSans-Regular.woff2) format("woff2");font-weight:400}
@font-face{font-family:"Ferrule Sans";src:url(fonts/IBMPlexSans-Medium.woff2) format("woff2");font-weight:500}
@font-face{font-family:"Ferrule Mono";src:url(fonts/JetBrainsMono-Regular.woff2) format("woff2");font-weight:400}
@font-face{font-family:"Ferrule Mono";src:url(fonts/JetBrainsMono-Medium.woff2) format("woff2");font-weight:500}
:root{--canvas:#0d0f11;--ink-100:rgba(236,238,240,.92);--ink-70:rgba(236,238,240,.62);--ink-50:rgba(236,238,240,.42);
--ink-30:rgba(236,238,240,.24);--ink-15:rgba(236,238,240,.12);--ink-08:rgba(236,238,240,.06);--accent:#3fb950;--error:#f85149;
--fill:rgba(236,238,240,.045);--sans:"Ferrule Sans",system-ui,-apple-system,"Segoe UI",sans-serif;--mono:"Ferrule Mono",ui-monospace,SFMono-Regular,Menlo,monospace}
*{box-sizing:border-box}html{scroll-behavior:smooth}
body{margin:0;background:var(--canvas);color:var(--ink-100);font:15px/1.55 var(--sans);-webkit-font-smoothing:antialiased}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
code{font-family:var(--mono);font-size:.92em;color:var(--ink-100)}
.wrap{max-width:1180px;margin:0 auto;padding:0 20px}
header.top{border-bottom:1px solid var(--ink-15);padding:36px 0 22px}
.brand{font:500 13px var(--mono);letter-spacing:.22em;color:var(--accent)}
h1{margin:10px 0 6px;font-weight:500;font-size:30px;letter-spacing:-.01em}
.lede{margin:0;max-width:720px;color:var(--ink-70)}
.links{margin-top:14px;font-size:13px;color:var(--ink-50)}.links a{margin-right:16px}
.search{position:sticky;top:0;z-index:5;background:rgba(13,15,17,.92);backdrop-filter:blur(8px);border-bottom:1px solid var(--ink-15);padding:10px 0}
.search .wrap{display:flex;gap:10px;align-items:center}
.search input{flex:1;max-width:520px;background:var(--fill);border:1px solid var(--ink-15);border-radius:6px;color:var(--ink-100);
font:14px var(--sans);padding:8px 12px;outline:none}.search input:focus{border-color:var(--accent)}
.search .hint{font:12px var(--mono);color:var(--ink-50)}
.count{font:12px var(--mono);color:var(--ink-50)}
nav.toc{padding:22px 0 6px;font-size:14px}nav.toc ol{list-style:none;margin:0;padding:0;display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:4px 24px}
nav.toc li a{color:var(--ink-70)}nav.toc li ol{padding-left:14px;margin-top:2px}nav.toc li ol a{color:var(--ink-50);font-size:13px}
section.role{padding:26px 0 8px;border-top:1px solid var(--ink-08)}
section.role>h2{margin:0 0 4px;font-weight:500;font-size:22px}section.role>p{margin:0 0 18px;color:var(--ink-70);max-width:720px}
section.feature{padding:8px 0 18px}section.feature>h3{margin:0 0 2px;font-weight:500;font-size:16px}
section.feature>p{margin:0 0 14px;color:var(--ink-50);font-size:14px}
.cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(340px,1fr));gap:18px}
.card{background:var(--fill);border:1px solid var(--ink-15);border-radius:8px;overflow:hidden;display:flex;flex-direction:column}
.card.hidden,section.hidden{display:none}
.card img{display:block;width:100%;height:auto;aspect-ratio:1280/860;object-fit:cover;object-position:top;cursor:zoom-in;background:#000;border-bottom:1px solid var(--ink-15)}
.card .cap{padding:12px 14px 14px}.card .cap b{display:block;font-weight:500;margin-bottom:3px}.card .cap span{color:var(--ink-70);font-size:13.5px}
.card.term{grid-column:1/-1}
.term .win{background:#08090b;border-bottom:1px solid var(--ink-15)}
.term .bar{display:flex;gap:6px;padding:9px 12px;border-bottom:1px solid var(--ink-08)}.term .bar i{width:10px;height:10px;border-radius:50%;background:var(--ink-15)}
.term pre{margin:0;padding:12px 14px;overflow-x:auto;font:12.5px/1.5 var(--mono);color:var(--ink-100);white-space:pre}
.term pre .p{color:var(--accent)}.term pre .c{color:var(--ink-100);font-weight:500}
.none{display:none;color:var(--ink-50);padding:30px 0}
footer{border-top:1px solid var(--ink-15);margin-top:30px;padding:20px 0 40px;font-size:13px;color:var(--ink-50)}
/* lightbox */
.lb{position:fixed;inset:0;background:rgba(0,0,0,.88);display:none;z-index:20;flex-direction:column;align-items:center;justify-content:center;touch-action:pan-y pinch-zoom}
.lb.open{display:flex}.lb img{max-width:96vw;max-height:78vh;object-fit:contain;border:1px solid var(--ink-15);border-radius:4px}
.lb .lcap{color:var(--ink-70);max-width:820px;padding:12px 20px 0;text-align:center;font-size:14px}.lb .lcap b{color:var(--ink-100);font-weight:500}
.lb .pos{font:12px var(--mono);color:var(--ink-50);margin-top:6px}
.lb button{position:absolute;background:var(--fill);border:1px solid var(--ink-15);color:var(--ink-100);border-radius:8px;min-width:44px;min-height:44px;font-size:20px;cursor:pointer}
.lb .x{top:14px;right:14px}.lb .prev{left:14px;top:50%;transform:translateY(-50%)}.lb .next{right:14px;top:50%;transform:translateY(-50%)}
@media (max-width:640px){h1{font-size:24px}.cards{grid-template-columns:1fr}.search .hint{display:none}.lb .prev,.lb .next{top:auto;bottom:16px;transform:none}}
"""

JS = r"""
(function(){
  const q = document.getElementById('q'), cards = [...document.querySelectorAll('.card')],
        feats = [...document.querySelectorAll('section.feature')], roles = [...document.querySelectorAll('section.role')],
        none = document.getElementById('none'), count = document.getElementById('count');
  function filter(){
    const v = q.value.trim().toLowerCase(); let n = 0;
    for (const c of cards){ const on = !v || c.dataset.search.includes(v); c.classList.toggle('hidden', !on); if (on) n++; }
    for (const f of feats) f.classList.toggle('hidden', ![...f.querySelectorAll('.card')].some(c => !c.classList.contains('hidden')));
    for (const r of roles) r.classList.toggle('hidden', ![...r.querySelectorAll('.card')].some(c => !c.classList.contains('hidden')));
    none.style.display = n ? 'none' : 'block'; count.textContent = v ? n + ' of ' + cards.length : cards.length + ' captures';
  }
  q.addEventListener('input', filter); filter();
  document.addEventListener('keydown', e => {
    if (e.key === '/' && document.activeElement !== q && !lb.classList.contains('open')) { e.preventDefault(); q.focus(); }
    if (e.key === 'Escape' && document.activeElement === q) { q.value = ''; filter(); q.blur(); }
  });
  // lightbox
  const lb = document.getElementById('lb'), li = lb.querySelector('img'), lcap = lb.querySelector('.lcap'), pos = lb.querySelector('.pos');
  let cur = -1, scrollY = 0;
  const shots = () => cards.filter(c => c.classList.contains('shot') && !c.classList.contains('hidden'));
  function show(i){
    const list = shots(); if (!list.length) return; cur = (i + list.length) % list.length; const c = list[cur];
    li.src = c.querySelector('img').src; lcap.innerHTML = '<b>' + c.dataset.title + '</b><br>' + c.dataset.caption;
    pos.textContent = c.dataset.role + ' · ' + c.dataset.feature + ' — ' + (cur + 1) + '/' + list.length;
    if (!lb.classList.contains('open')) { scrollY = window.scrollY; lb.classList.add('open'); document.body.style.overflow = 'hidden'; }
  }
  function close(){ lb.classList.remove('open'); document.body.style.overflow = ''; window.scrollTo(0, scrollY); }
  function jump(dir){
    const list = shots(); const f = list[cur].dataset.feature; let i = cur;
    if (dir > 0) { while (i < list.length && list[i].dataset.feature === f) i++; if (i >= list.length) i = 0; }
    else { while (i > 0 && list[i - 1].dataset.feature === f) i--; i--; if (i < 0) i = list.length - 1; const g = list[i].dataset.feature; while (i > 0 && list[i - 1].dataset.feature === g) i--; }
    show(i);
  }
  for (const c of cards) if (c.classList.contains('shot')) c.querySelector('img').addEventListener('click', () => show(shots().indexOf(c)));
  lb.querySelector('.x').onclick = close; lb.querySelector('.prev').onclick = () => show(cur - 1); lb.querySelector('.next').onclick = () => show(cur + 1);
  lb.addEventListener('click', e => { if (e.target === lb) close(); });
  document.addEventListener('keydown', e => {
    if (!lb.classList.contains('open')) return;
    if (e.key === 'Escape') close(); else if (e.key === 'ArrowLeft') show(cur - 1); else if (e.key === 'ArrowRight') show(cur + 1);
    else if (e.key === 'ArrowUp') jump(-1); else if (e.key === 'ArrowDown') jump(1);
  });
  let tx = 0, ty = 0;
  lb.addEventListener('touchstart', e => { tx = e.touches[0].clientX; ty = e.touches[0].clientY; }, { passive: true });
  lb.addEventListener('touchend', e => {
    if (e.changedTouches.length !== 1 || e.touches.length) return;
    const dx = e.changedTouches[0].clientX - tx, dy = e.changedTouches[0].clientY - ty;
    if (Math.abs(dx) > 60 && Math.abs(dx) > Math.abs(dy)) show(cur + (dx < 0 ? 1 : -1)); else if (dy > 80) close();
  }, { passive: true });
})();
"""

SGR = {"1": "font-weight:500", "2": "opacity:.6", "30": "color:#000", "31": "color:#f85149", "32": "color:#3fb950", "33": "color:#d29922",
       "34": "color:#58a6ff", "35": "color:#bc8cff", "36": "color:#39c5cf", "37": "color:#ecedf0", "90": "color:rgba(236,238,240,.42)"}


def ansi_to_html(text):
    """The few SGR codes a CLI uses, to inline spans; everything else is stripped."""
    out, open_span = [], False
    for part in re.split(r"(\x1b\[[0-9;]*m)", text):
        if part.startswith("\x1b["):
            if open_span:
                out.append("</span>"); open_span = False
            codes = [c for c in part[2:-1].split(";") if c and c != "0"]
            styles = [SGR[c] for c in codes if c in SGR]
            if styles:
                out.append(f'<span style="{";".join(styles)}">'); open_span = True
        else:
            out.append(html.escape(part))
    if open_span:
        out.append("</span>")
    return "".join(out)


def slugid(s):
    return re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")


def render_card(key, role_title, feature_title):
    title, caption = CAPTIONS[key]
    role, name = key.split("/", 1)
    png, txt = SHOTS / role / f"{name}.png", TXTS / role / f"{name}.txt"
    search = " ".join([role_title, feature_title, title, caption, name]).lower()
    cap = f'<div class="cap"><b>{html.escape(title)}</b><span>{inline(caption)}</span></div>'
    if png.exists():
        return (f'<figure class="card shot" data-title="{html.escape(title)}" data-caption="{html.escape(caption)}" '
                f'data-role="{html.escape(role_title)}" data-feature="{html.escape(feature_title)}" data-search="{html.escape(search)}">'
                f'<img src="screenshots/{role}/{name}.png" alt="{html.escape(title)}" loading="lazy">{cap}</figure>')
    if txt.exists():
        body = txt.read_text()
        first, _, rest = body.partition("\n")
        cmd = first[2:] if first.startswith("$ ") else first
        # A multi-line command (continuation lines end in a backslash) stays in the prompt colour.
        lines = rest.split("\n")
        while cmd.rstrip().endswith("\\") and lines:
            cmd += "\n" + lines.pop(0)
        pre = f'<span class="p">$</span> <span class="c">{html.escape(cmd)}</span>\n{ansi_to_html(chr(10).join(lines))}'
        return (f'<figure class="card term" data-search="{html.escape(search + " " + cmd.lower())}">'
                f'<div class="win"><div class="bar"><i></i><i></i><i></i></div><pre>{pre}</pre></div>{cap}</figure>')
    raise SystemExit(f"no capture for {key}: expected {png} or {txt}")


def inline(text):
    """Backticks to <code>, nothing else."""
    return re.sub(r"`([^`]+)`", lambda m: f"<code>{html.escape(m.group(1))}</code>", html.escape(text))


def build():
    FONTS.mkdir(exist_ok=True)
    for f in ("IBMPlexSans-Regular", "IBMPlexSans-Medium", "JetBrainsMono-Regular", "JetBrainsMono-Medium"):
        shutil.copyfile(REPO / "internal/ui/assets/fonts" / f"{f}.woff2", FONTS / f"{f}.woff2")

    toc, body, n = [], [], 0
    for role_title, role_intro, features in SECTIONS:
        rid = slugid(role_title)
        toc.append(f'<li><a href="#{rid}">{html.escape(role_title)}</a><ol>' + "".join(
            f'<li><a href="#{rid}-{slugid(ft)}">{html.escape(ft)}</a></li>' for ft, _, _ in features) + "</ol></li>")
        body.append(f'<section class="role" id="{rid}"><h2>{html.escape(role_title)}</h2><p>{inline(role_intro)}</p>')
        for ft, fi, keys in features:
            body.append(f'<section class="feature" id="{rid}-{slugid(ft)}"><h3>{html.escape(ft)}</h3><p>{inline(fi)}</p><div class="cards">')
            for k in keys:
                body.append(render_card(k, role_title, ft)); n += 1
            body.append("</div></section>")
        body.append("</section>")

    page = f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Ferrule — the guide</title>
<meta name="description" content="Every screen and command in Ferrule, the local key vault and model router, with what each one is for.">
<style>{CSS}</style></head>
<body>
<header class="top"><div class="wrap">
  <div class="brand">FERRULE</div>
  <h1>The guide</h1>
  <p class="lede">Every screen and command, captured from a running Ferrule with fake providers, and what each one is for. Keys stay on your machine; the endpoint is for the house.</p>
  <div class="links"><a href="../">the panel</a><a href="https://github.com/NakliTechie/ferrule">source</a><a href="https://github.com/NakliTechie/ferrule/releases/latest">download</a><a href="../llms.txt">llms.txt</a></div>
</div></header>
<div class="search"><div class="wrap"><input id="q" type="search" placeholder="Search screens and commands…" autocomplete="off" spellcheck="false"><span class="count" id="count"></span><span class="hint">/ to search · Esc to clear</span></div></div>
<div class="wrap">
<nav class="toc"><ol>{"".join(toc)}</ol></nav>
{"".join(body)}
<p class="none" id="none">Nothing matches.</p>
<footer>{n} captures, regenerated by <code>docs/regenerate.sh</code> from the build that ships. Screens open full-size: ← → step, ↑ ↓ jump sections, Esc closes.</footer>
</div>
<div class="lb" id="lb" role="dialog" aria-label="Screenshot"><button class="x" aria-label="Close">×</button><button class="prev" aria-label="Previous">‹</button><button class="next" aria-label="Next">›</button><img alt=""><div class="lcap"></div><div class="pos"></div></div>
<script>{JS}</script>
</body></html>
"""
    OUT.write_text(page)
    print(f"  {OUT.relative_to(REPO)}: {n} cards, {OUT.stat().st_size // 1024} KB")


if __name__ == "__main__":
    build()

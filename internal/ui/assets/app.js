/* Ferrule control surface. No framework, no build step, no network beyond this daemon.
   Every user-facing string comes from the server's string table — nothing is written
   into this file in English. */

let S = {};
const T = (k, ...a) => {
  let s = S[k];
  if (s === undefined) return "⟨" + k + "⟩";
  let i = 0;
  return s.replace(/%[sdv]|%\.\d+f/g, () => {
    const v = a[i++];
    return v === undefined ? "" : String(v);
  });
};

const $ = (sel, root = document) => root.querySelector(sel);
const el = (tag, attrs = {}, ...kids) => {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") n.className = v;
    else if (k === "text") n.textContent = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    n.appendChild(typeof kid === "string" ? document.createTextNode(kid) : kid);
  }
  return n;
};

async function op(name, args = {}) {
  const r = await fetch("/api/op/" + name, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Ferrule-Caller": "panel" },
    body: JSON.stringify(args),
  });
  const body = await r.json();
  if (!r.ok || body.error) throw new Error(body.error || r.statusText);
  return body;
}

let toastTimer;
function toast(msg, kind) {
  const t = $("#toast");
  t.textContent = msg;
  t.dataset.kind = kind || "info";
  t.dataset.on = "";
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => delete t.dataset.on, kind === "error" ? 7000 : 3200);
}

function modal(title, ...body) {
  const m = $("#modal");
  $("#modal-body").replaceChildren(el("h3", { text: title }), ...body);
  m.showModal();
  return m;
}
const closeModal = () => $("#modal").close();

/* ---------- state ---------- */
const state = {
  view: "board",
  sources: [],
  models: [],
  aliases: [],
  remaps: [],
  grants: [],
  staged: [],
  usage: null,
  egress: null,
  status: null,
  detecting: true,
  filters: { where: "", capability: "", q: "" },
  sort: { col: "model", dir: 1 },
};

const VIEWS = [
  ["board", "ui.nav.board", () => state.models.length],
  ["aliases", "ui.nav.aliases", () => state.aliases.length],
  ["add", "ui.nav.add", () => null],
  ["usage", "ui.nav.usage", () => null],
  ["grants", "ui.nav.grants", () => state.grants.filter((g) => !g.revoked).length],
];

function renderNav() {
  $("#nav").replaceChildren(
    ...VIEWS.map(([id, key, count]) =>
      el(
        "button",
        {
          type: "button",
          "aria-current": state.view === id ? "true" : "false",
          onclick: () => go(id),
        },
        el("span", { text: T(key) }),
        count() !== null ? el("span", { class: "count", text: String(count()) }) : null,
      ),
    ),
    state.staged.length
      ? el("button", {
          type: "button",
          "aria-current": state.view === "staged" ? "true" : "false",
          onclick: () => go("staged"),
        }, el("span", { text: T("ui.nav.staged") }),
           el("span", { class: "count", text: String(state.staged.length) }))
      : null,
  );
}

function go(view) {
  state.view = view;
  render();
}

/* ---------- data ---------- */
async function loadAll() {
  const [st, sources, models, aliases, remaps, grants, staged] = await Promise.all([
    op("status"), op("list_sources"), op("list_models"), op("list_aliases"),
    op("list_remaps"), op("list_grants"), op("list_staged"),
  ]);
  state.status = st;
  state.sources = sources.sources || [];
  state.models = models.models || [];
  state.catalogDate = models.catalog_date;
  state.aliases = aliases.aliases || [];
  state.remaps = remaps.remaps || [];
  state.grants = grants.grants || [];
  state.staged = staged.staged || [];
  $("#foot-vault").textContent = st.vault;
  $("#foot-catalog").textContent = st.catalog_date || "—";
  const dir = $("#foot-dir");
  dir.textContent = st.config_dir.replace(/^\/Users\/[^/]+/, "~");
  dir.title = st.config_dir;
}

async function loadUsage() {
  const [u, e] = await Promise.all([
    op("usage_summary", { by: ["app", "model"], since_hours: 0 }),
    op("egress_summary", { since_hours: 0 }),
  ]);
  state.usage = u;
  state.egress = e;
}

/* ---------- board ---------- */
const COLS = [
  { id: "model", key: "ui.board.col.model", get: (m) => m.model, mono: true },
  { id: "source", key: "ui.board.col.source", get: (m) => m.source },
  { id: "where", key: "ui.board.col.where", get: (m) => m.where },
  { id: "caps", key: "ui.board.col.caps", get: (m) => (m.capabilities || []).join(" ") },
  { id: "context", key: "ui.board.col.context", get: (m) => m.context_length || 0, num: true, right: true },
  { id: "cost", key: "ui.board.col.cost", get: (m) => m.in_cost_per_mtok || 0, num: true, right: true },
];

function visibleModels() {
  const f = state.filters;
  const q = f.q.trim().toLowerCase();
  let out = state.models.filter((m) => {
    if (f.where && m.where !== f.where) return false;
    if (f.capability && !(m.capabilities || []).includes(f.capability)) return false;
    if (q && !(m.model + " " + m.source + " " + (m.capabilities || []).join(" ")).toLowerCase().includes(q)) return false;
    return true;
  });
  const col = COLS.find((c) => c.id === state.sort.col) || COLS[0];
  out.sort((a, b) => {
    const x = col.get(a), y = col.get(b);
    const r = col.num ? x - y : String(x).localeCompare(String(y));
    return r * state.sort.dir;
  });
  return out;
}

function renderBoard(pane, bar) {
  const caps = [...new Set(state.models.flatMap((m) => m.capabilities || []))].sort();
  bar.replaceChildren(
    el("h2", { text: T("ui.nav.board") }),
    el("div", { class: "seg" },
      ...[["", "ui.filter.all"], ["local", "ui.filter.local"], ["cloud", "ui.filter.cloud"]].map(([v, k]) =>
        el("button", {
          type: "button",
          "aria-pressed": state.filters.where === v ? "true" : "false",
          text: T(k),
          onclick: () => { state.filters.where = v; render(); },
        }),
      ),
    ),
    el("select", {
      "aria-label": T("ui.board.col.caps"),
      onchange: (e) => { state.filters.capability = e.target.value; render(); },
    },
      el("option", { value: "", text: T("ui.filter.anyCapability"), selected: state.filters.capability === "" }),
      ...caps.map((c) => el("option", { value: c, text: c, selected: state.filters.capability === c })),
    ),
    el("input", {
      type: "search", placeholder: T("ui.filter.search"), value: state.filters.q,
      oninput: (e) => { state.filters.q = e.target.value; renderBoardBody(); },
    }),
    el("div", { class: "spacer" }),
    el("button", { class: "act", type: "button", text: T("ui.action.rescan"), onclick: rescan }),
  );

  if (!state.models.length) {
    pane.replaceChildren(emptyBoard());
    return;
  }
  const table = el("table", { class: "board" },
    el("thead", {}, el("tr", {},
      ...COLS.map((c) =>
        el("th", {
          class: c.right ? "r" : null,
          "data-sorted": state.sort.col === c.id ? (state.sort.dir === 1 ? "asc" : "desc") : null,
          text: T(c.key),
          onclick: () => {
            if (state.sort.col === c.id) state.sort.dir *= -1;
            else { state.sort.col = c.id; state.sort.dir = 1; }
            render();
          },
        }),
      ),
    )),
    el("tbody", { id: "board-body" }),
  );
  pane.replaceChildren(sourceStrip(), table);
  renderBoardBody();
}

function renderBoardBody() {
  const body = $("#board-body");
  if (!body) return;
  const rows = visibleModels();
  body.replaceChildren(
    ...rows.map((m) =>
      el("tr", {},
        el("td", { class: "mono", title: m.model, text: m.model }),
        el("td", { text: m.source }),
        el("td", {}, el("span", { class: "tag", "data-where": m.where, text: m.where })),
        el("td", {}, ...(m.capabilities || []).map((c) => el("span", { class: "tag", text: c }))),
        el("td", { class: "r num", text: m.context_length ? m.context_length.toLocaleString() : "—" }),
        el("td", { class: "r num", text: m.in_cost_per_mtok ? m.in_cost_per_mtok.toFixed(2) : "—" }),
      ),
    ),
  );
  if (!rows.length) {
    body.replaceChildren(el("tr", {}, el("td", { colspan: String(COLS.length) },
      el("div", { class: "state", text: T("ui.board.noMatch") }))));
  }
}

function sourceStrip() {
  return el("div", { class: "grid", style: "grid-template-columns: repeat(auto-fill, minmax(240px, 1fr))" },
    ...state.sources.map((s) =>
      el("div", { class: "card" },
        el("h3", {}, el("span", { class: "dot", "data-s": s.status }), s.name),
        el("div", { class: "meta" },
          el("span", { class: "tag", "data-where": s.where, text: s.where }), " ",
          el("span", { class: "tag", text: s.lane }), " ",
          el("span", { class: "mono", text: T("ui.source.modelCount", s.models) }),
        ),
        s.status === "failed" ? el("div", { class: "why", text: s.reason }) : null,
        el("div", { style: "margin-top:8px; display:flex; gap:6px" },
          el("button", { class: "act", type: "button", text: T("ui.action.refresh"),
            onclick: () => run(() => op("refresh_source", { id: s.id }), T("ui.action.refresh")) }),
          el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.action.remove"),
            onclick: () => confirmRemove(s) }),
        ),
      ),
    ),
  );
}

function emptyBoard() {
  if (state.detecting) {
    return el("div", { class: "state" },
      el("h3", { class: "pulse", text: T("ui.board.empty") }),
      el("p", { text: T("ui.board.emptyHint") }));
  }
  return el("div", { class: "state" },
    el("h3", { text: T("ui.board.emptyDone") }),
    el("p", { text: T("ui.board.emptyHint") }),
    el("button", { class: "act", "data-primary": true, type: "button",
      text: T("ui.nav.add"), onclick: () => go("add") }));
}

async function rescan() {
  state.detecting = true;
  render();
  await run(() => op("detect_local"), T("ui.action.rescan"));
  state.detecting = false;
  render();
}

function confirmRemove(s) {
  modal(T("ui.confirm.removeSource", s.name),
    el("p", { class: "note", text: T("ui.confirm.removeSourceBody") }),
    el("div", { style: "display:flex; gap:8px; margin-top:12px" },
      el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.action.remove"),
        onclick: async () => { closeModal(); await run(() => op("remove_source", { id: s.id }), T("ui.action.remove")); } }),
      el("button", { class: "act", type: "button", text: T("ui.action.cancel"), onclick: closeModal }),
    ));
}

/* ---------- aliases ---------- */
function renderAliases(pane, bar) {
  bar.replaceChildren(
    el("h2", { text: T("ui.nav.aliases") }),
    el("div", { class: "spacer" }),
    el("button", { class: "act", "data-primary": true, type: "button",
      text: T("ui.alias.new"), onclick: () => aliasEditor(null) }),
  );
  const cards = state.aliases.map((a) =>
    el("div", { class: "card" },
      el("h3", { text: a.name }),
      el("ol", { class: "ladder" },
        ...a.rungs.map((r, i) =>
          el("li", { "data-dead": !r.available || null, "data-first": r.available && i === firstLive(a) ? "" : null },
            el("span", { class: "rung", text: String(i + 1) }),
            el("span", { text: (r.source || r.source_id) + " / " + r.model }),
            r.available ? null : el("span", { class: "tag", text: r.reason || T("source.status.failed") }),
          ),
        ),
      ),
      el("div", { style: "margin-top:8px; display:flex; gap:6px" },
        el("button", { class: "act", type: "button", text: T("ui.action.edit"), onclick: () => aliasEditor(a) }),
        el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.action.remove"),
          onclick: () => run(() => op("remove_alias", { name: a.name }), T("ui.action.remove")) }),
      ),
    ),
  );
  const remaps = el("div", { class: "card" },
    el("h3", { text: T("ui.remap.title") }),
    el("p", { class: "note", text: T("ui.remap.body") }),
    ...state.remaps.map((r) =>
      el("div", { class: "mono", style: "font-size:11.5px; margin-top:4px" },
        r.from_model + " → " + r.target + "  ",
        el("button", { class: "act", type: "button", text: T("ui.action.remove"),
          onclick: () => run(() => op("remove_remap", { from: r.from_model }), T("ui.action.remove")) }),
      ),
    ),
    remapForm(),
  );
  pane.replaceChildren(
    state.aliases.length
      ? el("div", { class: "grid", style: "grid-template-columns: repeat(auto-fill, minmax(300px, 1fr))" }, ...cards)
      : el("div", { class: "state" },
          el("h3", { text: T("ui.alias.emptyTitle") }),
          el("p", { text: T("ui.alias.emptyBody") })),
    el("div", { class: "grid", style: "margin-top:1px" }, remaps),
  );
}

const firstLive = (a) => a.rungs.findIndex((r) => r.available);

function remapForm() {
  const from = el("input", { type: "text", placeholder: "gpt-4o" });
  const to = el("select", {}, ...routeOptions());
  return el("form", { class: "row", style: "margin-top:10px",
    onsubmit: (e) => { e.preventDefault();
      run(() => op("set_remap", { from: from.value, to: to.value }), T("ui.remap.title")); } },
    el("label", { class: "field" }, T("ui.remap.from"), from),
    el("label", { class: "field" }, T("ui.remap.to"), to),
    el("button", { class: "act", "data-primary": true, type: "submit", text: T("ui.action.save") }),
  );
}

function routeOptions() {
  const opts = state.aliases.map((a) => el("option", { value: a.name, text: a.name + " (alias)" }));
  for (const m of state.models) {
    const sid = (state.sources.find((s) => s.name === m.source) || {}).id;
    if (sid) opts.push(el("option", { value: sid + "/" + m.model, text: m.source + " / " + m.model }));
  }
  return opts;
}

function aliasEditor(existing) {
  const name = el("input", { type: "text", value: existing ? existing.name : "",
    placeholder: "fast", readonly: existing ? true : null });
  const rows = el("div", {});
  const ladder = existing ? existing.rungs.map((r) => r.source_id + "/" + r.model) : [""];
  const draw = () => {
    rows.replaceChildren(
      ...ladder.map((v, i) =>
        el("div", { class: "row", style: "display:flex; gap:6px; align-items:center; margin-top:5px" },
          el("span", { class: "mono", style: "color:var(--ink-30); width:16px", text: String(i + 1) }),
          el("select", { onchange: (e) => { ladder[i] = e.target.value; } },
            el("option", { value: "", text: "—" }),
            ...routeOptions().filter((o) => o.value.includes("/")).map((o) => {
              if (o.value === v) o.selected = true;
              return o;
            })),
          el("button", { class: "act", "data-danger": true, type: "button", text: "−",
            onclick: () => { ladder.splice(i, 1); draw(); } }),
        ),
      ),
      el("button", { class: "act", type: "button", text: T("ui.alias.addRung"),
        style: "margin-top:6px", onclick: () => { ladder.push(""); draw(); } }),
    );
  };
  draw();
  modal(existing ? T("ui.alias.edit", existing.name) : T("ui.alias.new"),
    el("label", { class: "field" }, T("ui.alias.name"), name,
      el("span", { class: "hint", text: T("ui.alias.nameHint") })),
    el("div", { style: "margin-top:10px" }, el("div", { class: "note", text: T("ui.alias.ladderHint") }), rows),
    el("div", { style: "display:flex; gap:8px; margin-top:14px" },
      el("button", { class: "act", "data-primary": true, type: "button", text: T("ui.action.save"),
        onclick: async () => {
          const rungs = ladder.filter(Boolean);
          if (!name.value.trim() || !rungs.length) { toast(T("alias.empty"), "error"); return; }
          closeModal();
          await run(() => op("set_alias", { name: name.value.trim(), ladder: rungs }), T("ui.action.save"));
        } }),
      el("button", { class: "act", type: "button", text: T("ui.action.cancel"), onclick: closeModal }),
    ));
}

/* ---------- add a source ---------- */
function renderAdd(pane, bar) {
  bar.replaceChildren(el("h2", { text: T("ui.nav.add") }));
  const providers = state.providers || [];
  const sel = el("select", {}, ...providers.map((p) =>
    el("option", { value: p.id, text: p.label + (p.where === "local" ? " · " + T("ui.filter.local") : "") })));
  const nameIn = el("input", { type: "text", placeholder: T("ui.add.namePlaceholder") });
  const keyIn = el("input", { type: "password", placeholder: "—", autocomplete: "off", spellcheck: "false" });
  const baseIn = el("input", { type: "text", placeholder: "https://…/v1" });
  const out = el("div", {});

  const sync = () => {
    const p = providers.find((x) => x.id === sel.value) || {};
    keyIn.placeholder = p.key_hint || "—";
    keyIn.disabled = !p.needs_key && p.where === "local";
    baseIn.value = baseIn.dataset.touched ? baseIn.value : (p.default_base_url || "");
    baseIn.required = !!p.needs_base_url;
    nameIn.placeholder = p.id || "";
  };
  sel.addEventListener("change", sync);
  baseIn.addEventListener("input", () => { baseIn.dataset.touched = "1"; });
  sync();

  pane.replaceChildren(el("div", { class: "pad" },
    el("p", { class: "note", text: T("ui.add.body") }),
    el("form", { class: "row", style: "margin-top:14px",
      onsubmit: async (e) => {
        e.preventDefault();
        out.replaceChildren(el("div", { class: "state pulse", text: T("ui.add.testing") }));
        try {
          const r = await op("add_source", {
            provider: sel.value, name: nameIn.value, base_url: baseIn.value, key: keyIn.value,
          });
          keyIn.value = "";
          if (r.live) {
            out.replaceChildren(el("div", { class: "state" },
              el("h3", { text: T("source.added", r.source.name, r.source.provider, T("source.status.live"), r.models) })));
            await refresh();
          } else {
            out.replaceChildren(el("div", { class: "state" },
              el("h3", { text: T("source.failed", r.source.name, "") }),
              el("div", { class: "why", text: r.reason }),
              el("p", { class: "note", text: T("ui.add.failHint") })));
            await refresh();
          }
        } catch (err) {
          out.replaceChildren(el("div", { class: "state" }, el("div", { class: "why", text: err.message })));
        }
      } },
      el("label", { class: "field" }, T("ui.add.provider"), sel),
      el("label", { class: "field" }, T("ui.add.name"), nameIn),
      el("label", { class: "field" }, T("ui.add.key"), keyIn),
      el("label", { class: "field", style: "flex:1; min-width:260px" }, T("ui.add.baseUrl"), baseIn),
      el("button", { class: "act", "data-primary": true, type: "submit", text: T("ui.add.submit") }),
    ),
    out,
    el("div", { class: "note", style: "margin-top:22px" },
      el("strong", { text: T("ui.add.detectTitle") }), " ", T("ui.add.detectBody"), " ",
      el("button", { class: "act", type: "button", text: T("ui.action.rescan"), onclick: rescan }),
    ),
  ));
}

/* ---------- usage + egress ---------- */
function renderUsage(pane, bar) {
  bar.replaceChildren(el("h2", { text: T("ui.nav.usage") }),
    el("div", { class: "spacer" }),
    el("button", { class: "act", type: "button", text: T("ui.action.refresh"),
      onclick: () => run(async () => { await loadUsage(); }, T("ui.action.refresh")) }));

  if (!state.usage) { pane.replaceChildren(el("div", { class: "state pulse", text: T("ui.usage.loading") })); return; }
  const u = state.usage, e = state.egress;
  if (!u.buckets || !u.buckets.length) {
    pane.replaceChildren(el("div", { class: "state" }, el("h3", { text: T("usage.empty") }),
      el("p", { text: T("ui.usage.emptyHint") })));
    return;
  }

  const total = (e.local.requests || 0) + (e.cloud.requests || 0);
  const cloudPct = total ? Math.round((e.cloud.requests / total) * 100) : 0;
  const egressCard = el("div", { class: "card" },
    el("h3", { text: T("ui.egress.title") }),
    el("p", { class: "note", text: T("ui.egress.body") }),
    el("div", { class: "meter", "data-cloud": cloudPct > 50 ? "" : null, style: "margin:10px 0 6px" },
      el("i", { style: "width:" + (100 - cloudPct) + "%" })),
    el("dl", { class: "kv" },
      el("dt", { text: T("usage.egress.local") }),
      el("dd", { text: T("ui.egress.line", e.local.requests || 0, fmtBytes((e.local.req_bytes || 0) + (e.local.resp_bytes || 0))) }),
      el("dt", { text: T("usage.egress.cloud") }),
      el("dd", { text: T("ui.egress.line", e.cloud.requests || 0, fmtBytes((e.cloud.req_bytes || 0) + (e.cloud.resp_bytes || 0))) }),
      el("dt", { text: T("ui.egress.offShare") }),
      el("dd", { text: cloudPct + "%" }),
    ),
  );

  const detail = el("table", { class: "board" },
    el("thead", {}, el("tr", {},
      el("th", { text: T("ui.usage.col.app") }),
      el("th", { text: T("ui.board.col.model") }),
      el("th", { class: "r", text: T("ui.usage.col.requests") }),
      el("th", { class: "r", text: T("ui.usage.col.errors") }),
      el("th", { class: "r", text: T("ui.usage.col.tokens") }),
      el("th", { class: "r", text: T("ui.usage.col.latency") }),
      el("th", { class: "r", text: T("ui.usage.col.cost") }),
    )),
    el("tbody", {},
      ...u.buckets.map((b) =>
        el("tr", {},
          el("td", { text: b.app || "—" }),
          el("td", { class: "mono", text: b.model_id || "—" }),
          el("td", { class: "r num", text: String(b.requests) }),
          el("td", { class: "r num", style: b.errors ? "color:var(--error)" : null, text: String(b.errors) }),
          el("td", { class: "r num", text: (b.prompt_tokens + b.completion_tokens).toLocaleString() }),
          el("td", { class: "r num", text: b.avg_latency_ms + " ms" }),
          el("td", { class: "r num", text: "$" + b.cost.toFixed(4) }),
        ),
      ),
      el("tr", {},
        el("td", { text: T("ui.usage.total") }), el("td", {}),
        el("td", { class: "r num", text: String(u.total.requests) }),
        el("td", { class: "r num", text: String(u.total.errors) }),
        el("td", { class: "r num", text: (u.total.prompt_tokens + u.total.completion_tokens).toLocaleString() }),
        el("td", {}),
        el("td", { class: "r num", text: "$" + u.total.cost.toFixed(4) }),
      ),
    ),
  );
  pane.replaceChildren(el("div", { class: "grid" }, egressCard), detail);
}

function fmtBytes(n) {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : String(n)) + " " + u[i];
}

/* ---------- grants ---------- */
function renderGrants(pane, bar) {
  bar.replaceChildren(el("h2", { text: T("ui.nav.grants") }),
    el("div", { class: "spacer" }),
    el("button", { class: "act", "data-primary": true, type: "button", text: T("ui.grants.mint"), onclick: mintDialog }));
  pane.replaceChildren(
    el("div", { class: "pad" }, el("p", { class: "note", text: T("ui.grants.body") })),
    state.grants.length
      ? el("table", { class: "board" },
          el("thead", {}, el("tr", {},
            el("th", { text: T("ui.grants.col.app") }),
            el("th", { text: T("ui.grants.col.id") }),
            el("th", { class: "r", text: T("ui.usage.col.requests") }),
            el("th", { class: "r", text: T("ui.usage.col.cost") }),
            el("th", { text: T("ui.board.col.status") }),
            el("th", {}),
          )),
          el("tbody", {}, ...state.grants.map((g) =>
            el("tr", {},
              el("td", { text: g.app }),
              el("td", { class: "mono", text: g.id }),
              el("td", { class: "r num", text: String(g.requests || 0) }),
              el("td", { class: "r num", text: "$" + (g.cost || 0).toFixed(4) }),
              el("td", {}, el("span", { class: "tag", text: g.revoked ? T("ui.grants.revoked") : T("source.status.live") })),
              el("td", { class: "r" }, g.revoked ? null :
                el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.grants.revoke"),
                  onclick: () => run(() => op("revoke_grant", { id: g.id }), T("ui.grants.revoke")) })),
            ))),
        )
      : el("div", { class: "state" }, el("h3", { text: T("ui.grants.emptyTitle") }),
          el("p", { text: T("ui.grants.emptyBody") })),
  );
}

function mintDialog() {
  const appIn = el("input", { type: "text", placeholder: "my-script" });
  modal(T("ui.grants.mint"),
    el("label", { class: "field" }, T("ui.grants.col.app"), appIn),
    el("div", { style: "display:flex; gap:8px; margin-top:14px" },
      el("button", { class: "act", "data-primary": true, type: "button", text: T("ui.grants.mint"),
        onclick: async () => {
          try {
            const r = await op("mint_grant", { app: appIn.value.trim() || "app" });
            const base = location.origin + "/v1";
            $("#modal-body").replaceChildren(
              el("h3", { text: T("ui.grants.shownOnce") }),
              el("pre", { class: "mono",
                style: "background:var(--fill); padding:10px; border-radius:3px; overflow:auto; user-select:all",
                text: r.token }),
              el("p", { class: "note", text: T("grant.minted", r.grant.app, "", location.host).split("\n").pop() }),
              el("pre", { class: "mono", style: "background:var(--fill); padding:10px; border-radius:3px; overflow:auto",
                text: "OPENAI_BASE_URL=" + base + "\nOPENAI_API_KEY=" + r.token }),
              el("button", { class: "act", type: "button", text: T("ui.action.close"),
                onclick: async () => { closeModal(); await refresh(); } }),
            );
          } catch (err) { toast(err.message, "error"); }
        } }),
      el("button", { class: "act", type: "button", text: T("ui.action.cancel"), onclick: closeModal }),
    ));
}

/* ---------- staged agent ops ---------- */
function renderStaged(pane, bar) {
  bar.replaceChildren(el("h2", { text: T("ui.nav.staged") }));
  pane.replaceChildren(
    el("div", { class: "pad" }, el("p", { class: "note", text: T("ui.staged.body") })),
    el("div", { class: "grid" }, ...state.staged.map((s) => {
      const args = JSON.parse(s.payload || "{}");
      const extra = el("input", { type: "password", placeholder: T("ui.staged.secretPlaceholder"), autocomplete: "off" });
      const needsKey = s.op === "add_source";
      return el("div", { class: "card" },
        el("h3", { text: s.op }),
        el("div", { class: "meta mono", text: JSON.stringify(args) }),
        el("div", { class: "meta", text: T("ui.staged.from", s.door, s.caller || "—") }),
        needsKey ? el("label", { class: "field", style: "margin-top:8px" }, T("ui.add.key"), extra) : null,
        el("div", { style: "margin-top:8px; display:flex; gap:6px" },
          el("button", { class: "act", "data-primary": true, type: "button", text: T("ui.staged.apply"),
            onclick: async () => {
              const body = needsKey && extra.value ? { key: extra.value } : {};
              await run(async () => {
                const r = await fetch("/api/staged/" + s.id + "/apply", {
                  method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
                const j = await r.json();
                if (!r.ok || j.error) throw new Error(j.error || r.statusText);
              }, T("ui.staged.apply"));
            } }),
          el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.staged.discard"),
            onclick: () => run(async () => {
              const r = await fetch("/api/staged/" + s.id + "/discard", { method: "POST" });
              if (!r.ok) throw new Error(r.statusText);
            }, T("ui.staged.discard")) }),
        ));
    })),
  );
}

/* ---------- shell ---------- */
async function run(fn, label) {
  try {
    await fn();
    await refresh();
    toast(T("ui.toast.done", label));
  } catch (err) {
    toast(err.message, "error");
  }
}

async function refresh() {
  await loadAll();
  if (state.view === "usage") await loadUsage();
  render();
}

function render() {
  renderNav();
  const bar = $("#bar");
  for (const p of document.querySelectorAll(".pane")) delete p.dataset.active;
  const map = {
    board: [$("#pane-board"), renderBoard],
    aliases: [$("#pane-aliases"), renderAliases],
    add: [$("#pane-add"), renderAdd],
    usage: [$("#pane-usage"), renderUsage],
    grants: [$("#pane-grants"), renderGrants],
    staged: [$("#pane-grants"), renderStaged],
  };
  const [pane, fn] = map[state.view] || map.board;
  pane.dataset.active = "";
  fn(pane, bar);
}

$("#foot-sov").addEventListener("click", () =>
  modal(T("ui.sovereignty.title"),
    el("p", { class: "note", text: T("app.sovereignty") }),
    el("button", { class: "act", type: "button", text: T("ui.action.close"), onclick: closeModal })));

document.addEventListener("keydown", (e) => {
  if (e.key === "?" && !/^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName)) {
    modal(T("ui.help.title"),
      el("dl", { class: "kv" },
        ...VIEWS.map(([id, key], i) => [el("dt", { text: String(i + 1) }), el("dd", { text: T(key) })]).flat(),
        el("dt", { text: "/" }), el("dd", { text: T("ui.filter.search") }),
      ),
      el("p", { class: "note", style: "margin-top:12px", text: T("ui.help.body") }),
      el("button", { class: "act", type: "button", text: T("ui.action.close"), onclick: closeModal }));
  }
  const n = parseInt(e.key, 10);
  if (n >= 1 && n <= VIEWS.length && !/^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName)) {
    go(VIEWS[n - 1][0]);
  }
});

(async function boot() {
  S = await (await fetch("/ui/strings.json")).json();
  state.providers = (await (await fetch("/ui/providers.json")).json()).providers;
  render();                        // paints the rail and the detecting state immediately
  await loadAll();
  render();
  // Detection runs at daemon start; a fresh board may still be filling in.
  state.detecting = state.models.length === 0;
  render();
  if (state.detecting) {
    setTimeout(async () => { await loadAll(); state.detecting = false; render(); }, 1500);
  }
})();

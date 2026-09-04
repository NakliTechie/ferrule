/* Ferrule control surface. No framework, no build step, no network beyond this daemon.
   Every user-facing string comes from the server's string table — nothing is written
   into this file in English. */

let S = {};
// The string table is written for Go's fmt and read by both sides, so this has to know
// every verb the table actually uses. It did not know %q: an unmatched verb is not a
// visible error, it silently shifts every later argument by one and drops the last, which
// is how the token dialog came to tell people to point their app at "http:///v1".
// TestThePanelUnderstandsEveryVerbInTheTable is what keeps the two in step.
const T = (k, ...a) => {
  let s = S[k];
  if (s === undefined) return "⟨" + k + "⟩";
  let i = 0;
  return s.replace(/%[sdqv]|%\.\d+f/g, (verb) => {
    const v = a[i++];
    if (v === undefined) return "";
    return verb === "%q" ? '"' + String(v) + '"' : String(v);
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

// The run's control token, handed to this page by the daemon that served it. Every
// control call carries it; a page on another origin cannot read this one, so it cannot
// obtain the token.
const CONTROL = (document.querySelector('meta[name="ferrule-control"]') || {}).content || "";

function controlHeaders(extra = {}) {
  return {
    "Content-Type": "application/json",
    "X-Ferrule-Caller": "panel",
    "X-Ferrule-Control": CONTROL,
    ...extra,
  };
}

async function op(name, args = {}) {
  const r = await fetch("/api/op/" + name, {
    method: "POST",
    headers: controlHeaders(),
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
  lastAdd: null,
  addProvider: "",
  status: null,
  detecting: true,
  // Simple is the default because the person this runs for is setting up their
  // household, not auditing a router. Advanced is every screen that existed before.
  mode: readMode(),
  filters: { where: "", capability: "", q: "", maxCost: 0 },
  sort: { col: "model", dir: 1 },
};

function readMode() {
  try {
    return localStorage.getItem("ferrule.mode") === "advanced" ? "advanced" : "simple";
  } catch { return "simple"; }
}

function setMode(m) {
  state.mode = m;
  try { localStorage.setItem("ferrule.mode", m); } catch { /* private window; the session still works */ }
  if (m === "simple") state.view = "home";
  else if (state.view === "home") state.view = "board";
  render();
}

const VIEWS = [
  ["board", "ui.nav.board", () => state.models.length],
  ["aliases", "ui.nav.aliases", () => state.aliases.length],
  ["add", "ui.nav.add", () => null],
  ["usage", "ui.nav.usage", () => null],
  ["grants", "ui.nav.grants", () => state.grants.filter((g) => !g.revoked).length],
];

function renderNav() {
  const foot = $("#foot-mode");
  if (foot) {
    foot.textContent = state.mode === "simple" ? T("ui.mode.toAdvanced") : T("ui.mode.toSimple");
    foot.onclick = () => setMode(state.mode === "simple" ? "advanced" : "simple");
  }
  if (state.mode === "simple") {
    $("#nav").replaceChildren();
    return;
  }
  const items = [
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
  ].filter(Boolean);
  $("#nav").replaceChildren(...items);
}

async function go(view) {
  state.view = view;
  render();
  // The ledger is only read when the person asks to see it, and the pane paints its
  // loading state first rather than blocking the switch.
  if (view === "usage") {
    try { await loadUsage(); } catch (err) { toast(err.message, "error"); }
    render();
  }
}

/* ---------- data ---------- */
async function loadAll() {
  const [st, sources, models, aliases, remaps, grants, staged] = await Promise.all([
    op("status"), op("list_sources"), op("list_models"), op("list_aliases"),
    op("list_remaps"), op("list_grants"), op("list_staged"),
  ]);
  state.status = st;
  state.detecting = !!st.scanning;
  state.sources = sources.sources || [];
  state.models = models.models || [];
  state.catalogDate = models.catalog_date;
  state.aliases = aliases.aliases || [];
  state.remaps = remaps.remaps || [];
  state.grants = grants.grants || [];
  state.staged = staged.staged || [];
  $("#foot-vault").textContent = st.vault;
  $("#foot-catalog").textContent = st.catalog_date || "—";
  const lanRow = $("#foot-lan");
  if (st.lan_endpoint) {
    lanRow.hidden = false;
    $("#foot-lan-value").textContent = st.lan_endpoint;
  } else {
    lanRow.hidden = true;
  }
  const dir = $("#foot-dir");
  dir.textContent = st.config_dir.replace(/^\/Users\/[^/]+/, "~");
  dir.title = st.config_dir;
}

async function loadUsage() {
  const [u, e, f] = await Promise.all([
    op("usage_summary", { by: ["app", "model"], since_hours: 0 }),
    op("egress_summary", { since_hours: 0 }),
    op("recent_errors", { limit: 20 }),
  ]);
  state.usage = u;
  state.egress = e;
  state.failures = (f && f.errors) || [];
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
    // A cap answers "what can I run without thinking about it". A model with no price is
    // not known to be under the cap, so it is not shown as though it were — the row count
    // below says how many were held back for that reason rather than hiding the fact.
    if (f.maxCost && !(m.in_cost_per_mtok > 0 && m.in_cost_per_mtok <= f.maxCost)) return false;
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
    el("input", {
      type: "number", class: "cap", min: "0", step: "0.25", inputmode: "decimal",
      "aria-label": T("ui.filter.maxCost"), placeholder: T("ui.filter.maxCostPlaceholder"),
      value: state.filters.maxCost || "",
      oninput: (e) => {
        state.filters.maxCost = parseFloat(e.target.value) || 0;
        renderBoardBody();
      },
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
  pane.replaceChildren(sourceStrip(), table,
    el("p", { id: "board-note", class: "note pad", hidden: true }));
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
  const note = $("#board-note");
  if (note) {
    // A price cap silently drops every model the catalog has no price for, and on a
    // provider the catalog barely covers that is most of them. Say the number.
    const unpriced = state.filters.maxCost
      ? state.models.filter((m) => !(m.in_cost_per_mtok > 0)).length : 0;
    note.textContent = unpriced ? T("ui.board.unpriced", unpriced) : "";
    note.hidden = !unpriced;
  }
}

function sourceStrip() {
  return el("div", { class: "grid cards" },
    ...state.sources.map((s) =>
      el("div", { class: "card" },
        el("h3", {}, el("span", { class: "dot", "data-s": s.status }), s.name),
        el("div", { class: "meta" },
          el("span", { class: "tag", "data-where": s.where, text: s.where }), " ",
          el("span", { class: "tag", text: s.lane }),
          s.insecure ? el("span", { class: "tag", "data-warn": "", text: T("ui.source.insecure") }) : null,
          // A failed source is not routable, so its last-known model count would only
          // contradict the board below it.
          s.status === "live"
            ? el("span", {
                text: " " + (s.models === 1
                  ? T("ui.source.modelCountOne")
                  : T("ui.source.modelCount", s.models)),
              })
            : null,
        ),
        // The remedy leads. It is the half that says what to do, and it was rendered
        // under a wall of the provider's raw JSON — a real DeepSeek refusal is 180
        // characters of it, which made the useful sentence the one you had to scroll to.
        // Nothing is hidden: the provider's own words are one click away, verbatim.
        s.status === "failed" && s.remedy ? el("div", { class: "why", text: s.remedy }) : null,
        s.status === "failed" ? el("details", { class: "verbatim" },
          el("summary", { text: T("ui.source.whatProviderSaid") }),
          el("div", { class: "note", text: s.reason })) : null,
        el("div", { class: "actions" },
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
      el("p", { text: T("ui.board.detectingBody") }),
      el("p", { class: "note", text: T("ui.board.emptyHint") }));
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
    el("div", { class: "actions-lg" },
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
      el("div", { class: "actions" },
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
      el("div", { class: "mono remap-line" },
        r.from_model + " → " + r.target + "  ",
        el("button", { class: "act", type: "button", text: T("ui.action.remove"),
          onclick: () => run(() => op("remove_remap", { from: r.from_model }), T("ui.action.remove")) }),
      ),
    ),
    remapForm(),
  );
  pane.replaceChildren(
    state.aliases.length
      ? el("div", { class: "grid cards-wide" }, ...cards)
      : el("div", { class: "state" },
          el("h3", { text: T("ui.alias.emptyTitle") }),
          el("p", { text: T("ui.alias.emptyBody") })),
    el("div", { class: "grid stack" }, remaps),
  );
}

const firstLive = (a) => a.rungs.findIndex((r) => r.available);

function remapForm() {
  const from = el("input", { type: "text", placeholder: "gpt-4o" });
  const to = el("select", {}, ...routeOptions());
  return el("form", { class: "row mt10",
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
  // "Alias", "rung" and "ladder" are the right words for what this is and the wrong words
  // for the person setting up their house. Same dialog, same op, different reader.
  const simple = state.mode === "simple";
  const name = el("input", { type: "text", value: existing ? existing.name : "",
    placeholder: simple ? "everyday" : "fast", readonly: existing ? true : null });
  const rows = el("div", {});
  const ladder = existing ? existing.rungs.map((r) => r.source_id + "/" + r.model) : [""];
  const draw = () => {
    rows.replaceChildren(
      ...ladder.map((v, i) =>
        el("div", { class: "rung-row" },
          el("span", { class: "rung", text: String(i + 1) }),
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
      el("button", { class: "act mt6", type: "button",
        text: simple ? T("ui.home.addBackup") : T("ui.alias.addRung"),
        onclick: () => { ladder.push(""); draw(); } }),
    );
  };
  draw();
  modal(existing
      ? (simple ? T("ui.home.changeChoice", existing.name) : T("ui.alias.edit", existing.name))
      : (simple ? T("ui.home.addChoice") : T("ui.alias.new")),
    el("label", { class: "field" }, simple ? T("ui.home.choiceName") : T("ui.alias.name"), name,
      el("span", { class: "hint", text: simple ? T("ui.home.choiceNameHint") : T("ui.alias.nameHint") })),
    el("div", { class: "mt10" },
      el("div", { class: "note", text: simple ? T("ui.home.choiceBody") : T("ui.alias.ladderHint") }), rows),
    el("div", { class: "actions-lg" },
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
// buildAddForm is the provider form itself, used by the Advanced pane and by the Simple
// view's dialog. Two placements, one form: the fields, the validation and the submit
// path cannot drift apart, which is exactly how `test_model` came to exist in the CLI
// and nowhere a person could reach it.
//
// onDone runs after a successful add, so a dialog can close itself and a pane need not.
function buildAddForm(onDone) {
  const providers = state.providers || [];
  if (!state.addProvider && providers.length) state.addProvider = providers[0].id;
  const sel = el("select", {}, ...providers.map((p) =>
    el("option", {
      value: p.id, selected: p.id === state.addProvider,
      text: p.label + (p.where === "local" ? " · " + T("ui.filter.local") : ""),
    })));
  const nameIn = el("input", { type: "text", placeholder: T("ui.add.namePlaceholder") });
  const keyIn = el("input", { type: "password", placeholder: "—", autocomplete: "off", spellcheck: "false" });
  const baseIn = el("input", { type: "text", placeholder: "https://…/v1" });
  const testIn = el("input", { type: "text", placeholder: T("ui.add.testModelPlaceholder"),
    autocomplete: "off", spellcheck: "false" });
  const out = el("div", {}, addResult());

  const sync = () => {
    const p = providers.find((x) => x.id === sel.value) || {};
    keyIn.placeholder = p.key_hint || "—";
    keyIn.disabled = !p.needs_key && p.where === "local";
    baseIn.value = baseIn.dataset.touched ? baseIn.value : (p.default_base_url || "");
    baseIn.required = !!p.needs_base_url;
    nameIn.placeholder = p.id || "";
  };
  sel.addEventListener("change", () => {
    // Clear the previous result in place. A re-render here would rebuild the form and
    // throw away the choice that just triggered it.
    state.addProvider = sel.value;
    state.lastAdd = null;
    out.replaceChildren();
    // The base URL does not follow you to the next provider. It used to: type a host for
    // an OpenAI-compatible endpoint, switch the picker to Anthropic, paste the Anthropic
    // key — and the key went to the host you typed, which is a working credential handed
    // to somebody else's server. Choosing a provider chooses its endpoint; overriding it
    // again is one keystroke and is now a deliberate act every time.
    delete baseIn.dataset.touched;
    sync();
  });
  baseIn.addEventListener("input", () => { baseIn.dataset.touched = "1"; });
  sync();

  const form = el("form", { class: "row mt10",
    onsubmit: async (e) => {
      e.preventDefault();
      state.lastAdd = null;
      out.replaceChildren(el("div", { class: "state pulse", text: T("ui.add.testing") }));
      try {
        const r = await op("add_source", {
          provider: sel.value, name: nameIn.value, base_url: baseIn.value, key: keyIn.value,
          test_model: testIn.value.trim(),
        });
        keyIn.value = "";
        // The result is held in state, not in this node: the refresh below rebuilds
        // the pane, and a loud failure that vanished on re-render would be no failure
        // at all.
        state.lastAdd = r;
        await refresh();
        if (onDone) onDone(r);
      } catch (err) {
        state.lastAdd = { error: err.message };
        out.replaceChildren(addResult());
      }
    } },
    el("label", { class: "field" }, T("ui.add.provider"), sel),
    el("label", { class: "field" }, T("ui.add.name"), nameIn),
    el("label", { class: "field" }, T("ui.add.key"), keyIn),
    el("label", { class: "field" }, T("ui.add.testModel"), testIn),
    el("label", { class: "field grow" }, T("ui.add.baseUrl"), baseIn),
    el("button", { class: "act", "data-primary": true, type: "submit", text: T("ui.add.submit") }),
  );
  return { form, out };
}

function renderAdd(pane, bar) {
  bar.replaceChildren(el("h2", { text: T("ui.nav.add") }));
  const { form, out } = buildAddForm(null);
  pane.replaceChildren(el("div", { class: "pad" },
    el("p", { class: "note", text: T("ui.add.body") }),
    form,
    el("p", { class: "note mt10", text: T("ui.add.testModelHint") }),
    out,
    el("div", { class: "note mt22" },
      el("strong", { text: T("ui.add.detectTitle") }), " ", T("ui.add.detectBody"), " ",
      el("button", { class: "act", type: "button", text: T("ui.action.rescan"), onclick: rescan }),
    ),
  ));
}

// addProviderDialog is the Simple view's door to the same form. It closes itself on a
// success and stays open on a failure, so the reason is read where it was caused.
function addProviderDialog() {
  state.lastAdd = null;
  const { form, out } = buildAddForm((r) => { if (r.live) closeModal(); });
  modal(T("ui.home.addSource"),
    el("p", { class: "note", text: T("ui.add.body") }),
    form,
    el("p", { class: "note mt10", text: T("ui.add.testModelHint") }),
    out,
  );
}

// addResult renders the outcome of the last add. It reads from state so it survives the
// re-render that follows a source change.
function addResult() {
  const r = state.lastAdd;
  if (!r) return null;
  if (r.error) {
    return el("div", { class: "state" }, el("div", { class: "why", text: r.error }));
  }
  if (r.live) {
    return el("div", { class: "state" }, el("h3", {
      text: T("source.added", r.source.name, r.source.provider, T("source.status.live"), r.models),
    }));
  }
  // r.reason is a typed {code, message, remedy}; rendering the object would print
  // "[object Object]" where the reason belongs.
  const reason = r.reason || {};
  return el("div", { class: "state" },
    el("h3", { text: T("source.failed", r.source.name, "") }),
    el("div", { class: "why", text: reason.message || "" }),
    reason.remedy ? el("p", { class: "note", text: reason.remedy }) : null,
    el("p", { class: "note", text: r.kept
      ? T("ui.add.keptHint", r.source.name)
      : T("ui.add.failHint") }));
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
    // The filled portion is what stayed on the machine, so the accessible name says
    // that. Labelling it "off-machine share" while filling it with the local share
    // misreports the one number this view exists for, to exactly the readers who cannot
    // see the figures printed underneath.
    el("progress", { class: "meter", "data-cloud": cloudPct > 50 ? "" : null,
      value: String(100 - cloudPct), max: "100",
      "aria-label": T("ui.egress.meterLabel", 100 - cloudPct) }),
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
          el("td", { class: b.errors ? "r num err" : "r num", text: String(b.errors) }),
          el("td", { class: "r num", text: (b.prompt_tokens + b.completion_tokens).toLocaleString() }),
          el("td", { class: "r num", text: fmtLatency(b.avg_latency_ms, b.requests) }),
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
  pane.replaceChildren(el("div", { class: "grid" }, egressCard), detail, failuresCard());
}

// failuresCard is where the reason a request failed finally shows up. The ledger has
// recorded it since the ledger existed and nothing displayed it, so the only copy a
// person could read was whatever their app logged.
function failuresCard() {
  const rows = state.failures || [];
  const card = el("div", { class: "pad mt22" },
    el("h3", { text: T("ui.usage.errors") }),
    el("p", { class: "note", text: T("ui.usage.errorsBody") }));
  if (!rows.length) {
    card.append(el("p", { class: "note empty", text: T("ui.usage.noErrors") }));
    return card;
  }
  card.append(el("table", { class: "board mt10" },
    el("thead", {}, el("tr", {},
      el("th", { text: T("ui.usage.col.when") }),
      el("th", { text: T("ui.usage.col.app") }),
      el("th", { text: T("ui.board.col.model") }),
      el("th", { class: "r", text: T("ui.usage.col.status") }),
      el("th", { text: T("ui.usage.col.reason") }))),
    el("tbody", {}, ...rows.map((f) =>
      el("tr", {},
        el("td", { class: "num", text: when(f.ts) }),
        el("td", { text: f.app || "—" }),
        el("td", { class: "mono", text: f.model || "—" }),
        el("td", { class: "r num", text: f.status ? String(f.status) : "—" }),
        el("td", { class: "why-cell", text: f.err || "" }))))));
  return card;
}

// when renders a ledger timestamp as something a person reads at a glance. The ledger
// stores milliseconds and always has, so there is no unit to guess at.
function when(ts) {
  if (!ts) return "—";
  const d = new Date(ts);
  const mins = Math.round((Date.now() - d.getTime()) / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return mins + "m ago";
  if (mins < 1440) return Math.round(mins / 60) + "h ago";
  return d.toLocaleDateString();
}

// A sub-millisecond average is real, not missing. Saying "0 ms" reads as a broken
// column; saying "<1 ms" is the same fact, legibly.
function fmtLatency(ms, requests) {
  if (!requests) return "—";
  if (ms === 0) return "<1 ms";
  return ms + " ms";
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
  // One op, two audiences. Advanced is minting an app token; Simple is giving someone in
  // the house their own way in. The words differ because the reader does.
  const simple = state.mode === "simple";
  const title = simple ? T("ui.home.addPerson") : T("ui.grants.mint");
  const appIn = el("input", { type: "text", placeholder: simple ? T("ui.home.personPlaceholder") : "my-script" });
  modal(title,
    simple ? el("p", { class: "note", text: T("ui.home.addPersonBody") }) : null,
    el("label", { class: "field" }, simple ? T("ui.home.personName") : T("ui.grants.col.app"), appIn),
    el("div", { class: "actions-lg" },
      el("button", { class: "act", "data-primary": true, type: "button", text: title,
        onclick: async () => {
          try {
            const r = await op("mint_grant", { app: appIn.value.trim() || "app" });
            const lan = state.status && state.status.lan_endpoint;
            const here = location.origin + "/v1";
            const kids = [
              el("h3", { text: simple ? T("ui.home.keyShownOnce") : T("ui.grants.shownOnce") }),
              el("pre", { class: "mono code", text: r.token }),
              el("p", { class: "note", text: simple
                ? T("ui.home.keyBody")
                : T("grant.minted", r.grant.app, "", location.host).split("\n").pop() }),
            ];
            if (lan) {
              // A token minted for someone else needs the address that works from their
              // machine. localhost is this one.
              kids.push(
                // Simple mode already said what these two lines are for; a second
                // explanation of the same thing reads as a warning about something else.
                simple ? null : el("p", { class: "note", text: T("ui.grants.lanHint") }),
                el("p", { class: "note", text: T("ui.grants.forOthers") }),
                el("pre", { class: "mono code",
                  text: "OPENAI_BASE_URL=http://" + lan + "/v1\nOPENAI_API_KEY=" + r.token }),
                el("p", { class: "note", text: T("ui.grants.forThisMachine") }),
              );
            }
            kids.push(
              el("pre", { class: "mono code",
                text: "OPENAI_BASE_URL=" + here + "\nOPENAI_API_KEY=" + r.token }),
              el("button", { class: "act", type: "button", text: T("ui.action.close"),
                onclick: async () => { closeModal(); await refresh(); } }),
            );
            $("#modal-body").replaceChildren(...kids.filter(Boolean));
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
        needsKey ? el("label", { class: "field mt6" }, T("ui.add.key"), extra) : null,
        el("div", { class: "actions" },
          el("button", { class: "act", "data-primary": true, type: "button", text: T("ui.staged.apply"),
            onclick: async () => {
              const body = needsKey && extra.value ? { key: extra.value } : {};
              await run(async () => {
                const r = await fetch("/api/staged/" + s.id + "/apply", {
                  method: "POST", headers: controlHeaders(), body: JSON.stringify(body) });
                const j = await r.json();
                if (!r.ok || j.error) throw new Error(j.error || r.statusText);
              }, T("ui.staged.apply"));
            } }),
          el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.staged.discard"),
            onclick: () => run(async () => {
              const r = await fetch("/api/staged/" + s.id + "/discard", {
                method: "POST", headers: controlHeaders() });
              if (!r.ok) throw new Error(r.statusText);
            }, T("ui.staged.discard")) }),
        ));
    })),
  );
}


/* ---------- the default view ----------
   Arranged around what a person is doing, not around what Ferrule is made of. The three
   steps are the whole job in order: put your keys in, give your household plain names
   for what they get, hand each person their own way in. Everything the old screens show
   — the model table, the ladders, the egress split, the ledger — is still there, one
   click away under Advanced, for the day someone wants it. */

function renderHome(pane, bar) {
  bar.replaceChildren(el("h2", { text: T("ui.home.title") }));
  pane.replaceChildren(el("div", { class: "home" },
    shareCard(),
    homeStep("1", T("ui.home.sources"), T("ui.home.sourcesBody"),
      el("button", { class: "act", "data-primary": true, type: "button",
        text: T("ui.home.addSource"), onclick: addProviderDialog }),
      state.sources.length ? el("div", { class: "rows" }, ...state.sources.map(sourceRow))
        : homeEmpty(state.detecting ? T("ui.home.sourcesScanning") : T("ui.home.sourcesEmpty"))),
    homeStep("2", T("ui.home.choices"), T("ui.home.choicesBody"),
      el("button", { class: "act", type: "button", text: T("ui.home.addChoice"),
        disabled: !liveModelCount(), onclick: () => aliasEditor(null) }),
      state.aliases.length ? el("div", { class: "rows" }, ...state.aliases.map(choiceRow))
        : homeEmpty(liveModelCount() ? T("ui.home.choicesEmpty") : T("ui.home.choicesNoModels"))),
    // Per-person keys are an upgrade, not the price of admission: the household key
    // above already works for everyone. This step is for the day you want the usage view
    // to say who, or to cut one person off without cutting off the house.
    homeStep("3", T("ui.home.people"), T("ui.home.peopleBody"),
      el("button", { class: "act", type: "button", text: T("ui.home.addPerson"), onclick: mintDialog }),
      named().length ? el("div", { class: "rows" }, ...named().map(personRow))
        : homeEmpty(T("ui.home.peopleEmpty"))),
  ));
}

const livePeople = () => state.grants.filter((g) => !g.revoked);
// The household key lives in the share card, not in the list of people.
const named = () => livePeople().filter((g) => !g.shared);
// Four decimals is right in the ledger and wrong on a household screen, where it reads
// as a rounding error rather than as a number. Two, unless two would round it to zero.
const money = (c) => {
  const n = c || 0;
  return "$" + (n > 0 && n < 0.01 ? n.toFixed(4) : n.toFixed(2));
};
const liveModelCount = () =>
  state.models.filter((m) => (state.sources.find((s) => s.name === m.source) || {}).status === "live").length;

function homeStep(n, title, body, action, ...kids) {
  return el("section", { class: "step" },
    el("header", {},
      el("span", { class: "step-n", text: n }),
      el("div", {}, el("h3", { text: title }), el("p", { class: "note", text: body })),
      el("div", { class: "spacer" }),
      action),
    ...kids);
}

const homeEmpty = (text) => el("p", { class: "note empty", text });

// shareCard answers the question the whole product exists for: can my family use this,
// and what do I give them. When Ferrule is bound to this machine only, it says so and
// says how to change it, rather than leaving an address-shaped hole.
function shareCard() {
  const st = state.status || {};
  const lan = st.lan_endpoint || "";
  const on = st.sharing !== "off";
  // No LAN address at all means the listener was bound narrowly on purpose. That is not
  // a toggle this panel gets to flip, and offering one that cannot work would be a lie.
  if (!lan) {
    return el("div", { class: "share", "data-off": "" },
      el("h3", { text: T("ui.home.bound") }),
      el("p", { class: "note", text: T("ui.home.boundBody") }),
      el("pre", { class: "mono code", text: "ferrule serve" }));
  }
  const url = "http://" + lan + "/v1";
  return el("div", { class: "share", "data-off": on ? null : "" },
    el("div", { class: "share-head" },
      el("h3", { text: on ? T("ui.home.shared") : T("ui.home.notShared") }),
      el("div", { class: "spacer" }),
      toggle(on, T("ui.home.shareToggle"), (next) =>
        run(() => op("set_setting", { key: "sharing", value: next ? "on" : "off" }),
          T("ui.home.shareToggle")))),
    el("p", { class: "note", text: on ? T("ui.home.sharedBody") : T("ui.home.notSharedBody") }),
    on ? el("div", { class: "share-url" },
      el("code", { class: "mono", text: url }),
      copyButton(url)) : null,
    on ? addressPicker() : null,
    on ? householdKeyRow() : null,
    startupRow(),
  );
}

// startupRow is the other half of "be there when I get here". A switch that cannot work
// says so instead of sitting in a position it does not hold: an unsupported platform, or
// a passphrase vault the daemon cannot open without a person, are both reasons this is
// off and neither is a reason to hide it.
function startupRow() {
  const s = (state.status && state.status.startup) || {};
  if (!s.supported) return null;
  const can = s.unattended;
  const row = el("div", { class: "share-startup" },
    toggle(!!s.enabled, T("ui.home.startup"), (next) =>
      run(() => op("set_startup", { value: next ? "on" : "off" }), T("ui.home.startup"))),
    el("span", { class: "note", text: s.reason || T("ui.home.startupHint") }));
  if (!can) {
    const box = row.querySelector("input");
    if (box) box.disabled = true;
  }
  return row;
}

// toggle is a checkbox that reads as a switch. It is a real input so it is reachable by
// keyboard and announced as one, rather than a div that happens to respond to clicks.
function toggle(on, label, onChange) {
  const box = el("input", { type: "checkbox", checked: on ? true : null,
    onchange: (e) => onChange(e.target.checked) });
  return el("label", { class: "switch" }, box, el("span", { text: label }));
}

// householdKeyRow shows the one key the whole house uses. It is fetched on demand rather
// than carried in the board's payload, so a credential is not sitting in memory on every
// screen that happens to render.
function householdKeyRow() {
  const holder = el("div", { class: "share-key" },
    el("button", { class: "act", type: "button", text: T("ui.home.showKey"),
      onclick: async (e) => {
        try {
          const r = await op("household_key");
          holder.replaceChildren(
            el("code", { class: "mono", text: r.token }),
            copyButton(r.token),
            el("button", { class: "act", "data-danger": true, type: "button",
              text: T("ui.home.newKey"), onclick: regenerateHouseholdKey }),
            el("span", { class: "note", text: T("ui.home.keyHint") }));
        } catch (err) { toast(err.message, "error"); }
      } }),
    el("span", { class: "note", text: T("ui.home.keyHint") }));
  return holder;
}

function regenerateHouseholdKey() {
  modal(T("ui.home.newKey"),
    el("p", { class: "note", text: T("ui.home.newKeyBody") }),
    el("div", { class: "actions-lg" },
      el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.home.newKey"),
        onclick: async () => { closeModal(); await run(() => op("regenerate_household_key"), T("ui.home.newKey")); } }),
      el("button", { class: "act", type: "button", text: T("ui.action.cancel"), onclick: closeModal })));
}

// addressPicker appears only when this machine has more than one address, which is more
// often than it looks: a VPN, a container bridge, a second network. Ferrule leads with its
// best guess — a tunnel sorts last, because the house is not on the other end of one —
// but a guess is not a fact, and an address that is listening and unreachable looks
// exactly like an address that works.
function addressPicker() {
  const eps = (state.status && state.status.lan_endpoints) || [];
  if (eps.length < 2) return null;
  const cur = (state.status && state.status.lan_endpoint) || eps[0];
  const sel = el("select", {
    "aria-label": T("ui.home.addressLabel"),
    onchange: (e) => run(() => op("set_share_address", { address: e.target.value }),
      T("ui.home.addressLabel")),
  }, ...eps.map((ep) => el("option", { value: ep, text: ep, selected: ep === cur })));
  return el("div", { class: "share-pick" },
    el("span", { class: "note", text: T("ui.home.addressHint") }), sel);
}

function copyButton(text) {
  return el("button", { class: "act", type: "button", text: T("ui.action.copy"),
    onclick: async (e) => {
      try {
        await navigator.clipboard.writeText(text);
        toast(T("ui.toast.copied"));
      } catch {
        // A denied clipboard is not a failure to route around silently: select the text
        // so the person can copy it the ordinary way.
        const node = e.target.previousElementSibling;
        if (node) {
          const r = document.createRange();
          r.selectNodeContents(node);
          const sel = window.getSelection();
          sel.removeAllRanges();
          sel.addRange(r);
        }
        toast(T("ui.toast.copyManual"), "error");
      }
    } });
}

function sourceRow(s) {
  const live = s.status === "live";
  const count = s.models === 1 ? T("ui.source.modelCountOne") : T("ui.source.modelCount", s.models);
  return el("div", { class: "row-item" },
    el("span", { class: "dot", "data-s": s.status }),
    el("div", { class: "grow" },
      el("div", { class: "row-title" }, s.name,
        el("span", { class: "tag", "data-where": s.where, text: s.where })),
      el("div", { class: "note", text: live ? count : T("ui.home.needsAttention") }),
      // On a failed source the remedy is the whole content of the row. The provider's
      // verbatim words stay in Advanced; a person setting up their house does not need
      // 180 characters of somebody's JSON to learn their card was declined.
      live ? null : el("div", { class: "why", text: s.remedy || s.reason }),
      live && s.models ? el("details", { class: "verbatim" },
        el("summary", { text: T("ui.home.seeModels") }),
        el("div", { class: "model-list mono" },
          ...state.models.filter((m) => m.source === s.name).map((m) =>
            el("div", { text: m.model })))) : null),
    el("div", { class: "actions" },
      el("button", { class: "act", type: "button", text: T("ui.action.checkAgain"),
        onclick: () => run(() => op("refresh_source", { id: s.id }), T("ui.action.checkAgain")) }),
      el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.action.remove"),
        onclick: () => confirmRemove(s) })));
}

function choiceRow(a) {
  const i = firstLive(a);
  const serving = i >= 0 ? a.rungs[i] : null;
  return el("div", { class: "row-item" },
    el("span", { class: "dot", "data-s": serving ? "live" : "failed" }),
    el("div", { class: "grow" },
      el("div", { class: "row-title" }, a.name),
      el("div", { class: "note mono",
        text: serving ? serving.model : T("ui.home.choiceDark") }),
      // A ladder is the point of an alias and the last thing a person needs on screen.
      // One line, only when there is actually a fallback behind the first rung.
      serving && a.rungs.length > 1
        ? el("div", { class: "note", text: T("ui.home.choiceFallback", a.rungs.length - 1) }) : null),
    el("div", { class: "actions" },
      el("button", { class: "act", type: "button", text: T("ui.action.change"),
        onclick: () => aliasEditor(a) }),
      el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.action.remove"),
        onclick: () => run(() => op("remove_alias", { name: a.name }), T("ui.action.remove")) })));
}

function personRow(g) {
  return el("div", { class: "row-item" },
    el("span", { class: "dot", "data-s": "live" }),
    el("div", { class: "grow" },
      el("div", { class: "row-title" }, g.app),
      el("div", { class: "note", text: g.requests === 1
        ? T("ui.home.personUsageOne", money(g.cost))
        : T("ui.home.personUsage", g.requests, money(g.cost)) })),
    el("div", { class: "actions" },
      el("button", { class: "act", type: "button", text: T("ui.home.setup"),
        onclick: () => setupDialog(g) }),
      el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.home.turnOff"),
        onclick: () => run(() => op("revoke_grant", { id: g.id }), T("ui.home.turnOff")) })));
}

// setupDialog is what one person hands another. The token itself was shown once when it
// was minted and Ferrule does not keep it, so this says so rather than pretending to
// produce it again — and offers the one thing that does fix it, a fresh key.
function setupDialog(g) {
  const lan = (state.status && state.status.lan_endpoint) || "";
  const base = "http://" + (lan || location.host) + "/v1";
  modal(T("ui.home.setupFor", g.app),
    el("p", { class: "note", text: T("ui.home.setupBody") }),
    el("pre", { class: "mono code", text: "OPENAI_BASE_URL=" + base + "\nOPENAI_API_KEY=" + T("ui.home.theirKey") }),
    lan ? null : el("p", { class: "note", text: T("ui.home.setupLocalOnly") }),
    el("p", { class: "note", text: T("ui.home.setupLost") }),
    el("div", { class: "actions-lg" },
      copyButtonFor("OPENAI_BASE_URL=" + base),
      el("button", { class: "act", type: "button", text: T("ui.action.close"), onclick: closeModal })));
}

const copyButtonFor = (text) =>
  el("button", { class: "act", type: "button", text: T("ui.home.copyBase"),
    onclick: async () => {
      try { await navigator.clipboard.writeText(text); toast(T("ui.toast.copied")); }
      catch { toast(T("ui.toast.copyManual"), "error"); }
    } });

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
    home: [$("#pane-home"), renderHome],
    board: [$("#pane-board"), renderBoard],
    aliases: [$("#pane-aliases"), renderAliases],
    add: [$("#pane-add"), renderAdd],
    usage: [$("#pane-usage"), renderUsage],
    grants: [$("#pane-grants"), renderGrants],
    staged: [$("#pane-grants"), renderStaged],
  };
  if (state.mode === "simple") state.view = "home";
  const [pane, fn] = map[state.view] || (state.mode === "simple" ? map.home : map.board);
  pane.dataset.active = "";
  fn(pane, bar);
}

// The sovereignty statement and the one knob that can contradict it live together: a
// person reading "nothing but metadata is recorded" should be able to see, in the same
// breath, whether that is currently true on their machine — and change it there.
function sovereigntyModal() {
  const on = state.status && state.status.content_logging === "on";
  modal(T("ui.sovereignty.title"),
    el("p", { class: "note", text: T("app.sovereignty") }),
    el("p", { class: on ? "why" : "note", text: on ? T("ui.sov.contentOn") : T("ui.sov.contentOff") }),
    el("div", { class: "actions-lg" },
      el("button", { class: "act", type: "button",
        text: on ? T("ui.sov.turnOff") : T("ui.sov.turnOn"),
        onclick: async () => {
          closeModal();
          await run(() => op("set_setting", { key: "content_logging", value: on ? "off" : "on" }),
            on ? T("ui.sov.turnOff") : T("ui.sov.turnOn"));
        } }),
      on ? el("button", { class: "act", "data-danger": true, type: "button", text: T("ui.sov.forget"),
        onclick: async () => {
          closeModal();
          await run(() => op("forget_content"), T("ui.sov.forget"));
        } }) : null,
      el("button", { class: "act", type: "button", text: T("ui.action.close"), onclick: closeModal }),
    ));
}

$("#foot-sov").addEventListener("click", sovereigntyModal);

document.addEventListener("keydown", (e) => {
  if (e.key === "?" && !/^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName)) {
    modal(T("ui.help.title"),
      el("dl", { class: "kv" },
        ...VIEWS.map(([id, key], i) => [el("dt", { text: String(i + 1) }), el("dd", { text: T(key) })]).flat(),
        el("dt", { text: "/" }), el("dd", { text: T("ui.filter.search") }),
      ),
      el("p", { class: "note mt10", text: T("app.tagline") }),
      el("p", { class: "note", text: T("ui.help.body") }),
      el("button", { class: "act", type: "button", text: T("ui.action.close"), onclick: closeModal }));
  }
  const n = parseInt(e.key, 10);
  if (n >= 1 && n <= VIEWS.length && !/^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName)) {
    go(VIEWS[n - 1][0]);
  }
});

(async function boot() {
  S = await (await fetch("/ui/strings.json")).json();
  document.title = T("app.name");
  $("#brand h1").textContent = T("app.name");
  $("#brand .sub").textContent = T("app.tagline-short");
  state.providers = (await (await fetch("/ui/providers.json")).json()).providers;
  render();                        // paints the rail and the detecting state immediately
  await loadAll();
  render();
  // Detection runs at daemon start and can legitimately take a minute when a local
  // runtime has to load a model to answer the test request. The daemon says when it is
  // still looking; guessing on a timer here is how a board announces "nothing found"
  // while the scan is still running.
  pollWhileScanning();
})();

async function pollWhileScanning() {
  let waited = 0;
  while (state.detecting && waited < 5 * 60 * 1000) {
    await new Promise((r) => setTimeout(r, 1200));
    waited += 1200;
    try {
      await loadAll();
    } catch { break; }
    render();
  }
  if (state.detecting) {
    state.detecting = false;
    render();
  }
}

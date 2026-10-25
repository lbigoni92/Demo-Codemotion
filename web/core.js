/* Core: router, console log, narratore, gestione delle run (SSE) e piccoli helper UI. */
"use strict";

const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
function h(html) { const t = document.createElement("template"); t.innerHTML = html.trim(); return t.content.firstElementChild; }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const Pages = [];

/* ─────────────── console ─────────────── */
const Log = (() => {
  const body = $("#cBody"), count = $("#cCount"), follow = $("#cFollow"), search = $("#cSearch");
  const MAX = 3000;
  let n = 0;
  const enabled = () => new Set($$("#cFilters input:checked").map((i) => i.value));
  function visible(el) {
    const en = enabled(), q = search.value.trim().toLowerCase();
    const lv = el.dataset.lv;
    let ok = !lv || lv === "say" || en.has(lv);
    if (ok && q) ok = el.textContent.toLowerCase().includes(q);
    el.style.display = ok ? "" : "none";
  }
  function fmtT(ms) { const s = (ms / 1000).toFixed(2); return "+" + s.padStart(6, " ") + "s"; }
  function add(e) {
    const lv = e.type === "say" ? "say" : e.level || "info";
    const el = h(`<div class="ln ${lv}" data-lv="${lv}"><span class="t">${fmtT(e.t || 0)}</span><span class="lv ${lv}">${lv === "say" ? "▶" : lv}</span><span class="s" title="${esc(e.src)}">${esc(e.src || (lv === "say" ? "narratore" : ""))}</span><span class="m">${esc(e.msg)}</span></div>`);
    visible(el);
    body.appendChild(el);
    n++;
    while (body.childElementCount > MAX) body.firstElementChild.remove();
    count.textContent = n + " righe";
    if (follow.checked) body.scrollTop = body.scrollHeight;
  }
  function sep(text) {
    body.appendChild(h(`<div class="ln sep">── ${esc(text)} · ${new Date().toLocaleTimeString()} ──</div>`));
    if (follow.checked) body.scrollTop = body.scrollHeight;
  }
  function clear() { body.innerHTML = ""; n = 0; count.textContent = "0 righe"; }
  $("#cClear").onclick = clear;
  $$("#cFilters input").forEach((i) => (i.onchange = () => $$(".ln", body).forEach(visible)));
  search.oninput = () => $$(".ln", body).forEach(visible);
  const con = $("#console");
  const toggle = () => { con.classList.toggle("collapsed"); $("#cToggle").textContent = (con.classList.contains("collapsed") ? "▸" : "▾") + " Log"; };
  $("#cToggle").onclick = toggle;
  return { add, sep, clear, toggle };
})();

/* ─────────────── narratore ─────────────── */
const Narrator = {
  set(level, msg) {
    const n = $("#narrator");
    n.className = "narrator " + (level || "info");
    void n.offsetWidth; n.classList.add("flash");
    $("#narrText").textContent = msg;
    $(".narr-icon", n).textContent = { ok: "✅", warn: "⚠️", error: "🔥", info: "💬" }[level] || "💬";
  },
};

/* ─────────────── run (SSE) ─────────────── */
const Runner = (() => {
  let es = null, runId = null, cur = null, gen = 0;
  const pill = $("#runState");
  function setRunning(on, label) {
    pill.classList.toggle("running", on);
    $(".txt", pill).textContent = label || (on ? "in esecuzione" : "pronto");
    $$(".nav-item").forEach((a) => a.classList.toggle("running", on && a.dataset.id === location.hash.slice(1)));
    // il pulsante principale resta sempre attivo: durante la run ricomincia da capo,
    // alla fine rilancia lo stesso scenario
    $$('#page [data-act="run"]').forEach((b) => (b.innerHTML = on ? "↻ Ricomincia" : "↻ Rilancia"));
    $$('#page [data-act="stop"]').forEach((b) => (b.disabled = !on));
    if (cur && cur.onRunning) cur.onRunning(on);
  }
  function stop(reason) {
    if (es) { es.close(); es = null; }
    if (cur) { const c = cur; cur = null; setRunning(false, reason || "fermato"); c.onStop && c.onStop(); }
    runId = null;
  }
  /* start(demo, params, handlers) — handlers: onState(kind, data, e), onLog(e), onSay(e), onDone(data), onStop(), onRunning(bool) */
  function start(demo, params, handlers) {
    stop();
    gen++;
    const q = new URLSearchParams(params).toString();
    Log.sep(`run ${demo}${q ? "?" + q : ""}`);
    cur = handlers || {};
    const me = cur;
    setRunning(true);
    es = new EventSource(`/api/run/${demo}?${q}`);
    es.onmessage = (m) => {
      if (cur !== me) return;
      const e = JSON.parse(m.data);
      e.msg = e.msg || "";
      switch (e.type) {
        case "run": runId = e.data.id; break;
        case "log": Log.add(e); me.onLog && me.onLog(e); break;
        case "say": Log.add(e); Narrator.set(e.level, e.msg); me.onSay && me.onSay(e); break;
        case "state": me.onState && me.onState(e.msg, e.data, e); break;
        case "done":
          es.close(); es = null; runId = null; cur = null;
          setRunning(false, "completato");
          me.onDone && me.onDone(e.data);
          break;
      }
    };
    es.onerror = () => {
      if (cur !== me) return;
      Log.add({ type: "log", level: "error", src: "browser", msg: "connessione allo stream interrotta (il server è ancora acceso?)", t: 0 });
      stop("errore");
    };
  }
  async function control(cmd) {
    if (!runId) return false;
    const r = await fetch(`/api/control/${runId}?cmd=${encodeURIComponent(cmd)}`, { method: "POST" });
    return r.ok;
  }
  /* gen cambia a ogni start: le animazioni asincrone di una run vecchia lo
     confrontano con il proprio e si fermano, così un rilancio parte pulito */
  return { start, stop, control, get running() { return !!cur; }, get gen() { return gen; } };
})();

/* ─────────────── helper UI ─────────────── */
const UI = {
  /* slider: valore letto con form.get(name) */
  slider(name, label, min, max, val, step = 1, unit = "") {
    return `<label class="ctl"><span>${label} <b data-out="${name}">${val}${unit}</b></span><input type="range" name="${name}" min="${min}" max="${max}" step="${step}" value="${val}" data-unit="${unit}"></label>`;
  },
  seg(name, label, opts, val) {
    return `<div class="ctl"><span>${label}</span><div class="seg" data-name="${name}">${opts.map((o) => `<button type="button" data-v="${o[0]}" class="${o[0] === val ? "on " : ""}${o[2] || ""}">${o[1]}</button>`).join("")}</div></div>`;
  },
  toggle(name, label, checked = false, danger = false) {
    return `<label class="switch ${danger ? "danger" : ""}"><input type="checkbox" name="${name}" ${checked ? "checked" : ""}><i></i>${label}</label>`;
  },
  /* collega gli input di un contenitore e restituisce { get(), set(obj) } */
  form(root, onChange) {
    $$("input[type=range]", root).forEach((i) => (i.oninput = () => { $(`[data-out="${i.name}"]`, root).textContent = i.value + (i.dataset.unit || ""); onChange && onChange(); }));
    $$(".seg", root).forEach((s) => $$("button", s).forEach((b) => (b.onclick = () => { $$("button", s).forEach((x) => x.classList.remove("on")); b.classList.add("on"); onChange && onChange(); })));
    $$("input[type=checkbox]", root).forEach((i) => (i.onchange = () => onChange && onChange()));
    return {
      get() {
        const o = {};
        $$("input[type=range]", root).forEach((i) => (o[i.name] = i.value));
        $$("input[type=checkbox]", root).forEach((i) => (o[i.name] = i.checked ? "1" : "0"));
        $$(".seg", root).forEach((s) => (o[s.dataset.name] = $("button.on", s)?.dataset.v));
        return o;
      },
      set(obj) {
        for (const [k, v] of Object.entries(obj)) {
          const r = $(`input[type=range][name="${k}"]`, root);
          if (r) { r.value = v; $(`[data-out="${k}"]`, root).textContent = v + (r.dataset.unit || ""); }
          const c = $(`input[type=checkbox][name="${k}"]`, root);
          if (c) c.checked = v === true || v === "1";
          const s = $(`.seg[data-name="${k}"]`, root);
          if (s) $$("button", s).forEach((b) => b.classList.toggle("on", b.dataset.v === String(v)));
        }
        onChange && onChange();
      },
    };
  },
  /* items: [titolo, testo, dettaglio?]. Il dettaglio («sotto il cofano») si apre con il pulsante i */
  steps(items) {
    return `<ol class="steps">${items.map((s) => `<li><div><b>${s[0]}</b>${s[1]}${s[2] ? `<div class="deep"><small>Sotto il cofano</small>${s[2]}</div>` : ""}</div>${s[2] ? `<button type="button" class="info" title="Cosa succede sotto il cofano" aria-label="Dettaglio tecnico">i</button>` : ""}</li>`).join("")}</ol>`;
  },
  /* accende lo step i (0-based) */
  lit(root, i, cls = "") {
    const li = $$(".steps li", root)[i];
    if (li) { li.classList.add("lit"); if (cls) li.classList.add(cls); }
  },
  unlitAll(root) { $$(".steps li", root).forEach((l) => l.classList.remove("lit", "bad", "good")); },
  stat(id, label, cls = "") { return `<div class="stat ${cls}" data-stat="${id}"><small>${label}</small><b>–</b></div>`; },
  setStat(root, id, v) {
    const s = $(`[data-stat="${id}"]`, root); if (!s) return;
    const b = $("b", s); const nv = String(v);
    if (b.textContent !== nv) { b.textContent = nv; s.classList.remove("bump"); void s.offsetWidth; s.classList.add("bump"); }
  },
  /* evidenziazione Go minimale */
  code(src, hlLines = []) {
    const RE = /(\/\/.*$)|("(?:[^"\\]|\\.)*"|`[^`]*`|'(?:[^'\\]|\\.)')|\b(func|go|defer|select|case|default|for|range|if|else|return|var|const|type|struct|chan|map|make|close|break|continue|switch|package|import|nil|true|false|interface)\b|\b(\d+)\b|\b([A-Za-z_]\w*)(?=\()/g;
    return src.split("\n").map((line, idx) => {
      let out = "", last = 0, m;
      RE.lastIndex = 0;
      while ((m = RE.exec(line))) {
        out += esc(line.slice(last, m.index));
        const cls = m[1] ? "c" : m[2] ? "s" : m[3] ? "k" : m[4] ? "n" : "f";
        out += `<span class="${cls}">${esc(m[0])}</span>`;
        last = m.index + m[0].length;
        if (m[0].length === 0) RE.lastIndex++;
      }
      out += esc(line.slice(last));
      return hlLines.includes(idx + 1) ? `<span class="hl">${out || " "}</span>` : out;
    }).join("\n");
  },
  codeCard(snippets) {
    const keys = Object.keys(snippets);
    const el = h(`<div class="card"><h3>Il codice Go che sta girando</h3><div class="code-tabs">${keys.map((k, i) => `<button class="${i ? "" : "on"}" data-k="${esc(k)}">${esc(k)}</button>`).join("")}</div><div class="code"></div></div>`);
    const show = (k) => { const s = snippets[k]; $(".code", el).innerHTML = UI.code(s.src || s, s.hl || []); $$(".code-tabs button", el).forEach((b) => b.classList.toggle("on", b.dataset.k === k)); };
    $$(".code-tabs button", el).forEach((b) => (b.onclick = () => show(b.dataset.k)));
    show(keys[0]);
    return el;
  },
};

/* grafico a linee su canvas, con più serie e linee orizzontali di riferimento */
class LineChart {
  constructor(canvas, opt) {
    this.c = canvas; this.opt = Object.assign({ max: 100, window: 120, unit: "", refs: [], ticks: 4 }, opt);
    this.series = this.opt.series.map((s) => Object.assign({ data: [] }, s));
    this.resize(); window.addEventListener("resize", () => this.resize());
  }
  resize() { const r = this.c.getBoundingClientRect(); const d = devicePixelRatio || 1; this.c.width = Math.max(10, r.width * d); this.c.height = Math.max(10, r.height * d); this.d = d; this.draw(); }
  push(vals) { this.series.forEach((s, i) => { s.data.push(vals[i]); if (s.data.length > this.opt.window) s.data.shift(); }); this.draw(); }
  reset() { this.series.forEach((s) => (s.data = [])); this.draw(); }
  draw() {
    const { c, d, opt } = this; const g = c.getContext("2d");
    const W = c.width, H = c.height, pl = 48 * d, pr = 12 * d, pt = 14 * d, pb = 22 * d;
    g.clearRect(0, 0, W, H);
    let max = opt.max;
    this.series.forEach((s) => s.data.forEach((v) => (max = Math.max(max, v * 1.1))));
    opt.refs.forEach((r) => r.v && (max = Math.max(max, r.v * 1.12)));
    const x = (i) => pl + (i / (opt.window - 1)) * (W - pl - pr);
    const y = (v) => pt + (1 - v / max) * (H - pt - pb);
    g.font = `${11 * d}px ui-monospace, Consolas, monospace`; g.fillStyle = "#5d6a8f"; g.strokeStyle = "#1f2a47"; g.lineWidth = 1 * d;
    for (let k = 0; k <= opt.ticks; k++) { const v = (max / opt.ticks) * k; g.beginPath(); g.moveTo(pl, y(v)); g.lineTo(W - pr, y(v)); g.stroke(); g.fillText(v.toFixed(v < 10 ? 1 : 0) + opt.unit, 4 * d, y(v) + 4 * d); }
    opt.refs.forEach((r) => { if (!r.v) return; g.setLineDash([6 * d, 5 * d]); g.strokeStyle = r.color; g.lineWidth = 1.5 * d; g.beginPath(); g.moveTo(pl, y(r.v)); g.lineTo(W - pr, y(r.v)); g.stroke(); g.setLineDash([]); g.fillStyle = r.color; g.fillText(r.label, W - pr - g.measureText(r.label).width - 4 * d, y(r.v) - 5 * d); });
    this.series.forEach((s) => {
      if (!s.data.length) return;
      if (s.fill) { g.beginPath(); g.moveTo(x(0), y(0)); s.data.forEach((v, i) => g.lineTo(x(i), y(v))); g.lineTo(x(s.data.length - 1), y(0)); g.closePath(); g.fillStyle = s.fill; g.fill(); }
      g.beginPath(); s.data.forEach((v, i) => (i ? g.lineTo(x(i), y(v)) : g.moveTo(x(i), y(v)))); g.strokeStyle = s.color; g.lineWidth = (s.width || 2) * d; g.setLineDash(s.dash ? [4 * d, 4 * d] : []); g.stroke(); g.setLineDash([]);
    });
  }
}

/* pulsante i dei passi: apre e chiude il dettaglio tecnico (resta aperto tra un rilancio e l'altro) */
document.addEventListener("click", (e) => {
  const b = e.target.closest(".steps .info");
  if (b) b.closest("li").classList.toggle("open");
});

/* ─────────────── router ─────────────── */
const App = (() => {
  let current = null, ctx = null;
  function menu() {
    const nav = $("#menu");
    nav.innerHTML = Pages.map((p) => `<a class="nav-item" href="#${p.id}" data-id="${p.id}"><span class="nav-ico">${p.icon}</span><span class="nav-txt"><b>${p.title}</b><small>${p.short}</small></span></a>`).join("");
  }
  function go() {
    const id = location.hash.slice(1) || Pages[0].id;
    const p = Pages.find((x) => x.id === id) || Pages[0];
    if (current === p) return;
    Runner.stop();
    if (ctx && ctx.cleanup) ctx.cleanup();
    current = p;
    $$(".nav-item").forEach((a) => a.classList.toggle("active", a.dataset.id === p.id));
    $("#pageNum").textContent = p.num;
    $("#pageTitle").textContent = p.title;
    $("#pageSub").textContent = p.sub;
    Narrator.set("info", p.intro || "Imposta i parametri e premi «Avvia simulazione».");
    const root = $("#page");
    root.innerHTML = ""; root.scrollTop = 0;
    ctx = { root, run: null, stop: () => Runner.stop("fermato"), cleanup: null };
    p.mount(root, ctx);
  }
  window.addEventListener("hashchange", go);
  document.addEventListener("keydown", (e) => {
    if (e.target.matches("input[type=text], input:not([type]), textarea")) return;
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    const k = e.key.toLowerCase();
    if (/^[0-6]$/.test(k)) { const p = Pages[+k]; if (p) location.hash = p.id; }
    else if (k === "r" && ctx && ctx.run) ctx.run();
    else if (k === "s") Runner.stop("fermato");
    else if (k === "l") Log.toggle();
  });
  async function init() {
    menu(); go();
    liveReload();
    try { const i = await (await fetch("/api/info")).json(); $("#sysinfo").textContent = `${i.go} · ${i.os}/${i.arch} · ${i.cpus} CPU${i.container ? " · 🐳 container" : ""}`; } catch { $("#sysinfo").textContent = "server non raggiungibile"; }
  }
  /* live reload: controlla ogni 1,5 s l'id di avvio del server. Se cambia
     (codice modificato → rebuild → riavvio) ricarica la pagina, restando
     sulla stessa sezione. Se una simulazione è in corso aspetta che finisca. */
  function liveReload() {
    let boot = null, pending = false, down = false;
    setInterval(async () => {
      try {
        const i = await (await fetch("/api/info", { cache: "no-store" })).json();
        if (down) { down = false; $("#sysinfo").classList.remove("offline"); }
        if (boot === null) { boot = i.boot; return; }
        if (i.boot !== boot && !pending) {
          pending = true;
          if (Runner.running) Narrator.set("warn", "🔄 Codice aggiornato: la pagina si ricarica alla fine della simulazione");
          const wait = setInterval(() => { if (!Runner.running) { clearInterval(wait); location.reload(); } }, 300);
        }
      } catch {
        if (!down) { down = true; Narrator.set("warn", "🔄 Server in riavvio (rebuild in corso?)… mi ricollego da solo"); }
      }
    }, 1500);
  }
  return { init };
})();

/* layout standard di una pagina: colonna principale + colonna laterale.
   guide = come leggere l'animazione; ogni preset può avere hint = cosa guardare */
function pageLayout(root, { controls, viz, side, presets, guide }) {
  const el = h(`<div class="page-grid">
    <div class="col">
      <div class="card"><h3>Parametri</h3><div class="controls">${controls}<div class="actions"><button class="btn ghost" data-act="stop" disabled title="Ferma (S)">■ Ferma</button><button class="btn primary" data-act="run" title="Avvia o ricomincia (R)">▶ Avvia simulazione</button></div></div>${presets ? `<div class="presets"><span>Scenari pronti · un clic avvia (o riavvia):</span>${presets.map((p, i) => `<button class="chip" data-preset="${i}">▶ ${p.label}</button>`).join("")}</div>` : ""}</div>
      <div class="watch"><div class="watch-h">👀 Cosa guardare<span class="watch-s"></span></div><div class="watch-t">${guide || ""}</div></div>
      ${viz}
    </div>
    <div class="col side"></div>
  </div>`);
  el._presets = presets || [];
  el._guide = guide || "";
  root.appendChild(el);
  const sideEl = $(".side", el);
  side.forEach((s) => sideEl.appendChild(typeof s === "string" ? h(s) : s));
  return el;
}

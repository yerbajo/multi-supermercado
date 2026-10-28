/* Direction 03 «Tienda cercana», no framework and no external request: the postal code
   and the store cards come first, then the search, the basket and the comparison.
   Everything the page knows about a chain comes from /api/caps or from the documents the
   server returns; nothing here invents a store, a zone or a price. */
"use strict";

const CHAIN_NAMES = {
  alcampo: "ALCAMPO", aldi: "ALDI", carrefour: "CARREFOUR",
  dia: "DIA", lidl: "LIDL", mas: "MAS",
};
// A store selector appears only where T29 found a store a human can actually choose.
// ALDI, ALCAMPO and CARREFOUR have none, and the card says why — that is the point of
// this direction and it is also S11: a chain that cannot fix a store must say so.
const PICKABLE = new Set(["dia", "mas", "lidl"]);
const STORE_HINT = {
  dia: "Almacén de tu zona · ejemplo",
  mas: "Punto de recogida · ejemplo",
  lidl: "Tienda más cercana · ejemplo",
  aldi: "Sin tienda: el catálogo va por región",
  alcampo: "Compra online: el código postal se pide en su web",
  carrefour: "Precios de un comparador externo",
};
const MODE_LABEL = {
  warehouse: "Zona del servidor",
  pickup: "Recogida en tienda",
  region: "Precios por región",
  none: "Sin zona",
};
const MODE_TAG = {
  warehouse: "tag-ok", pickup: "tag", region: "tag-warn", none: "tag",
};
const ICON = {
  check: '<svg width="11" height="11" viewBox="0 0 12 12" fill="none" aria-hidden="true"><path d="M2 6.4 4.8 9.2 10 3.2" stroke="currentColor" stroke-width="2"/></svg>',
  warn: '<svg width="11" height="11" viewBox="0 0 12 12" fill="none" aria-hidden="true"><circle cx="6" cy="6" r="4.6" stroke="currentColor" stroke-width="1.5"/><path d="M6 3.6v2.8" stroke="currentColor" stroke-width="1.5"/><path d="M6 8.2v.3" stroke="currentColor" stroke-width="1.7"/></svg>',
  x: '<svg width="11" height="11" viewBox="0 0 12 12" fill="none" aria-hidden="true"><circle cx="6" cy="6" r="4.6" stroke="currentColor" stroke-width="1.5"/><path d="M3.9 3.9l4.2 4.2M8.1 3.9 3.9 8.1" stroke="currentColor" stroke-width="1.5"/></svg>',
};

// One brand mark per chain, instantiated from the sprite in index.html. The mark is the
// chain's accessible name wherever it replaces the word: role="img" plus aria-label.
// It carries its own chip, so it reads on the light cards and on the dark recommendation.
function logo(store, extra) {
  return `<svg class="logo${extra ? " " + extra : ""}" viewBox="0 0 72 24" role="img" aria-label="${esc(nameOf(store))}" focusable="false"><use href="#logo-${esc(store)}"></use></svg>`;
}

// A chain that did not answer keeps its reason in two words: the compact line under the
// comparison names it, so a failure is quieter, never hidden (S13).
const SHORT_ERROR = {
  quota: "sin cuota", timeout: "sin respuesta", blocked: "bloqueada",
  network: "sin conexión", parse: "respuesta ilegible", unknown: "sin respuesta",
};
const SOFT_ERROR = new Set(["quota", "parse"]);


const state = {
  cp: "",
  caps: null,
  chosen: { dia: true, mas: true, lidl: true },
  lines: [],          // [{id, quantity, pins: {store: {id, name}}}]
  basket: null,
  basketError: null,
  candidates: null,
  candidatesStale: true,
  candidatesLoading: false,
  lastTerm: "",
  refreshTimer: null,
  seq: 0,
};

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s == null ? "" : s)
  .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
  .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
const nameOf = (store) => CHAIN_NAMES[store] || String(store).toUpperCase();
const euros = (value) => Number(value).toFixed(2).replace(".", ",") + " €";
const money = (cents) => {
  const n = Math.trunc(cents);
  return Math.trunc(n / 100) + "," + String(Math.abs(n % 100)).padStart(2, "0") + " €";
};
const chainOrder = () => (state.caps && state.caps.chains.length
  ? state.caps.chains.map((c) => c.store)
  : Object.keys(CHAIN_NAMES));

function announce(msg) { $("live").textContent = msg; }
function note(msg, bad) {
  const box = $("shops-note");
  box.hidden = !msg;
  box.innerHTML = (bad ? ICON.warn : "") + "<span>" + esc(msg) + "</span>";
}
async function api(path, body) {
  const res = await fetch(path, body === undefined ? { method: "GET" } : {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const doc = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(doc.error || "HTTP " + res.status);
  return doc;
}

/* ---------------- store cards, from /api/caps ---------------- */

function zoneText(zone) {
  const head = {
    warehouse: "Zona del servidor (28041) hasta que se fije",
    pickup: "La tienda no cambia el precio",
    region: "El precio depende de la región",
    none: "Esta cadena no tiene zona",
  }[zone.mode] || "Esta cadena no tiene zona";
  const scope = zone.price_scoped ? "el precio depende de la zona" : "el precio no depende de la zona";
  return head + " · " + zone.label + " · " + scope;
}

function zoneShort(zone) {
  return zone.price_scoped ? "precio por zona" : "precio de catálogo";
}

function shopCard(cap) {
  const name = nameOf(cap.store);
  const pickable = PICKABLE.has(cap.store);
  const zone = cap.zone || { mode: "none", price_scoped: false, label: "sin datos" };
  const tagClass = MODE_TAG[zone.mode] || "tag";
  const li = document.createElement("li");
  li.className = "shop" + (pickable ? "" : " no-pick");
  li.dataset.chain = cap.store;
  const control = pickable
    ? `<input type="checkbox" data-chain="${esc(cap.store)}" aria-label="Usar la tienda de ${name}"${state.chosen[cap.store] ? " checked" : ""}>`
    : "";
  const label = pickable ? "Usar esta tienda" : "Sin tienda que elegir";
  const body = `
      <span class="shop-check">${control}<span class="lab">${label}</span></span>
      <span class="shop-name">${logo(cap.store)}<span class="dist">${esc(zoneShort(zone))}</span></span>
      <span class="addr">${esc(STORE_HINT[cap.store] || "Sin tienda en el modelo")}</span>
      <span class="cap">${esc(zoneText(zone))}</span>`;
  li.innerHTML = (pickable ? `<label class="shop-label">${body}</label>` : `<span class="shop-label">${body}</span>`) +
    `<div class="shop-foot"><span class="tag ${tagClass}">${zone.price_scoped ? ICON.warn : ICON.check}${esc(MODE_LABEL[zone.mode] || "Sin zona")}</span></div>`;
  return li;
}

function pickedCount() {
  return chainOrder().filter((s) => PICKABLE.has(s) && state.chosen[s]).length;
}

function updatePicked() {
  const n = pickedCount();
  $("picked").textContent = n === 0 ? "Sin tiendas elegidas"
    : n + (n === 1 ? " tienda elegida" : " tiendas elegidas");
}

function renderShops() {
  const shops = $("shops");
  shops.innerHTML = "";
  const caps = (state.caps && state.caps.chains) || [];
  if (!caps.length) {
    $("picked").textContent = "";
    note("No se pudo leer qué puede hacer cada cadena, así que no se puede decir qué haría con tu zona.", true);
    return;
  }
  note("");
  for (const cap of caps) shops.appendChild(shopCard(cap));
  updatePicked();
}

async function loadCaps() {
  try {
    state.caps = await api("/api/caps");
  } catch (err) {
    state.caps = { chains: [] };
    note("No se pudo leer qué puede hacer cada cadena: " + err.message + " · reintentar", true);
  }
  renderShops();
}

/* ---------------- postal code: no filtering yet ---------------- */

$("postal-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const raw = $("cp").value.trim();
  $("cp-fine").hidden = true;
  if (!/^\d{5}$/.test(raw)) {
    $("cp-status").textContent = "Escribe los cinco dígitos del código postal.";
    $("cp").focus();
    return;
  }
  state.cp = raw;
  $("zone-cp").textContent = raw;
  $("cp-head").textContent = raw;
  const msg = "Código postal " + raw + " guardado. En esta fase no filtra precios: cada tarjeta dice qué haría esa cadena con él.";
  $("cp-status").textContent = msg;
  announce(msg);
});
$("cp").addEventListener("input", () => { $("cp-status").textContent = ""; });

/* ---------------- search ---------------- */

// fresh skips the server's 60 s cache: that is what «reintentar» means, since a cached
// failure would otherwise replay the same answer.
async function runSearch(term, fresh) {
  const hits = $("hits");
  $("search-status").textContent = "Buscando «" + term + "» en las seis cadenas…";
  hits.innerHTML = skeletonRows(3, "hit");
  hits.setAttribute("aria-busy", "true");
  try {
    const doc = await api("/api/search" + (fresh ? "?fresh=1" : ""), { term: term, limit: 5 });
    state.lastTerm = term;
    renderHits(doc, term);
  } catch (err) {
    hits.innerHTML = "";
    $("search-status").textContent = "No se pudo buscar: " + err.message + " · reintentar más abajo.";
  } finally {
    hits.removeAttribute("aria-busy");
  }
}

function renderHits(doc, term) {
  const hits = $("hits");
  hits.innerHTML = "";
  const failed = doc.results.filter((r) => r.error).map(failureOf);
  const answered = doc.results.filter((r) => !r.error);
  $("search-status").textContent = doc.count + (doc.count === 1 ? " resultado" : " resultados") +
    " en " + answered.length + " de " + doc.results.length + " cadenas para «" + term + "».";
  if (answered.length) {
    const bar = document.createElement("li");
    bar.className = "hit-add";
    bar.innerHTML = `<button class="btn btn-quiet btn-small" type="button" data-add="${esc(term)}">Añadir «${esc(term)}» a la cesta</button>`;
    hits.appendChild(bar);
  }
  for (const r of answered) hits.appendChild(hitChain(r, term));
  // A chain that did not answer is one compact line at the end, not a block that looks
  // like a result: the same rule the comparison follows.
  if (failed.length) {
    const li = document.createElement("li");
    li.className = "fail-line";
    li.innerHTML = failBody(failed, "search");
    hits.appendChild(li);
  }
}

function hitChain(r, term) {
  const name = nameOf(r.store);
  const li = document.createElement("li");
  li.className = "hit-chain";
  li.dataset.chain = r.store;
  const products = r.hits.slice(0, 3).map((h) => `
    <li>
      <span class="pname">${esc(h.name)}${h.size ? " · " + esc(h.size) : ""}</span>
      <span class="pprice">${h.price == null ? "sin precio" : euros(h.price)}</span>
      <button class="btn btn-quiet btn-small" type="button" data-pin="${esc(r.store)}" data-id="${esc(h.id)}" data-name="${esc(h.name)}" data-term="${esc(term)}">Elegir en ${name}</button>
    </li>`).join("");
  li.innerHTML = `<div class="hit-head">${logo(r.store)}
      <span class="tag">${r.count} ${r.count === 1 ? "resultado" : "resultados"}</span></div>
    <ul class="hit-products">${products || '<li class="hit-down">Sin resultados para este término.</li>'}</ul>`;
  return li;
}

$("search-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const term = $("q").value.trim();
  if (!term) { $("search-status").textContent = "Escribe qué quieres comprar."; $("q").focus(); return; }
  runSearch(term);
});

$("hits").addEventListener("click", (event) => {
  const add = event.target.closest("[data-add]");
  if (add) { addLine(add.dataset.add); return; }
  const pin = event.target.closest("[data-pin]");
  if (pin) {
    addLine(pin.dataset.term, { store: pin.dataset.pin, id: pin.dataset.id, name: pin.dataset.name });
  }
});

/* ---------------- basket ---------------- */

function basketItems() {
  const items = [];
  for (const line of state.lines) {
    items.push({ line: line.id, query: line.id, quantity: line.quantity });
    for (const store in line.pins) {
      items.push({ line: line.id, store: store, id: line.pins[store].id, name: line.pins[store].name, quantity: line.quantity });
    }
  }
  return items;
}

function addLine(term, pin) {
  term = String(term || "").trim();
  if (!term) return;
  let line = state.lines.find((l) => l.id === term);
  if (!line) {
    line = { id: term, quantity: 1, pins: {} };
    state.lines.push(line);
  }
  if (pin) line.pins[pin.store] = { id: pin.id, name: pin.name };
  state.candidatesStale = true;
  $("add-status").textContent = pin
    ? "«" + term + "» con " + nameOf(pin.store) + ": " + pin.name
    : "«" + term + "» añadido a la cesta.";
  renderLines();
  refresh(false);
}

function basketItem(line) {
  if (!state.basket) return null;
  return state.basket.items.find((it) => (it.line || it.query) === line.id) || null;
}

function lineNote(line) {
  const pins = Object.keys(line.pins).length;
  const it = basketItem(line);
  if (!it) return pins ? pins + (pins === 1 ? " producto elegido a mano" : " productos elegidos a mano") : "comparando en las seis cadenas…";
  const total = state.basket ? state.basket.chains.length : 6;
  const parts = [Object.keys(it.resolved).length + " de " + total + " cadenas la tienen"];
  if (it.missing.length) parts.push("falta en " + it.missing.map(nameOf).join(", "));
  if (it.stale.length) parts.push("sin confirmar en " + it.stale.map(nameOf).join(", "));
  if (pins) parts.push(pins === 1 ? "1 producto elegido a mano" : pins + " productos elegidos a mano");
  return parts.join(" · ");
}

function candidateItem(line) {
  if (!state.candidates) return null;
  return state.candidates.items.find((it) => it.line === line.id) || null;
}

function choicesHTML(line) {
  const cand = candidateItem(line);
  return chainOrder().map((store) => {
    const name = nameOf(store);
    const answer = state.candidates && state.candidates.chains.find((c) => c.store === store);
    if (answer && answer.error) {
      return `<div class="choice"><span class="choice-chain">${name}</span>
        <span class="choice-state">no disponible ahora · <button class="linkbtn" type="button" data-retry="candidates">reintentar</button></span></div>`;
    }
    const list = cand && cand.candidates && cand.candidates[store];
    if (!list) {
      return `<div class="choice"><span class="choice-chain">${name}</span>
        <span class="choice-state">${state.candidatesStale ? "pidiendo sus productos…" : "sin candidatos para esta línea"}</span></div>`;
    }
    const pin = line.pins[store];
    const current = pin ? pin.id : "";
    const options = [`<option value=""${current ? "" : " selected"}>Automático: la mejor coincidencia para «${esc(line.id)}»</option>`]
      .concat(list.map((c) => `<option value="${esc(c.id)}" data-name="${esc(c.name)}"${c.id === current ? " selected" : ""}>${esc(c.name)} — ${money(c.unitCents)}${c.current ? " (el actual)" : ""}</option>`));
    return `<label class="choice"><span class="choice-chain">${name}</span>
      <select data-line="${esc(line.id)}" data-store="${esc(store)}" aria-label="Producto de la línea ${esc(line.id)} en ${name}">${options.join("")}</select></label>`;
  }).join("");
}

function lineEl(line) {
  const li = document.createElement("li");
  li.className = "line";
  li.dataset.line = line.id;
  li.innerHTML = `
    <div class="line-head">
      <span>
        <span class="line-name">${esc(line.id)}</span>
        <span class="line-note">${esc(lineNote(line))}</span>
      </span>
      <span class="stepper">
        <button type="button" data-step="-1" aria-label="Quitar una unidad de ${esc(line.id)}"${line.quantity <= 1 ? " disabled" : ""}>−</button>
        <span class="q">${line.quantity}</span>
        <button type="button" data-step="1" aria-label="Añadir una unidad de ${esc(line.id)}"${line.quantity >= 9 ? " disabled" : ""}>+</button>
      </span>
    </div>
    <details class="chooser">
      <summary>Elegir producto por cadena</summary>
      <div class="choices">${choicesHTML(line)}</div>
    </details>`;
  return li;
}

function renderLines() {
  const list = $("lines");
  const open = new Set([...document.querySelectorAll("details.chooser[open]")]
    .map((d) => d.closest(".line").dataset.line));
  list.innerHTML = "";
  if (!state.lines.length) {
    list.innerHTML = '<li class="empty">Todavía no hay líneas. Busca un artículo y añádelo.</li>';
    return;
  }
  for (const line of state.lines) list.appendChild(lineEl(line));
  for (const id of open) {
    const det = [...document.querySelectorAll("details.chooser")]
      .find((d) => d.closest(".line").dataset.line === id);
    if (det) det.open = true;
  }
}

$("add-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const term = $("add-term").value.trim();
  if (!term) { $("add-status").textContent = "Escribe qué quieres añadir."; $("add-term").focus(); return; }
  $("add-term").value = "";
  addLine(term);
});

$("lines").addEventListener("click", (event) => {
  const btn = event.target.closest("button[data-step]");
  if (!btn) return;
  const line = state.lines.find((l) => l.id === btn.closest(".line").dataset.line);
  if (!line) return;
  line.quantity = Math.min(9, Math.max(1, line.quantity + Number(btn.dataset.step)));
  renderLines();
  refresh(false);
});

$("lines").addEventListener("change", (event) => {
  const select = event.target.closest("select[data-store]");
  if (!select) return;
  const line = state.lines.find((l) => l.id === select.dataset.line);
  if (!line) return;
  const store = select.dataset.store;
  if (select.value === "") {
    delete line.pins[store];
    announce("En " + nameOf(store) + ", «" + line.id + "» vuelve a la mejor coincidencia.");
  } else {
    const opt = select.selectedOptions[0];
    line.pins[store] = { id: select.value, name: opt.dataset.name || select.value };
    announce("En " + nameOf(store) + ", «" + line.id + "» queda como " + line.pins[store].name + ".");
  }
  renderLines();
  refresh(true);
});

$("lines").addEventListener("toggle", (event) => {
  const det = event.target;
  if (!det.classList || !det.classList.contains("chooser") || !det.open) return;
  ensureCandidates();
}, true);

/* ---------------- comparison ---------------- */

function refresh(immediate, fresh) {
  clearTimeout(state.refreshTimer);
  if (!state.lines.length) { state.basket = null; state.basketError = null; renderComparison(); return; }
  state.refreshTimer = setTimeout(() => recalc(fresh), immediate ? 0 : 180);
}

async function recalc(fresh) {
  const seq = ++state.seq;
  if (!state.basket) renderComparison();
  try {
    const doc = await api("/api/basket" + (fresh ? "?fresh=1" : ""), { items: basketItems() });
    if (seq !== state.seq) return;
    state.basket = doc;
    state.basketError = null;
  } catch (err) {
    if (seq !== state.seq) return;
    state.basket = null;
    state.basketError = err.message;
  }
  renderComparison();
}

async function ensureCandidates(fresh) {
  if (!state.candidatesStale || state.candidatesLoading) return;
  state.candidatesLoading = true;
  try {
    state.candidates = await api("/api/candidates" + (fresh ? "?fresh=1" : ""), { items: basketItems() });
    state.candidatesStale = false;
  } catch (err) {
    $("add-status").textContent = "No se pudieron pedir los productos por cadena: " + err.message;
  } finally {
    state.candidatesLoading = false;
    for (const det of document.querySelectorAll("details.chooser[open]")) {
      const id = det.closest(".line").dataset.line;
      const line = state.lines.find((l) => l.id === id);
      const box = det.querySelector(".choices");
      if (line && box) box.innerHTML = choicesHTML(line);
    }
  }
}

/* ---------------- failures and placeholders ---------------- */

// One failure, reduced to the store and its stable code, so the compact line and the
// page-level empty state say the same thing in two lengths.
function failureOf(chain) {
  return { store: chain.store, code: (chain.error && chain.error.code) || "unknown" };
}

const failWhy = (code) => SHORT_ERROR[code] || SHORT_ERROR.unknown;

function failItems(list) {
  return list.map((f) => {
    const soft = SOFT_ERROR.has(f.code);
    return `<span class="fail-item${soft ? " is-soft" : ""}">${soft ? ICON.warn : ICON.x}${nameOf(f.store)} <span class="fail-why">(${esc(failWhy(f.code))})</span></span>`;
  }).join(", ");
}

function failPlain(list) {
  return list.map((f) => nameOf(f.store) + " (" + failWhy(f.code) + ")").join(", ");
}

// The whole failure report: who did not answer, why, and the retry that skips the cache.
function failBody(list, retry) {
  const lead = list.length === 1 ? "1 cadena no respondió:" : list.length + " cadenas no respondieron:";
  return `<span class="fail-lead">${lead}</span> ${failItems(list)} <span class="fail-sep" aria-hidden="true">·</span> <button class="linkbtn" type="button" data-retry="${retry}">reintentar</button>`;
}

// Skeletons are decorative: aria-busy on the container already announces the wait, and
// the pulse only runs when the visitor has not asked for reduced motion.
function skeletonRows(n, kind) {
  const row = kind === "rank"
    ? '<span class="sk sk-logo"></span><span class="sk sk-line"></span><span class="sk sk-val"></span>'
    : '<span class="sk sk-logo"></span><span class="sk sk-line"></span>';
  return Array.from({ length: n }, () => `<li class="sk-row sk-${kind}" aria-hidden="true">${row}</li>`).join("");
}

function isDiscarded(store) {
  return PICKABLE.has(store) && !state.chosen[store];
}

function compEmpty(text, retry) {
  $("comp-empty").hidden = false;
  $("comp-empty-text").textContent = text;
  $("comp-empty-retry").hidden = !retry;
}

function renderComparison() {
  const rank = $("rank");
  const down = $("down");
  const rec = $("rec");
  rank.innerHTML = "";
  down.innerHTML = "";
  $("comp-note").hidden = true;
  $("comp-fail").hidden = true;
  $("comp-fail").innerHTML = "";
  $("comp-empty").hidden = true;
  $("down-h").hidden = true;
  $("down-sub").hidden = true;

  if (!state.lines.length) {
    $("comp-meta").textContent = "Sin cesta todavía";
    rec.hidden = true;
    compEmpty("Todavía no hay cesta: añade un artículo y las seis cadenas compiten por tu compra.", false);
    return;
  }
  if (!state.basket) {
    rec.hidden = true;
    $("comp-meta").textContent = "Comparando…";
    if (state.basketError) {
      compEmpty("No se pudo comparar: " + state.basketError + ". Reintenta: la comparación no inventa precios.", true);
    } else {
      rank.setAttribute("aria-busy", "true");
      rank.innerHTML = skeletonRows(3, "rank");
    }
    return;
  }
  rank.removeAttribute("aria-busy");

  const basket = state.basket;
  const failed = basket.chains.filter((c) => c.error).map(failureOf);
  const answered = basket.chains.filter((c) => !c.error);
  const units = state.lines.reduce((n, l) => n + l.quantity, 0);
  const complete = basket.ranking.length;
  $("comp-meta").textContent = state.lines.length + (state.lines.length === 1 ? " línea · " : " líneas · ") +
    units + (units === 1 ? " unidad · " : " unidades · ") +
    complete + (complete === 1 ? " cadena completa" : " cadenas completas") + " · " +
    failed.length + " sin respuesta";

  // No chain answered at all: the comparison is empty for a stated reason, with a retry,
  // instead of a blank panel that looks broken.
  if (!answered.length) {
    rec.hidden = true;
    compEmpty("Hoy no respondió ninguna cadena: " + failPlain(failed) +
      ". Reintenta en un minuto; la comparación no inventa precios ni existencias.", true);
    announce("Ninguna cadena respondió a la cesta: " + failPlain(failed) + ".");
    return;
  }

  const best = basket.ranking.find((r) => !isDiscarded(r.store)) || null;
  if (best) {
    const rival = basket.ranking.find((r) => r.store !== best.store && !isDiscarded(r.store));
    rec.hidden = false;
    $("rec-chain").innerHTML = logo(best.store);
    $("rec-total").textContent = money(best.totalCents);
    const coverage = state.lines.length === 1 ? "la única línea" : "las " + state.lines.length + " líneas";
    $("rec-meta").textContent = rival
      ? money(rival.totalCents - best.totalCents) + " menos que " + nameOf(rival.store) + " · cubre " + coverage
      : "es la única cadena que cubre " + coverage;
  } else {
    rec.hidden = true;
  }

  // Every chain that covers the whole basket, cheapest first, with the delta against the
  // headline. A discarded store stays visible, struck through, instead of disappearing.
  basket.ranking.forEach((r, i) => {
    const chain = basket.chains.find((c) => c.store === r.store);
    if (!chain) return;
    const discarded = isDiscarded(r.store);
    const isBest = Boolean(best && r.store === best.store);
    const delta = isBest ? ICON.check + "la más barata"
      : best ? "+" + money(r.totalCents - best.totalCents) : "";
    const li = document.createElement("li");
    li.className = "row" + (discarded ? " is-out" : "") + (isBest ? " is-best" : "");
    li.dataset.chain = r.store;
    li.innerHTML = `<span class="pos" aria-hidden="true">${i + 1}</span>
      <span class="who">${logo(r.store)}<span class="st">${discarded ? "tienda descartada" : esc(STORE_HINT[r.store] || "")}</span></span>
      <span class="cov">${esc(chain.coverage || "")}</span>
      <span class="val">${money(r.totalCents)}</span>
      <span class="delta${isBest ? " is-best" : ""}">${delta}</span>`;
    rank.appendChild(li);
  });

  // Chains that did not answer: one compact line under the ranking, never a competitor row.
  if (failed.length) {
    const box = $("comp-fail");
    box.hidden = false;
    box.innerHTML = failBody(failed, "basket");
  }

  // Chains that answered but do not cover the basket keep their own section.
  const incomplete = answered.filter((c) => c.missing.length || c.stale.length);
  if (incomplete.length) {
    $("down-h").hidden = false;
    $("down-sub").hidden = false;
    for (const chain of incomplete) {
      const li = document.createElement("li");
      li.dataset.chain = chain.store;
      const why = [];
      if (chain.missing.length) why.push("le falta " + chain.missing.join(", "));
      if (chain.stale.length) why.push("sin confirmar " + chain.stale.join(", "));
      li.innerHTML = `<span class="who">${logo(chain.store)}
          <span class="why">${esc(chain.coverage || "")} · ${esc(why.join(" · "))}</span></span>
        <span class="end">${chain.stale.length
          ? '<span class="tag tag-warn">' + ICON.warn + "Sin confirmar</span>"
          : '<span class="tag">' + ICON.warn + "Falta un artículo</span>"}</span>`;
      down.appendChild(li);
    }
  }
  if (basket.ranking.length && !best) {
    $("comp-note").hidden = false;
    $("comp-note").textContent = "Has descartado todas las tiendas completas: vuelve a marcar alguna para ver la recomendación.";
  }

  for (const li of document.querySelectorAll(".line[data-line]")) {
    const line = state.lines.find((l) => l.id === li.dataset.line);
    const text = li.querySelector(".line-note");
    if (line && text) text.textContent = lineNote(line);
  }

  if (best) {
    const rival = basket.ranking.find((r) => r.store !== best.store && !isDiscarded(r.store));
    announce("Cesta completa más barata: " + nameOf(best.store) + " " + money(best.totalCents) +
      (rival ? ", " + money(rival.totalCents - best.totalCents) + " menos que " + nameOf(rival.store) : "") + ".");
  } else if (!basket.ranking.length) {
    announce("Ninguna cadena cubre la cesta completa: la comparación sigue con lo que responde cada una.");
  }
}

document.addEventListener("click", (event) => {
  const btn = event.target.closest("[data-retry]");
  if (!btn) return;
  const what = btn.dataset.retry;
  if (what === "search" && state.lastTerm) { runSearch(state.lastTerm, true); return; }
  if (what === "candidates") { state.candidatesStale = true; ensureCandidates(true); return; }
  refresh(true, true);
});

loadCaps();
renderComparison();

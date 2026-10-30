/* Direction 03 «Tienda cercana», no framework and no external request: the postal code
   and the store cards come first, then the search and the basket, one section per chain.
   Everything the page knows about a chain comes from /api/caps or from the documents the
   server returns; nothing here invents a store, a zone or a price.

   S15: a product the shopper adds belongs to the basket of the chain it was added in, and
   to no other. Every entry this page sends is pinned to its chain, and each section holds
   only its own lines — there are no parallel baskets and no chain is «the cheapest» for a
   basket that spans several of them. */
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
  lidl: "Tienda física · ejemplo",
  aldi: "Sin tienda: el catálogo va por región",
  alcampo: "Compra online: el código postal se pide en su web",
  carrefour: "Precios de un comparador externo",
};
// The chip is a function of the zone mode alone, so a card says the mode once instead of
// writing it again in prose. One mode, one chip, four different texts.
const MODE_CHIP = {
  warehouse: "Zona del servidor (28041)",
  pickup: "Sin precio por tienda",
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
  // The shopping basket: the same shape as the brand mark, drawn here so the pin control
  // is an inline mark in currentColor — no file, no URL, no icon font (S14).
  basket: '<svg width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true" focusable="false"><path d="M2.6 6.4h14.8l-1.5 8.4a1.6 1.6 0 0 1-1.6 1.3H5.7a1.6 1.6 0 0 1-1.6-1.3L2.6 6.4Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/><path d="M7.3 6.3a2.7 2.7 0 0 1 5.4 0" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>',
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
  lines: [],          // [{key, store, id, name, quantity}]: one product in one chain
  basket: null,
  basketError: null,
  lastTerm: "",
  searched: null,    // {term, asked}: what the results count is a count of
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
// How many items one chain's basket holds: the count belongs to that chain, never to a
// basket that spans several (S15).
function countOf(n) {
  return n + (n === 1 ? " artículo" : " artículos");
}
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

// The one mechanism sentence is the capability table's own label, capitalized and closed:
// T29's wording verbatim, so no card states a mechanism the investigation did not prove.
function mechanism(zone) {
  const text = String(zone.label || "sin datos").replace(/[.·]+$/, "");
  return text.charAt(0).toUpperCase() + text.slice(1) + ".";
}

// The price scope, once, beside the logo: does the price follow the zone at all?
function zoneShort(zone) {
  return zone.price_scoped ? "precio según la zona" : "precio de catálogo";
}

// One card, four facts: the status header, the logo with the price scope, one mechanism
// sentence and one chip. The capability table supplies each of them once (T37).
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
  // The store line is the example the section disclaimer covers, so only a card that has
  // a store to choose keeps it; a chain with no store has no example to show.
  const body = `
      <span class="shop-check">${control}<span class="lab">${label}</span></span>
      <span class="shop-name">${logo(cap.store)}<span class="dist">${esc(zoneShort(zone))}</span></span>
      ${pickable ? `<span class="addr">${esc(STORE_HINT[cap.store] || "")}</span>` : ""}
      <span class="cap">${esc(mechanism(zone))}</span>`;
  li.innerHTML = (pickable ? `<label class="shop-label">${body}</label>` : `<span class="shop-label">${body}</span>`) +
    `<div class="shop-foot"><span class="tag ${tagClass}">${zone.price_scoped ? ICON.warn : ICON.check}${esc(MODE_CHIP[zone.mode] || "Sin zona")}</span></div>`;
  return li;
}

function updatePicked() {
  const chains = chainOrder();
  const pickable = chains.filter((s) => PICKABLE.has(s));
  const n = pickable.filter((s) => state.chosen[s]).length;
  // The counter names the capability before the selection, so next to a card that says
  // «sin tienda que elegir» it reads as a fact instead of a contradiction (T37).
  $("picked").textContent = pickable.length + " de " + chains.length +
    " cadenas permiten elegir tienda · " + n + (n === 1 ? " elegida" : " elegidas");
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

// The checkbox is the store choice: it moves the counter and strikes that chain's own basket
// in the comparison, which is why the counter reads as a sentence and stays live.
$("shops").addEventListener("change", (event) => {
  const box = event.target.closest("input[data-chain]");
  if (!box) return;
  const name = nameOf(box.dataset.chain);
  state.chosen[box.dataset.chain] = box.checked;
  updatePicked();
  announce(box.checked ? "Vuelve a competir la tienda de " + name + "." : "Descartada la tienda de " + name + " de la comparación.");
  renderComparison();
});

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

// Ten rows per chain is the page a shopper can read, and «ver más» asks the same query for
// a bigger one: the store's own relevance keeps deciding the order, we only ask it for
// more rows (T39). A chain that fills the page it was asked for may have more.
const PAGE = 10;
const PAGE_CAP = 100;
const nextLimit = (limit) => Math.min(limit * 2, PAGE_CAP);

// A hit's identity is the pair, not the id: each chain numbers its own catalogue, and a
// page appended twice must never draw a row twice.
const hitKey = (store, hit) => store + "/" + hit.id;
const seenHits = new Set();
const chainPages = {}; // store -> {term, limit}: how far that block has been paged

// fresh skips the server's 60 s cache: that is what «reintentar» means, since a cached
// failure would otherwise replay the same answer.
async function runSearch(term, fresh) {
  const hits = $("hits");
  $("search-status").textContent = "Buscando «" + term + "» en las seis cadenas…";
  hits.innerHTML = skeletonRows(3, "hit");
  hits.setAttribute("aria-busy", "true");
  seenHits.clear();
  for (const store of Object.keys(chainPages)) delete chainPages[store];
  try {
    const doc = await api("/api/search" + (fresh ? "?fresh=1" : ""), { term: term, limit: PAGE });
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
  state.searched = { term: term, asked: doc.results.length };
  for (const r of answered) hits.appendChild(hitChain(r, term));
  // A chain that did not answer is one compact line at the end, not a block that looks
  // like a result: the same rule the comparison follows.
  if (failed.length) {
    const li = document.createElement("li");
    li.className = "fail-line";
    li.innerHTML = failBody(failed, "search");
    hits.appendChild(li);
  }
  updateSearchStatus();
}

// The count is live and counts what is on the page, so appending a page of one chain moves
// it: the same aria-live line the visitor already hears from the basket.
function updateSearchStatus() {
  const s = state.searched;
  if (!s) return;
  const n = seenHits.size;
  $("search-status").textContent = n + (n === 1 ? " resultado" : " resultados") +
    " en " + Object.keys(chainPages).length + " de " + s.asked + " cadenas para «" + s.term + "».";
}

// The pin is an icon-only button: the basket mark is the whole label, so its accessible name
// carries both the product and the chain, and the title carries the same words for the mouse
// (an icon with no name is not a control, S14). The action is unchanged.
function hitRow(store, h, term) {
  const name = nameOf(store);
  const label = "Añadir " + (h.size ? h.name + " " + h.size : h.name) + " a la cesta de " + name;
  return `<li>
      <span class="pname">${esc(h.name)}${h.size ? " · " + esc(h.size) : ""}</span>
      <span class="pprice">${h.price == null ? "sin precio" : euros(h.price)}</span>
      <button class="btn btn-quiet btn-icon" type="button" data-pin="${esc(store)}" data-id="${esc(h.id)}" data-name="${esc(h.name)}" aria-label="${esc(label)}" title="${esc(label)}">${ICON.basket}</button>
    </li>`;
}

function hitChain(r, term) {
  const li = document.createElement("li");
  li.className = "hit-chain";
  li.dataset.chain = r.store;
  li.innerHTML = `<div class="hit-head">${logo(r.store)}
      <span class="tag" data-count></span></div>
    <ul class="hit-products"></ul>
    <p class="hit-more" hidden></p>`;
  chainPages[r.store] = { term: term, limit: PAGE };
  appendHits(li, r, term);
  return li;
}

// Appends only what the block has not shown, then decides the control's fate: ask for more
// only while the chain filled the page it was asked for and the cap still leaves room. A
// chain that came up short, or reached the cap, gets a line instead of a dead button.
function appendHits(el, r, term) {
  const list = el.querySelector(".hit-products");
  let added = 0;
  for (const h of r.hits) {
    const key = hitKey(r.store, h);
    if (seenHits.has(key)) continue;
    seenHits.add(key);
    list.insertAdjacentHTML("beforeend", hitRow(r.store, h, term));
    added++;
  }
  if (!list.children.length) list.innerHTML = '<li class="hit-down">Sin resultados para este término.</li>';
  el.querySelector("[data-count]").textContent = r.count + (r.count === 1 ? " resultado" : " resultados");
  const page = chainPages[r.store];
  const more = el.querySelector(".hit-more");
  more.hidden = !r.count;
  if (r.hits.length < page.limit || page.limit >= PAGE_CAP) {
    more.innerHTML = "Sin más resultados en " + nameOf(r.store) + ".";
  } else {
    more.innerHTML = `<button class="btn btn-quiet btn-small" type="button" data-more="${esc(r.store)}">Ver más en ${nameOf(r.store)}</button>`;
  }
  return added;
}

// «ver más» is one request for the same term and a bigger page: no sort, no filter, no
// brand rule, so the order the shopper reads is still the store's own.
async function pageMore(store, btn) {
  const page = chainPages[store];
  if (!page) return;
  const name = nameOf(store);
  const chain = btn.closest(".hit-chain");
  const limit = nextLimit(page.limit);
  btn.disabled = true;
  btn.textContent = "Pidiendo más…";
  try {
    const doc = await api("/api/search", { term: page.term, limit: limit });
    const r = doc.results.find((x) => x.store === store);
    if (!r) throw new Error("la cadena no vino en la respuesta");
    if (r.error) throw new Error(failWhy(failureOf(r).code));
    page.limit = limit;
    const added = appendHits(chain, r, page.term);
    updateSearchStatus();
    announce(added
      ? added + (added === 1 ? " resultado más en " : " resultados más en ") + name + "."
      : "Sin más resultados en " + name + ".");
  } catch (err) {
    chain.querySelector(".hit-more").innerHTML =
      `<span class="hit-down">No se pudo pedir más a ${name}: ${esc(err.message)}</span> ` +
      `<button class="linkbtn" type="button" data-more="${esc(store)}">reintentar</button>`;
  }
}

$("search-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const term = $("q").value.trim();
  if (!term) { $("search-status").textContent = "Escribe qué quieres comprar."; $("q").focus(); return; }
  runSearch(term);
});

$("hits").addEventListener("click", (event) => {
  const more = event.target.closest("[data-more]");
  if (more) { pageMore(more.dataset.more, more); return; }
  const pin = event.target.closest("[data-pin]");
  if (pin) {
    addLine(pin.dataset.pin, pin.dataset.id, pin.dataset.name);
  }
});

/* ---------------- basket ---------------- */

// S15: every entry the page sends is the product the shopper picked, bound to the chain it
// was picked in — {line, store, id, name, quantity}. There is no automatic entry, so no line
// is resolved in a chain the shopper did not choose it in: no parallel baskets.
function basketItems() {
  return state.lines.map((line) => ({
    line: line.key, store: line.store, id: line.id, name: line.name, quantity: line.quantity,
  }));
}

// A line is one product in one chain: the pair is its identity, so adding the same product
// twice raises its quantity instead of drawing the same row again.
const lineKey = (store, id) => store + "/" + id;

function addLine(store, id, name) {
  if (!store || !id || !name) return;
  const key = lineKey(store, id);
  const line = state.lines.find((l) => l.key === key);
  if (line) line.quantity = Math.min(9, line.quantity + 1);
  else state.lines.push({ key: key, store: store, id: id, name: name, quantity: 1 });
  $("add-status").textContent = "«" + name + "» en la cesta de " + nameOf(store) + ".";
  renderLines();
  refresh(false);
}

function dropLine(key) {
  const line = state.lines.find((l) => l.key === key);
  if (!line) return;
  state.lines = state.lines.filter((l) => l.key !== key);
  $("add-status").textContent = "«" + line.name + "» fuera de la cesta de " + nameOf(line.store) + ".";
  renderLines();
  refresh(false);
}

function basketItem(line) {
  if (!state.basket) return null;
  return state.basket.items.find((it) => it.line === line.key) || null;
}

// The chain is named on both ends of the quantity control: «+ de leche» alone would be
// ambiguous from a screen reader (S14).
function stepperHTML(line) {
  const where = esc(line.name) + " en " + esc(nameOf(line.store));
  return `<button type="button" data-step="-1" aria-label="Quitar una unidad de ${where}"${line.quantity <= 1 ? " disabled" : ""}>−</button>
    <span class="q">${line.quantity}</span>
    <button type="button" data-step="1" aria-label="Añadir una unidad de ${where}"${line.quantity >= 9 ? " disabled" : ""}>+</button>`;
}

// What this chain charges for this line, in its own cents: the product it publishes today,
// the line total and the fact that the shopper chose it by hand. A chosen product that is
// no longer published says so instead of showing a price (S13).
function sectionNote(line, r) {
  if (!r) return state.basket ? "sin confirmar hoy: el producto elegido no está en su catálogo" : "pidiendo el precio…";
  const parts = [];
  if (line.quantity > 1) parts.push(money(r.unitCents) + "/ud");
  parts.push(money(r.lineCents));
  parts.push("elegido a mano");
  return parts.join(" · ");
}

// One line inside the section of the chain that owns it, with the way out of the basket:
// there is no control that moves it into another chain, because that basket would be the
// parallel one S15 forbids.
function sectionLine(line, store) {
  const it = basketItem(line);
  const r = it && it.resolved[store];
  return `<li class="line" data-line="${esc(line.key)}" data-store="${esc(store)}">
    <div class="line-head">
      <span>
        <span class="line-name">${esc(r ? r.name : line.name)}</span>
        <span class="line-note">${esc(sectionNote(line, r))}</span>
      </span>
      <span class="line-end">
        <span class="stepper">${stepperHTML(line)}</span>
        <button class="linkbtn" type="button" data-drop="${esc(line.key)}" aria-label="Quitar ${esc(line.name)} de la cesta">quitar</button>
      </span>
    </div>
  </li>`;
}

// The basket, read per supermarket and owned: a section holds only the lines added from
// that chain, with its mark, the payload's subtotal and the state of a chosen product that
// is gone. Nothing is copied into a chain the shopper did not choose (S15).
function chainSection(chain) {
  const store = chain.store;
  const lines = state.lines.filter((line) => line.store === store);
  const sub = chain.totalCents == null ? "—" : money(chain.totalCents);
  const li = document.createElement("li");
  li.className = "basket-chain";
  li.dataset.chain = store;
  li.innerHTML = `
    <div class="bc-head">
      ${logo(store)}
      <span class="bc-name">${esc(nameOf(store))}</span>
      ${chain.stale && chain.stale.length ? `<span class="tag tag-warn">${ICON.warn}Sin confirmar</span>` : ""}
      <span class="bc-sub">${sub}</span>
    </div>
    <ul class="bc-lines">${lines.map((line) => sectionLine(line, store)).join("")}</ul>
    ${lines.length ? "" : '<p class="bc-none">Hoy esta cadena no resuelve ninguna línea de tu cesta.</p>'}`;
  return li;
}

// One section per chain that holds something: the chains that answered and own at least
// one line. Before the first answer the sections are only named, with no invented total.
function sectionsFor() {
  const owned = new Set(state.lines.map((line) => line.store));
  if (state.basket) return state.basket.chains.filter((c) => !c.error && owned.has(c.store));
  return chainOrder().filter((s) => owned.has(s)).map((store) => ({ store: store, totalCents: null }));
}

// The panel is rebuilt only when the shape changes — a line added or removed, a chain's
// answer arriving or failing, the set of sections moving — so a stepper keeps its focus
// while the prices refresh underneath.
function linesKey() {
  const per = state.lines.map((line) => {
    const it = basketItem(line);
    return line.key + ":" + (it ? Object.keys(it.resolved).sort().join("+") : "?");
  }).join("|");
  const failed = state.basket ? state.basket.chains.filter((c) => c.error).map((c) => c.store).join(",") : "?";
  return per + "#" + failed;
}

let renderedKey = "";

function syncLines() {
  // The subtotal is the payload's, moved into place when a quantity changes: the section is
  // not rebuilt, so the stepper under the finger keeps its focus.
  for (const sec of document.querySelectorAll(".basket-chain[data-chain]")) {
    const chain = state.basket && state.basket.chains.find((c) => c.store === sec.dataset.chain);
    if (!chain) continue;
    const sub = sec.querySelector(".bc-sub");
    if (sub) sub.textContent = chain.totalCents == null ? "—" : money(chain.totalCents);
  }
  for (const el of document.querySelectorAll(".line[data-line]")) {
    const line = state.lines.find((l) => l.key === el.dataset.line);
    if (!line) continue;
    const q = el.querySelector(".q");
    if (q) q.textContent = line.quantity;
    for (const btn of el.querySelectorAll("button[data-step]")) {
      btn.disabled = Number(btn.dataset.step) < 0 ? line.quantity <= 1 : line.quantity >= 9;
    }
    const it = basketItem(line);
    const r = it && it.resolved[el.dataset.store];
    const name = el.querySelector(".line-name");
    if (name) name.textContent = r ? r.name : line.name;
    const note = el.querySelector(".line-note");
    if (note) note.textContent = sectionNote(line, r);
  }
}

function renderLines() {
  const list = $("lines");
  // A rebuild must not drop the keyboard: remember which stepper of which line had the
  // focus and put it back on the same one.
  const active = document.activeElement && list.contains(document.activeElement) ? document.activeElement : null;
  const box = active && active.closest(".line");
  const focus = box && active.dataset.step ? { key: box.dataset.line, step: active.dataset.step } : null;
  list.innerHTML = "";
  renderedKey = linesKey();
  if (!state.lines.length) {
    list.innerHTML = '<li class="empty">Todavía no hay líneas. Busca un artículo y añádelo desde la fila de su cadena.</li>';
    return;
  }
  if (!state.basket) list.innerHTML = '<li class="empty">Comparando tu cesta en las seis cadenas…</li>';
  for (const chain of sectionsFor()) list.appendChild(chainSection(chain));
  const failed = state.basket ? state.basket.chains.filter((c) => c.error).map(failureOf) : [];
  if (failed.length) {
    const li = document.createElement("li");
    li.className = "fail-line";
    li.innerHTML = failBody(failed, "basket");
    list.appendChild(li);
  }
  if (focus) {
    const line = [...list.querySelectorAll(".line")].find((l) => l.dataset.line === focus.key);
    const target = line && line.querySelector('button[data-step="' + focus.step + '"]');
    if (target) target.focus();
  }
}

$("lines").addEventListener("click", (event) => {
  const drop = event.target.closest("[data-drop]");
  if (drop) { dropLine(drop.dataset.drop); return; }
  const btn = event.target.closest("button[data-step]");
  if (!btn) return;
  const line = state.lines.find((l) => l.key === btn.closest(".line").dataset.line);
  if (!line) return;
  line.quantity = Math.min(9, Math.max(1, line.quantity + Number(btn.dataset.step)));
  renderLines();
  refresh(false);
});

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

// S15: the chains that hold at least one of the shopper's lines, each with its own basket.
// Two baskets of different products are not comparable, so nothing here is ranked and no
// chain is called the cheapest for goods it was never given.
function chainBaskets() {
  const owned = new Set(state.lines.map((line) => line.store));
  return state.basket.chains
    .filter((c) => !c.error && owned.has(c.store))
    .map((c) => ({ chain: c, lines: state.lines.filter((line) => line.store === c.store) }));
}

// One chain's own basket: its mark, how many items it holds and what it costs. No position
// and no delta against another chain — those would rank baskets that hold different goods.
function comparisonRow(chain, lines) {
  const discarded = isDiscarded(chain.store);
  const li = document.createElement("li");
  li.className = "row" + (discarded ? " is-out" : "");
  li.dataset.chain = chain.store;
  const why = discarded ? " · tienda descartada" : chain.stale.length ? " · sin confirmar" : "";
  li.innerHTML = `<span class="who">${logo(chain.store)}<span class="st">${esc(countOf(lines.length))}${esc(why)}</span></span>
      <span class="val">${chain.totalCents == null ? "—" : money(chain.totalCents)}</span>`;
  return li;
}

function renderComparison() {
  const rank = $("rank");
  rank.innerHTML = "";
  $("comp-fail").hidden = true;
  $("comp-fail").innerHTML = "";
  $("comp-empty").hidden = true;

  // The basket panel follows the same answer: the sections appear when the chains answer,
  // and a price moves in place without stealing the focus of a stepper.
  if (linesKey() !== renderedKey) renderLines();
  else syncLines();

  if (!state.lines.length) {
    $("comp-meta").textContent = "Sin cesta todavía";
    compEmpty("Todavía no hay cesta: busca un producto y añádelo desde la fila de la cadena que lo vende.", false);
    return;
  }
  if (!state.basket) {
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
  const baskets = chainBaskets();
  $("comp-meta").textContent = countOf(state.lines.length) + " · " + units +
    (units === 1 ? " unidad · " : " unidades · ") + baskets.length +
    (baskets.length === 1 ? " cesta de supermercado · " : " cestas de supermercado · ") +
    failed.length + " sin respuesta";

  // No chain answered at all: the comparison is empty for a stated reason, with a retry,
  // instead of a blank panel that looks broken.
  if (!answered.length) {
    compEmpty("Hoy no respondió ninguna cadena: " + failPlain(failed) +
      ". Reintenta en un minuto; la comparación no inventa precios ni existencias.", true);
    announce("Ninguna cadena respondió a la cesta: " + failPlain(failed) + ".");
    return;
  }

  // One row per chain that holds something, in the table's order: there is no cheapest
  // basket, because each of these rows prices a different set of goods.
  for (const { chain, lines } of baskets) rank.appendChild(comparisonRow(chain, lines));

  // Chains that did not answer: one compact line under the rows, never a competitor row.
  if (failed.length) {
    const box = $("comp-fail");
    box.hidden = false;
    box.innerHTML = failBody(failed, "basket");
  }

  announce(baskets.length
    ? "Una cesta por supermercado: " + baskets.map((b) => nameOf(b.chain.store) + ", " + countOf(b.lines.length) + " por " + money(b.chain.totalCents)).join("; ") + "."
    : "Cada producto vive en la cesta de su supermercado; hoy no respondió ninguna de esas cadenas.");
}

document.addEventListener("click", (event) => {
  const btn = event.target.closest("[data-retry]");
  if (!btn) return;
  if (btn.dataset.retry === "search" && state.lastTerm) { runSearch(state.lastTerm, true); return; }
  refresh(true, true);
});

loadCaps();
renderComparison();

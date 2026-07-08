// overview.js — renderar den enda vyn (Översikt) och kopplar interaktionerna.
// All data kommer från GET /api/overview; SSE-eventet "overview" är bara en
// ping som triggar refetch. Öppet/stängt-läge hålls i minnes-Sets så
// omrenderingar inte fäller ihop pågående arbete.

import { get, post, onEvent, connectSSE, toast, act, debounce, esc, ApiError } from './api.js';
import { initShell, setWorkerState } from './shell.js';
import { initSickan } from './sickan.js';

const $ = (id) => document.getElementById(id);
const openOrders = new Set();
const openRows = new Set(); // utfällda artikelrader (per delivery_row_id, som sträng)
let ov = null; // senaste overview-svaret
let orderQuery = ''; // fritextfiltret från toppbarens sökruta (rå, trimmas i renderOrders)

// localToday: dagens datum i LOKAL tid som YYYY-MM-DD. toISOString() ger
// UTC-dygnet, vilket felbucketar Försenade/Idag/Kommande mellan midnatt och
// 01/02 svensk tid.
function localToday() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

// fmtLocal: RFC3339-UTC-stämpel → "YYYY-MM-DD HH:MM" i lokal tid. Rå slice av
// UTC-strängen visade tider 1–2 h fel året runt.
function fmtLocal(ts) {
  if (!ts) return '';
  const d = new Date(ts);
  if (isNaN(d)) return ts;
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} `
    + `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

// pendingReload: en omrendering som sköts upp för att användaren höll på att
// skriva i ett fält (omrender kastar all pågående inmatning). Körs i kapp vid
// focusout. Deklareras FÖRE init-anropet av load() nedan — annars dör första
// renderingen i TDZ ("Cannot access 'pendingReload' before initialization")
// och sidan förblir tom tills första SSE-pingen.
let pendingReload = false;

initShell();
initSickan();
onEvent('overview', debounce(load, 250));
// SSE-avbrott: overview-pingar under avbrottet är förlorade — refetcha vid
// återanslutning så vyn inte blir stående stale.
onEvent('sse-open', ({ reconnect }) => { if (reconnect) load(); });
connectSSE();
load();

function overviewHasFocusedInput() {
  const a = document.activeElement;
  return !!a && (a.tagName === 'INPUT' || a.tagName === 'TEXTAREA') && !!a.closest('main');
}

async function load() {
  if (overviewHasFocusedInput()) {
    pendingReload = true;
    return;
  }
  pendingReload = false;
  try {
    ov = await get('/api/overview');
  } catch (err) {
    toast('Kunde inte hämta översikten: ' + err.message, true);
    return;
  }
  setWorkerState(ov.running);
  $('lastSync').textContent = ov.last_sync ? 'sync ' + fmtLocal(ov.last_sync) : '';
  renderBanner();
  renderUnlinked();
  renderOrders();
  renderSaved();
  renderTasks();
}

function renderBanner() {
  const b = $('banner');
  if (!ov.errors.length) { b.classList.add('hidden'); return; }
  b.innerHTML = `
    <span class="banner-text">⚠️ Intagsfel (ligger kvar i inkorgen): ${esc(ov.errors.join(' · '))}</span>
    <button class="banner-close" data-action="ack-errors"
            title="Kvittera — göm de här felen (nya fel visas fortfarande)">✕</button>`;
  b.classList.remove('hidden');
}

// ---------------------------------------------------------------------------
// Okopplade cert
// ---------------------------------------------------------------------------

function renderUnlinked() {
  const list = ov.unlinked_certs;
  $('unlinkedSection').classList.toggle('hidden', !list.length);
  $('unlinkedCount').textContent = list.length ? `(${list.length})` : '';
  $('unlinkedList').innerHTML = list.map(unlinkedCard).join('');
}

function unlinkedCard(c) {
  const sugg = c.suggestions.map((s) => `
    <span class="chip">${esc(s.order_number)}${s.part_number ? ' · ' + esc(s.part_number) : ''}
      ${s.delivery_date ? ' · ' + esc(s.delivery_date) : ''} — koppla?
      <button data-action="confirm-suggestion" data-cert="${c.id}" data-row="${s.delivery_row_id}"
              data-order="${esc(s.order_number)}" title="Bekräfta">✓</button>
      <button data-action="reject-link" data-link="${s.link_id}" title="Avvisa">✕</button>
    </span>`).join('');
  // Fria bekräftade kopplingar (B-nummer utan rad i Monitor-fönstret ännu):
  // certet är kopplat och sparbart härifrån; raden tar över när den dyker upp.
  const free = (c.free_orders || []).map((o) => `
    <span class="chip">🔗 ${esc(o)} <span class="muted small">bekräftad — utanför Monitor-fönstret</span></span>`).join('');
  return `
  <div class="card">
    <div class="origname">📄 <a href="/api/pdf?cert_id=${c.id}" target="_blank"
        title="Ursprungligt filnamn — förhandsvisar ofta charge/heat">${esc(c.original_filename)}</a></div>
    <div class="copyline">
      charge <span class="copy" data-action="copy">${esc(c.effective.charge) || '—'}</span> ·
      ${esc(c.effective.material) || '—'} · ${esc(c.effective.dimensions) || '—'} ·
      B-nr: <span class="copy" data-action="copy">${esc(c.effective_b_numbers.join(' ')) || '—'}</span>
      ${c.issues.length ? `<span class="badge warn" title="${esc(c.issues.join('; '))}">⚠ ${c.issues.length}</span>` : ''}
    </div>
    ${certQualityWarnings(c)}
    ${certExtraLine(c)}
    ${certGetingeBadges(c)}
    ${free}
    ${sugg}
    <div class="inline-form">
      <input type="text" placeholder="B-nummer, t.ex. B127575" data-linkinput="${c.id}"
             pattern="[Bb]\\d{6}" title="B + sex siffror">
      <button class="btn btn-small" data-action="link-free" data-cert="${c.id}">🔗 Koppla</button>
      <button class="btn btn-small" data-action="archive-cert" data-cert="${c.id}"
              title="Inte ett cert / irrelevant">🗑 Arkivera</button>
    </div>
    ${nameLine(c)}
  </div>`;
}

// ---------------------------------------------------------------------------
// Ordrar (grupperade, kategorisektioner)
// ---------------------------------------------------------------------------

const CATS = [
  ['forsenade', '⏰ Försenade'],
  ['idag', '📦 Väntas idag'],
  ['kommande', '🔜 Kommande'],
  ['levererade', '✓ Levererade utan sparat cert'],
];

// rowMatches: träffar sökningen den HÄR artikelraden (rad-nivå-fälten)? Används
// för sök-avslöjning — en artikel-träff fäller ut just den raden. q förväntas
// redan lowercased och trimmat.
function rowMatches(r, q) {
  return [r.part_number, r.description, r.req_material]
    .filter(Boolean).join(' ').toLowerCase().includes(q);
}

// orderMatches: skiftlägesokänslig delsträngsmatchning över ordernummer +
// leverantör + varje rads artikeldata (som V1:s #upcomingFilter). q förväntas
// redan lowercased och trimmat.
function orderMatches(g, q) {
  const hay = [g.order_number, g.supplier_name,
    ...g.rows.flatMap((r) => [r.part_number, r.description, r.req_material])]
    .filter(Boolean).join(' ').toLowerCase();
  return hay.includes(q);
}

function renderOrders() {
  const today = localToday();
  const q = orderQuery.trim().toLowerCase();
  const source = q ? ov.orders.filter((g) => orderMatches(g, q)) : ov.orders;
  const byCat = { forsenade: [], idag: [], kommande: [], levererade: [] };
  for (const g of source) {
    const cat = categorize(g, today);
    if (cat) byCat[cat].push(g);
  }
  let html = '';
  let shown = 0;
  for (const [key, label] of CATS) {
    if (!byCat[key].length) continue;
    shown += byCat[key].length;
    html += `<div class="cat-head">${label}</div>`;
    html += byCat[key].map(orderCard).join('');
  }
  $('ordersList').innerHTML = html;
  $('ordersCount').textContent = q ? `${shown}/${ov.orders.length}` : (shown ? `(${shown})` : '');
  $('ordersEmpty').textContent = q
    ? 'Inga träffar.'
    : 'Inga orderrader ännu — kör 🔄 Uppdatera för att hämta från Monitor.';
  $('ordersEmpty').classList.toggle('hidden', shown > 0);
  if (q) $('ordersSection').open = true; // annars rör vi inte användarens infäll-läge
}

// categorize: null = göm (allt levererat och alla cert sparade — klar).
function categorize(g, today) {
  const active = g.rows.filter((r) => !r.delivered);
  if (!active.length) {
    const unsaved = g.rows.some((r) => r.cert_required &&
      !r.links.some((l) => l.cert && l.cert.status === 'sparad'));
    return unsaved ? 'levererade' : null;
  }
  const dates = active.map((r) => r.delivery_date).filter(Boolean).sort();
  if (!dates.length) return 'kommande';
  if (dates[0] < today) return 'forsenade';
  if (dates[0] === today) return 'idag';
  return 'kommande';
}

function orderCard(g) {
  const q = orderQuery.trim().toLowerCase();
  // Sök-avslöjning: har ordern en artikel som matchar? Fäll då ut den (och nedan
  // just den raden) så artikeln syns direkt. En ren order-/leverantörsträff
  // (t.ex. B-nummer i order_number) räcker med rubriken → lämnas infälld. Rör
  // aldrig användarens sparade openOrders/openRows-läge.
  const open = openOrders.has(g.order_number) || (!!q && g.rows.some((r) => rowMatches(r, q)));
  const nRows = g.rows.length;
  const mismatch = g.rows.some((r) => r.links.some((l) => l.material_ok === 'mismatch'));
  const missing = g.rows.some((r) => r.cert_required && !r.links.length);
  const badges = [
    mismatch ? '<span class="badge bad">⚠ materialavvikelse</span>' : '',
    missing ? '<span class="badge warn">cert saknas</span>' : '',
  ].join('');
  return `
  <div class="card">
    <div class="order-head ${open ? 'open' : ''}" data-action="toggle-order" data-order="${esc(g.order_number)}">
      <span class="chev">▶</span>
      <span class="order-title">${esc(g.order_number)}</span>
      <span class="muted">${esc(g.supplier_name)}</span>
      <span class="muted small">${nRows} rad${nRows === 1 ? '' : 'er'}</span>
      <span class="order-badges">${badges}</span>
    </div>
    ${open ? `<div class="order-rows">${g.rows.map(artRow).join('')}</div>` : ''}
  </div>`;
}

// Kompakt statusbadge för en infälld artikelrad — så man ser cert-läget utan
// att fälla ut raden: materialavvikelse (röd), sparat cert (grön) eller
// cert saknas (gul, bara när cert krävs och inget är bekräftat).
function rowStatusBadge(r) {
  if (r.links.some((l) => l.material_ok === 'mismatch')) return '<span class="badge bad">⚠ material</span>';
  if (r.links.some((l) => l.cert && l.cert.status === 'sparad')) return '<span class="badge ok">✓ cert</span>';
  if (r.cert_required && !r.links.some((l) => l.status === 'bekraftad' && l.cert)) return '<span class="badge warn">cert saknas</span>';
  return '';
}

function artRow(r) {
  const today = localToday();
  const late = r.delivery_date && r.delivery_date < today && !r.delivered;
  const activeLinks = r.links; // avfärdade filtreras redan av servern
  const q = orderQuery.trim().toLowerCase();
  // Sök-avslöjning: en artikel-träff fäller ut raden så man ser den direkt.
  const open = openRows.has(String(r.delivery_row_id)) || (!!q && rowMatches(r, q));
  return `
  <div class="artrow">
    <div class="artrow-head ${open ? 'open' : ''}" data-action="toggle-row" data-row="${r.delivery_row_id}">
      <span class="chev">▶</span>
      <span class="partnum">${esc(r.part_number)}</span>
      <span>${esc(r.description)}</span>
      <span class="muted">${r.planned_qty} st</span>
      <span class="${late ? 'date-late' : 'muted'}">${esc(r.delivery_date)}${late ? ' ⏰' : ''}</span>
      ${r.delivered ? '<span class="badge ok">✓ levererad</span>' : ''}
      ${!r.in_monitor ? '<span class="badge" title="Raden fanns inte i senaste Monitor-hämtningen">utanför fönstret</span>' : ''}
      <span class="artrow-badges">${rowStatusBadge(r)}</span>
    </div>
    ${open ? `<div class="artrow-body">
    ${r.extra_description ? `<div class="extra-desc">Extra benämning: ${esc(r.extra_description)}</div>` : ''}
    ${reqTextSection(r)}
    ${drawingSection(r)}
    ${activeLinks.map((l) => linkBlock(r, l)).join('')}
    ${!activeLinks.some((l) => l.status === 'bekraftad' && l.cert) && rowHasReq(r)
      ? `<div class="kravonly">${cmpTable(r, null, null)}</div>` : ''}
    ${r.cert_required && !activeLinks.length ? '<div class="hint">Inget cert kopplat ännu.</div>' : ''}
    ${notesBlock('order_row', r.delivery_row_id, r.notes, r.order_number, r.part_number)}
    <div class="row-actions">
      <button class="btn btn-small" data-action="mark-delivered" data-row="${r.delivery_row_id}"
              data-delivered="${r.delivered ? 'false' : 'true'}">${r.delivered ? '↩ Ångra levererad' : '✓ Levererad'}</button>
    </div>
    </div>` : ''}
  </div>`;
}

function linkBlock(r, l) {
  const c = l.cert;
  if (!c) return '';
  if (l.status === 'foreslagen') {
    return `<span class="chip">Förslag: 📄 ${esc(c.original_filename)} — koppla?
      <button data-action="confirm-suggestion" data-cert="${c.id}" data-row="${l.delivery_row_id}"
              data-order="${esc(l.order_number)}" title="Bekräfta">✓</button>
      <button data-action="reject-link" data-link="${l.id}" title="Avvisa">✕</button>
    </span>`;
  }
  return `
  <div class="certblock ${c.status === 'sparad' ? 'frozen' : ''}">
    ${cmpTable(r, l, c)}
    ${certQualityWarnings(c)}
    <div class="origname">📄 <a href="/api/pdf?cert_id=${c.id}" target="_blank"
        title="Ursprungligt filnamn — förhandsvisar ofta charge/heat">${esc(c.original_filename)}</a></div>
    <div class="copyline">
      Charge <span class="copy" data-action="copy" title="Kopiera">${esc(c.effective.charge) || '—'}</span> ·
      B-nr <span class="copy" data-action="copy" title="Kopiera">${esc(c.effective_b_numbers.join(' ')) || '—'}</span>
      <span class="editcell" data-edit="b_numbers" data-cert="${c.id}"
            data-value="${esc(c.effective_b_numbers.join(', '))}" title="Redigera B-nummer">✎</span>
      ${l.status === 'bekraftad' && c.status !== 'sparad'
        ? `<button class="btn btn-small" data-action="reject-link" data-link="${l.id}" title="Koppla loss">✕ koppla loss</button>` : ''}
    </div>
    ${certExtraLine(c)}
    ${certGetingeBadges(c)}
    ${l.ai_notes ? `<div class="hint">🤖 ${esc(l.ai_notes)}</div>` : ''}
    ${nameLine(c)}
  </div>`;
}

// ---------------------------------------------------------------------------
// Nya order_rows-fält (Task 6): rå kravtext + artikeldata från Monitor.
// AI-parsning av kravtexterna kommer i en senare task — här visas de bara
// oförädlade. Kompakt sektion, gömd helt om allt är tomt.
// ---------------------------------------------------------------------------

function alloyText(r) {
  if (r.alloy_code && r.alloy_description) return `${r.alloy_code} — ${r.alloy_description}`;
  return r.alloy_code || r.alloy_description || '';
}

function reqTextSection(r) {
  const parts = [
    ['Godsmeddelande', r.receiving_message],
    ['Mottagningskontroll (rad)', r.receiving_inspection_instruction],
    ['Mottagningsinstruktion (artikel)', r.part_receiving_instruction],
    ['Inköpskommentar', r.part_purchase_comment],
    ['Artikelkommentar', r.part_comment],
    ['Godsmärke (rad)', r.row_goods_label],
    ['Godsmärke (order)', r.order_goods_label],
    ['Radnotering', r.row_notes],
    ['Fritext', r.free_text],
    ['Extern kommentar', r.external_comment],
    ['Legering', alloyText(r)],
  ].filter(([, v]) => v);
  if (!parts.length) return '';
  return `<div class="copyline">${parts.map(([label, v]) => `${label} <span class="muted">${esc(v)}</span>`).join(' · ')}</div>`;
}

// Ritningslänk: http(s) → klickbar länk (ny flik); file:// eller UNC-sökväg
// (\\server\share\...) → kopierbar sökväg, eftersom webbläsare blockerar
// file:// från http-sidor. Ritningsnummer (Monitors + leverantörens) visas
// som text intill när de finns.
function drawingSection(r) {
  const hyperlinks = r.hyperlinks || [];
  const nums = [];
  if (r.drawing_numbers) nums.push(r.drawing_numbers);
  if (r.supplier_drawing_number) {
    nums.push(r.supplier_drawing_number + (r.supplier_revision_number ? ' rev ' + r.supplier_revision_number : ''));
  }
  if (!hyperlinks.length && !nums.length) return '';
  const bits = [];
  if (nums.length) bits.push(`Ritningsnr <span class="muted">${esc(nums.join(' · '))}</span>`);
  bits.push(...hyperlinks.map(drawingLinkItem));
  return `<div class="copyline">${bits.join(' · ')}</div>`;
}

function drawingLinkItem(h) {
  const label = esc(h.description || 'Ritning');
  const link = h.link || '';
  if (/^https?:\/\//i.test(link)) {
    return `<a href="${esc(link)}" target="_blank" rel="noopener">🔗 ${label}</a>`;
  }
  // file:// eller UNC — inte klickbar (webbläsaren blockerar den från en
  // http-sida); span.copy kopierar EXAKT sökvägen (utan ikonen) vid klick.
  return `${label}: 📋 <span class="copy" data-action="copy" title="Kopiera sökväg">${esc(link)}</span>`;
}

// ---------------------------------------------------------------------------
// Nya extraktionsfält (Task 1-3): slag-, kemi- och märkningsdata.
// ---------------------------------------------------------------------------

// hasVal: skiljer null/undefined (ej angivet) från 0 (ett giltigt tal).
function hasVal(v) {
  return v !== null && v !== undefined;
}

// Slagprov: "27J / -20°C" när båda finns, annars bara den som finns.
// impact_energy_j === 0 betyder "ej angivet" (icke-nullable fält i domänen).
function slagprovText(c) {
  const bits = [];
  if (c.impact_energy_j) bits.push(c.impact_energy_j + 'J');
  if (hasVal(c.impact_temp_c)) bits.push(c.impact_temp_c + '°C');
  return bits.length ? bits.join(' / ') : '—';
}

// Kemi: "C 0.12 · P 0.01 · S 0.002" — bara de fält som finns.
function kemiText(c) {
  const bits = [];
  if (hasVal(c.carbon_pct)) bits.push('C ' + c.carbon_pct);
  if (hasVal(c.p_pct)) bits.push('P ' + c.p_pct);
  if (hasVal(c.s_pct)) bits.push('S ' + c.s_pct);
  return bits.length ? bits.join(' · ') : '—';
}

// Kompakt teknisk detaljrad. Helt tom (allt "—") → gömd, för att inte lägga
// en rad med bara streck på cert som saknar Task 1-3-data (t.ex. V1-import).
function certExtraLine(c) {
  const parts = [
    ['Normsystem', c.norm_system || '—'],
    ['Normutgåva', c.norm_edition || '—'],
    ['PED', c.ped_directive || '—'],
    ['Leveranstillstånd', c.delivery_condition || '—'],
    ['Slagprov', slagprovText(c)],
    ['CEV', hasVal(c.cev) ? String(c.cev) : '—'],
    ['Kemi', kemiText(c)],
    ['Min-temp', hasVal(c.min_temperature_c) ? c.min_temperature_c + '°C' : '—'],
  ];
  if (parts.every(([, v]) => v === '—')) return '';
  return `<div class="copyline">${parts.map(([label, v]) => `${label} <span class="muted">${esc(v)}</span>`).join(' · ')}</div>`;
}

// Varningar ENDAST vid explicit false — null/undefined (gamla rader utan
// data) och true ska inte generera något grönt brus.
function certQualityWarnings(c) {
  const warns = [];
  if (c.is_english === false) warns.push('⚠ Ej engelska');
  if (c.is_legible === false) warns.push('⚠ Delvis oläslig');
  if (c.is_unaltered === false) warns.push('⚠ Möjligen redigerad');
  if (!warns.length) return '';
  return `<div class="copyline">${warns.map((w) => `<span class="badge warn">${esc(w)}</span>`).join(' ')}</div>`;
}

// Getinge-krav: diskreta badges bara när testet/fotot finns (true).
function certGetingeBadges(c) {
  const badges = [];
  if (c.has_bend_test) badges.push('Bocktest');
  if (c.has_intergranular_test) badges.push('Intergranulärtest');
  if (c.has_stamp_photo) badges.push('Stämpelfoto');
  if (!badges.length) return '';
  return `<div class="copyline">${badges.map((b) => `<span class="badge">${esc(b)}</span>`).join(' ')}</div>`;
}

// rowHasReq: har raden några parsade krav (Task 8) att visa?
function rowHasReq(r) {
  return !!(r.req_material || r.req_en_norm || r.req_cert_type || r.req_english ||
    r.req_product_form || r.req_dimensions || r.req_impact);
}

// KRAV vs CERT-jämförelsen. Krav-kolumnen läser radens parsade krav (r.req_*)
// med fallback till AI-domens required_* för oparsade rader. Engelska/Cert-typ/
// Slagseghet döms regelrätt i Go (l.*_verdict, färskt per render). Anropas även
// UTAN cert (l och c = null) — då visas bara Krav-kolumnen (cert-celler "—").
function cmpTable(r, l, c) {
  const li = l || {};
  const cEff = (c && c.effective) || {};
  const icon = (ok) => ok === 'ok' ? '<span class="icon-ok">✓</span>'
    : ok === 'mismatch' ? '<span class="icon-bad">⚠</span>' : '<span class="icon-unk">—</span>';
  const krav = (reqVal, aiVal) => reqVal || aiVal || '';
  // FIX 3b: när radens parsade krav och AI-domens värde BÅDA finns men skiljer sig
  // dömde AI:n mot ett ANNAT värde än Krav-cellen visar — synliggör det i ikonens
  // title (skevheten självläker vid nästa refresh via cache-nyckeln).
  const iconSkew = (ok, reqVal, aiVal) =>
    reqVal && aiVal && reqVal !== aiVal
      ? icon(ok).replace('<span ', `<span title="AI-dom mot: ${esc(aiVal)}" `)
      : icon(ok);
  // FIX 10: EN-normen visas i Material-kravcellen — annars kan en rad vars enda
  // parsade krav är req_en_norm ge en helt tom kravtabell trots rowHasReq().
  const reqMaterial = [r.req_material, r.req_en_norm].filter(Boolean).join(' ');
  // Redigerbar cert-cell (bara när ett cert finns); annars ren "—"-cell.
  const editRow = (label, kravVal, field, certVal, ic) => `
    <tr><td class="lbl">${label}</td><td>${esc(kravVal) || '—'}</td>
    ${c ? `<td class="editcell" data-edit="${field}" data-cert="${c.id}" data-value="${esc(certVal)}">${esc(certVal) || '—'}</td>`
        : '<td>—</td>'}
    <td>${ic}</td></tr>`;
  // Icke-redigerbar cert-cell (fält utan editstöd: språk, slagseghet).
  const plainRow = (label, kravVal, certText, ic) => `
    <tr><td class="lbl">${label}</td><td>${esc(kravVal) || '—'}</td>
    <td>${esc(certText) || '—'}</td><td>${ic}</td></tr>`;
  return `
  <table class="cmp">
    <tr><th>Fält</th><th>Krav</th><th>Cert</th><th></th></tr>
    ${editRow('Material', krav(reqMaterial, li.required_material), 'material', cEff.material, iconSkew(li.material_ok, r.req_material, li.required_material))}
    ${editRow('Cert-typ', krav(r.req_cert_type, li.required_cert), 'cert_type', cEff.cert_type, icon(li.cert_type_verdict))}
    ${editRow('Typ', krav(r.req_product_form, li.required_product_form), 'product_form', cEff.product_form, iconSkew(li.product_form_ok, r.req_product_form, li.required_product_form))}
    ${editRow('Mått', krav(r.req_dimensions, ''), 'dimensions', cEff.dimensions, '')}
    ${plainRow('Engelska', r.req_english ? 'Ja' : '—', c ? (c.is_english ? 'Ja' : 'Nej') : '—', icon(li.english_verdict))}
    ${plainRow('Slagseghet', r.req_impact || '—', c ? slagprovText(c) : '—', icon(li.impact_verdict))}
  </table>`;
}

// Filnamnskomponenter (Task 10): exakt de fem fält cert.BuildFilename bygger
// namnet av, i namnets ordning — charge, produktform, mått, material(kod),
// B-nr (se internal/v2/domain/name.go + internal/cert/cert.go:BuildFilename).
// Varje komponent är en egen .editcell → POST /api/cert/update med det
// befintliga fältnamnet (b_numbers har sin egen redan etablerade väg via
// samma endpoint). Tom komponent varningsmarkeras (samma warn-ton som
// .badge.warn) så det syns exakt vad som saknas för ett komplett namn;
// icke-tomma visas neutralt. Anropas bara från nameLine()s levande gren —
// frysta cert (status sparad) får varken namnrad eller komponentrad.
function nameComponentsRow(c) {
  const comps = [
    ['Charge', 'charge', c.effective.charge],
    ['Kod', 'product_code', c.effective.product_code],
    ['Mått', 'dimensions', c.effective.dimensions],
    ['Material', 'material', c.effective.material],
    ['B-nr', 'b_numbers', c.effective_b_numbers.join(', ')],
  ];
  return `<div class="namecomponents">${comps.map(([label, field, val]) => {
    // FIX 13: BuildFilename utelämnar formsegmentet när det är tomt ELLER "okänt".
    // Segmentets källa är nu product_code (förkortningen) — markera tomt/"okänt"
    // som varning så det ofullständiga namnet syns i stället för att se giltigt ut.
    const empty = !val || (field === 'product_code' && /^okänt$/i.test(val));
    return `
    <span class="namecomp ${empty ? 'namecomp-empty' : ''}">
      <span class="namecomp-label">${esc(label)}</span>
      <span class="editcell" data-edit="${field}" data-cert="${c.id}"
            data-value="${esc(val)}" title="Redigera ${esc(label)}">${esc(val) || '—'}</span>
    </span>`;
  }).join('')}</div>`;
}

// Levande filnamnsrad + Spara-knapp; fryst rendering efter spar.
function nameLine(c) {
  if (c.status === 'sparad') {
    return `<div class="nameline">🔒 <span class="mono">${esc(c.final_filename)}</span>
      <span class="muted small">sparad ${esc(fmtLocal(c.saved_at))}</span></div>`;
  }
  const overridden = !!c.name_override;
  return `
  ${nameComponentsRow(c)}
  <div class="nameline">
    <input class="livename ${overridden ? 'override' : ''}" data-namecert="${c.id}"
           data-proposed="${esc(c.proposed_filename)}" value="${esc(c.proposed_filename)}"
           title="${overridden ? 'Manuellt namn — ↺ återgår till beräknat' : 'Levande namn — räknas om när fält/kopplingar ändras'}"
           spellcheck="false">
    ${overridden ? `<button class="btn btn-small" data-action="clear-override" data-cert="${c.id}"
        title="Tillbaka till beräknat namn">↺</button>` : ''}
    <button class="btn btn-small btn-primary" data-action="save-cert" data-cert="${c.id}">💾 Spara</button>
  </div>`;
}

function notesBlock(kind, refId, notes, orderNumber = '', partNumber = '') {
  const items = (notes || []).map((n) => `
    <div class="note"><span class="who">${n.author === 'sickan' ? '🤖' : '👤'}</span>${esc(n.text)}
      <span class="muted small">${esc((n.created_at || '').slice(0, 10))}</span>
      <span class="del" data-action="delete-note" data-note="${n.id}" title="Ta bort">✕</span></div>`).join('');
  return `
  <div class="notes">${items}
    <div class="inline-form">
      <input type="text" placeholder="Notering…" data-noteinput data-kind="${kind}" data-ref="${refId}"
             data-order="${esc(orderNumber)}" data-part="${esc(partNumber)}">
    </div>
  </div>`;
}

// ---------------------------------------------------------------------------
// Sparade + tasks
// ---------------------------------------------------------------------------

function renderSaved() {
  const list = ov.saved_recent;
  $('savedSection').classList.toggle('hidden', !list.length);
  $('savedCount').textContent = list.length ? `(${list.length})` : '';
  $('savedList').innerHTML = list.map((c) => `
    <div class="card">
      <span class="mono">💾 ${esc(c.final_filename)}</span>
      <span class="muted small"> · ${esc(c.original_filename)} · ${esc(fmtLocal(c.saved_at))}</span>
      <a class="small" href="/api/pdf?cert_id=${c.id}" target="_blank">original-PDF</a>
    </div>`).join('');
}

let lastUnmatchedCount = 0; // för att bara tvångsöppna "Att göra" när NYA omatchade dyker upp

function renderTasks() {
  // Omatchade cert (kunde inte kopplas automatiskt) lyfts överst i "Att göra" som
  // åtgärdsposter: skriv B-nummer → Koppla (skapar bekräftad länk direkt), eller
  // Arkivera. De är härledda ur overviewn, så de försvinner av sig själva så snart
  // certet kopplats/arkiverats. Samma kontroller som okopplat-kortet.
  const unmatched = ov.unmatched_certs || [];
  const total = unmatched.length + ov.tasks.length;
  $('taskCount').textContent = total ? `(${total})` : '';

  const unmatchedHtml = unmatched.map((c) => `
    <li class="unmatched-cert">
      <span>📄 <a href="/api/pdf?cert_id=${c.id}" target="_blank"
          title="Omatchat cert — ange B-nummer för att koppla">${esc(c.original_filename)}</a>
        <span class="meta">charge ${esc(c.effective.charge) || '—'} · ${esc(c.effective.material) || '—'}</span></span>
      <span class="inline-form">
        <input type="text" placeholder="B-nummer, t.ex. B127575" data-linkinput="${c.id}"
               pattern="[Bb]\\d{6}" title="B + sex siffror">
        <button class="btn btn-small" data-action="link-free" data-cert="${c.id}">🔗 Koppla</button>
        <button class="btn btn-small" data-action="archive-cert" data-cert="${c.id}"
                title="Inte ett cert / irrelevant">🗑 Arkivera</button>
      </span>
    </li>`).join('');

  const tasksHtml = ov.tasks.map((t) => `
    <li><span>${esc(t.text)}</span>
      ${t.order_number ? `<span class="meta">${esc(t.order_number)}</span>` : ''}
      ${t.due_date ? `<span class="meta">${esc(t.due_date)}</span>` : ''}
      <button class="btn btn-small" data-action="task-done" data-task="${t.id}">✓</button>
      <button class="btn btn-small" data-action="task-delete" data-task="${t.id}">✕</button>
    </li>`).join('');

  $('taskList').innerHTML = unmatchedHtml + tasksHtml;
  // Fäll ut "Att göra" när NYA omatchade cert dyker upp så de faktiskt syns —
  // men respektera att användaren fällt ihop sektionen (tvångsöppna inte om
  // antalet är oförändrat vid varje omrendering).
  if (unmatched.length > lastUnmatchedCount) $('tasksSection').open = true;
  lastUnmatchedCount = unmatched.length;
}

$('orderSearch').addEventListener('input', (e) => { orderQuery = e.target.value; renderOrders(); });

// Kör i kapp en uppskjuten omrendering när fältet som blockerade den lämnas.
document.addEventListener('focusout', () => {
  if (!pendingReload) return;
  setTimeout(() => { if (pendingReload && !overviewHasFocusedInput()) load(); }, 0);
});

$('taskForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const input = $('taskInput');
  if (!input.value.trim()) return;
  await act(() => post('/api/tasks', { text: input.value.trim() }));
  input.value = '';
});

// ---------------------------------------------------------------------------
// Interaktioner (event-delegering på data-action)
// ---------------------------------------------------------------------------

document.addEventListener('click', async (e) => {
  const el = e.target.closest('[data-action]');
  if (!el) return;
  const action = el.dataset.action;

  if (action === 'toggle-order') {
    const key = el.dataset.order;
    openOrders.has(key) ? openOrders.delete(key) : openOrders.add(key);
    renderOrders();
  } else if (action === 'toggle-row') {
    const key = el.dataset.row;
    openRows.has(key) ? openRows.delete(key) : openRows.add(key);
    renderOrders();
  } else if (action === 'copy') {
    navigator.clipboard?.writeText(el.textContent.trim());
    toast('Kopierat: ' + el.textContent.trim());
  } else if (action === 'confirm-suggestion') {
    await act(() => post('/api/link', {
      cert_id: el.dataset.cert, delivery_row_id: el.dataset.row, order_number: el.dataset.order,
    }), 'Koppling bekräftad');
  } else if (action === 'reject-link') {
    await act(() => post('/api/link/reject', { link_id: el.dataset.link }), 'Avvisad');
  } else if (action === 'link-free') {
    const input = document.querySelector(`[data-linkinput="${el.dataset.cert}"]`);
    const bnr = input?.value.trim();
    if (!bnr) { toast('Skriv ett B-nummer först', true); return; }
    if (input && !input.checkValidity()) {
      toast('B-nummer ska vara B + sex siffror, t.ex. B127575', true);
      return;
    }
    await act(() => post('/api/link', { cert_id: el.dataset.cert, order_number: bnr }), 'Kopplad till ' + bnr.toUpperCase());
  } else if (action === 'ack-errors') {
    // Göm direkt (kvitteringen känns omedelbar); SSE-refetchen bekräftar.
    await act(() => post('/api/errors/ack'), 'Intagsfel kvitterade');
    $('banner').classList.add('hidden');
  } else if (action === 'archive-cert') {
    await act(() => post('/api/cert/archive', { cert_id: el.dataset.cert }), 'Arkiverat');
  } else if (action === 'save-cert') {
    // Dubbelklicksskydd: servern har single-flight, men knappen ska inte
    // ens skicka två anrop.
    if (el.disabled) return;
    el.disabled = true;
    try { await saveCert(el.dataset.cert); } finally { el.disabled = false; }
  } else if (action === 'clear-override') {
    await act(() => post('/api/cert/name', { cert_id: el.dataset.cert, name: '' }), 'Tillbaka till beräknat namn');
  } else if (action === 'mark-delivered') {
    await act(() => post('/api/row/delivered', {
      delivery_row_ids: [el.dataset.row], delivered: el.dataset.delivered === 'true',
    }));
  } else if (action === 'delete-note') {
    await act(() => post('/api/note/delete', { id: el.dataset.note }));
  } else if (action === 'task-done') {
    await act(() => post('/api/tasks/done', { id: el.dataset.task }));
  } else if (action === 'task-delete') {
    await act(() => post('/api/tasks/delete', { id: el.dataset.task }));
  } else if (action === 'edit-cell') {
    // hanteras nedan via editcell-lyssnaren
  }
});

// Klicka-för-redigera (CERT-celler + B-nummer)
document.addEventListener('click', (e) => {
  const cell = e.target.closest('.editcell');
  if (!cell || cell.querySelector('input')) return;
  const field = cell.dataset.edit;
  const certId = cell.dataset.cert;
  const oldValue = cell.dataset.value ?? cell.textContent.trim();
  cell.innerHTML = `<input value="${esc(oldValue === '—' ? '' : oldValue)}" spellcheck="false">`;
  const input = cell.querySelector('input');
  input.focus();
  input.select();
  let done = false;
  const commit = async () => {
    if (done) return;
    done = true;
    const value = input.value.trim();
    if (value === oldValue) { load(); return; }
    await act(() => post('/api/cert/update', { cert_id: certId, field, value }),
      `${field} rättat`).catch(() => {});
    load();
  };
  input.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter') commit();
    if (ev.key === 'Escape') { done = true; load(); }
  });
  input.addEventListener('blur', commit);
});

// Levande namn: Enter/blur sätter override (eller rensar om = beräknat)
document.addEventListener('focusout', async (e) => {
  const input = e.target.closest('[data-namecert]');
  if (!input) return;
  const value = input.value.trim();
  const proposed = input.dataset.proposed;
  if (value === proposed) return;
  await act(() => post('/api/cert/name', {
    cert_id: input.dataset.namecert,
    name: value === '' ? '' : value,
  }), value === '' ? 'Tillbaka till beräknat namn' : 'Manuellt namn satt');
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && e.target.matches('[data-namecert]')) e.target.blur();
  if (e.key === 'Enter' && e.target.matches('[data-noteinput]')) {
    const el = e.target;
    const text = el.value.trim();
    if (!text) return;
    act(() => post('/api/note', {
      kind: el.dataset.kind, ref_id: el.dataset.ref, text,
      order_number: el.dataset.order, part_number: el.dataset.part,
    }));
    el.value = '';
  }
});

// Spara med 422-varningar → "Spara ändå"-dialog
async function saveCert(certId) {
  try {
    const res = await post('/api/cert/save', { cert_id: certId });
    toast('💾 Sparad som ' + res.final_filename);
  } catch (err) {
    if (err instanceof ApiError && err.status === 422 && err.json?.warnings) {
      const ok = await confirmDialog('Certet har varningar', err.json.warnings);
      if (!ok) return;
      const res = await act(() => post('/api/cert/save', { cert_id: certId, confirm: true }));
      toast('💾 Sparad (med varningar) som ' + res.final_filename);
    } else {
      toast(err.message, true);
    }
  }
}

function confirmDialog(title, items) {
  return new Promise((resolve) => {
    const dlg = $('confirmDialog');
    $('confirmTitle').textContent = title;
    $('confirmList').innerHTML = items.map((w) => `<li>${esc(w)}</li>`).join('');
    const done = (val) => { dlg.close(); resolve(val); };
    $('confirmYes').onclick = () => done(true);
    $('confirmNo').onclick = () => done(false);
    dlg.showModal();
  });
}

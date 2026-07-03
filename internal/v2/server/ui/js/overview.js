// overview.js — renderar den enda vyn (Översikt) och kopplar interaktionerna.
// All data kommer från GET /api/overview; SSE-eventet "overview" är bara en
// ping som triggar refetch. Öppet/stängt-läge hålls i minnes-Sets så
// omrenderingar inte fäller ihop pågående arbete.

import { get, post, onEvent, connectSSE, toast, act, debounce, esc, ApiError } from './api.js';
import { initShell, setWorkerState } from './shell.js';

const $ = (id) => document.getElementById(id);
const openOrders = new Set();
let ov = null; // senaste overview-svaret

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

initShell();
connectSSE();
onEvent('overview', debounce(load, 250));
load();

async function load() {
  try {
    ov = await get('/api/overview');
  } catch (err) {
    toast('Kunde inte hämta översikten: ' + err.message, true);
    return;
  }
  setWorkerState(ov.running);
  $('lastSync').textContent = ov.last_sync ? 'sync ' + ov.last_sync.slice(0, 16).replace('T', ' ') : '';
  renderBanner();
  renderUnlinked();
  renderOrders();
  renderSaved();
  renderTasks();
}

function renderBanner() {
  const b = $('banner');
  if (!ov.errors.length) { b.classList.add('hidden'); return; }
  b.textContent = '⚠️ Intagsfel (ligger kvar i inkorgen): ' + ov.errors.join(' · ');
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

function renderOrders() {
  const today = new Date().toISOString().slice(0, 10);
  const byCat = { forsenade: [], idag: [], kommande: [], levererade: [] };
  for (const g of ov.orders) {
    const cat = categorize(g, today);
    if (cat) byCat[cat].push(g);
  }
  let html = '';
  for (const [key, label] of CATS) {
    if (!byCat[key].length) continue;
    html += `<div class="cat-head">${label}</div>`;
    html += byCat[key].map(orderCard).join('');
  }
  $('ordersList').innerHTML = html;
  $('ordersEmpty').classList.toggle('hidden', ov.orders.length > 0);
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
  const open = openOrders.has(g.order_number);
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

function artRow(r) {
  const today = new Date().toISOString().slice(0, 10);
  const late = r.delivery_date && r.delivery_date < today && !r.delivered;
  const activeLinks = r.links; // avfärdade filtreras redan av servern
  return `
  <div class="artrow">
    <div class="artrow-head">
      <span class="partnum">${esc(r.part_number)}</span>
      <span>${esc(r.description)}</span>
      <span class="muted">${r.planned_qty} st</span>
      <span class="${late ? 'date-late' : 'muted'}">${esc(r.delivery_date)}${late ? ' ⏰' : ''}</span>
      ${r.delivered ? '<span class="badge ok">✓ levererad</span>' : ''}
      ${!r.in_monitor ? '<span class="badge" title="Raden fanns inte i senaste Monitor-hämtningen">utanför fönstret</span>' : ''}
    </div>
    ${r.extra_description ? `<div class="extra-desc">Extra benämning: ${esc(r.extra_description)}</div>` : ''}
    ${activeLinks.map((l) => linkBlock(r, l)).join('')}
    ${r.cert_required && !activeLinks.length ? '<div class="hint">Inget cert kopplat ännu.</div>' : ''}
    ${notesBlock('order_row', r.delivery_row_id, r.notes, r.order_number, r.part_number)}
    <div class="row-actions">
      <button class="btn btn-small" data-action="mark-delivered" data-row="${r.delivery_row_id}"
              data-delivered="${r.delivered ? 'false' : 'true'}">${r.delivered ? '↩ Ångra levererad' : '✓ Levererad'}</button>
    </div>
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
    ${cmpTable(l, c)}
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
    ${l.ai_notes ? `<div class="hint">🤖 ${esc(l.ai_notes)}</div>` : ''}
    ${nameLine(c)}
  </div>`;
}

// KRAV vs CERT-jämförelsen. CERT-cellerna är klicka-för-redigera.
function cmpTable(l, c) {
  const icon = (ok) => ok === 'ok' ? '<span class="icon-ok">✓</span>'
    : ok === 'mismatch' ? '<span class="icon-bad">⚠</span>' : '<span class="icon-unk">—</span>';
  const row = (label, krav, field, certVal, ic) => `
    <tr><td class="lbl">${label}</td><td>${esc(krav) || '—'}</td>
    <td class="editcell" data-edit="${field}" data-cert="${c.id}" data-value="${esc(certVal)}">${esc(certVal) || '—'}</td>
    <td>${ic}</td></tr>`;
  return `
  <table class="cmp">
    <tr><th>Fält</th><th>Krav</th><th>Cert</th><th></th></tr>
    ${row('Material', l.required_material, 'material', c.effective.material, icon(l.material_ok))}
    ${row('Cert-typ', l.required_cert, 'cert_type', c.effective.cert_type,
      l.required_cert && c.effective.cert_type
        ? icon(l.required_cert.includes(c.effective.cert_type) ? 'ok' : 'unknown') : icon('unknown'))}
    ${row('Typ', l.required_product_form, 'product_form', c.effective.product_form, icon(l.product_form_ok))}
    ${row('Mått', '', 'dimensions', c.effective.dimensions, '')}
    <tr><td class="lbl">Engelska</td><td>Engelska</td><td>${c.is_english ? 'Ja' : 'Nej'}</td>
    <td>${c.is_english ? '<span class="icon-ok">✓</span>' : '<span class="icon-bad">⚠</span>'}</td></tr>
  </table>`;
}

// Levande filnamnsrad + Spara-knapp; fryst rendering efter spar.
function nameLine(c) {
  if (c.status === 'sparad') {
    return `<div class="nameline">🔒 <span class="mono">${esc(c.final_filename)}</span>
      <span class="muted small">sparad ${esc((c.saved_at || '').slice(0, 16).replace('T', ' '))}</span></div>`;
  }
  const overridden = !!c.name_override;
  return `
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
  $('savedList').innerHTML = list.map((c) => `
    <div class="card">
      <span class="mono">💾 ${esc(c.final_filename)}</span>
      <span class="muted small"> · ${esc(c.original_filename)} · ${esc((c.saved_at || '').slice(0, 16).replace('T', ' '))}</span>
      <a class="small" href="/api/pdf?cert_id=${c.id}" target="_blank">original-PDF</a>
    </div>`).join('');
}

function renderTasks() {
  $('taskList').innerHTML = ov.tasks.map((t) => `
    <li><span>${esc(t.text)}</span>
      ${t.order_number ? `<span class="meta">${esc(t.order_number)}</span>` : ''}
      ${t.due_date ? `<span class="meta">${esc(t.due_date)}</span>` : ''}
      <button class="btn btn-small" data-action="task-done" data-task="${t.id}">✓</button>
      <button class="btn btn-small" data-action="task-delete" data-task="${t.id}">✕</button>
    </li>`).join('');
}

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
    await act(() => post('/api/link', { cert_id: el.dataset.cert, order_number: bnr }), 'Kopplad till ' + bnr.toUpperCase());
  } else if (action === 'archive-cert') {
    await act(() => post('/api/cert/archive', { cert_id: el.dataset.cert }), 'Arkiverat');
  } else if (action === 'save-cert') {
    await saveCert(el.dataset.cert);
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

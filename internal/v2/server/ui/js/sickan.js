// sickan.js — chatoverlayen. Protokollet (POST /api/sickan/stream med
// SSE-ramar i svaret) är detsamma som V1; parsern är porterad därifrån.

const $ = (id) => document.getElementById(id);
const SESSION = 'default';
let busy = false;
let currentBot = null;
let typing = null;
let model = ''; // '' = serverns sparade val

export function initSickan() {
  $('sickanFab').addEventListener('click', togglePanel);
  $('sickanClose').addEventListener('click', togglePanel);
  $('sickanReset').addEventListener('click', async () => {
    await fetch('/api/sickan/reset', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ session: SESSION }) });
    $('sickanLog').innerHTML = '';
  });
  $('sickanModel').addEventListener('change', (e) => { model = e.target.value; });
  $('sickanForm').addEventListener('submit', (e) => {
    e.preventDefault();
    const input = $('sickanInput');
    const text = input.value.trim();
    if (!text || busy) return;
    input.value = '';
    send(text);
  });
}

function togglePanel() {
  const panel = $('sickanPanel');
  panel.classList.toggle('hidden');
  if (!panel.classList.contains('hidden')) $('sickanInput').focus();
}

function append(el) {
  const log = $('sickanLog');
  log.appendChild(el);
  log.scrollTop = log.scrollHeight;
  return el;
}

function bubble(kind, text) {
  const el = document.createElement('div');
  el.className = 'sickan-bubble ' + kind;
  el.textContent = text;
  return el;
}

function toolCard(label) {
  const el = document.createElement('div');
  el.className = 'sickan-tool';
  el.textContent = '▸ ' + label;
  return el;
}

const TOOL_LABELS = {
  list_overview: 'läser översikten',
  get_cert: 'granskar cert',
  update_cert: 'rättar fält',
  link_cert: 'kopplar cert',
  unlink_cert: 'kopplar loss',
  propose_name: 'namnförslag',
  read_pdf: 'läser PDF',
  monitor_find_purchase_order: 'slår upp order i Monitor',
  monitor_find_supplier: 'söker leverantör i Monitor',
  monitor_lookup_charge: 'slår upp charge i Monitor',
  add_note: 'skriver notering',
  get_notes: 'läser noteringar',
  mark_delivered: 'markerar levererad',
  compose_deviation_mail: 'bygger mailutkast',
  remember_rule: 'sparar regel',
  list_rules: 'läser regler',
  add_task: 'lägger till task',
  list_tasks: 'läser tasks',
  complete_task: 'bockar av task',
};

function toolLabel(name) { return TOOL_LABELS[name] || name; }

async function send(text) {
  busy = true;
  $('sickanSend').disabled = true;
  append(bubble('user', text));
  currentBot = null;
  typing = append(bubble('typing', '…'));
  const pendingTools = new Map();

  try {
    const resp = await fetch('/api/sickan/stream', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session: SESSION, text, model }),
    });
    if (!resp.ok || !resp.body) {
      append(bubble('error', await resp.text()));
      return;
    }
    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let buf = '';
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let idx;
      while ((idx = buf.indexOf('\n\n')) >= 0) {
        const frame = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        let ev = '', data = '';
        for (const ln of frame.split('\n')) {
          if (ln.startsWith('event: ')) ev = ln.slice(7);
          else if (ln.startsWith('data: ')) data += ln.slice(6);
        }
        if (!ev) continue;
        let payload = '';
        try { payload = JSON.parse(data); } catch { payload = data; }
        handleEvent(ev, payload, pendingTools);
      }
    }
  } catch (err) {
    append(bubble('error', String(err)));
  } finally {
    busy = false;
    $('sickanSend').disabled = false;
    if (typing) { typing.remove(); typing = null; }
    currentBot = null;
  }
}

function handleEvent(kind, payload, pendingTools) {
  if (typing && (kind === 'text' || kind === 'tool_call' || kind === 'error')) {
    typing.remove();
    typing = null;
  }
  if (kind === 'text') {
    if (!currentBot) currentBot = append(bubble('bot', ''));
    currentBot.textContent += payload;
    $('sickanLog').scrollTop = $('sickanLog').scrollHeight;
  } else if (kind === 'tool_call') {
    currentBot = null;
    try {
      const p = JSON.parse(payload);
      const card = append(toolCard(toolLabel(p.name)));
      pendingTools.set(p.id, card);
    } catch { /* ignorera trasig payload */ }
  } else if (kind === 'tool_result') {
    try {
      const p = JSON.parse(payload);
      const card = pendingTools.get(p.id);
      if (!card) return;
      card.textContent = '▸ ' + toolLabel(p.name);
      const url = p.result && p.result.mailto_url;
      if (url && /^mailto:/.test(url)) {
        card.classList.add('clickable');
        card.textContent += ' — öppna mailutkastet';
        card.addEventListener('click', () => { window.location.href = url; });
      }
    } catch { /* ignorera */ }
  } else if (kind === 'tool_error') {
    try {
      const p = JSON.parse(payload);
      const card = pendingTools.get(p.id);
      if (card) { card.className = 'sickan-bubble error'; card.textContent = '⚠ ' + p.error; }
    } catch { /* ignorera */ }
  } else if (kind === 'error') {
    append(bubble('error', String(payload)));
  }
}

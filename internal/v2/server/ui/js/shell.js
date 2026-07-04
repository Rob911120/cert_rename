// shell.js — header, inställningar, logg, drag-drop-upload, SSE-status.

import { get, post, onEvent, toast, act } from './api.js';

const $ = (id) => document.getElementById(id);

export function initShell() {
  $('todayLabel').textContent = new Date().toLocaleDateString('sv-SE', {
    weekday: 'long', day: 'numeric', month: 'long',
  });

  // Worker på/av
  $('workerBtn').addEventListener('click', async () => {
    const running = $('workerDot').classList.contains('dot-on');
    await act(() => post(running ? '/api/stop' : '/api/start'),
      running ? 'Intag stoppat' : 'Intag startat');
  });
  onEvent('state', (s) => setWorkerState(s.running));

  // Monitor-sync
  $('refreshBtn').addEventListener('click', () =>
    act(() => post('/api/refresh'), 'Monitor-sync kickad — se loggen'));

  // Kostnadsbadge
  onEvent('costs', renderCosts);
  get('/api/costs').then(renderCosts).catch(() => {});

  // Logg
  $('logToggle').addEventListener('click', () =>
    $('logPanel').classList.toggle('collapsed'));
  onEvent('log', (l) => {
    const pre = $('logLines');
    pre.textContent += `${l.ts}  ${l.text}\n`;
    pre.scrollTop = pre.scrollHeight;
  });

  initSettings();
  initDragDrop();
  initTheme();
}

// --- Tema (auto / ljust / mörkt) ---------------------------------------------
// Auto = lämna data-theme osatt så OS-preferensen (CSS-media-frågan) styr.
// light/dark = tvinga via data-theme på <html>. Valet sparas i localStorage;
// index.html-inline-scriptet applicerar det redan före första målning.

const THEME_KEY = 'certv2-theme';

function initTheme() {
  const seg = $('themeSeg');
  if (!seg) return;
  let choice = 'auto';
  try { choice = localStorage.getItem(THEME_KEY) || 'auto'; } catch { /* ignore */ }
  if (choice !== 'light' && choice !== 'dark') choice = 'auto';
  applyTheme(choice);

  seg.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-theme-choice]');
    if (!btn) return;
    const next = btn.dataset.themeChoice;
    try {
      if (next === 'auto') localStorage.removeItem(THEME_KEY);
      else localStorage.setItem(THEME_KEY, next);
    } catch { /* ignore */ }
    applyTheme(next);
  });
}

function applyTheme(choice) {
  const root = document.documentElement;
  if (choice === 'auto') delete root.dataset.theme;
  else root.dataset.theme = choice;
  for (const btn of document.querySelectorAll('#themeSeg [data-theme-choice]')) {
    btn.setAttribute('aria-pressed', String(btn.dataset.themeChoice === choice));
  }
}

export function setWorkerState(running) {
  $('workerDot').className = 'dot ' + (running ? 'dot-on' : 'dot-off');
  $('workerLabel').textContent = running ? 'Stoppa' : 'Starta';
}

// --- Kostnad (samma prislista som V1:s costs.go) -----------------------------

const PRICING = {
  sonnet: { input: 3, output: 15, cache_creation: 3.75, cache_read: 0.30 },
  haiku:  { input: 1, output: 5,  cache_creation: 1.25, cache_read: 0.10 },
  opus:   { input: 15, output: 75, cache_creation: 18.75, cache_read: 1.50 },
};

function renderCosts(costs) {
  let usd = 0;
  for (const [model, p] of Object.entries(PRICING)) {
    const tc = costs[model];
    if (!tc) continue;
    usd += (tc.input * p.input + tc.output * p.output +
      tc.cache_creation * p.cache_creation + tc.cache_read * p.cache_read) / 1e6;
  }
  document.getElementById('costsBadge').textContent = usd > 0 ? `$${usd.toFixed(2)}` : '';
}

// --- Inställningar ------------------------------------------------------------

const CFG_FIELDS = ['inbox_dir', 'api_key', 'monitor_url', 'monitor_user',
  'monitor_password', 'upcoming_time', 'v2_store_dir', 'v2_output_dir', 'report_email'];
const CFG_NUMS = ['upcoming_window_days', 'upcoming_back_days'];
const CFG_BOOLS = ['autostart', 'upcoming_enabled'];

function initSettings() {
  const dlg = document.getElementById('settingsDialog');
  const form = document.getElementById('settingsForm');
  let current = {};

  document.getElementById('settingsBtn').addEventListener('click', async () => {
    current = await get('/api/config');
    for (const k of CFG_FIELDS) form.elements[k].value = current[k] ?? '';
    for (const k of CFG_NUMS) form.elements[k].value = current[k] ?? '';
    for (const k of CFG_BOOLS) form.elements[k].checked = !!current[k];
    dlg.showModal();
  });
  document.getElementById('settingsCancel').addEventListener('click', () => dlg.close());

  form.addEventListener('submit', async () => {
    const cfg = { ...current };
    for (const k of CFG_FIELDS) cfg[k] = form.elements[k].value.trim();
    for (const k of CFG_NUMS) cfg[k] = parseInt(form.elements[k].value, 10) || 0;
    for (const k of CFG_BOOLS) cfg[k] = form.elements[k].checked;
    await act(() => post('/api/config', cfg), 'Inställningar sparade');
  });
}

// --- Drag-drop-upload -----------------------------------------------------------

function initDragDrop() {
  const overlay = document.getElementById('dropOverlay');
  let depth = 0;
  document.addEventListener('dragenter', (e) => {
    e.preventDefault();
    if (++depth === 1) overlay.classList.remove('hidden');
  });
  document.addEventListener('dragleave', () => {
    if (--depth <= 0) { depth = 0; overlay.classList.add('hidden'); }
  });
  document.addEventListener('dragover', (e) => e.preventDefault());
  document.addEventListener('drop', async (e) => {
    e.preventDefault();
    depth = 0;
    overlay.classList.add('hidden');
    const files = [...(e.dataTransfer?.files ?? [])];
    if (!files.length) return;
    const fd = new FormData();
    for (const f of files) fd.append('files', f, f.name);
    try {
      const res = await fetch('/api/upload', { method: 'POST', body: fd });
      const text = await res.text();
      if (!res.ok) { toast(text, true); return; }
      const out = JSON.parse(text);
      toast(`Mottaget: ${out.eml} .eml, ${out.pdf} .pdf`);
    } catch (err) {
      toast(String(err), true);
    }
  });
}

// api.js — ALL serverkommunikation på ett ställe: fetch-hjälpare, SSE, toast.

export async function get(path) {
  const res = await fetch(path);
  if (!res.ok) throw new ApiError(res.status, await res.text());
  return res.json();
}

export async function post(path, body) {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body ?? {}),
  });
  const text = await res.text();
  if (!res.ok) throw new ApiError(res.status, text);
  try { return JSON.parse(text); } catch { return {}; }
}

export class ApiError extends Error {
  constructor(status, body) {
    super(body || `HTTP ${status}`);
    this.status = status;
    try { this.json = JSON.parse(body); } catch { this.json = null; }
  }
}

// --- SSE ------------------------------------------------------------------

const listeners = new Map(); // event → Set<fn>

export function onEvent(name, fn) {
  if (!listeners.has(name)) listeners.set(name, new Set());
  listeners.get(name).add(fn);
}

export function connectSSE() {
  const es = new EventSource('/api/events');
  for (const name of ['log', 'state', 'overview', 'costs']) {
    es.addEventListener(name, (e) => {
      let data = {};
      try { data = JSON.parse(e.data); } catch { /* tom ping */ }
      for (const fn of listeners.get(name) ?? []) fn(data);
    });
  }
  es.onerror = () => { /* EventSource återansluter själv */ };
  return es;
}

// --- Toast ------------------------------------------------------------------

export function toast(text, isError = false) {
  const box = document.getElementById('toasts');
  const el = document.createElement('div');
  el.className = 'toast' + (isError ? ' err' : '');
  el.textContent = text;
  box.appendChild(el);
  setTimeout(() => el.remove(), isError ? 7000 : 3500);
}

// Kör en mutation med enhetlig felhantering. 422 (valideringsvarningar)
// bubblas vidare så anroparen kan visa "Spara ändå".
export async function act(fn, okText) {
  try {
    const res = await fn();
    if (okText) toast(okText);
    return res;
  } catch (err) {
    if (err instanceof ApiError && err.status === 422) throw err;
    toast(err.message, true);
    throw err;
  }
}

export function debounce(fn, ms) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}

export function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

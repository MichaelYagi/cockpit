// DOM and formatting helpers. Text from the API is untrusted: it is only
// ever added as text nodes or attribute values, never as HTML.

// h creates an element. props: class, style (object), on<event>
// (function), anything else becomes an attribute. Children may be nodes,
// strings, numbers or nested arrays; null, undefined and false are
// skipped.
export function h(tag, props, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'style') Object.assign(el.style, v);
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  add(el, children);
  return el;
}

function add(el, children) {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (Array.isArray(c)) add(el, c);
    else el.append(c instanceof Node ? c : String(c));
  }
}

// replace swaps an element's children.
export function replace(el, ...children) {
  el.replaceChildren();
  add(el, children);
}

const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];

export function bytes(n) {
  if (n === null || n === undefined || !isFinite(n)) return '—';
  let i = 0;
  let v = Number(n);
  while (Math.abs(v) >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return (i === 0 ? v.toFixed(0) : v.toFixed(v < 10 ? 2 : 1)) + ' ' + units[i];
}

export function rate(n) {
  return n === null || n === undefined ? '—' : bytes(n) + '/s';
}

export function pct(n, digits = 1) {
  return n === null || n === undefined || !isFinite(n) ? '—' : Number(n).toFixed(digits) + '%';
}

export function num(n, digits = 0) {
  return n === null || n === undefined || !isFinite(n) ? '—' : Number(n).toLocaleString(undefined, { maximumFractionDigits: digits, minimumFractionDigits: digits });
}

export function duration(seconds) {
  if (seconds === null || seconds === undefined || !isFinite(seconds)) return '—';
  let s = Math.max(0, Math.floor(seconds));
  const d = Math.floor(s / 86400);
  s %= 86400;
  const hr = Math.floor(s / 3600);
  s %= 3600;
  const m = Math.floor(s / 60);
  if (d) return `${d}d ${hr}h`;
  if (hr) return `${hr}h ${m}m`;
  if (m) return `${m}m ${s % 60}s`;
  return `${s}s`;
}

export function clock(t) {
  if (!t) return '—';
  const d = new Date(t);
  const today = new Date();
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  if (d.toDateString() === today.toDateString()) return time;
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) + ' ' + time;
}

export function ago(t) {
  if (!t) return '—';
  return duration((Date.now() - new Date(t).getTime()) / 1000) + ' ago';
}

// level maps a percentage to ok, warn or crit.
export function level(p, warn = 75, crit = 90) {
  if (p === null || p === undefined) return '';
  return p >= crit ? 'crit' : p >= warn ? 'warn' : 'ok';
}

const statusWords = { ok: 'OK', warning: 'Warning', critical: 'Critical', unknown: 'Unknown', info: 'Info' };
const statusClass = { ok: 'ok', warning: 'warn', critical: 'crit', unknown: 'muted', info: 'accent' };

// pill shows a status as a coloured label with its name, so colour is
// never the only signal.
export function pill(status) {
  return h('span', { class: 'pill ' + (statusClass[status] || 'muted') }, statusWords[status] || status || '—');
}

// bar is a horizontal meter with a text value beside it.
export function bar(percent, cls) {
  const p = Math.max(0, Math.min(100, Number(percent) || 0));
  return h('span', { class: 'bar', role: 'meter', 'aria-valuemin': 0, 'aria-valuemax': 100, 'aria-valuenow': p.toFixed(0) },
    h('span', { class: 'fill ' + (cls || level(p)), style: { width: p + '%' } }));
}

// kv renders label/value rows.
export function kv(rows) {
  return h('dl', { class: 'kv' }, rows.filter(Boolean).map(([k, v]) => [h('dt', null, k), h('dd', null, v === '' || v === null || v === undefined ? '—' : v)]));
}

// table renders rows; columns: {label, value(row) -> node|string, class}.
export function table(columns, rows, empty = 'Nothing to show.') {
  if (!rows || rows.length === 0) return h('p', { class: 'muted empty' }, empty);
  return h('div', { class: 'table-wrap' },
    h('table', null,
      h('thead', null, h('tr', null, columns.map(c => h('th', { class: c.class }, c.label)))),
      h('tbody', null, rows.map(r => h('tr', null, columns.map(c => h('td', { class: c.class }, c.value(r))))))));
}

// card is a titled panel.
export function card(title, ...body) {
  return h('section', { class: 'card' }, title ? h('h2', null, title) : null, body);
}

// note explains why a section has no data.
export function note(text, cls = 'muted') {
  return h('p', { class: 'note ' + cls }, text);
}

// section unwraps an API Section, or explains why it has no data.
export function section(s, what) {
  if (!s) return { error: note(`No ${what} data.`) };
  if (s.status !== 'available' || !s.data) {
    const why = s.reason || `${what} is ${String(s.status || 'unavailable').replace(/_/g, ' ')}.`;
    if (s.data && s.stale) return { data: s.data, warn: note('Stale: ' + why, 'warn') };
    return { error: note(why) };
  }
  return { data: s.data, warn: s.stale ? note('This data is stale.', 'warn') : null };
}

// Cockpit web dashboard (D35): a read-only API client. Everything shown
// comes from /api/v1 with the key the user entered.

import { APIError, get, getKey, setKey, stream } from './api.js';
import { LineChart } from './chart.js';
import { ago, bar, bytes, card, clock, duration, h, kv, level, note, num, pct, pill, rate, replace, section, table } from './dom.js';

const root = document.getElementById('app');

// ---- shared state ----

const liveLimit = 150; // samples kept for live charts (5 minutes at 2 s)

const state = {
  system: null,
  agent: null,
  metrics: null,
  metricsError: null,
  health: null,
  healthError: null,
  live: { t: [], cpu: [], mem: [], rx: [], tx: [] },
  events: [],
  eventsCursor: null,
  eventsError: null,
  eventsLive: false,
  conn: 'connecting',
  updated: null,
};

let session = null; // running pollers and streams while signed in
let screen = null; // the mounted screen

// ---- polling ----

// poller runs fn every ms while the page is visible. Errors that concern
// the whole session (bad key, rate limit, network) are handled here;
// fn handles errors that concern only its own data.
function poller(fn, ms) {
  let timer = 0;
  let stopped = false;
  let busy = false;
  let again = false;
  let ctrl = null;
  const run = async () => {
    clearTimeout(timer);
    if (stopped) return;
    if (busy) {
      again = true;
      return;
    }
    if (document.hidden) {
      timer = setTimeout(run, 1000);
      return;
    }
    busy = true;
    ctrl = new AbortController();
    let wait = ms;
    try {
      await fn(ctrl.signal);
      setConn('live');
    } catch (e) {
      if (e.name === 'AbortError') {
        busy = false;
        return;
      }
      wait = sessionError(e, ms);
    }
    busy = false;
    if (stopped) return;
    if (again) {
      again = false;
      run();
      return;
    }
    timer = setTimeout(run, wait);
  };
  run();
  return {
    refresh: run,
    stop() {
      stopped = true;
      clearTimeout(timer);
      ctrl?.abort();
    },
  };
}

// sessionError handles failures that affect every request and returns
// how long to wait before trying again.
function sessionError(e, ms) {
  if (e instanceof APIError && e.status === 401) {
    signOut('The agent no longer accepts this key. It may have been revoked or rotated.');
    return ms;
  }
  if (e instanceof APIError && e.status === 429) {
    setConn('limited');
    return Math.max(ms, (e.retryAfter || 5) * 1000);
  }
  setConn('offline');
  console.warn('cockpit:', e);
  return Math.max(ms, 5000);
}

// fetchOr returns {data} or, for errors that concern only this resource
// (403, 404, 5xx…), {error}. Session-wide errors are thrown to the poller.
async function fetchOr(path, params, signal) {
  try {
    return { data: await get(path, params, signal) };
  } catch (e) {
    if (e instanceof APIError && e.status !== 401 && e.status !== 429) return { error: e };
    throw e;
  }
}

function errorNote(e) {
  if (e.status === 403) return note(`This key cannot read this. ${e.message}`, 'warn');
  return note(e.message || 'The request failed.', 'crit');
}

// ---- session ----

function start() {
  stopSession();
  state.events = [];
  state.eventsCursor = null;
  state.live = { t: [], cpu: [], mem: [], rx: [], tx: [] };
  layout();
  const s = { pollers: [], streamCtrl: null, running: true };
  session = s;
  s.pollers.push(poller(async signal => {
    const r = await fetchOr('/metrics', { sections: 'cpu,memory,filesystems,network,sensors,gpu,power' }, signal);
    state.metrics = r.data || null;
    state.metricsError = r.error || null;
    if (r.data) pushLive(r.data);
    state.updated = new Date();
    updateHeader();
    screen?.tick?.();
  }, 2000));
  s.pollers.push(poller(async signal => {
    const r = await fetchOr('/health', null, signal);
    state.health = r.data || null;
    state.healthError = r.error || null;
    updateHeader();
    screen?.healthChanged?.();
  }, 5000));
  s.pollers.push(poller(async signal => {
    const [sys, st] = await Promise.all([fetchOr('/system', null, signal), fetchOr('/status', null, signal)]);
    state.system = sys.data?.system?.data || null;
    state.agent = st.data?.agent || null;
    updateHeader();
  }, 60000));
  loadEvents().then(() => streamEvents(s));
  route();
}

function stopSession() {
  if (!session) return;
  session.running = false;
  session.pollers.forEach(p => p.stop());
  session.streamCtrl?.abort();
  session = null;
  screen?.destroy?.();
  screen = null;
}

function signOut(message) {
  stopSession();
  setKey('');
  signIn(message);
}

function pushLive(m) {
  const L = state.live;
  const cpu = m.cpu?.data?.usage_percent ?? null;
  const mem = m.memory?.data?.used_percent ?? null;
  let rx = null;
  let tx = null;
  for (const n of m.network?.data || []) {
    if (n.loopback) continue;
    if (n.rx_bytes_per_second != null) rx = (rx || 0) + n.rx_bytes_per_second;
    if (n.tx_bytes_per_second != null) tx = (tx || 0) + n.tx_bytes_per_second;
  }
  L.t.push(Date.now());
  L.cpu.push(cpu);
  L.mem.push(mem);
  L.rx.push(rx);
  L.tx.push(tx);
  if (L.t.length > liveLimit) for (const k of Object.keys(L)) L[k].shift();
}

function livePoints(values) {
  return state.live.t.map((t, i) => ({ t, v: values[i] }));
}

// liveRange starts at the first sample (at least one minute back), so a
// freshly opened page does not squeeze its few samples into one edge.
function liveRange() {
  const to = Date.now();
  const first = state.live.t[0] ?? to;
  return { from: Math.min(first, to - 60e3), to, step: 2000 };
}

// ---- events ----

const maxEvents = 500;

async function loadEvents(older) {
  const r = await fetchOr('/events', { limit: 100, cursor: older ? state.eventsCursor : undefined }).catch(e => {
    sessionError(e, 0);
    return {};
  });
  if (r.error) {
    state.eventsError = r.error;
    return;
  }
  if (!r.data) return;
  state.eventsError = null;
  const seen = new Set(state.events.map(e => e.id));
  const items = r.data.items.filter(e => !seen.has(e.id));
  state.events = older ? state.events.concat(items) : items.concat(state.events);
  state.eventsCursor = r.data.next_cursor;
  screen?.eventsChanged?.();
}

async function streamEvents(s) {
  let backoff = 2000;
  while (s.running && !state.eventsError) {
    const ctrl = new AbortController();
    s.streamCtrl = ctrl;
    try {
      await stream('/events/stream', null, () => {
        state.eventsLive = true;
        screen?.eventsChanged?.();
      }, (type, data) => {
        if (type !== 'event') return;
        let ev;
        try {
          ev = JSON.parse(data);
        } catch {
          return;
        }
        if (state.events.some(e => e.id === ev.id)) return;
        state.events.unshift(ev);
        if (state.events.length > maxEvents) state.events.length = maxEvents;
        if (ev.type.startsWith('finding.')) s.pollers[1]?.refresh();
        screen?.eventsChanged?.();
      }, ctrl.signal);
      backoff = 2000;
    } catch (e) {
      if (!s.running || e.name === 'AbortError') return;
      if (e instanceof APIError && e.status === 401) return signOut('The agent no longer accepts this key.');
      if (e instanceof APIError && e.status === 403) return;
      if (e instanceof APIError && e.status === 429) backoff = Math.max(backoff, (e.retryAfter || 5) * 1000);
    }
    state.eventsLive = false;
    screen?.eventsChanged?.();
    await new Promise(r => setTimeout(r, backoff));
    backoff = Math.min(backoff * 2, 60000);
    if (s.running) await loadEvents(); // catch up on anything missed
  }
}

// ---- layout ----

const screens = [
  { id: 'overview', name: 'Overview', mount: Overview },
  { id: 'performance', name: 'Performance', mount: Performance },
  { id: 'health', name: 'Health', mount: Health },
  { id: 'storage', name: 'Storage', mount: Storage },
  { id: 'network', name: 'Network', mount: Network },
  { id: 'processes', name: 'Processes', mount: Processes },
  { id: 'services', name: 'Services', mount: Services },
  { id: 'events', name: 'Events', mount: Events },
  { id: 'hardware', name: 'Hardware', mount: Hardware },
];

const els = {};

function layout() {
  els.host = h('span', { class: 'host' }, '…');
  els.os = h('span', { class: 'os muted' });
  els.health = h('span', { class: 'head-health' });
  els.conn = h('span', { class: 'conn' });
  els.nav = h('nav', { class: 'tabs', 'aria-label': 'Screens' },
    screens.map((s, i) => h('a', { href: '#/' + s.id, 'data-id': s.id }, h('span', { class: 'num' }, String(i + 1)), s.name)));
  els.main = h('main', { id: 'main', tabindex: '-1' });
  replace(root,
    h('header', { class: 'top' },
      h('div', { class: 'brand' }, h('img', { src: 'icon.svg', alt: '', width: 22, height: 22 }), h('span', null, 'Cockpit')),
      h('div', { class: 'ident' }, els.host, els.os),
      h('div', { class: 'head-right' }, els.health, els.conn,
        h('button', { class: 'ghost', type: 'button', onclick: () => signOut('Signed out. The key was removed from this tab.') }, 'Sign out'))),
    els.nav,
    els.main,
    h('footer', null, 'Read-only view. Your key is kept in this browser tab only and is forgotten when the tab closes.'));
  updateHeader();
}

function setConn(c) {
  if (state.conn === c) return;
  state.conn = c;
  updateHeader();
}

function updateHeader() {
  if (!els.host) return;
  const sys = state.system;
  els.host.textContent = sys?.hostname || location.hostname;
  els.os.textContent = sys ? [sys.os?.pretty_name || sys.os?.name, sys.architecture].filter(Boolean).join(' · ') : '';
  document.title = (sys?.hostname ? sys.hostname + ' · ' : '') + 'Cockpit';
  replace(els.health, state.health ? pill(state.health.status) : null);
  const words = {
    live: ['ok', 'Live' + (state.updated ? ' · ' + clock(state.updated) : '')],
    connecting: ['muted', 'Connecting…'],
    limited: ['warn', 'Rate limited, slowing down'],
    offline: ['crit', 'Agent unreachable, retrying'],
  }[state.conn];
  els.conn.className = 'conn ' + words[0];
  els.conn.textContent = words[1];
}

function route() {
  if (!session) return;
  const id = location.hash.replace(/^#\/?/, '') || 'overview';
  const def = screens.find(s => s.id === id) || screens[0];
  for (const a of els.nav.querySelectorAll('a')) {
    if (a.dataset.id === def.id) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  }
  screen?.destroy?.();
  els.main.replaceChildren();
  screen = def.mount(els.main) || {};
  screen.tick?.();
  screen.healthChanged?.();
  screen.eventsChanged?.();
}

window.addEventListener('hashchange', route);

document.addEventListener('keydown', e => {
  if (!session || e.ctrlKey || e.metaKey || e.altKey) return;
  const t = e.target;
  if (t instanceof HTMLInputElement || t instanceof HTMLSelectElement || t instanceof HTMLTextAreaElement) return;
  const n = Number(e.key);
  if (n >= 1 && n <= screens.length) {
    location.hash = '#/' + screens[n - 1].id;
    e.preventDefault();
  }
});

document.addEventListener('visibilitychange', () => {
  if (!document.hidden && session) session.pollers.forEach(p => p.refresh());
});

// ---- sign in ----

function signIn(message) {
  const input = h('input', { type: 'password', id: 'key', name: 'key', autocomplete: 'off', spellcheck: 'false', placeholder: 'ckp_…', required: true, autofocus: true });
  const show = h('input', { type: 'checkbox', id: 'show' });
  show.addEventListener('change', () => (input.type = show.checked ? 'text' : 'password'));
  const msg = h('p', { class: 'signin-msg', role: 'status' }, message || '');
  if (message) msg.classList.add('warn');
  const button = h('button', { type: 'submit', class: 'primary' }, 'Connect');
  const form = h('form', { class: 'signin', novalidate: true },
    h('div', { class: 'brand big' }, h('img', { src: 'icon.svg', alt: '', width: 34, height: 34 }), h('span', null, 'Cockpit')),
    h('p', { class: 'muted' }, `Enter an API key to view ${location.host}.`),
    h('label', { for: 'key' }, 'API key'),
    input,
    h('label', { class: 'check', for: 'show' }, show, ' Show key'),
    button,
    msg,
    h('p', { class: 'hint muted' },
      'Create a key in the terminal UI (API Access) or with ',
      h('code', null, 'cockpit keys create --scope all'),
      '. The key is kept in this tab only and is forgotten when you close it.'));
  form.addEventListener('submit', async e => {
    e.preventDefault();
    const key = input.value.trim();
    if (!key.startsWith('ckp_')) {
      replace(msg, 'Cockpit API keys start with ckp_.');
      msg.className = 'signin-msg crit';
      return;
    }
    button.disabled = true;
    replace(msg, 'Checking…');
    msg.className = 'signin-msg muted';
    setKey(key);
    try {
      await get('/system');
      start();
    } catch (err) {
      if (err instanceof APIError && err.status === 403) {
        start(); // a valid key without system:read
        return;
      }
      setKey('');
      button.disabled = false;
      msg.className = 'signin-msg crit';
      if (err instanceof APIError && err.status === 401) replace(msg, 'That key was not accepted. Check that it is complete and has not been revoked.');
      else if (err instanceof APIError && err.status === 429) replace(msg, 'Too many attempts. Wait a moment and try again.');
      else if (err instanceof APIError) replace(msg, err.message);
      else replace(msg, 'The agent could not be reached.');
    }
  });
  replace(root, h('div', { class: 'signin-wrap' }, form));
  input.focus();
}

// ---- screens ----
// Each screen gets an element and returns hooks: tick (after each
// metrics refresh), healthChanged, eventsChanged, destroy.

function metricsGate(body) {
  if (state.metricsError) {
    replace(body, errorNote(state.metricsError));
    return null;
  }
  if (!state.metrics) {
    replace(body, note('Loading…'));
    return null;
  }
  return state.metrics;
}

function Overview(el) {
  const cpuChart = new LineChart({ compact: true, height: 56, max: 100 });
  const memChart = new LineChart({ compact: true, height: 56, max: 100 });
  const netChart = new LineChart({ compact: true, height: 56, bytes: true });
  const box = {};
  const mk = (id, title, chart) => {
    box[id] = { body: h('div'), stat: h('div', { class: 'stat' }) };
    return h('section', { class: 'card ' + id }, h('h2', null, title), box[id].stat, chart ? chart.el : null, box[id].body);
  };
  const grid = h('div', { class: 'grid overview' },
    mk('cpu', 'CPU', cpuChart), mk('mem', 'Memory', memChart), mk('net', 'Network', netChart),
    mk('health', 'Health'), mk('system', 'System'), mk('fs', 'Storage'),
    mk('temps', 'Temperatures'), mk('power', 'Power'), mk('gpu', 'GPU'));
  const error = h('div');
  el.append(error, grid);

  const hide = (id, hidden) => box[id].body.parentElement.classList.toggle('hidden', hidden);

  function tick() {
    if (state.metricsError) replace(error, errorNote(state.metricsError));
    else replace(error);
    const m = state.metrics;
    const range = liveRange();
    // CPU
    const c = m ? section(m.cpu, 'CPU') : {};
    replace(box.cpu.stat, c.data ? h('span', { class: 'big ' + level(c.data.usage_percent) }, pct(c.data.usage_percent)) : null);
    cpuChart.set([{ name: 'CPU', color: 0, points: livePoints(state.live.cpu) }], range);
    replace(box.cpu.body, c.error || c.warn, c.data ? h('p', { class: 'muted' },
      `${c.data.logical_cores} logical cores`,
      c.data.load_average ? ` · load ${c.data.load_average['1m'].toFixed(2)} ${c.data.load_average['5m'].toFixed(2)} ${c.data.load_average['15m'].toFixed(2)}` : '') : null);
    // Memory
    const mm = m ? section(m.memory, 'Memory') : {};
    replace(box.mem.stat, mm.data ? h('span', { class: 'big ' + level(mm.data.used_percent, 85, 95) }, pct(mm.data.used_percent)) : null);
    memChart.set([{ name: 'Memory', color: 1, points: livePoints(state.live.mem) }], range);
    replace(box.mem.body, mm.error || mm.warn, mm.data ? [
      h('p', { class: 'muted' }, `${bytes(mm.data.used_bytes)} of ${bytes(mm.data.total_bytes)} used · ${bytes(mm.data.available_bytes)} available`),
      mm.data.swap_total_bytes ? h('p', { class: 'muted' }, `Swap ${bytes(mm.data.swap_used_bytes)} of ${bytes(mm.data.swap_total_bytes)}`) : null,
    ] : null);
    // Network
    const L = state.live;
    const rx = L.rx[L.rx.length - 1];
    const tx = L.tx[L.tx.length - 1];
    replace(box.net.stat, h('span', { class: 'big small' }, h('span', { class: 's2' }, '↓ ', rate(rx)), '  ', h('span', { class: 's3' }, '↑ ', rate(tx))));
    netChart.set([{ name: 'Received', color: 2, points: livePoints(L.rx) }, { name: 'Sent', color: 3, points: livePoints(L.tx) }], range);
    const n = m ? section(m.network, 'Network') : {};
    replace(box.net.body, n.error || n.warn, n.data ? h('p', { class: 'muted' },
      n.data.filter(i => !i.loopback && i.oper_state === 'up').map(i => i.name).join(', ') || 'No active interfaces') : null);
    // System
    const sys = state.system;
    replace(box.system.body, sys ? kv([
      ['Host', sys.hostname],
      ['OS', sys.os?.pretty_name || sys.os?.name],
      ['Kernel', sys.kernel],
      ['Uptime', duration(sys.boot_time ? (Date.now() - Date.parse(sys.boot_time)) / 1000 : sys.uptime_seconds)],
      state.agent ? ['Agent', state.agent.version] : null,
    ]) : note('Needs the system:read scope.'));
    // Storage
    const fs = m ? section(m.filesystems, 'Storage') : {};
    replace(box.fs.body, fs.error || fs.warn, fs.data ? fsList([...fs.data].sort((a, b) => b.used_percent - a.used_percent).slice(0, 6)) : null);
    // Temperatures
    const sn = m ? section(m.sensors, 'Sensors') : {};
    const temps = (sn.data || []).filter(s => s.kind === 'temperature').sort((a, b) => b.value - a.value).slice(0, 8);
    replace(box.temps.body, sn.error || null, temps.length ? sensorList(temps) : sn.data ? note('No temperature sensors found.') : null);
    // Power
    const pw = m ? section(m.power, 'Power') : {};
    hide('power', !pw.data?.length);
    if (pw.data?.length) replace(box.power.body, table([
      { label: 'Supply', value: p => p.name },
      { label: 'Charge', value: p => pct(p.capacity_percent, 0) },
      { label: 'Status', value: p => p.status || (p.online ? 'Online' : p.online === false ? 'Offline' : '—') },
      { label: 'Left', value: p => (p.hours_remaining != null ? duration(p.hours_remaining * 3600) : '—') },
    ], pw.data));
    // GPU
    const g = m ? section(m.gpu, 'GPU') : {};
    hide('gpu', !g.data?.length);
    if (g.data?.length) replace(box.gpu.body, table([
      { label: 'GPU', value: x => x.name },
      { label: 'Use', value: x => pct(x.utilization_percent, 0), class: 'r' },
      { label: 'Temp', value: x => (x.temperature_celsius != null ? x.temperature_celsius.toFixed(0) + ' °C' : '—'), class: 'r' },
      { label: 'Memory', value: x => (x.memory_total_bytes ? `${bytes(x.memory_used_bytes)} / ${bytes(x.memory_total_bytes)}` : '—'), class: 'r' },
    ], g.data));
  }

  function healthChanged() {
    if (state.healthError) return replace(box.health.body, errorNote(state.healthError));
    const hl = state.health;
    if (!hl) return replace(box.health.body, note('Loading…'));
    const findings = hl.checks.flatMap(c => c.findings);
    replace(box.health.stat, pill(hl.status));
    replace(box.health.body,
      h('p', { class: 'muted' }, `${hl.checks.length} checks · ${findings.length} active ${findings.length === 1 ? 'finding' : 'findings'}`),
      findings.length
        ? h('ul', { class: 'findings-mini' }, findings.slice(0, 5).map(f => h('li', null, pill(f.severity), ' ', h('b', null, f.subject), ' ', f.summary)))
        : h('p', { class: 'ok' }, 'No problems found.'),
      h('a', { href: '#/health' }, 'Details →'));
  }

  return { tick, healthChanged };
}

function fsList(list) {
  return h('ul', { class: 'meters' }, list.map(f => h('li', null,
    h('span', { class: 'name', title: f.mount_point }, f.mount_point),
    bar(f.used_percent, level(f.used_percent, 80, 90)),
    h('span', { class: 'val' }, `${bytes(f.used_bytes)} / ${bytes(f.total_bytes)}`))));
}

function sensorList(list) {
  return h('ul', { class: 'meters' }, list.map(s => {
    const crit = s.critical ?? 100;
    const high = s.high ?? crit - 10;
    const cls = s.value >= crit ? 'crit' : s.value >= high ? 'warn' : 'ok';
    return h('li', null,
      h('span', { class: 'name', title: s.chip }, s.label || s.chip),
      bar((s.value / crit) * 100, cls),
      h('span', { class: 'val ' + cls }, s.value.toFixed(0) + ' °C'));
  }));
}

const ranges = [
  { id: 'live', name: 'Live', ms: 0 },
  { id: '1h', name: '1 h', ms: 3600e3 },
  { id: '6h', name: '6 h', ms: 6 * 3600e3 },
  { id: '24h', name: '24 h', ms: 86400e3 },
  { id: '7d', name: '7 d', ms: 7 * 86400e3 },
  { id: '30d', name: '30 d', ms: 30 * 86400e3 },
];

function Performance(el) {
  let rangeId = 'live';
  try {
    rangeId = sessionStorage.getItem('cockpit.range') || 'live';
  } catch {
    // ignore
  }
  const charts = {
    cpu: new LineChart({ title: 'CPU usage', max: 100, format: v => v.toFixed(0) + '%' }),
    mem: new LineChart({ title: 'Memory used', max: 100, format: v => v.toFixed(0) + '%' }),
    load: new LineChart({ title: 'Load average', format: v => v.toFixed(2) }),
    net: new LineChart({ title: 'Network', format: v => rate(v), bytes: true }),
    fs: new LineChart({ title: 'Storage used', max: 100, format: v => v.toFixed(0) + '%' }),
  };
  const msg = h('div');
  const buttons = h('div', { class: 'seg', role: 'group', 'aria-label': 'Time range' });
  const cards = {
    cpu: card('CPU usage', charts.cpu.el),
    mem: card('Memory used', charts.mem.el),
    load: card('Load average (1 minute)', charts.load.el),
    net: card('Network', charts.net.el),
    fs: card('Storage used', charts.fs.el),
  };
  el.append(h('div', { class: 'toolbar' }, buttons, msg), h('div', { class: 'grid charts' }, Object.values(cards)));
  let poll = null;
  let seriesNames = null;

  function drawButtons() {
    replace(buttons, ranges.map(r => h('button', {
      type: 'button', 'aria-pressed': String(r.id === rangeId),
      onclick: () => {
        rangeId = r.id;
        try {
          sessionStorage.setItem('cockpit.range', r.id);
        } catch {
          // ignore
        }
        drawButtons();
        restart();
      },
    }, r.name)));
  }

  function liveTick() {
    if (rangeId !== 'live') return;
    const range = liveRange();
    const L = state.live;
    replace(msg, h('span', { class: 'muted' }, 'Last 5 minutes, updated every 2 seconds. Longer ranges come from stored history.'));
    charts.cpu.set([{ name: 'CPU', color: 0, points: livePoints(L.cpu) }], range);
    charts.mem.set([{ name: 'Memory', color: 1, points: livePoints(L.mem) }], range);
    charts.net.set([{ name: 'Received', color: 2, points: livePoints(L.rx) }, { name: 'Sent', color: 3, points: livePoints(L.tx) }], range);
    // Load and storage have no live samples; they show for history ranges.
    cards.load.classList.add('hidden');
    cards.fs.classList.add('hidden');
  }

  function points(s, raw) {
    return s.points.map(p => (raw ? { t: Date.parse(p.t), v: p.avg } : { t: Date.parse(p.t), v: p.avg, lo: p.min, hi: p.max }));
  }

  async function history(signal) {
    const r = ranges.find(x => x.id === rangeId);
    const to = new Date();
    const from = new Date(to.getTime() - r.ms);
    if (!seriesNames) {
      const list = await fetchOr('/metrics/history/series', null, signal);
      if (list.error) return replace(msg, errorNote(list.error));
      if (list.data.status !== 'available') return replace(msg, note(list.data.reason || 'History is not recorded on this agent.', 'warn'));
      seriesNames = list.data.items.map(i => i.name);
    }
    const ifaces = (state.metrics?.network?.data || []).filter(i => !i.loopback && i.oper_state === 'up').map(i => i.name);
    const net = seriesNames.filter(n => {
      const m = /^net\.(.+)\.(rx|tx)_bytes_per_second$/.exec(n);
      return m && (ifaces.length ? ifaces.includes(m[1]) : true);
    }).slice(0, 6);
    const fs = seriesNames.filter(n => /^fs\..+\.used_percent$/.test(n)).slice(0, 6);
    const base = ['cpu.usage_percent', 'memory.used_percent', 'cpu.load_1m'].filter(n => seriesNames.includes(n));
    const q = names => names.length ? fetchOr('/metrics/history', { series: names.join(','), from: from.toISOString(), to: to.toISOString() }, signal) : Promise.resolve({ data: null });
    const [a, b, c] = await Promise.all([q(base), q(net), q(fs)]);
    const err = a.error || b.error || c.error;
    if (err) return replace(msg, errorNote(err));
    const res = a.data || b.data || c.data;
    replace(msg, res ? h('span', { class: 'muted' }, `Resolution: ${res.resolution === 'raw' ? 'every 10 seconds' : res.resolution === '1m' ? '1-minute averages, band shows min–max' : '1-hour averages, band shows min–max'}.`) : null);
    const range = { from: from.getTime(), to: to.getTime(), step: (res?.step_seconds || 10) * 1000 };
    const find = (d, name) => d?.series.find(s => s.name === name);
    const raw = res?.resolution === 'raw';
    const one = (chart, d, name, label, color) => {
      const s = find(d, name);
      chart.set(s ? [{ name: label, color, points: points(s, raw) }] : [], range);
    };
    one(charts.cpu, a.data, 'cpu.usage_percent', 'CPU', 0);
    one(charts.mem, a.data, 'memory.used_percent', 'Memory', 1);
    one(charts.load, a.data, 'cpu.load_1m', 'Load 1m', 4);
    cards.load.classList.toggle('hidden', !find(a.data, 'cpu.load_1m'));
    charts.net.set((b.data?.series || []).map((s, i) => ({
      name: s.name.replace(/^net\.(.+)\.(rx|tx)_bytes_per_second$/, (_, i, d) => `${i} ${d === 'rx' ? '↓' : '↑'}`),
      color: 2 + i, points: points(s, raw),
    })), range);
    charts.fs.set((c.data?.series || []).map((s, i) => ({ name: s.name.replace(/^fs\.(.+)\.used_percent$/, '$1'), color: i, points: points(s, raw) })), range);
    cards.fs.classList.toggle('hidden', !c.data?.series?.length);
  }

  function restart() {
    poll?.stop();
    poll = null;
    if (rangeId === 'live') {
      cards.load.classList.add('hidden');
      liveTick();
      return;
    }
    const r = ranges.find(x => x.id === rangeId);
    poll = poller(history, r.ms <= 3600e3 ? 10000 : 60000);
  }

  drawButtons();
  restart();
  return { tick: liveTick, destroy: () => poll?.stop() };
}

// A check that cannot run here (no such hardware, or turned off) is still
// "unknown" in the API; it is shown as N/A so it does not look like a
// problem, and listed last.
function notApplicable(c) {
  return c.status === 'unknown' && (c.availability === 'unsupported' || c.availability === 'not_configured');
}

function sortChecks(checks) {
  const rank = c => notApplicable(c) ? 4 : ({ critical: 0, warning: 1, unknown: 2, ok: 3 }[c.status] ?? 2);
  return checks.map((c, i) => [c, i]).sort((a, b) => rank(a[0]) - rank(b[0]) || a[1] - b[1]).map(x => x[0]);
}

function Health(el) {
  const body = h('div');
  el.append(body);
  function healthChanged() {
    if (state.healthError) return replace(body, errorNote(state.healthError));
    const hl = state.health;
    if (!hl) return replace(body, note('Loading…'));
    const findings = hl.checks.flatMap(c => c.findings);
    replace(body,
      h('div', { class: 'toolbar' }, h('span', { class: 'big-pill' }, 'Overall ', pill(hl.status)), h('span', { class: 'muted' }, 'Evaluated ' + clock(hl.evaluated_at))),
      card('Checks', table([
        { label: 'Check', value: c => c.name },
        { label: 'Status', value: c => notApplicable(c) ? pill('n/a') : pill(c.status) },
        { label: 'Details', value: c => c.summary || c.reason || (c.availability !== 'available' ? c.availability.replace(/_/g, ' ') : '') },
      ], sortChecks(hl.checks))),
      card(`Findings (${findings.length})`, findings.length ? findings.map(f => h('article', { class: 'finding ' + (f.severity === 'critical' ? 'crit' : 'warn') },
        h('header', null, pill(f.severity), h('b', null, f.subject), h('span', { class: 'muted' }, 'since ' + clock(f.since))),
        h('p', null, f.summary),
        Object.keys(f.evidence || {}).length ? kv(Object.entries(f.evidence)) : null,
        f.recommendation ? h('p', { class: 'muted' }, f.recommendation) : null)) : h('p', { class: 'ok' }, 'No active findings.')));
  }
  return { healthChanged };
}

function Storage(el) {
  const body = h('div', null, note('Loading…'));
  el.append(body);
  const poll = poller(async signal => {
    const r = await fetchOr('/storage', null, signal);
    if (r.error) return replace(body, errorNote(r.error));
    const d = r.data;
    const fs = section(d.filesystems, 'Filesystems');
    const dh = section(d.disk_health, 'Disk health');
    const bd = section(d.block_devices, 'Disks');
    const z = section(d.zfs, 'ZFS');
    replace(body,
      card('Filesystems', fs.error || fs.warn, fs.data ? table([
        { label: 'Mount', value: f => f.mount_point },
        { label: 'Type', value: f => f.type, class: 'muted' },
        { label: 'Used', value: f => h('span', { class: 'meter-cell' }, bar(f.used_percent, level(f.used_percent, 80, 90)), pct(f.used_percent, 0)) },
        { label: 'Used / total', value: f => `${bytes(f.used_bytes)} / ${bytes(f.total_bytes)}`, class: 'r' },
        { label: 'Free', value: f => bytes(f.available_bytes), class: 'r' },
        { label: 'Inodes', value: f => pct(f.inodes_used_percent, 0), class: 'r' },
        { label: '', value: f => (f.read_only ? 'read-only' : ''), class: 'muted' },
      ], fs.data) : null),
      card('Disk health', dh.error || dh.warn, dh.data ? table([
        { label: 'Disk', value: x => x.device },
        { label: 'Type', value: x => x.protocol, class: 'muted' },
        { label: 'SMART', value: x => (x.smart_passed == null ? '—' : x.smart_passed ? pill('ok') : pill('critical')) },
        { label: 'Temp', value: x => (x.temperature_celsius != null ? x.temperature_celsius.toFixed(0) + ' °C' : '—'), class: 'r' },
        { label: 'Power-on', value: x => (x.power_on_hours != null ? num(x.power_on_hours) + ' h' : '—'), class: 'r' },
        { label: 'Wear', value: x => (x.percentage_used != null ? x.percentage_used + '%' : '—'), class: 'r' },
        { label: 'Errors', value: x => num((x.media_errors || 0) + (x.reallocated_sectors || 0) + (x.pending_sectors || 0)), class: 'r' },
      ], dh.data, 'No disks reported.') : null),
      card('Disks', bd.error || bd.warn, bd.data ? table([
        { label: 'Name', value: x => x.name },
        { label: 'Model', value: x => x.model },
        { label: 'Kind', value: x => (x.rotational == null ? '—' : x.rotational ? 'HDD' : 'SSD') + (x.removable ? ', removable' : '') },
        { label: 'Bus', value: x => x.transport, class: 'muted' },
        { label: 'Size', value: x => bytes(x.size_bytes), class: 'r' },
      ], bd.data) : null),
      z.data && z.data.pools?.length ? card('ZFS', z.warn, table([
        { label: 'Pool', value: p => p.name },
        { label: 'State', value: p => p.state },
        { label: 'Used', value: p => (p.size_bytes ? `${bytes(p.allocated_bytes)} / ${bytes(p.size_bytes)}` : '—'), class: 'r' },
        { label: 'Errors (r/w/ck)', value: p => [p.read_errors, p.write_errors, p.checksum_errors].map(v => v ?? '—').join(' / '), class: 'r' },
      ], z.data.pools), z.data.arc ? kv([['ARC size', bytes(z.data.arc.size_bytes)], ['ARC hit rate', pct(z.data.arc.hit_percent)]]) : null, z.data.detail_reason ? note(z.data.detail_reason) : null) : null);
  }, 5000);
  return { destroy: () => poll.stop() };
}

function Network(el) {
  const body = h('div', null, note('Loading…'));
  el.append(body);
  const poll = poller(async signal => {
    const r = await fetchOr('/network', null, signal);
    if (r.error) return replace(body, errorNote(r.error));
    const d = r.data;
    const ifs = section(d.interfaces, 'Interfaces');
    const rt = section(d.routes, 'Routes');
    const dns = section(d.dns, 'DNS');
    replace(body,
      card('Interfaces', ifs.error || ifs.warn, ifs.data ? table([
        { label: 'Name', value: i => i.name },
        { label: 'State', value: i => h('span', { class: i.oper_state === 'up' ? 'ok' : 'muted' }, i.oper_state) },
        { label: '↓ Rate', value: i => rate(i.rx_bytes_per_second), class: 'r s2' },
        { label: '↑ Rate', value: i => rate(i.tx_bytes_per_second), class: 'r s3' },
        { label: 'Received', value: i => bytes(i.rx_bytes), class: 'r' },
        { label: 'Sent', value: i => bytes(i.tx_bytes), class: 'r' },
        { label: 'Errors', value: i => { const n = i.rx_errors + i.tx_errors; return h('span', { class: n ? 'warn' : 'muted' }, num(n)); }, class: 'r' },
        { label: 'Speed', value: i => (i.speed_mbps ? num(i.speed_mbps) + ' Mb/s' : '—'), class: 'r' },
        { label: 'Addresses', value: i => (i.addresses || []).join(', '), class: 'addr' },
      ], [...ifs.data].sort((a, b) => a.loopback - b.loopback || a.name.localeCompare(b.name))) : null),
      h('div', { class: 'grid two' },
        card('Routes', rt.error || rt.warn, rt.data ? table([
          { label: 'Destination', value: x => x.destination + (x.default ? ' (default)' : '') },
          { label: 'Gateway', value: x => x.gateway || '—' },
          { label: 'Interface', value: x => x.interface },
          { label: 'Metric', value: x => x.metric, class: 'r' },
        ], rt.data) : null),
        card('DNS', dns.error || dns.warn, dns.data ? kv([
          ['Nameservers', dns.data.nameservers.join(', ')],
          ['Search', dns.data.search.join(', ')],
          ['Resolver', dns.data.resolver],
        ]) : null)));
  }, 2000);
  return { destroy: () => poll.stop() };
}

function Processes(el) {
  const opts = { sort: 'cpu', search: '', kernel: false };
  const search = h('input', { type: 'search', placeholder: 'Filter by name or user', 'aria-label': 'Filter processes', maxlength: 64 });
  const kernel = h('input', { type: 'checkbox', id: 'kthreads' });
  const info = h('span', { class: 'muted' });
  const body = h('div', null, note('Loading…'));
  let timer = 0;
  search.addEventListener('input', () => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      opts.search = search.value.trim();
      poll.refresh();
    }, 300);
  });
  kernel.addEventListener('change', () => {
    opts.kernel = kernel.checked;
    poll.refresh();
  });
  el.append(h('div', { class: 'toolbar' }, search, h('label', { class: 'check', for: 'kthreads' }, kernel, ' Kernel threads'), info), card(null, body));
  const sortBy = key => () => {
    opts.sort = key;
    poll.refresh();
  };
  // Column headers that sort the list; cpu and memory sort high first.
  const head = (label, key) => h('button', { type: 'button', class: 'th-sort' + (opts.sort === key ? ' on' : ''), onclick: sortBy(key) },
    label, opts.sort === key ? (key === 'cpu' || key === 'memory' ? ' ▾' : ' ▴') : '');
  const poll = poller(async signal => {
    const r = await fetchOr('/processes', { sort: opts.sort, search: opts.search, kernel_threads: opts.kernel, limit: 100 }, signal);
    if (r.error) return replace(body, errorNote(r.error));
    const d = r.data;
    replace(info, `${d.items.length} of ${num(d.total)} processes` + (d.partial ? ' · some could not be read' : '') + ' · command lines are never shown');
    if (d.status !== 'available' && !d.items.length) return replace(body, note(d.reason || 'Processes are not available.'));
    replace(body, table([
      { label: head('PID', 'pid'), value: p => p.pid, class: 'r' },
      { label: head('Name', 'name'), value: p => h('span', { title: p.executable || p.executable_reason || '' }, p.name) },
      { label: 'User', value: p => p.user || p.uid, class: 'muted' },
      { label: head('CPU %', 'cpu'), value: p => h('span', { class: level(p.cpu_percent, 50, 90) }, p.cpu_percent == null ? '—' : p.cpu_percent.toFixed(1)), class: 'r' },
      { label: head('Mem %', 'memory'), value: p => p.memory_percent.toFixed(1), class: 'r' },
      { label: 'RSS', value: p => bytes(p.rss_bytes), class: 'r' },
      { label: 'Threads', value: p => p.threads, class: 'r' },
      { label: 'State', value: p => p.state, class: 'muted' },
      { label: 'Started', value: p => (p.started_at ? ago(p.started_at) : '—'), class: 'muted r' },
    ], d.items, 'No matching processes.'));
  }, 2000);
  return { destroy: () => poll.stop() };
}

function Services(el) {
  let filter = '';
  let stateFilter = '';
  const search = h('input', { type: 'search', placeholder: 'Filter by name or description', 'aria-label': 'Filter services' });
  const chips = h('div', { class: 'seg', role: 'group', 'aria-label': 'Service state' });
  const body = h('div', null, note('Loading…'));
  let last = null;
  el.append(h('div', { class: 'toolbar' }, chips, search), card(null, body));
  search.addEventListener('input', () => {
    filter = search.value.trim().toLowerCase();
    draw();
  });
  function draw() {
    if (!last) return;
    if (last.error) return replace(body, errorNote(last.error));
    const s = section(last.data.services, 'Services');
    if (!s.data) return replace(body, s.error);
    const d = s.data;
    replace(chips, [['', 'All', d.total], ['active', 'Active', d.active], ['failed', 'Failed', d.failed], ['inactive', 'Inactive', d.inactive]].map(([id, name, n]) =>
      h('button', { type: 'button', 'aria-pressed': String(stateFilter === id), class: id === 'failed' && n ? 'crit' : '', onclick: () => { stateFilter = id; poll.refresh(); } }, `${name} ${n}`)));
    const items = d.items.filter(x => !filter || x.name.toLowerCase().includes(filter) || x.description.toLowerCase().includes(filter));
    replace(body, s.warn, table([
      { label: 'Service', value: x => x.name },
      { label: 'State', value: x => h('span', { class: x.active_state === 'active' ? 'ok' : x.active_state === 'failed' ? 'crit' : 'muted' }, x.active_state) },
      { label: 'Sub-state', value: x => x.sub_state, class: 'muted' },
      { label: 'Description', value: x => x.description },
    ], items, 'No matching services.'));
  }
  const poll = poller(async signal => {
    last = await fetchOr('/services', { state: stateFilter }, signal);
    draw();
  }, 10000);
  return { destroy: () => poll.stop() };
}

function Events(el) {
  let sev = '';
  const select = h('select', { 'aria-label': 'Minimum severity' },
    h('option', { value: '' }, 'All severities'), h('option', { value: 'warning' }, 'Warning and critical'), h('option', { value: 'critical' }, 'Critical only'));
  select.addEventListener('change', () => {
    sev = select.value;
    eventsChanged();
  });
  const live = h('span');
  const body = h('div');
  const more = h('button', { type: 'button', class: 'ghost', onclick: () => loadEvents(true) }, 'Load older');
  el.append(h('div', { class: 'toolbar' }, select, live), card(null, body), h('div', { class: 'more' }, more));
  const rank = { info: 0, warning: 1, critical: 2 };
  function eventsChanged() {
    live.className = state.eventsLive ? 'ok' : 'muted';
    live.textContent = state.eventsLive ? '● Live' : 'Not live, reconnecting…';
    if (state.eventsError) {
      live.textContent = '';
      more.hidden = true;
      return replace(body, errorNote(state.eventsError));
    }
    more.hidden = !state.eventsCursor;
    const items = state.events.filter(e => !sev || rank[e.severity] >= rank[sev]);
    replace(body, table([
      { label: 'Time', value: e => clock(e.time), class: 'muted nowrap' },
      { label: 'Severity', value: e => pill(e.severity) },
      { label: 'Type', value: e => e.type, class: 'muted' },
      { label: 'Subject', value: e => e.subject },
      { label: 'Message', value: e => e.message },
    ], items, 'No events yet.'));
  }
  return { eventsChanged };
}

function Hardware(el) {
  const body = h('div', null, note('Loading…'));
  el.append(body);
  const poll = poller(async signal => {
    const r = await fetchOr('/hardware', null, signal);
    if (r.error) return replace(body, errorNote(r.error));
    const d = r.data;
    const cpu = section(d.cpu, 'CPU');
    const mem = section(d.memory, 'Memory');
    const mods = section(d.memory_modules, 'Memory modules');
    const board = section(d.board, 'Board');
    const gpus = section(d.gpus, 'GPUs');
    const disks = section(d.block_devices, 'Disks');
    const nics = section(d.network_interfaces, 'Network interfaces');
    const pci = section(d.pci_devices, 'PCI devices');
    const usb = section(d.usb_devices, 'USB devices');
    const c = cpu.data;
    const b = board.data;
    const fold = (title, s, cols) => (s.data?.length ? h('details', { class: 'card' }, h('summary', null, `${title} (${s.data.length})`), table(cols, s.data)) : card(title, s.error || note('None found.')));
    replace(body,
      h('div', { class: 'grid two' },
        card('Processor', cpu.error, c ? kv([
          ['Model', c.model_name],
          ['Vendor', c.vendor],
          ['Cores', `${c.cores || '—'} cores, ${c.logical_cores} threads` + (c.sockets > 1 ? `, ${c.sockets} sockets` : '')],
          ['Max frequency', c.max_frequency_mhz ? (c.max_frequency_mhz / 1000).toFixed(2) + ' GHz' : ''],
          ['Caches', (c.caches || []).map(x => `L${x.level}${x.type === 'Data' ? 'd' : x.type === 'Instruction' ? 'i' : ''} ${bytes(x.size_bytes)}`).join(', ')],
          ['Virtualization', c.virtualization + (c.hypervisor ? ' (running in a VM)' : '')],
        ]) : null),
        card('Memory and board', mem.error && board.error ? mem.error : null, kv([
          mem.data ? ['Memory', bytes(mem.data.total_bytes)] : null,
          mem.data ? ['Swap', bytes(mem.data.swap_total_bytes)] : null,
          b ? ['System', [b.system_vendor, b.product_name].filter(Boolean).join(' ')] : null,
          b ? ['Board', [b.board_vendor, b.board_name].filter(Boolean).join(' ')] : null,
          b ? ['Firmware', [b.bios_vendor, b.bios_version, b.bios_date].filter(Boolean).join(' ')] : null,
          b ? ['Chassis', b.chassis_type] : null,
        ]))),
      gpus.data?.length ? card('Graphics', table([
        { label: 'Name', value: g => g.name },
        { label: 'Vendor', value: g => g.vendor },
        { label: 'Driver', value: g => [g.driver, g.driver_version].filter(Boolean).join(' ') },
        { label: 'Memory', value: g => bytes(g.memory_total_bytes), class: 'r' },
      ], gpus.data)) : null,
      fold('Memory modules', mods, [
        { label: 'Slot', value: m => m.locator },
        { label: 'Size', value: m => bytes(m.size_bytes), class: 'r' },
        { label: 'Type', value: m => m.type },
        { label: 'Speed', value: m => (m.speed_mts ? m.speed_mts + ' MT/s' : '—'), class: 'r' },
        { label: 'Maker', value: m => [m.manufacturer, m.part_number].filter(Boolean).join(' ') },
      ]),
      fold('Disks', disks, [
        { label: 'Name', value: x => x.name },
        { label: 'Model', value: x => x.model },
        { label: 'Bus', value: x => x.transport },
        { label: 'Size', value: x => bytes(x.size_bytes), class: 'r' },
      ]),
      fold('Network interfaces', nics, [
        { label: 'Name', value: x => x.name },
        { label: 'MTU', value: x => x.mtu, class: 'r' },
        { label: 'Speed', value: x => (x.speed_mbps ? num(x.speed_mbps) + ' Mb/s' : '—'), class: 'r' },
      ]),
      fold('PCI devices', pci, [
        { label: 'Address', value: x => x.address, class: 'muted' },
        { label: 'Class', value: x => x.class_name },
        { label: 'Device', value: x => [x.vendor_name, x.device_name].filter(Boolean).join(' ') },
        { label: 'Driver', value: x => x.driver, class: 'muted' },
      ]),
      fold('USB devices', usb, [
        { label: 'Product', value: x => [x.manufacturer, x.product].filter(Boolean).join(' ') || `${x.vendor_id}:${x.product_id}` },
        { label: 'ID', value: x => `${x.vendor_id}:${x.product_id}`, class: 'muted' },
        { label: 'Speed', value: x => (x.speed_mbps ? num(x.speed_mbps) + ' Mb/s' : '—'), class: 'r' },
      ]));
  }, 60000);
  return { destroy: () => poll.stop() };
}

// ---- start ----

if (getKey()) start();
else signIn();

// API access from the browser (D35). Key in the Authorization header, no
// cookies. This page's own key lives in the tab's sessionStorage, so it is
// gone when the tab closes. Calls can also go, after setTarget, to another
// machine (D45, D46), each with its own key; those keys are remembered in
// this browser's localStorage until the machine is removed.

const storageName = 'cockpit.key';
const memoryKeys = {};
let target = ''; // '' is this page's origin; otherwise another agent's origin
const versions = {};

// setTarget points get and stream at another agent's origin ('' for this
// page's own agent).
export function setTarget(origin) {
  target = origin || '';
}

export function getTarget() {
  return target;
}

function storageKey(origin) {
  return origin ? storageName + ':' + origin : storageName;
}

// keyFor and setKeyFor read and write the key for one origin.
export function keyFor(origin) {
  const k = storageKey(origin);
  try {
    return sessionStorage.getItem(k) || (origin ? localStorage.getItem(k) : '') || memoryKeys[origin || ''] || '';
  } catch {
    return memoryKeys[origin || ''] || '';
  }
}

export function setKeyFor(origin, key) {
  memoryKeys[origin || ''] = key;
  const k = storageKey(origin);
  try {
    if (origin) {
      sessionStorage.removeItem(k);
      if (key) localStorage.setItem(k, key);
      else localStorage.removeItem(k);
    } else if (key) sessionStorage.setItem(k, key);
    else sessionStorage.removeItem(k);
  } catch {
    // Storage blocked: the key stays in memory until the page reloads.
  }
}

export class APIError extends Error {
  constructor(status, code, message, retryAfter) {
    super(message);
    this.status = status;
    this.code = code;
    this.retryAfter = retryAfter;
  }
}

export function getKey() {
  return keyFor(target);
}

export function setKey(key) {
  setKeyFor(target, key);
}

function url(base, path, params) {
  const u = new URL('/api/v1' + path, base || location.origin);
  for (const [k, v] of Object.entries(params || {})) {
    if (v !== undefined && v !== null && v !== '') u.searchParams.set(k, String(v));
  }
  return u;
}

function headers(key, accept) {
  return { Authorization: 'Bearer ' + key, Accept: accept };
}

async function failure(res) {
  let code = '';
  let message = res.statusText || 'Request failed.';
  try {
    const body = await res.json();
    code = body.error.code;
    message = body.error.message;
  } catch {
    // Not an error envelope.
  }
  return new APIError(res.status, code, message, Number(res.headers.get('Retry-After')) || 0);
}

// version returns the Cockpit version from the last authenticated
// response (X-Cockpit-Version) of the current target, or of origin.
export function version(origin) {
  return versions[origin === undefined ? target : origin] || '';
}

function remember(origin, res) {
  const v = res.headers.get('X-Cockpit-Version');
  if (v) versions[origin] = v;
}

// getFrom fetches one API resource as JSON from origin ('' for this page's
// agent) with key.
export async function getFrom(origin, key, path, params, signal) {
  const res = await fetch(url(origin, path, params), {
    headers: headers(key, 'application/json'),
    cache: 'no-store',
    credentials: 'omit',
    mode: 'cors',
    signal,
  });
  remember(origin, res);
  if (!res.ok) throw await failure(res);
  return res.json();
}

// get fetches one API resource as JSON from the current target.
export function get(path, params, signal) {
  return getFrom(target, getKey(), path, params, signal);
}

// stream reads Server-Sent Events with fetch, since EventSource cannot
// send an Authorization header. It calls onOpen once connected, then
// onEvent(type, data, id) for each event, and returns when the stream
// ends.
export async function stream(path, params, onOpen, onEvent, signal) {
  const origin = target;
  const res = await fetch(url(origin, path, params), {
    headers: headers(getKey(), 'text/event-stream'),
    cache: 'no-store',
    credentials: 'omit',
    mode: 'cors',
    signal,
  });
  remember(origin, res);
  if (!res.ok) throw await failure(res);
  onOpen();
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) return;
    buf += value.replace(/\r\n?/g, '\n');
    let end;
    while ((end = buf.indexOf('\n\n')) >= 0) {
      const block = buf.slice(0, end);
      buf = buf.slice(end + 2);
      let type = 'message';
      let id = '';
      const data = [];
      for (const line of block.split('\n')) {
        if (line === '' || line.startsWith(':')) continue;
        const i = line.indexOf(':');
        const field = i < 0 ? line : line.slice(0, i);
        const val = i < 0 ? '' : line.slice(i + 1).replace(/^ /, '');
        if (field === 'event') type = val;
        else if (field === 'data') data.push(val);
        else if (field === 'id') id = val;
      }
      if (data.length) onEvent(type, data.join('\n'), id);
    }
    if (buf.length > 1 << 20) throw new Error('Event stream message too large.');
  }
}

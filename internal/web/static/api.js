// API access from the browser (D35). Same origin, key in the
// Authorization header, no cookies. The key lives in this tab's
// sessionStorage only, so it is gone when the tab closes.

const storageName = 'cockpit.key';
let memoryKey = '';

export class APIError extends Error {
  constructor(status, code, message, retryAfter) {
    super(message);
    this.status = status;
    this.code = code;
    this.retryAfter = retryAfter;
  }
}

export function getKey() {
  try {
    return sessionStorage.getItem(storageName) || memoryKey;
  } catch {
    return memoryKey;
  }
}

export function setKey(key) {
  memoryKey = key;
  try {
    if (key) sessionStorage.setItem(storageName, key);
    else sessionStorage.removeItem(storageName);
  } catch {
    // Storage blocked: the key stays in memory until the page reloads.
  }
}

function url(path, params) {
  const u = new URL('/api/v1' + path, location.origin);
  for (const [k, v] of Object.entries(params || {})) {
    if (v !== undefined && v !== null && v !== '') u.searchParams.set(k, String(v));
  }
  return u;
}

function headers(accept) {
  return { Authorization: 'Bearer ' + getKey(), Accept: accept };
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

// get fetches one API resource as JSON.
export async function get(path, params, signal) {
  const res = await fetch(url(path, params), {
    headers: headers('application/json'),
    cache: 'no-store',
    credentials: 'omit',
    signal,
  });
  if (!res.ok) throw await failure(res);
  return res.json();
}

// stream reads Server-Sent Events with fetch, since EventSource cannot
// send an Authorization header. It calls onOpen once connected, then
// onEvent(type, data, id) for each event, and returns when the stream
// ends.
export async function stream(path, params, onOpen, onEvent, signal) {
  const res = await fetch(url(path, params), {
    headers: headers('text/event-stream'),
    cache: 'no-store',
    credentials: 'omit',
    signal,
  });
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

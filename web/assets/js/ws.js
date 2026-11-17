// Client for the /api/v1/ws contract (docs/WEBSOCKET.md). One connection per tab, opened on
// the first subscription and closed after the last one. It reconnects with exponential backoff
// and jitter, re-subscribing every channel; the server then resends each channel's history.

const RETRY_MIN_MS = 1000;
const RETRY_MAX_MS = 30000;
const COMMAND_TIMEOUT_MS = 10000;
const CLOSE_UNAUTHENTICATED = 4401;

export class WsError extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

let socket = null;
let state = 'closed'; // closed | connecting | open | reconnecting
let retryMs = RETRY_MIN_MS;
let retryTimer = null;
let seq = 0;
const channels = new Map(); // channel -> Set of handlers
const pending = new Map(); // request id -> { resolve, reject, timer }
const stateListeners = new Set();

function setState(next) {
  state = next;
  for (const fn of stateListeners) fn(state);
}

function send(message) {
  socket.send(JSON.stringify({ version: 1, ...message }));
}

function connect() {
  clearTimeout(retryTimer);
  retryTimer = null;
  setState(state === 'reconnecting' ? 'reconnecting' : 'connecting');
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${scheme}://${location.host}/api/v1/ws`);
  socket = ws;

  ws.addEventListener('open', () => {
    retryMs = RETRY_MIN_MS;
    setState('open');
    for (const channel of channels.keys()) send({ type: 'subscribe', channel });
  });

  ws.addEventListener('message', (event) => {
    let msg;
    try {
      msg = JSON.parse(event.data);
    } catch {
      return;
    }
    if (msg.id && pending.has(msg.id)) {
      const p = pending.get(msg.id);
      pending.delete(msg.id);
      clearTimeout(p.timer);
      if (msg.type === 'error') p.reject(new WsError(msg.payload.code, msg.payload.message));
      else p.resolve(msg);
      return;
    }
    for (const handler of channels.get(msg.channel) ?? []) handler(msg);
  });

  ws.addEventListener('close', (event) => {
    if (socket !== ws) return; // a connection we already dropped
    socket = null;
    rejectPending('The connection to the panel was lost.');
    if (event.code === CLOSE_UNAUTHENTICATED) {
      setState('closed');
      window.dispatchEvent(new CustomEvent('session-expired'));
      return;
    }
    if (channels.size === 0) {
      setState('closed');
      return;
    }
    setState('reconnecting');
    const delay = retryMs / 2 + Math.random() * (retryMs / 2);
    retryMs = Math.min(retryMs * 2, RETRY_MAX_MS);
    retryTimer = setTimeout(connect, delay);
  });
}

function rejectPending(message) {
  for (const p of pending.values()) {
    clearTimeout(p.timer);
    p.reject(new WsError('disconnected', message));
  }
  pending.clear();
}

function disconnect() {
  clearTimeout(retryTimer);
  retryTimer = null;
  const ws = socket;
  socket = null;
  rejectPending('The connection was closed.');
  ws?.close(1000);
  setState('closed');
}

// subscribe delivers every message of channel to handler until the returned function is
// called. Handlers also receive "subscribed" and channel errors (e.g. not_found).
export function subscribe(channel, handler) {
  let set = channels.get(channel);
  if (!set) {
    set = new Set();
    channels.set(channel, set);
    if (state === 'open') send({ type: 'subscribe', channel });
  }
  set.add(handler);
  if (!socket && !retryTimer) connect();

  return () => {
    if (!set.delete(handler) || set.size > 0) return;
    channels.delete(channel);
    if (channels.size === 0) disconnect();
    else if (state === 'open') send({ type: 'unsubscribe', channel });
  };
}

// command sends a console command; resolves on ack, rejects with WsError.
export function command(channel, text) {
  if (state !== 'open') {
    return Promise.reject(new WsError('disconnected', 'Not connected to the panel.'));
  }
  const id = `c${++seq}`;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      pending.delete(id);
      reject(new WsError('timeout', 'The panel did not answer in time.'));
    }, COMMAND_TIMEOUT_MS);
    pending.set(id, { resolve, reject, timer });
    send({ type: 'console.command', channel, id, payload: { command: text } });
  });
}

// onState calls fn with the connection state now and on every change.
export function onState(fn) {
  stateListeners.add(fn);
  fn(state);
  return () => stateListeners.delete(fn);
}

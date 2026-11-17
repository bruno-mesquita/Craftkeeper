// Server page: header with live status and lifecycle actions, and the live console
// (WebSocket channel server:<id>, docs/WEBSOCKET.md). Usage is polled over REST.
import { api, ApiError } from '../api.js';
import { h, toast } from '../dom.js';
import { bytes, percent } from '../format.js';
import { ACTIONS, STATUS, confirmAction, lifecycleActions, runAction } from '../server-actions.js';
import * as ws from '../ws.js';

const MAX_LINES = 2000; // older lines are dropped from the page (the server keeps the log)
const MAX_HISTORY = 50; // remembered commands for ↑/↓
const USAGE_MS = 3000;
const SERVER_ID = /^[A-Z2-7]{26}$/;
const ACCEPTS_COMMANDS = new Set(['running', 'starting']);

const CONNECTION = {
  open: { label: 'Live', tone: 'text-success border-success/40' },
  connecting: { label: 'Connecting…', tone: 'text-muted' },
  reconnecting: { label: 'Reconnecting…', tone: 'text-warning border-warning/40' },
  closed: { label: 'Disconnected', tone: 'text-danger border-danger/40' },
};

export function mount(root, { param: id }) {
  const back = h('a', { href: '#/servers', class: 'text-muted hover:text-foreground' }, '← Servers');
  if (!SERVER_ID.test(id ?? '')) {
    root.replaceChildren(back, h('p', { class: 'mt-4 text-danger', role: 'alert' }, 'Server not found.'));
    return null;
  }

  const channel = `server:${id}`;
  let server = null;
  let busy = null; // label of the action in flight
  let connection = 'closed';
  const sent = []; // command history, newest last
  let cursor = -1; // position while browsing history with ↑/↓

  const title = h('h1', { class: 'text-xl font-semibold' }, 'Loading…');
  const status = h('span', { class: 'badge' });
  const meta = h('p', { class: 'text-xs text-muted' });
  const usage = h('p', { class: 'text-xs text-muted tabular-nums' });
  const actions = h('div', { class: 'flex flex-wrap gap-2' });
  const notice = h('p', { class: 'text-warning', role: 'status', hidden: true });

  const conn = h('span', { class: 'badge', role: 'status' });
  const log = h('div', {
    class: 'h-[60vh] min-h-64 overflow-y-auto rounded-md border border-border bg-background p-3 font-mono text-xs leading-5 whitespace-pre-wrap break-words',
    role: 'log', 'aria-label': 'Console output', 'aria-live': 'off', tabindex: '0',
  });
  const jump = h('button', { class: 'btn btn-primary absolute right-4 bottom-4 px-2 py-1 text-xs shadow-lg', type: 'button', hidden: true }, 'Jump to latest ↓');
  const input = h('input', {
    class: 'input font-mono', name: 'command', autocomplete: 'off', spellcheck: 'false',
    maxlength: '1024', 'aria-label': 'Console command', placeholder: 'Type a command, e.g. list',
  });
  const sendBtn = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Send');
  const form = h('form', { class: 'mt-3 flex gap-2' }, input, sendBtn);

  root.replaceChildren(
    back,
    h('div', { class: 'mt-4 mb-6 flex flex-wrap items-start justify-between gap-4' },
      h('div', { class: 'flex flex-col gap-1' },
        h('div', { class: 'flex flex-wrap items-center gap-3' }, title, status),
        meta, usage),
      actions),
    notice,
    h('section', { class: 'card', 'aria-labelledby': 'console-title' },
      h('div', { class: 'mb-3 flex items-center justify-between gap-3' },
        h('h2', { id: 'console-title', class: 'text-base font-semibold' }, 'Console'),
        conn),
      h('div', { class: 'relative' }, log, jump),
      form));

  // --- header -------------------------------------------------------------------------

  function renderHeader() {
    if (!server) return;
    document.title = `${server.name} · Mesquita Panel`;
    title.textContent = server.name;
    const st = STATUS[server.status] ?? STATUS.unknown;
    status.textContent = busy ?? st.label;
    status.className = `badge whitespace-nowrap ${busy ? 'text-primary border-primary/40' : st.tone}`;
    meta.textContent = `Minecraft ${server.version} · port ${server.port} · ${bytes(server.memory_mb * 1048576)} · ${server.cpu_cores} CPU`;
    actions.replaceChildren(...lifecycleActions(server.status).map((a) =>
      h('button', { class: `btn ${a === 'start' ? 'btn-primary' : 'btn-ghost'}`, type: 'button', disabled: Boolean(busy), onclick: () => act(a) },
        ACTIONS[a].label)));
    renderInput();
  }

  async function act(action) {
    if (!(await confirmAction(server, action))) return;
    busy = ACTIONS[action].busy;
    renderHeader();
    try {
      server = await runAction(server, action);
    } catch (err) {
      toast(`${server.name}: ${err.message}`, 'error');
    } finally {
      busy = null;
      renderHeader();
    }
  }

  async function load() {
    try {
      server = await api('GET', `/servers/${id}`);
      renderHeader();
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        title.textContent = 'Server not found';
        unsubscribe();
        form.hidden = true;
        return;
      }
      notice.hidden = false;
      notice.textContent = `Could not load the server: ${err.message}`;
    }
  }

  async function refreshUsage() {
    try {
      const { containers } = await api('GET', '/system/containers');
      const u = containers.find((c) => c.server_id === id);
      usage.textContent = u && u.state === 'running'
        ? `CPU ${percent(u.cpu_percent)} · RAM ${bytes(u.memory.used_bytes)} of ${bytes(u.memory.limit_bytes)}`
        : '';
    } catch {
      usage.textContent = '';
    }
  }

  // --- console ------------------------------------------------------------------------

  const atBottom = () => log.scrollHeight - log.scrollTop - log.clientHeight < 24;

  function lineEl(l) {
    return h('div', {
      class: l.stream === 'stderr' ? 'text-danger' : null,
      title: l.time ? new Date(l.time).toLocaleString() : null,
    }, l.data === '' ? ' ' : l.data);
  }

  function append(lines, replace) {
    const stick = replace || atBottom();
    const els = lines.map(lineEl);
    if (replace) log.replaceChildren(...els);
    else log.append(...els);
    while (log.childElementCount > MAX_LINES) log.firstElementChild.remove();
    if (stick) {
      log.scrollTop = log.scrollHeight;
      jump.hidden = true;
    } else {
      jump.hidden = false;
    }
  }

  function onMessage(msg) {
    switch (msg.type) {
      case 'console.history':
        append(msg.payload.lines, true);
        break;
      case 'console.output':
        append([msg.payload], false);
        break;
      case 'server.status':
        if (server && server.status !== msg.payload.status) {
          server = { ...server, status: msg.payload.status };
          renderHeader();
        }
        break;
      case 'error':
        notice.hidden = false;
        notice.textContent = `Console: ${msg.payload.message}`;
        break;
    }
  }

  function renderInput() {
    const ready = connection === 'open' && server && ACCEPTS_COMMANDS.has(server.status);
    input.disabled = !ready;
    sendBtn.disabled = !ready;
    if (connection !== 'open') input.placeholder = 'Waiting for the connection…';
    else if (!ready) input.placeholder = 'Start the server to send commands';
    else input.placeholder = 'Type a command, e.g. list';
  }

  log.addEventListener('scroll', () => {
    if (atBottom()) jump.hidden = true;
  });
  jump.addEventListener('click', () => {
    log.scrollTop = log.scrollHeight;
    jump.hidden = true;
    log.focus();
  });

  input.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
    if (sent.length === 0) return;
    e.preventDefault();
    if (e.key === 'ArrowUp') cursor = cursor < 0 ? sent.length - 1 : Math.max(0, cursor - 1);
    else cursor = cursor < 0 || cursor >= sent.length - 1 ? -1 : cursor + 1;
    input.value = cursor < 0 ? '' : sent[cursor];
  });

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const text = input.value.trim();
    if (!text) return;
    sendBtn.disabled = true;
    try {
      await ws.command(channel, text);
      if (sent.at(-1) !== text) sent.push(text);
      if (sent.length > MAX_HISTORY) sent.shift();
      cursor = -1;
      input.value = '';
      log.scrollTop = log.scrollHeight; // the answer appears at the bottom
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      renderInput();
      input.focus();
    }
  });

  const unsubscribe = ws.subscribe(channel, onMessage);
  const offState = ws.onState((s) => {
    connection = s;
    const c = CONNECTION[s];
    conn.textContent = c.label;
    conn.className = `badge whitespace-nowrap ${c.tone}`;
    renderInput();
  });

  load();

  // Usage polling only while the tab is visible.
  let timer = null;
  const startUsage = () => {
    clearInterval(timer);
    refreshUsage();
    timer = setInterval(refreshUsage, USAGE_MS);
  };
  const onVisibility = () => (document.hidden ? clearInterval(timer) : startUsage());
  document.addEventListener('visibilitychange', onVisibility);
  if (!document.hidden) startUsage();

  return () => {
    clearInterval(timer);
    document.removeEventListener('visibilitychange', onVisibility);
    offState();
    unsubscribe();
    document.querySelectorAll('dialog.dialog').forEach((d) => d.close());
  };
}

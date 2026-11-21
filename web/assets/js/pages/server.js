// Server page: header with live status and lifecycle actions, then tabs: the live console
// (WebSocket channel server:<id>, docs/WEBSOCKET.md) with old log files and search, the file
// manager, backups, players, the server list icon with the server.properties form, the memory/CPU
// limits, the game version and world tools, Modrinth mods, and the server's activity. Usage is polled over REST.
import { api, ApiError } from '../api.js';
import { openDialog } from '../dialog.js';
import { h, toast } from '../dom.js';
import { bytes, percent } from '../format.js';
import { mountActivity } from '../activity.js';
import { mountBackupManager } from '../backup-manager.js';
import { mountFileManager } from '../file-manager.js';
import { mountIconManager, serverIcon } from '../icon-manager.js';
import { mountModManager } from '../mod-manager.js';
import { filterLines, listLogs, matchesLine, readLog } from '../log-viewer.js';
import { mountPlayerManager } from '../player-manager.js';
import { mountPropertiesManager } from '../properties-manager.js';
import { mountResourcesManager } from '../resources-manager.js';
import { mountWorldManager } from '../world-manager.js';
import { ACTIONS, ACTIVE, STATUS, confirmAction, gameVersion, lifecycleActions, runAction } from '../server-actions.js';
import * as ws from '../ws.js';

const MAX_LINES = 2000; // older lines are dropped from the page (the server keeps the log)
const MAX_HISTORY = 50; // remembered commands for ↑/↓ (loaded from the audit log)
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

  const title = h('h1', null, 'Loading…');
  const headerIcon = serverIcon(id, 'size-12');
  const status = h('span', { class: 'badge' });
  const meta = h('p', { class: 'text-xs text-muted' });
  const usage = h('p', { class: 'text-xs text-muted tabular-nums' });
  const actions = h('div', { class: 'flex flex-wrap gap-2' });
  const notice = h('p', { class: 'text-warning', role: 'status', hidden: true });
  const crashText = h('p', null);
  const crashNotice = h('div', { class: 'card mb-4 flex flex-wrap items-center justify-between gap-3 border-danger/40 text-danger', role: 'status', hidden: true },
    crashText,
    h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => showCrashReport() }, 'View crash report'));

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

  const logSelect = h('select', { class: 'input w-auto max-w-60', 'aria-label': 'Log shown' }, h('option', { value: '' }, 'Live console'));
  const search = h('input', {
    class: 'input w-auto min-w-0 flex-1 sm:max-w-64', type: 'search', autocomplete: 'off', spellcheck: 'false',
    placeholder: 'Search', 'aria-label': 'Search the log',
  });
  const matchCount = h('span', { class: 'text-xs text-muted tabular-nums', 'aria-live': 'polite' });
  const fileLog = h('div', {
    class: 'h-[60vh] min-h-64 overflow-y-auto rounded-md border border-border bg-background p-3 font-mono text-xs leading-5 whitespace-pre-wrap break-words',
    role: 'log', 'aria-label': 'Log file', 'aria-live': 'off', tabindex: '0', hidden: true,
  });
  const fileNote = h('p', { class: 'mt-3 text-xs text-muted', role: 'status', hidden: true });
  let fileLoad = null; // AbortController of the log file being read

  const consolePanel = h('section', { class: 'card', id: 'panel-console', role: 'tabpanel', 'aria-labelledby': 'tab-console' },
    h('div', { class: 'mb-3 flex flex-wrap items-center gap-3' }, logSelect, search, matchCount, h('span', { class: 'ml-auto' }, conn)),
    h('div', { class: 'relative' }, log, jump),
    fileLog,
    fileNote,
    form);
  const filesPanel = h('section', { class: 'card', id: 'panel-files', role: 'tabpanel', 'aria-labelledby': 'tab-files', hidden: true });
  let files = null; // file manager, mounted on first use
  const backupsPanel = h('section', { class: 'card', id: 'panel-backups', role: 'tabpanel', 'aria-labelledby': 'tab-backups', hidden: true });
  let backups = null; // backup manager, mounted on first use
  const playersPanel = h('section', { class: 'card', id: 'panel-players', role: 'tabpanel', 'aria-labelledby': 'tab-players', hidden: true });
  let players = null; // player manager, mounted on first use
  const propertiesPanel = h('section', { class: 'card', id: 'panel-properties', role: 'tabpanel', 'aria-labelledby': 'tab-properties', hidden: true });
  let properties = null; // server.properties form, mounted on first use
  let icon = null; // server-icon.png, mounted with the properties form
  const resourcesPanel = h('section', { class: 'card', id: 'panel-resources', role: 'tabpanel', 'aria-labelledby': 'tab-resources', hidden: true });
  let resources = null; // memory/CPU limits form, mounted on first use
  const worldPanel = h('section', { class: 'card', id: 'panel-world', role: 'tabpanel', 'aria-labelledby': 'tab-world', hidden: true });
  let world = null; // game version and world tools, mounted on first use
  const modsPanel = h('section', { class: 'card', id: 'panel-mods', role: 'tabpanel', 'aria-labelledby': 'tab-mods', hidden: true });
  let mods = null; // Modrinth mods, mounted on first use
  const activityPanel = h('section', { class: 'card', id: 'panel-activity', role: 'tabpanel', 'aria-labelledby': 'tab-activity', hidden: true });
  let activity = null; // this server's audit events, mounted on first use

  const tabButtons = {
    console: h('button', { class: 'nav-link', type: 'button', role: 'tab', id: 'tab-console', 'aria-controls': 'panel-console' }, 'Console'),
    files: h('button', { class: 'nav-link', type: 'button', role: 'tab', id: 'tab-files', 'aria-controls': 'panel-files' }, 'Files'),
    backups: h('button', { class: 'nav-link', type: 'button', role: 'tab', id: 'tab-backups', 'aria-controls': 'panel-backups' }, 'Backups'),
    players: h('button', { class: 'nav-link', type: 'button', role: 'tab', id: 'tab-players', 'aria-controls': 'panel-players' }, 'Players'),
    properties: h('button', { class: 'nav-link', type: 'button', role: 'tab', id: 'tab-properties', 'aria-controls': 'panel-properties' }, 'Properties'),
    resources: h('button', { class: 'nav-link', type: 'button', role: 'tab', id: 'tab-resources', 'aria-controls': 'panel-resources' }, 'Resources'),
    world: h('button', { class: 'nav-link', type: 'button', role: 'tab', id: 'tab-world', 'aria-controls': 'panel-world' }, 'World'),
    mods: h('button', { class: 'nav-link', type: 'button', role: 'tab', id: 'tab-mods', 'aria-controls': 'panel-mods', hidden: true }, 'Mods'),
    activity: h('button', { class: 'nav-link', type: 'button', role: 'tab', id: 'tab-activity', 'aria-controls': 'panel-activity' }, 'Activity'),
  };
  const panels = { console: consolePanel, files: filesPanel, backups: backupsPanel, players: playersPanel, properties: propertiesPanel, resources: resourcesPanel, world: worldPanel, mods: modsPanel, activity: activityPanel };

  function showTab(name) {
    for (const [n, b] of Object.entries(tabButtons)) {
      const on = n === name;
      b.setAttribute('aria-selected', String(on));
      b.tabIndex = on ? 0 : -1;
      if (on) b.setAttribute('aria-current', 'page');
      else b.removeAttribute('aria-current');
      panels[n].hidden = !on;
    }
    if (name === 'files' && !files) {
      files = mountFileManager(filesPanel, { id });
      files.setRunning(Boolean(server && ACTIVE.has(server.status)));
    }
    if (name === 'players' && !players) {
      players = mountPlayerManager(playersPanel, { id });
      players.setRunning(Boolean(server && ACTIVE.has(server.status)));
    } else if (name === 'players') {
      players.refresh();
    }
    if (name === 'properties' && !properties) {
      const iconRoot = h('div');
      const formRoot = h('div');
      propertiesPanel.replaceChildren(iconRoot, formRoot);
      icon = mountIconManager(iconRoot, { id, onRestart: () => act('restart'), onChange: headerIcon.reload });
      properties = mountPropertiesManager(formRoot, { id, onRestart: () => act('restart') });
      icon.setRunning(Boolean(server && ACTIVE.has(server.status)));
      properties.setRunning(Boolean(server && ACTIVE.has(server.status)));
    } else if (name === 'properties') {
      icon.refresh();
      properties.refresh();
    }
    if (name === 'resources' && !resources) {
      resources = mountResourcesManager(resourcesPanel, { id, onSaved: (view) => { server = view; renderHeader(); } });
      if (server) resources.setServer(server);
    }
    if (name === 'world' && !world) {
      world = mountWorldManager(worldPanel, {
        id,
        onChanged: (view) => { server = view; renderHeader(); },
        onRestored: () => { files?.refresh(); backups?.refresh(); },
      });
      if (server) world.setServer(server);
    } else if (name === 'world') {
      world.refresh();
    }
    if (name === 'mods' && !mods) {
      mods = mountModManager(modsPanel, { id, onRestart: () => act('restart') });
      if (server) mods.setStatus(server.status);
    } else if (name === 'mods') {
      mods.refresh();
    }
    if (name === 'activity') {
      if (!activity) activity = mountActivity(activityPanel, { serverId: id });
      else activity.refresh();
    }
    if (name === 'backups') {
      if (!backups) {
        backups = mountBackupManager(backupsPanel, { id, onRestored: () => files?.refresh() });
        backups.setRunning(Boolean(server && ACTIVE.has(server.status)));
      } else {
        backups.refresh(); // resumes polling, which stops while the tab is hidden
      }
    }
  }
  for (const [n, b] of Object.entries(tabButtons)) b.addEventListener('click', () => showTab(n));
  // Arrow keys move between tabs (WAI-ARIA tabs pattern).
  const tablist = h('div', { class: 'mb-3 flex flex-wrap gap-1', role: 'tablist', 'aria-label': 'Server sections' }, ...Object.values(tabButtons));
  tablist.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    const names = Object.keys(tabButtons).filter((n) => !tabButtons[n].hidden);
    const i = names.indexOf(Object.keys(panels).find((n) => !panels[n].hidden));
    const next = names[(i + (e.key === 'ArrowRight' ? 1 : names.length - 1)) % names.length];
    showTab(next);
    tabButtons[next].focus();
  });

  root.replaceChildren(
    back,
    h('div', { class: 'mt-4 mb-6 flex flex-wrap items-start justify-between gap-4' },
      h('div', { class: 'flex items-start gap-4' },
        headerIcon.el,
        h('div', { class: 'flex flex-col gap-1' },
          h('div', { class: 'flex flex-wrap items-center gap-3' }, title, status),
          meta, usage)),
      actions),
    notice,
    crashNotice,
    tablist,
    consolePanel,
    filesPanel,
    backupsPanel,
    playersPanel,
    propertiesPanel,
    resourcesPanel,
    worldPanel,
    modsPanel,
    activityPanel);
  showTab('console');

  // --- header -------------------------------------------------------------------------

  function renderHeader() {
    if (!server) return;
    document.title = `${server.name} · Craftkeeper`;
    title.textContent = server.name;
    const st = STATUS[server.status] ?? STATUS.unknown;
    status.textContent = busy ?? st.label;
    status.className = `badge whitespace-nowrap ${busy ? 'text-primary border-primary/40' : st.tone}`;
    if (server.failure) {
      notice.hidden = false;
      notice.textContent = `Creation failed: ${server.failure}. Delete this server from the Servers page and try again.`;
    }
    crashNotice.hidden = !(server.status === 'crashed' && server.crash);
    if (!crashNotice.hidden) {
      crashText.textContent = `The server kept crashing (${server.crash.restarts} automatic restarts within 10 minutes), so the panel stopped it at ${new Date(server.crash.at).toLocaleString()}. Check the console and the crash report, fix the cause, then start it again.`;
    }
    meta.textContent = `Minecraft ${gameVersion(server)} · port ${server.port} · ${bytes(server.memory_mb * 1048576)} · ${server.cpu_cores} CPU`;
    actions.replaceChildren(...lifecycleActions(server.status).map((a) =>
      h('button', { class: `btn ${a === 'start' ? 'btn-primary' : 'btn-ghost'}`, type: 'button', disabled: Boolean(busy), onclick: () => act(a) },
        ACTIONS[a].label)));
    renderInput();
    files?.setRunning(ACTIVE.has(server.status));
    backups?.setRunning(ACTIVE.has(server.status));
    players?.setRunning(ACTIVE.has(server.status));
    properties?.setRunning(ACTIVE.has(server.status));
    icon?.setRunning(ACTIVE.has(server.status));
    resources?.setServer(server);
    world?.setServer(server);
    // Vanilla has no mod loader (a modpack server is explained inside the tab).
    tabButtons.mods.hidden = server.loader === 'vanilla';
    if (tabButtons.mods.hidden && !modsPanel.hidden) showTab('console');
    mods?.setStatus(server.status);
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
        tablist.hidden = true;
        consolePanel.hidden = true;
        filesPanel.hidden = true;
        backupsPanel.hidden = true;
        playersPanel.hidden = true;
        propertiesPanel.hidden = true;
        resourcesPanel.hidden = true;
        worldPanel.hidden = true;
        modsPanel.hidden = true;
        activityPanel.hidden = true;
        return;
      }
      notice.hidden = false;
      notice.textContent = `Could not load the server: ${err.message}`;
    }
  }

  // showCrashReport opens the newest file of crash-reports/ (written by the game when it
  // crashes; errors such as missing mod dependencies only appear in the console).
  async function showCrashReport() {
    const pre = h('pre', { class: 'max-h-[60vh] overflow-auto rounded-md border border-border bg-background p-3 font-mono text-xs whitespace-pre-wrap break-words' }, 'Loading…');
    const close = h('button', { class: 'btn btn-primary', type: 'button' }, 'Close');
    const dlg = openDialog({ title: 'Latest crash report', content: h('div', { class: 'flex w-[min(90vw,60rem)] flex-col gap-4' }, pre, h('div', { class: 'flex justify-end' }, close)) });
    close.addEventListener('click', dlg.close);
    try {
      const { entries } = await api('GET', `/servers/${id}/files?path=crash-reports`);
      const latest = entries.filter((e) => e.type === 'file').sort((a, b) => b.modified_at.localeCompare(a.modified_at))[0];
      if (!latest) throw new ApiError(404, 'not_found', '');
      const { content } = await api('GET', `/servers/${id}/files/content?path=${encodeURIComponent(`crash-reports/${latest.name}`)}`);
      pre.textContent = `crash-reports/${latest.name}\n\n${content}`;
    } catch (err) {
      pre.textContent = err instanceof ApiError && err.status === 404
        ? 'No crash report was written. The cause is usually in the console output (for example a missing mod dependency).'
        : `Could not read the crash report: ${err.message}`;
    }
  }

  async function refreshUsage() {
    // No container events exist before the build ends: re-read the server meanwhile.
    if (server?.status === 'creating') load();
    try {
      const { containers } = await api('GET', '/system/containers');
      const u = containers.find((c) => c.server_id === id);
      const live = u && u.state === 'running'
        ? `CPU ${percent(u.cpu_percent)} · RAM ${bytes(u.memory.used_bytes)} of ${bytes(u.memory.limit_bytes)}`
        : '';
      usage.textContent = u ? [`Container ${u.name}`, live].filter(Boolean).join(' · ') : '';
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
    const q = search.value.trim().toLowerCase();
    const els = lines.map(lineEl);
    for (const el of els) el.hidden = !matchesLine(el, q);
    if (replace) log.replaceChildren(...els);
    else log.append(...els);
    while (log.childElementCount > MAX_LINES) log.firstElementChild.remove();
    if (q && !logSelect.value) scheduleMatches();
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
          refreshLogList();
          if (server.status === 'crashed') load(); // fetch the crash record for the notice
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

  // --- search and old log files ---------------------------------------------------------

  const shownLog = () => (logSelect.value ? fileLog : log);

  function renderMatches() {
    const q = search.value.trim();
    const n = filterLines(shownLog(), q);
    matchCount.textContent = q ? `${n} ${n === 1 ? 'match' : 'matches'}` : '';
  }

  // Live lines can arrive fast: recount at most once per frame.
  let matchFrame = 0;
  function scheduleMatches() {
    if (matchFrame) return;
    matchFrame = requestAnimationFrame(() => {
      matchFrame = 0;
      renderMatches();
    });
  }

  search.addEventListener('input', () => {
    renderMatches();
    const el = shownLog();
    el.scrollTop = el.scrollHeight;
  });
  search.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape' || !search.value) return;
    e.preventDefault();
    search.value = '';
    renderMatches();
  });

  // The game rotates latest.log into a .log.gz when it starts: the list is read on load and
  // again on every status change.
  async function refreshLogList() {
    try {
      const files = await listLogs(id);
      const current = logSelect.value;
      logSelect.replaceChildren(h('option', { value: '' }, 'Live console'),
        ...files.map((f) => h('option', { value: f.name }, f.name)));
      logSelect.value = files.some((f) => f.name === current) ? current : '';
    } catch {
      // no logs/ folder yet (server never started) or files unavailable: only the live console
    }
  }

  logSelect.addEventListener('change', async () => {
    fileLoad?.abort();
    fileLoad = null;
    const name = logSelect.value;
    const live = name === '';
    log.parentElement.hidden = !live;
    form.hidden = !live;
    fileLog.hidden = live;
    fileNote.hidden = live;
    if (live) {
      fileLog.replaceChildren();
      renderMatches();
      log.scrollTop = log.scrollHeight;
      return;
    }
    const ctrl = new AbortController();
    fileLoad = ctrl;
    fileLog.replaceChildren();
    fileNote.textContent = `Reading logs/${name}…`;
    try {
      const { lines, truncated } = await readLog(id, name, ctrl.signal);
      if (ctrl.signal.aborted) return;
      fileLog.replaceChildren(...lines.map((l) => h('div', null, l === '' ? ' ' : l)));
      fileNote.textContent = `logs/${name} · ${lines.length} lines${truncated ? ' (only the end of a large file is shown)' : ''} · read-only`;
      renderMatches();
      fileLog.scrollTop = fileLog.scrollHeight;
    } catch (err) {
      if (!ctrl.signal.aborted) fileNote.textContent = `Could not read logs/${name}: ${err.message}`;
    }
  });

  // --- command history -----------------------------------------------------------------

  // Commands typed earlier (any browser) come from the audit log, newest first.
  async function loadHistory() {
    try {
      const q = new URLSearchParams({ server_id: id, action: 'server.console_command', limit: '200' });
      const { events } = await api('GET', `/audit?${q}`);
      const earlier = [];
      for (const e of events.toReversed()) {
        if (!e.success || !e.user) continue;
        const i = earlier.indexOf(e.target);
        if (i >= 0) earlier.splice(i, 1);
        earlier.push(e.target);
      }
      // Commands sent while loading stay the newest.
      const merged = [...earlier.filter((c) => !sent.includes(c)), ...sent].slice(-MAX_HISTORY);
      sent.splice(0, sent.length, ...merged);
    } catch {
      // history is a convenience: the console works without it
    }
  }

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
      const i = sent.indexOf(text);
      if (i >= 0) sent.splice(i, 1);
      sent.push(text);
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
  loadHistory();
  refreshLogList();

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
    cancelAnimationFrame(matchFrame);
    fileLoad?.abort();
    document.removeEventListener('visibilitychange', onVisibility);
    offState();
    unsubscribe();
    backups?.destroy();
    players?.destroy();
    document.querySelectorAll('dialog.dialog').forEach((d) => d.close());
  };
}

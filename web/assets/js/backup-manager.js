// Backups of one server (docs/API.md, "Backups"): list, create (also while the game runs,
// with its saving paused), download, restore and delete, plus the automatic schedule with
// retention. The list is polled only while a backup is being made and the tab is visible.
import { api } from './api.js';
import { confirmDialog } from './dialog.js';
import { field, h, toast } from './dom.js';
import { bytes, dateTime } from './format.js';

const POLL_MS = 3000;

const STATUS = {
  creating: { label: 'Creating…', tone: 'text-primary border-primary/40' },
  ready: { label: 'Ready', tone: 'text-success border-success/40' },
  failed: { label: 'Failed', tone: 'text-danger border-danger/40' },
};

const FAILURES = {
  interrupted: 'The panel stopped while the backup was being made.',
  save_timeout: 'The game did not confirm saving the world in time.',
  server_busy: 'The server was starting or restarting.',
  insufficient_storage: 'Not enough free disk space.',
  docker_unavailable: 'Docker was not reachable.',
  error: 'Unexpected error; see the panel log.',
};

const INTERVALS = [
  [0, 'Off'], [6, 'Every 6 hours'], [12, 'Every 12 hours'], [24, 'Daily'], [48, 'Every 2 days'], [168, 'Weekly'],
];

export function mountBackupManager(root, { id, onRestored }) {
  const base = `/servers/${id}/backups`;
  let backups = [];
  let running = false;
  let restoring = false;
  let loading = 0; // guards against out-of-order listings
  let timer = null;

  const createBtn = h('button', { class: 'btn btn-primary', type: 'button', onclick: () => create() }, 'Create backup');
  const refreshBtn = h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => load() }, 'Refresh');
  const status = h('p', { class: 'text-xs text-muted', role: 'status' });
  const tbody = h('tbody');
  const empty = h('p', { class: 'py-6 text-center text-muted', hidden: true }, 'No backups yet.');

  const interval = h('select', { class: 'input', name: 'interval_hours' },
    INTERVALS.map(([v, label]) => h('option', { value: String(v) }, label)));
  const retain = h('input', { class: 'input', name: 'retain', type: 'number', min: '0', max: '100', step: '1', required: true, value: '0' });
  const saveSchedule = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Save schedule');
  const nextRun = h('p', { class: 'text-xs text-muted', role: 'status' });
  const scheduleForm = h('form', { class: 'flex flex-wrap items-end gap-3' },
    field('Automatic backups', interval),
    field('Keep the newest (0 = all)', retain),
    saveSchedule);

  root.replaceChildren(
    h('div', { class: 'mb-3 flex flex-wrap items-center justify-between gap-2' },
      h('p', { class: 'text-xs text-muted' },
        'A backup is a .zip of the whole server folder. A running server is backed up with saving paused, so the world stays consistent.'),
      h('div', { class: 'flex gap-1' }, createBtn, refreshBtn)),
    h('div', { class: 'overflow-x-auto' },
      h('table', { class: 'w-full text-left' },
        h('thead', { class: 'text-xs text-muted' },
          h('tr', null, ['Created', 'Type', 'Size', 'Status', h('span', { class: 'sr-only' }, 'Actions')]
            .map((c) => h('th', { class: 'pb-2 pr-4 font-medium', scope: 'col' }, c)))),
        tbody)),
    empty,
    status,
    h('div', { class: 'mt-6 flex flex-col gap-2 border-t border-border pt-4' },
      h('h2', { class: 'text-sm font-semibold' }, 'Schedule'),
      scheduleForm,
      nextRun,
      h('p', { class: 'text-xs text-muted' },
        'Retention only removes automatic backups; manual ones are kept until you delete them.')));

  // --- list ---------------------------------------------------------------------------

  async function load() {
    const current = ++loading;
    try {
      const out = await api('GET', base);
      if (current !== loading) return;
      backups = out.backups;
      render();
    } catch (err) {
      if (current !== loading) return;
      status.textContent = `Could not list the backups: ${err.message}`;
    }
    poll();
  }

  function poll() {
    clearTimeout(timer);
    timer = null;
    if (backups.some((b) => b.status === 'creating') && !document.hidden && !root.hidden) {
      timer = setTimeout(load, POLL_MS);
    }
  }

  function render() {
    tbody.replaceChildren(...backups.map(row));
    empty.hidden = backups.length > 0;
    const ready = backups.filter((b) => b.status === 'ready');
    const total = ready.reduce((n, b) => n + b.size_bytes, 0);
    if (!restoring) {
      status.textContent = `${ready.length} backup${ready.length === 1 ? '' : 's'} · ${bytes(total)}`;
    }
    createBtn.disabled = restoring || backups.some((b) => b.status === 'creating');
  }

  function row(b) {
    const st = STATUS[b.status] ?? STATUS.failed;
    const when = dateTime(b.created_at);
    const btn = (label, onclick, extra = {}) => h('button', {
      class: `btn btn-ghost px-2 py-1 text-xs ${extra.cls ?? ''}`, type: 'button', onclick,
      disabled: extra.disabled, title: extra.title, 'aria-label': `${label} backup of ${when}`,
    }, label);
    const actions = [];
    if (b.status === 'ready') {
      actions.push(h('a', {
        class: 'btn btn-ghost px-2 py-1 text-xs', href: `/api/v1${base}/${b.id}/download`, download: '',
        'aria-label': `Download backup of ${when}`,
      }, 'Download'));
      actions.push(btn('Restore', () => restore(b), {
        disabled: running || restoring, title: running ? 'Stop the server to restore a backup' : null,
      }));
    }
    if (b.status !== 'creating') actions.push(btn('Delete', () => remove(b), { cls: 'text-danger', disabled: restoring }));
    const failure = b.status === 'failed' ? (FAILURES[b.failure] ?? FAILURES.error) : null;
    return h('tr', { class: 'border-t border-border' },
      h('td', { class: 'py-2 pr-4 whitespace-nowrap' }, when),
      h('td', { class: 'py-2 pr-4 text-muted' }, b.kind === 'scheduled' ? 'Automatic' : 'Manual'),
      h('td', { class: 'py-2 pr-4 whitespace-nowrap tabular-nums text-muted' }, b.status === 'ready' ? bytes(b.size_bytes) : '—'),
      h('td', { class: 'py-2 pr-4' },
        h('span', { class: `badge whitespace-nowrap ${st.tone}` }, st.label),
        failure && h('p', { class: 'mt-1 text-xs text-muted' }, failure)),
      h('td', { class: 'py-2' }, h('div', { class: 'flex justify-end gap-1' }, actions)));
  }

  // --- actions ------------------------------------------------------------------------

  async function create() {
    createBtn.disabled = true;
    try {
      await api('POST', base);
      toast(running ? 'Backup started; the game\'s saving is paused until it finishes.' : 'Backup started.', 'success');
    } catch (err) {
      toast(`Backup not started: ${err.message}`, 'error');
    } finally {
      load();
    }
  }

  async function restore(b) {
    const ok = await confirmDialog({
      title: `Restore the backup of ${dateTime(b.created_at)}?`,
      message: 'All of the server\'s files are replaced by the backup. The current files are moved aside on the host (servers/.deleted), where they can still be recovered by hand.',
      confirmLabel: 'Restore', danger: true,
    });
    if (!ok) return;
    restoring = true;
    render();
    status.textContent = 'Restoring… this can take a few minutes. Keep this page open.';
    try {
      await api('POST', `${base}/${b.id}/restore`);
      toast('Backup restored.', 'success');
      onRestored?.();
    } catch (err) {
      toast(`Restore failed: ${err.message}`, 'error');
    } finally {
      restoring = false;
      load();
    }
  }

  async function remove(b) {
    const ok = await confirmDialog({
      title: `Delete the backup of ${dateTime(b.created_at)}?`,
      message: 'The archive is deleted. This cannot be undone.',
      confirmLabel: 'Delete', danger: true,
    });
    if (!ok) return;
    try {
      await api('DELETE', `${base}/${b.id}`);
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      load();
    }
  }

  // --- schedule -----------------------------------------------------------------------

  function renderSchedule(sc) {
    const value = String(sc.interval_hours);
    if (![...interval.options].some((o) => o.value === value)) {
      interval.append(h('option', { value }, `Every ${sc.interval_hours} hours`));
    }
    interval.value = value;
    retain.value = String(sc.retain);
    nextRun.textContent = sc.next_run_at
      ? `Next automatic backup: ${dateTime(sc.next_run_at)}`
      : 'Automatic backups are off.';
  }

  async function loadSchedule() {
    try {
      renderSchedule(await api('GET', `${base}/schedule`));
    } catch (err) {
      nextRun.textContent = `Could not load the schedule: ${err.message}`;
    }
  }

  scheduleForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    saveSchedule.disabled = true;
    try {
      renderSchedule(await api('PUT', `${base}/schedule`, {
        interval_hours: Number(interval.value), retain: Number(retain.value),
      }));
      toast('Schedule saved.', 'success');
    } catch (err) {
      toast(`Schedule not saved: ${err.message}`, 'error');
      if (err.field) scheduleForm.querySelector(`[name="${CSS.escape(err.field)}"]`)?.focus();
    } finally {
      saveSchedule.disabled = false;
    }
  });

  const onVisibility = () => (document.hidden ? clearTimeout(timer) : poll());
  document.addEventListener('visibilitychange', onVisibility);

  load();
  loadSchedule();

  return {
    setRunning(value) {
      if (running === value) return;
      running = value;
      render();
    },
    refresh: () => load(),
    destroy() {
      clearTimeout(timer);
      document.removeEventListener('visibilitychange', onVisibility);
    },
  };
}

// Audit log views (GET /audit): the action labels, the event row shared with the Overview and
// the filterable, paginated table used by the Activity page and the server's Activity tab.
import { api } from './api.js';
import { h } from './dom.js';
import { dateTime } from './format.js';

// Grouped as shown in the action filter. Keys are the API's action strings.
const GROUPS = [
  ['Sign-in', { 'auth.setup': 'Setup completed', 'auth.login': 'Sign in', 'auth.logout': 'Sign out', 'auth.password_change': 'Password changed' }],
  ['Server', {
    'server.create': 'Server created', 'server.update': 'Server edited', 'server.start': 'Start', 'server.stop': 'Stop',
    'server.restart': 'Restart', 'server.kill': 'Kill', 'server.delete': 'Server deleted', 'server.crash_loop': 'Stopped after crashes',
    'server.version_change': 'Version changed', 'server.migrate': 'Container migrated', 'container.delete': 'Orphan container removed',
  }],
  ['Console', { 'server.console_command': 'Console command' }],
  ['World', { 'world.reset': 'World reset', 'world.import': 'World imported' }],
  ['Files', {
    'file.upload': 'Upload', 'file.write': 'File edited', 'file.mkdir': 'Folder created', 'file.rename': 'Rename',
    'file.delete': 'Delete', 'file.extract': 'Extract', 'modpack.configure': 'Modpack configured', 'modpack.install': 'Modpack install',
  }],
  ['Mods', {
    'mod.install': 'Mod installed', 'mod.update': 'Mod updated', 'mod.remove': 'Mod removed',
    'mod.enable': 'Mod enabled', 'mod.disable': 'Mod disabled',
    'modpack.update': 'Modpack updated', 'settings.curseforge_key': 'CurseForge key',
    'settings.public_address': 'Public address',
  }],
  ['Backups', { 'backup.create': 'Backup', 'backup.restore': 'Restore', 'backup.delete': 'Backup deleted', 'backup.schedule_update': 'Backup schedule' }],
  ['Players', {
    'whitelist.toggle': 'Whitelist on/off', 'whitelist.add': 'Whitelist add', 'whitelist.remove': 'Whitelist remove',
    'player.ban': 'Ban', 'player.pardon': 'Pardon', 'ip.ban': 'IP ban', 'ip.pardon': 'IP pardon',
    'player.op': 'Op', 'player.deop': 'Deop',
  }],
];
const LABELS = Object.assign({}, ...GROUPS.map(([, actions]) => actions));

export const actionLabel = (action) => LABELS[action] ?? action;

const cell = (cls, ...children) => h('td', { class: `py-2 pr-4 ${cls}` }, ...children);

function serverCell(e) {
  if (!e.server_id) return cell('text-muted', '—');
  if (!e.server_name) return cell('text-muted', h('span', { title: e.server_id }, 'deleted'));
  return cell('', h('a', { class: 'underline-offset-2 hover:underline', href: `#/servers/${e.server_id}` }, e.server_name));
}

// eventRow renders one event; withServer adds the server column.
export function eventRow(e, { withServer = true } = {}) {
  return h('tr', { class: 'border-t border-border' },
    cell('whitespace-nowrap tabular-nums', dateTime(e.created_at)),
    cell('', h('span', { title: e.action }, actionLabel(e.action))),
    withServer && serverCell(e),
    cell(`break-all ${e.action === 'server.console_command' ? 'font-mono text-xs' : ''}`, e.target),
    cell(e.user ? '' : 'text-muted', e.user ?? 'system'),
    cell('font-mono text-xs text-muted', e.ip),
    h('td', { class: `py-2 ${e.success ? 'text-success' : 'text-danger'}` }, e.success ? 'OK' : 'Failed'));
}

export const eventHeaders = (withServer = true) =>
  ['Time', 'Action', withServer && 'Server', 'Target', 'User', 'IP', 'Result'].filter(Boolean);

// mountActivity renders the filters and the table into root. With serverId, only that
// server's events are listed and the server column is left out. Returns { refresh }.
export function mountActivity(root, { serverId = null } = {}) {
  const withServer = !serverId;
  const action = h('select', { class: 'input w-auto', 'aria-label': 'Action' },
    h('option', { value: '' }, 'All actions'),
    GROUPS.map(([group, actions]) => h('optgroup', { label: group },
      Object.entries(actions).map(([value, label]) => h('option', { value }, label)))));
  const failed = h('input', { type: 'checkbox', class: 'size-4 accent-primary' });
  const rows = h('tbody');
  const status = h('p', { class: 'mt-3 text-sm text-muted', role: 'status' });
  const more = h('button', { class: 'btn btn-ghost mt-3', type: 'button', hidden: true }, 'Load more');

  root.replaceChildren(
    h('div', { class: 'mb-3 flex flex-wrap items-center gap-3' },
      action,
      h('label', { class: 'flex items-center gap-2 text-sm' }, failed, 'Failures only')),
    h('div', { class: 'overflow-x-auto' },
      h('table', { class: 'w-full text-left text-sm' },
        h('thead', { class: 'text-xs text-muted' },
          h('tr', null, eventHeaders(withServer).map((c) => h('th', { class: 'pb-2 font-medium', scope: 'col' }, c)))),
        rows)),
    status,
    more);

  let next = null;
  let generation = 0; // drops answers to filters that changed meanwhile

  async function load(append) {
    const gen = ++generation;
    const q = new URLSearchParams();
    if (serverId) q.set('server_id', serverId);
    if (action.value) q.set('action', action.value);
    if (failed.checked) q.set('failed', 'true');
    if (append && next) q.set('before', String(next));
    more.disabled = true;
    try {
      const page = await api('GET', `/audit?${q}`);
      if (gen !== generation) return;
      const els = page.events.map((e) => eventRow(e, { withServer }));
      if (append) rows.append(...els);
      else rows.replaceChildren(...els);
      next = page.next_before;
      more.hidden = next == null;
      status.textContent = rows.childElementCount === 0 ? 'No events.' : '';
    } catch (err) {
      if (gen === generation) status.textContent = `Could not load the events: ${err.message}`;
    } finally {
      more.disabled = false;
    }
  }

  action.addEventListener('change', () => load(false));
  failed.addEventListener('change', () => load(false));
  more.addEventListener('click', () => load(true));
  load(false);
  return { refresh: () => load(false) };
}

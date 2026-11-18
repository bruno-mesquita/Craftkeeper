// Servers: list with live status and usage, creation, editing and lifecycle actions.
// Polled every few seconds while visible (status and usage of every server; the server page
// gets live status over WebSocket). Rows are keyed by server ID and updated in place so
// keyboard focus survives refreshes.
import { api } from '../api.js';
import { confirmDialog, openDialog } from '../dialog.js';
import { clearFormError, field, formValues, h, showFormError, toast } from '../dom.js';
import { bytes, percent } from '../format.js';
import { serverIcon } from '../icon-manager.js';
import { mountModpackManager } from '../modpack-manager.js';
import { detectModpack } from '../modpack-zip.js';
import { ACTIONS, ACTIVE, CREATING_POLL_MS, LOADERS, STATUS, confirmAction, gameVersion, lifecycleActions, limitFields, runAction } from '../server-actions.js';

const REFRESH_MS = 3000;
const EULA_URL = 'https://aka.ms/MinecraftEULA';

export function mount(root) {
  const rows = new Map(); // server ID -> row handle
  const busy = new Map(); // server ID -> label of the action in flight
  let servers = [];
  let usage = new Map(); // server ID -> container stats

  const tbody = h('tbody');
  const empty = h('p', { class: 'text-muted', hidden: true }, 'No servers yet. Create one to get started.');
  const notice = h('p', { class: 'mb-4 text-warning', role: 'status', hidden: true });

  root.replaceChildren(
    h('div', { class: 'mb-6 flex flex-wrap items-center justify-between gap-3' },
      h('h1', null, 'Servers'),
      h('button', { class: 'btn btn-primary', type: 'button', onclick: () => createDialog() }, 'New server')),
    notice,
    h('section', { class: 'card overflow-x-auto' },
      h('table', { class: 'w-full text-left' },
        h('thead', { class: 'text-xs text-muted' },
          h('tr', null, ['Name', 'Status', 'Version', 'Port', 'Limits', 'Usage', h('span', { class: 'sr-only' }, 'Actions')]
            .map((c) => h('th', { class: 'pb-2 pr-4 font-medium', scope: 'col' }, c)))),
        tbody),
      empty));

  async function refresh() {
    try {
      const [list, stats] = await Promise.all([
        api('GET', '/servers'),
        api('GET', '/system/containers').catch(() => ({ containers: [] })),
      ]);
      servers = list.servers;
      usage = new Map(stats.containers.filter((c) => c.server_id).map((c) => [c.server_id, c]));
      notice.hidden = !servers.some((s) => s.status === 'unknown');
      notice.textContent = 'Docker is unreachable: server status is unknown.';
      render();
    } catch (err) {
      notice.hidden = false;
      notice.textContent = `Could not load servers: ${err.message}`;
    }
  }

  function render() {
    empty.hidden = servers.length > 0;
    const seen = new Set();
    servers.forEach((s, i) => {
      seen.add(s.id);
      let row = rows.get(s.id);
      if (!row) {
        row = createRow(s.id);
        rows.set(s.id, row);
      }
      row.update(s);
      // Move a row only when it is out of place: moving a node drops its focus.
      if (tbody.children[i] !== row.tr) tbody.insertBefore(row.tr, tbody.children[i] ?? null);
    });
    for (const [id, row] of rows) {
      if (!seen.has(id)) {
        row.tr.remove();
        rows.delete(id);
      }
    }
  }

  function createRow(id) {
    const name = h('a', { class: 'font-medium hover:underline' });
    const desc = h('span', { class: 'block max-w-xs truncate text-xs text-muted' });
    const failure = h('span', { class: 'block max-w-xs text-xs text-danger' });
    const status = h('span', { class: 'badge' });
    const version = h('td', { class: 'py-3 pr-4 whitespace-nowrap' });
    const port = h('td', { class: 'py-3 pr-4 font-mono text-xs' });
    const limits = h('td', { class: 'py-3 pr-4 whitespace-nowrap tabular-nums' });
    const use = h('td', { class: 'py-3 pr-4 whitespace-nowrap tabular-nums text-muted' });
    const actions = h('div', { class: 'flex justify-end gap-1' });
    const tr = h('tr', { class: 'border-t border-border align-top' },
      h('td', { class: 'py-3 pr-4' },
        h('div', { class: 'flex items-start gap-3' }, serverIcon(id, 'size-8').el, h('div', null, name, desc, failure))),
      h('td', { class: 'py-3 pr-4' }, status),
      version, port, limits, use,
      h('td', { class: 'py-3' }, actions));
    let actionsKey = '';

    return {
      tr,
      update(s) {
        name.textContent = s.name;
        name.href = `#/servers/${s.id}`;
        desc.textContent = s.description;
        desc.hidden = !s.description;
        failure.textContent = s.failure ? `Creation failed: ${s.failure}` : '';
        failure.hidden = !s.failure;
        const st = STATUS[s.status] ?? STATUS.unknown;
        const working = busy.get(s.id);
        status.textContent = working ?? st.label;
        status.className = `badge whitespace-nowrap ${working ? 'text-primary border-primary/40' : st.tone}`;
        version.textContent = gameVersion(s);
        port.textContent = String(s.port);
        limits.textContent = `${bytes(s.memory_mb * 1048576)} · ${s.cpu_cores} CPU`;
        const u = usage.get(s.id);
        use.textContent = u && u.state === 'running'
          ? `${percent(u.cpu_percent)} · ${bytes(u.memory.used_bytes)}`
          : '—';
        // Rebuild buttons only when what they depend on changes, so focus is not lost.
        const key = `${s.status}|${working ?? ''}`;
        if (key !== actionsKey) {
          actionsKey = key;
          actions.replaceChildren(...actionButtons(s, Boolean(working)));
        }
      },
    };
  }

  function actionButtons(s, disabled) {
    const btn = (label, onclick, cls = 'btn-ghost') =>
      h('button', { class: `btn ${cls} px-2 py-1`, type: 'button', disabled, onclick, 'aria-label': `${label} ${s.name}` }, label);
    const list = lifecycleActions(s.status).map((a) => btn(ACTIONS[a].label, async () => {
      if (await confirmAction(s, a)) act(s, a, ACTIONS[a].busy);
    }));
    // A server being created has nothing to act on yet; a failed one can only be deleted.
    if (!['unknown', 'creating', 'create_failed'].includes(s.status)) list.push(btn('Edit', () => editDialog(s)));
    if (!['unknown', 'creating'].includes(s.status)) list.push(btn('Delete', () => deleteServer(s), 'btn-ghost text-danger'));
    return list;
  }

  // act runs a lifecycle request; the row shows the action until it finishes.
  // Resolves to true on success; failures are reported in a toast.
  async function act(s, action, label, request = () => runAction(s, action)) {
    busy.set(s.id, label);
    render();
    try {
      await request();
      return true;
    } catch (err) {
      toast(`${s.name}: ${err.message}`, 'error');
      return false;
    } finally {
      busy.delete(s.id);
      await refresh();
    }
  }

  async function deleteServer(s) {
    const ok = await confirmDialog({
      title: `Delete ${s.name}?`,
      message: 'The server is stopped and its container removed. Without the option below, its files are kept in servers/.deleted on the host.',
      confirmLabel: 'Delete', danger: true,
      checkbox: 'Also delete the world and all server files (cannot be undone)',
    });
    if (!ok) return;
    const deleted = await act(s, 'delete', 'Deleting…',
      () => api('DELETE', `/servers/${s.id}${ok.checked ? '?delete_files=true' : ''}`));
    if (deleted) toast(`${s.name} deleted.`, 'success');
  }

  // submitForm wires a dialog form: disables the button while sending, shows API errors inline.
  function submitForm(form, button, busyLabel, send, close) {
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      clearFormError(form);
      const label = button.textContent;
      button.disabled = true;
      button.textContent = busyLabel;
      try {
        const stay = await send(formValues(form));
        if (stay !== 'stay-open') close();
        refresh();
      } catch (err) {
        showFormError(form, err);
      } finally {
        button.disabled = false;
        button.textContent = label;
      }
    });
  }

  function createDialog() {
    const button = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Create');
    const cancel = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Cancel');
    const loader = h('select', { class: 'input', name: 'loader' },
      Object.entries(LOADERS).map(([value, label]) => h('option', { value }, label)));
    const versionSlot = h('div');
    const loaderVersion = h('select', { class: 'input', name: 'loader_version', disabled: true });
    const loaderVersionField = field('Loader version', loaderVersion);
    loaderVersionField.hidden = true;
    const versionStatus = h('p', { class: 'text-xs text-muted', role: 'status', hidden: true });
    const moddedHint = h('p', { class: 'text-xs text-muted', hidden: true },
      'Mod loaders need more memory (4 GB or more for most modpacks). Forge and NeoForge install on the first start, which takes a few minutes. Add mods to the mods folder in the Files tab.');
    const limits = limitFields();
    const memory = limits[0].querySelector('input');
    const packInput = h('input', { class: 'input', name: 'modpack', type: 'file', accept: '.zip,application/zip' });
    const packField = field('Modpack server pack ZIP (optional)', packInput);
    const packStatus = h('p', { class: 'text-xs text-muted', role: 'status', hidden: true });
    // Type and version fields: replaced by what the pack's variables.txt declares.
    const manual = h('div', { class: 'flex flex-col gap-4' },
      h('div', { class: 'grid grid-cols-2 gap-4' }, field('Server type', loader), versionSlot),
      loaderVersionField, versionStatus, moddedHint);
    let detected = null;
    const form = h('form', { class: 'flex flex-col gap-4', novalidate: true },
      field('Name', h('input', { class: 'input', name: 'name', required: true, maxlength: '48', autocomplete: 'off' })),
      field('Description', h('input', { class: 'input', name: 'description', maxlength: '500', autocomplete: 'off' })),
      packField,
      packStatus,
      manual,
      h('div', { class: 'grid grid-cols-2 gap-4' }, ...limits),
      field('Port (empty = first free)', h('input', { class: 'input', name: 'port', type: 'number', min: '1024', max: '65535', autocomplete: 'off' })),
      h('label', { class: 'flex items-start gap-2' },
        h('input', { type: 'checkbox', name: 'accept_eula', class: 'mt-0.5 size-4' }),
        h('span', null, 'I accept the ',
          h('a', { href: EULA_URL, target: '_blank', rel: 'noopener noreferrer', class: 'text-primary underline' }, 'Minecraft EULA'),
          '.')),
      h('p', { class: 'text-xs text-muted' }, 'The first server of each Java version downloads its game image, which can take a few minutes.'),
      h('p', { class: 'form-error', 'data-error': true, role: 'alert', hidden: true }),
      h('div', { class: 'flex justify-end gap-2' }, cancel, button));

    // Version lists come from the official sources through the panel. Each change bumps
    // `request`, so a slow answer for a previous choice is ignored.
    let request = 0;
    const setStatus = (text) => {
      versionStatus.textContent = text;
      versionStatus.hidden = !text;
    };
    const options = (select, values, label = (v) => v) =>
      select.replaceChildren(...values.map((v) => h('option', { value: v }, label(v))));

    async function loadVersions() {
      const id = ++request;
      const kind = loader.value;
      const modded = kind !== 'vanilla';
      moddedHint.hidden = !modded;
      if (modded && Number(memory.value) < 4096) memory.value = '4096';
      loaderVersionField.hidden = !modded;
      loaderVersion.disabled = true;
      button.disabled = true;
      setStatus('Loading versions…');
      try {
        const { versions } = await api('GET', `/installers/${kind}/versions`);
        if (id !== request) return;
        const select = h('select', { class: 'input', name: 'version' });
        options(select, versions, (v) => (v === 'LATEST' ? 'Latest release (updates on restart)' : v));
        if (modded) select.addEventListener('change', () => loadLoaderVersions(select.value));
        versionSlot.replaceChildren(field('Minecraft version', select));
        if (modded) {
          if (!versions.length) throw new Error(`No ${LOADERS[kind]} versions are available.`);
          await loadLoaderVersions(select.value, id);
        } else {
          setStatus('');
          button.disabled = false;
        }
      } catch (err) {
        if (id !== request) return;
        if (modded) {
          versionSlot.replaceChildren();
          setStatus(`Could not load ${LOADERS[kind]} versions: ${err.message}`);
          return;
        }
        // Vanilla does not need the list: the game image resolves the version itself.
        versionSlot.replaceChildren(field('Minecraft version',
          h('input', { class: 'input', name: 'version', value: 'LATEST', required: true, autocomplete: 'off' })));
        setStatus('The version list is unavailable; type "LATEST" or a release such as 1.21.4.');
        button.disabled = false;
      }
    }

    async function loadLoaderVersions(mc, id = ++request) {
      loaderVersion.disabled = true;
      button.disabled = true;
      setStatus('Loading loader versions…');
      try {
        const lv = await api('GET', `/installers/${loader.value}/versions/${encodeURIComponent(mc)}`);
        if (id !== request) return;
        options(loaderVersion, lv.loader_versions, (v) => (v === lv.recommended ? `${v} (recommended)` : v));
        loaderVersion.value = lv.recommended;
        loaderVersion.disabled = false;
        button.disabled = false;
        setStatus('');
      } catch (err) {
        if (id === request) setStatus(`Could not load loader versions: ${err.message}`);
      }
    }

    packInput.addEventListener('change', async () => {
      const pack = packInput.files?.[0];
      detected = pack ? await detectModpack(pack) : null;
      if (packInput.files?.[0] !== pack) return; // a newer choice replaced this one
      manual.hidden = Boolean(detected);
      packStatus.hidden = !pack;
      if (detected) {
        packStatus.textContent = `Detected from the pack: ${LOADERS[detected.loader]} ${detected.version}${detected.loaderVersion ? ` · ${detected.loaderVersion}` : ''}${detected.heapMB ? `, heap ${detected.heapMB} MB` : ''}.`;
        // The container limit needs headroom over the JVM heap (metaspace, threads, native memory).
        memory.value = String(Math.min(16384, detected.heapMB ? detected.heapMB + 1024 : Math.max(4096, Number(memory.value))));
        button.disabled = false;
      } else if (pack) {
        packStatus.textContent = 'No usable variables.txt in the ZIP: choose the server type and Minecraft version below.';
      }
    });
    loader.addEventListener('change', loadVersions);
    loadVersions();

    let closed = false;
    let pollTimer = null;
    const { close } = openDialog({
      title: 'New server', content: form,
      onClose: () => { closed = true; clearTimeout(pollTimer); },
    });
    cancel.addEventListener('click', close);
    form.querySelector('[name="name"]').focus();
    submitForm(form, button, 'Creating…', async (v) => {
      const pack = form.querySelector('[name="modpack"]').files?.[0];
      // The panel answers at once and builds the server in the background (status
      // "creating"). Without a modpack it starts by itself when ready; with one, the
      // modpack installer starts it.
      const created = await api('POST', '/servers', {
        name: v.name.trim(),
        description: v.description.trim(),
        loader: detected?.loader ?? v.loader,
        version: detected?.version ?? (v.version ?? '').trim(),
        ...((detected ? detected.loaderVersion : v.loader_version) ? { loader_version: detected ? detected.loaderVersion : v.loader_version } : {}),
        memory_mb: Number(v.memory_mb),
        cpu_cores: Number(v.cpu_cores),
        port: v.port ? Number(v.port) : 0,
        accept_eula: v.accept_eula === 'on',
        start: !pack,
      });
      if (!pack) {
        toast(`${created.name} is being created on port ${created.port}. It starts when ready.`, 'success');
        return;
      }
      // Keep the dialog: wait for the build, then continue with the modpack step.
      const root = h('div');
      const done = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Close');
      done.addEventListener('click', close);
      const waiting = h('p', { class: 'text-sm text-muted', role: 'status' },
        `Creating ${created.name}… The first server of each Java version downloads its game image, which can take a few minutes. Keep this dialog open: the modpack import continues here when the server is ready.`);
      root.append(waiting);
      form.replaceChildren(root, h('div', { class: 'flex justify-end' }, done));
      const poll = async () => {
        if (closed) return;
        let s;
        try {
          s = await api('GET', `/servers/${created.id}`);
        } catch {
          s = { status: 'creating' }; // transient: try again
        }
        if (closed) return;
        if (s.status === 'creating') {
          pollTimer = setTimeout(poll, CREATING_POLL_MS);
          return;
        }
        if (s.status === 'create_failed') {
          waiting.className = 'text-sm text-danger';
          waiting.textContent = `Could not create ${created.name}: ${s.failure}`;
          return;
        }
        root.replaceChildren();
        mountModpackManager(root, {
          id: created.id,
          initialFile: pack,
          onInstall: () => { close(); location.hash = `#/servers/${created.id}`; },
        });
      };
      poll();
      return 'stay-open';
    }, close);
  }

  function editDialog(s) {
    const running = ACTIVE.has(s.status);
    const button = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Save');
    const cancel = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Cancel');
    const limits = limitFields(s);
    if (running) limits.forEach((f) => { f.querySelector('input').disabled = true; });
    const form = h('form', { class: 'flex flex-col gap-4', novalidate: true },
      field('Name', h('input', { class: 'input', name: 'name', required: true, maxlength: '48', value: s.name, autocomplete: 'off' })),
      field('Description', h('input', { class: 'input', name: 'description', maxlength: '500', value: s.description, autocomplete: 'off' })),
      h('div', { class: 'grid grid-cols-2 gap-4' }, ...limits),
      running && h('p', { class: 'text-xs text-muted' }, 'Stop the server to change its memory or CPU limits.'),
      h('p', { class: 'form-error', 'data-error': true, role: 'alert', hidden: true }),
      h('div', { class: 'flex justify-end gap-2' }, cancel, button));

    const { close } = openDialog({ title: `Edit ${s.name}`, content: form });
    cancel.addEventListener('click', close);
    form.querySelector('[name="name"]').focus();
    submitForm(form, button, 'Saving…', (v) => {
      const body = { name: v.name.trim(), description: v.description.trim() };
      if (!running) Object.assign(body, { memory_mb: Number(v.memory_mb), cpu_cores: Number(v.cpu_cores) });
      return api('PATCH', `/servers/${s.id}`, body);
    }, close);
  }

  // Poll only while the tab is visible to keep idle cost near zero.
  let timer = null;
  const start = () => {
    stop();
    refresh();
    timer = setInterval(refresh, REFRESH_MS);
  };
  const stop = () => {
    clearInterval(timer);
    timer = null;
  };
  const onVisibility = () => (document.hidden ? stop() : start());
  document.addEventListener('visibilitychange', onVisibility);
  if (!document.hidden) start();

  return () => {
    stop();
    document.removeEventListener('visibilitychange', onVisibility);
    document.querySelectorAll('dialog.dialog').forEach((d) => d.close());
  };
}

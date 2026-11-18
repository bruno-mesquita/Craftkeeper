// World tab: change the game version or loader (POST /servers/{id}/version), download the
// world (the files API zips the folder), reset it (POST /world/reset) or import one from a
// zip (upload with the files API, then POST /world/import). See docs/API.md. Every change
// needs a stopped server and the panel makes a full backup first, so the requests can take
// minutes on big worlds.
import { api, upload } from './api.js';
import { openDialog } from './dialog.js';
import { clearFormError, field, formValues, h, showFormError, toast } from './dom.js';
import { LOADERS, STOPPED, gameVersion } from './server-actions.js';
import { compareVersions, versionFields } from './version-fields.js';

const BACKUP_NOTE = 'A full backup of the server is made first (Backups tab); restore it to undo.';

export function mountWorldManager(root, { id, onChanged, onRestored }) {
  let server = null;
  let world = 'world';
  let modpack = false;
  let busy = ''; // what is in progress, shown instead of the buttons' state

  const versionLabel = h('p', { class: 'text-sm' });
  const versionBtn = h('button', { class: 'btn', type: 'button' }, 'Change version…');
  const worldLabel = h('p', { class: 'text-sm' });
  const downloadLink = h('a', { class: 'btn', download: '' }, 'Download world');
  const resetBtn = h('button', { class: 'btn btn-danger', type: 'button' }, 'Reset world…');
  const importBtn = h('button', { class: 'btn', type: 'button' }, 'Import world…');
  const note = h('p', { class: 'text-xs text-muted', role: 'status' });

  root.replaceChildren(
    h('div', { class: 'flex flex-col gap-2' },
      h('h2', { class: 'text-sm font-semibold' }, 'Game version'),
      h('div', { class: 'flex flex-wrap items-center justify-between gap-2' }, versionLabel, versionBtn),
      h('p', { class: 'text-xs text-muted' },
        'Changing the version or loader recreates the container with the same port, limits and files. Mods must match the new loader and version.')),
    h('div', { class: 'mt-6 flex flex-col gap-2 border-t border-border pt-4' },
      h('h2', { class: 'text-sm font-semibold' }, 'World'),
      h('div', { class: 'flex flex-wrap items-center justify-between gap-2' },
        worldLabel,
        h('div', { class: 'flex flex-wrap gap-1' }, downloadLink, importBtn, resetBtn)),
      h('p', { class: 'text-xs text-muted' },
        'Import accepts a .zip with level.dat at its root or in one folder, such as a single-player save or a downloaded world.')),
    h('p', { class: 'mt-4 text-xs text-muted' }, BACKUP_NOTE),
    note);

  const stopped = () => STOPPED.has(server?.status);

  function render() {
    if (server) versionLabel.textContent = gameVersion(server);
    worldLabel.replaceChildren('Folder ', h('code', { class: 'font-mono' }, world));
    downloadLink.href = `/api/v1/servers/${id}/files/download?path=${encodeURIComponent(world)}`;
    const off = !stopped() || Boolean(busy);
    versionBtn.disabled = off || modpack;
    resetBtn.disabled = off;
    importBtn.disabled = off;
    if (busy) note.textContent = busy;
    else if (!server || stopped()) note.textContent = modpack ? 'This server runs a modpack: its scripts choose the game version.' : '';
    else note.textContent = 'Stop the server to change its version or world.';
  }

  async function load() {
    try {
      const props = await api('GET', `/servers/${id}/files/content?path=server.properties`);
      world = levelName(props.content);
    } catch {
      world = 'world'; // no server.properties until the first start
    }
    try {
      modpack = Boolean((await api('GET', `/servers/${id}/modpack`)).start_script);
    } catch {
      modpack = false;
    }
    render();
  }

  // run performs one long change, showing what is going on.
  async function run(label, request) {
    busy = label;
    render();
    try {
      return await request();
    } finally {
      busy = '';
      render();
    }
  }

  versionBtn.addEventListener('click', () => {
    const button = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Back up and change');
    const cancel = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Cancel');
    const warning = h('p', { class: 'text-sm text-warning', role: 'alert', hidden: true });
    const versions = versionFields({ initial: server, onBusy: (b) => { button.disabled = b; } });
    const form = h('form', { class: 'flex flex-col gap-4', novalidate: true },
      h('p', { class: 'text-sm text-muted' }, `Now: ${gameVersion(server)}.`),
      versions.root,
      warning,
      h('p', { class: 'text-xs text-muted' }, BACKUP_NOTE),
      h('p', { class: 'form-error', 'data-error': true, role: 'alert', hidden: true }),
      h('div', { class: 'flex justify-end gap-2' }, cancel, button));

    const warn = () => {
      const v = formValues(form);
      const lines = [];
      if (v.version && compareVersions(v.version, server.version) < 0) {
        lines.push(`Downgrade: Minecraft does not convert worlds to older versions. ${v.version} may refuse to load or corrupt the world made by ${server.version}.`);
      }
      if (v.loader && v.loader !== server.loader) {
        lines.push(`Loader change (${LOADERS[server.loader]} → ${LOADERS[v.loader]}): mods in the mods folder made for ${LOADERS[server.loader]} can keep the server from starting.`);
      }
      warning.textContent = lines.join(' ');
      warning.hidden = !lines.length;
    };
    form.addEventListener('change', warn);

    const { close } = openDialog({ title: 'Change version', content: form });
    cancel.addEventListener('click', close);
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      clearFormError(form);
      const v = formValues(form);
      const body = { loader: v.loader, version: (v.version ?? '').trim(), ...(v.loader_version ? { loader_version: v.loader_version } : {}) };
      if (body.loader === server.loader && body.version === server.version && (body.loader_version ?? '') === server.loader_version) {
        showFormError(form, new Error('The server already runs this version.'));
        return;
      }
      button.disabled = true;
      cancel.disabled = true;
      button.textContent = 'Backing up…';
      try {
        const view = await run('Backing up the server and changing its version…', () => api('POST', `/servers/${id}/version`, body));
        close();
        server = view;
        render();
        onChanged?.(view);
        toast(`${view.name} now runs ${gameVersion(view)}. Start it to install the new version.`, 'success');
      } catch (err) {
        button.disabled = false;
        cancel.disabled = false;
        button.textContent = 'Back up and change';
        showFormError(form, err);
      }
    });
  });

  resetBtn.addEventListener('click', () => {
    const seed = h('input', { class: 'input', name: 'seed', maxlength: '64', autocomplete: 'off', placeholder: 'keep the current setting' });
    const confirmName = h('input', { class: 'input', name: 'confirm', autocomplete: 'off', spellcheck: 'false' });
    const button = h('button', { class: 'btn btn-danger', type: 'submit', disabled: true }, 'Back up and reset');
    const cancel = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Cancel');
    const form = h('form', { class: 'flex flex-col gap-4', novalidate: true },
      h('p', { class: 'text-sm text-muted' },
        `The folder ${world} is deleted; the game generates a new world on the next start. Players, configuration and mods are kept.`),
      field('New seed (optional)', seed),
      h('p', { class: 'text-xs text-muted' }, 'Leave empty to keep level-seed as it is (empty there means a random seed).'),
      field(`Type ${server.name} to confirm`, confirmName),
      h('p', { class: 'text-xs text-muted' }, BACKUP_NOTE),
      h('p', { class: 'form-error', 'data-error': true, role: 'alert', hidden: true }),
      h('div', { class: 'flex justify-end gap-2' }, cancel, button));
    confirmName.addEventListener('input', () => { button.disabled = confirmName.value !== server.name; });
    const { close } = openDialog({ title: 'Reset world', content: form });
    cancel.addEventListener('click', close);
    confirmName.focus();
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      if (confirmName.value !== server.name) return;
      clearFormError(form);
      const s = seed.value.trim();
      button.disabled = true;
      cancel.disabled = true;
      button.textContent = 'Backing up…';
      try {
        await run('Backing up the server and resetting its world…', () => api('POST', `/servers/${id}/world/reset`, s ? { seed: s } : {}));
        close();
        onRestored?.();
        toast('World reset. A new one is generated on the next start.', 'success');
      } catch (err) {
        button.disabled = false;
        cancel.disabled = false;
        button.textContent = 'Back up and reset';
        showFormError(form, err);
      }
    });
  });

  importBtn.addEventListener('click', () => {
    const file = h('input', { class: 'input', name: 'archive', type: 'file', accept: '.zip,application/zip', required: true });
    const progress = h('progress', { class: 'w-full', max: '1', value: '0', hidden: true });
    const button = h('button', { class: 'btn btn-primary', type: 'submit', disabled: true }, 'Upload and import');
    const cancel = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Cancel');
    const form = h('form', { class: 'flex flex-col gap-4', novalidate: true },
      h('p', { class: 'text-sm text-muted' }, `The world in the archive replaces the folder ${world}.`),
      field('World .zip', file),
      progress,
      h('p', { class: 'text-xs text-muted' }, BACKUP_NOTE),
      h('p', { class: 'form-error', 'data-error': true, role: 'alert', hidden: true }),
      h('div', { class: 'flex justify-end gap-2' }, cancel, button));
    file.addEventListener('change', () => { button.disabled = !file.files?.length; });
    const { close } = openDialog({ title: 'Import world', content: form });
    cancel.addEventListener('click', close);
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const chosen = file.files?.[0];
      if (!chosen) return;
      clearFormError(form);
      // A name of its own, so it never clashes with a file of the server.
      const name = `world-import-${Date.now()}.zip`;
      button.disabled = true;
      cancel.disabled = true;
      file.disabled = true;
      let uploaded = false;
      try {
        await run('Uploading the world…', async () => {
          button.textContent = 'Uploading…';
          progress.hidden = false;
          await upload(`/servers/${id}/files/upload?path=`, [new File([chosen], name, { type: chosen.type })], (v) => { progress.value = v; });
          uploaded = true;
          progress.hidden = true;
          button.textContent = 'Backing up…';
          busy = 'Backing up the server and importing the world…';
          render();
          await api('POST', `/servers/${id}/world/import`, { path: name });
        });
        close();
        onRestored?.();
        toast('World imported.', 'success');
      } catch (err) {
        progress.hidden = true;
        // The panel keeps an archive it could not use; it is not the user's file, so remove it.
        if (uploaded) api('DELETE', `/servers/${id}/files?path=${encodeURIComponent(name)}`).catch(() => {});
        button.disabled = false;
        cancel.disabled = false;
        file.disabled = false;
        button.textContent = 'Upload and import';
        showFormError(form, err);
      }
    });
  });

  load();
  return {
    // setServer follows the page's server view.
    setServer(next) {
      const wasStopped = stopped();
      server = next;
      if (stopped() && !wasStopped) load(); // the game may have written server.properties
      render();
    },
    refresh: load,
  };
}

// levelName reads level-name from server.properties text ("world" when unset).
function levelName(content) {
  for (const line of content.split('\n')) {
    const [key, ...rest] = line.replace(/\r$/, '').split('=');
    if (key === 'level-name') {
      const v = rest.join('=').trim();
      return v || 'world';
    }
  }
  return 'world';
}

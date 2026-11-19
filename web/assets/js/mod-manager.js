// Mods tab: the jars in the server's mods folder, identified on Modrinth by their hash, with
// updates (after an optional full backup), enable/disable and removal; and a Modrinth search filtered
// to the server's loader and Minecraft version, whose installs bring required dependencies
// along. See docs/API.md, "Mods". Mods may change while the server runs; the game loads them on
// its next start, so the tab offers a restart.
import { api, ApiError } from './api.js';
import { confirmDialog, openDialog } from './dialog.js';
import { field, h, toast } from './dom.js';
import { bytes } from './format.js';
import { ACTIVE } from './server-actions.js';

const compact = new Intl.NumberFormat(undefined, { notation: 'compact' });

const BLOCKED = {
  modpack_server: 'This server runs a modpack, whose files manage its mods. Use the Files tab to change them.',
  mods_unsupported: 'Vanilla servers have no mod loader. Change the loader in the World tab to use mods.',
};

export function mountModManager(root, { id, onRestart }) {
  let running = false;
  let changed = false; // mods changed while the server runs: a restart applies them
  let mods = [];
  let busy = '';
  let searchOffset = 0;
  let searchTotal = 0;
  let searchQuery = '';

  const restartNote = h('div', {
    class: 'flex flex-wrap items-center justify-between gap-2 rounded-md border border-warning/40 px-3 py-2 text-sm', role: 'status', hidden: true,
  },
  h('span', null, 'Mods changed while the server is running. Restart it to apply them.'),
  h('button', { class: 'btn btn-primary', type: 'button', onclick: () => onRestart() }, 'Restart'));
  const blocked = h('p', { class: 'text-muted', hidden: true });
  const updateAllBtn = h('button', { class: 'btn', type: 'button', hidden: true }, 'Update all');
  const note = h('p', { class: 'text-xs text-muted', role: 'status' });
  const list = h('ul', { class: 'mt-3 divide-y divide-border' });
  const none = h('p', { class: 'mt-3 text-muted', hidden: true }, 'No mods installed.');

  const query = h('input', {
    class: 'input', name: 'q', type: 'search', placeholder: 'Search Modrinth (empty: most downloaded)',
    autocomplete: 'off', spellcheck: 'false', maxlength: '100', 'aria-label': 'Search mods on Modrinth',
  });
  const searchBtn = h('button', { class: 'btn', type: 'submit' }, 'Search');
  const searchForm = h('form', { class: 'mt-3 flex gap-2', role: 'search' }, query, searchBtn);
  const results = h('ul', { class: 'mt-3 divide-y divide-border' });
  const searchNote = h('p', { class: 'mt-3 text-muted', hidden: true });
  const moreBtn = h('button', { class: 'btn btn-ghost mt-2', type: 'button', hidden: true }, 'Load more');

  const installed = h('div', { class: 'flex flex-col gap-2' },
    h('div', { class: 'flex flex-wrap items-center justify-between gap-2' },
      h('h2', { class: 'text-sm font-semibold' }, 'Installed mods'), updateAllBtn),
    note, list, none);
  const search = h('section', { class: 'mt-6 border-t border-border pt-4' },
    h('h2', { class: 'text-sm font-semibold' }, 'Add mods'),
    h('p', { class: 'text-xs text-muted' }, 'Server-side mods from Modrinth for this server\'s loader and Minecraft version. Required dependencies are installed too.'),
    searchForm, results, searchNote, moreBtn);
  root.replaceChildren(h('div', { class: 'flex flex-col gap-3' }, restartNote, blocked, installed), search);

  const installedProjects = () => new Set(mods.map((m) => m.version?.project_id).filter(Boolean));

  function modIcon(project, fallbackName) {
    const box = 'size-10 shrink-0 rounded-md border border-border';
    const fallback = () => h('span', {
      class: `${box} flex items-center justify-center bg-surface text-xs font-semibold text-muted`, 'aria-hidden': 'true',
    }, (project?.title || fallbackName || '?').charAt(0).toUpperCase());
    if (!project?.has_icon) return fallback();
    const img = h('img', {
      class: `${box} bg-surface object-cover`, src: `/api/v1/mods/icons/${encodeURIComponent(project.id)}`,
      alt: '', width: '40', height: '40', loading: 'lazy', decoding: 'async',
    });
    img.addEventListener('error', () => img.replaceWith(fallback()), { once: true });
    return img;
  }

  const smallBtn = (label, onclick, extra = '') =>
    h('button', { class: `btn btn-ghost px-2 py-1 text-xs ${extra}`, type: 'button', disabled: Boolean(busy), onclick }, label);

  function render() {
    list.replaceChildren(...mods.map((m) => {
      const title = m.project?.title ?? m.file;
      const sub = [
        m.version ? m.version.number : 'Not on Modrinth (added by hand)',
        m.project ? m.file : null,
        bytes(m.size),
      ].filter(Boolean).join(' · ');
      return h('li', { class: 'flex flex-wrap items-center justify-between gap-3 py-2' },
        h('div', { class: `flex min-w-0 items-center gap-3 ${m.enabled ? '' : 'opacity-60'}` },
          modIcon(m.project, m.file),
          h('div', { class: 'min-w-0' },
            h('p', { class: 'flex items-center gap-2 font-medium' },
              h('span', { class: 'truncate' }, title),
              !m.enabled && h('span', { class: 'badge text-muted' }, 'Disabled'),
              m.update && h('span', { class: 'badge text-primary border-primary/40' }, `Update ${m.update.number}`)),
            h('p', { class: 'truncate text-xs text-muted' }, sub))),
        h('div', { class: 'flex flex-wrap gap-1' },
          m.update && smallBtn('Update', () => update([m.file], `${title} → ${m.update.number}`)),
          smallBtn(m.enabled ? 'Disable' : 'Enable', () => toggle(m)),
          smallBtn('Remove', () => remove(m, title), 'text-danger')));
    }));
    none.hidden = mods.length > 0;
    const updates = mods.filter((m) => m.update).length;
    updateAllBtn.hidden = updates < 2;
    updateAllBtn.textContent = `Update all (${updates})`;
    updateAllBtn.disabled = Boolean(busy);
    restartNote.hidden = !(running && changed);
    searchBtn.disabled = Boolean(busy);
    renderResults();
  }

  function setNote(text) {
    note.textContent = busy || text;
  }

  function block(code) {
    blocked.textContent = BLOCKED[code];
    blocked.hidden = false;
    installed.hidden = true;
    search.hidden = true;
  }

  async function load() {
    try {
      const out = await api('GET', `/servers/${id}/mods`);
      mods = out.mods;
      blocked.hidden = true;
      installed.hidden = false;
      search.hidden = false;
      setNote(out.catalog_unavailable ? 'Modrinth could not be reached: mods are listed without details or updates.' : '');
      render();
    } catch (err) {
      if (err instanceof ApiError && BLOCKED[err.code]) return block(err.code);
      setNote(`Could not list the mods: ${err.message}`);
    }
  }

  // run performs one change, then reloads the list.
  async function run(label, request, done) {
    busy = label;
    setNote('');
    render();
    try {
      const out = await request();
      if (running) changed = true;
      toast(done(out), 'success');
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      busy = '';
      setNote('');
      await load();
    }
  }

  async function toggle(m) {
    await run(m.enabled ? 'Disabling…' : 'Enabling…',
      () => api('PATCH', `/servers/${id}/mods/files/${encodeURIComponent(m.file)}`, { enabled: !m.enabled }),
      () => `${m.project?.title ?? m.file} ${m.enabled ? 'disabled' : 'enabled'}.`);
  }

  async function remove(m, title) {
    const ok = await confirmDialog({
      title: 'Remove mod', message: `Delete ${m.file} from the mods folder? Worlds may lose blocks or items that ${title} added.`,
      confirmLabel: 'Remove', danger: true,
    });
    if (!ok) return;
    await run('Removing…', () => api('DELETE', `/servers/${id}/mods/files/${encodeURIComponent(m.file)}`), () => `${title} removed.`);
  }

  async function update(files, what) {
    const ok = await confirmDialog({
      title: files ? 'Update mod' : 'Update all mods',
      message: `${what}. The old files are replaced.`,
      checkbox: 'Back up the whole server first (Backups tab; can take minutes on big worlds)',
      checked: true,
      confirmLabel: 'Update',
    });
    if (!ok) return;
    const body = { backup: ok.checked, ...(files ? { files } : {}) };
    await run(ok.checked ? 'Backing up and updating…' : 'Updating…', () => api('POST', `/servers/${id}/mods/update`, body), (out) => {
      const deps = out.installed.length ? ` and ${out.installed.length} new ${out.installed.length === 1 ? 'dependency' : 'dependencies'}` : '';
      return `Updated ${out.updated.length} ${out.updated.length === 1 ? 'mod' : 'mods'}${deps}.`;
    });
  }

  updateAllBtn.addEventListener('click', () => {
    const n = mods.filter((m) => m.update).length;
    update(null, `${n} mods have an update`);
  });

  // --- search -------------------------------------------------------------------------

  let hits = [];

  function renderResults() {
    const have = installedProjects();
    results.replaceChildren(...hits.map((p) => h('li', { class: 'flex flex-wrap items-center justify-between gap-3 py-2' },
      h('div', { class: 'flex min-w-0 flex-1 items-center gap-3' },
        modIcon(p),
        h('div', { class: 'min-w-0' },
          h('p', { class: 'truncate font-medium' }, p.title),
          h('p', { class: 'line-clamp-2 text-xs text-muted' }, p.description),
          h('p', { class: 'text-xs text-muted' }, `${compact.format(p.downloads)} downloads · server ${p.server_side}`))),
      have.has(p.id)
        ? h('span', { class: 'badge text-success border-success/40' }, 'Installed')
        : h('button', { class: 'btn btn-ghost px-2 py-1 text-xs', type: 'button', disabled: Boolean(busy), onclick: () => installDialog(p) }, 'Install…'))));
    moreBtn.hidden = hits.length === 0 || searchOffset >= searchTotal;
    moreBtn.disabled = Boolean(busy);
  }

  async function runSearch(append) {
    const offset = append ? searchOffset : 0;
    searchBtn.disabled = true;
    moreBtn.disabled = true;
    try {
      const out = await api('GET', `/servers/${id}/mods/search?q=${encodeURIComponent(searchQuery)}&offset=${offset}`);
      hits = append ? [...hits, ...out.hits] : out.hits;
      searchTotal = out.total;
      searchOffset = offset + out.hits.length;
      searchNote.hidden = hits.length > 0;
      searchNote.textContent = 'No server-side mods match.';
    } catch (err) {
      if (err instanceof ApiError && BLOCKED[err.code]) return block(err.code);
      searchNote.hidden = false;
      searchNote.textContent = `Search failed: ${err.message}`;
    } finally {
      searchBtn.disabled = Boolean(busy);
      renderResults();
    }
  }

  searchForm.addEventListener('submit', (e) => {
    e.preventDefault();
    searchQuery = query.value.trim();
    runSearch(false);
  });
  moreBtn.addEventListener('click', () => runSearch(true));

  // installDialog picks a version (newest release by default) and installs it.
  function installDialog(p) {
    const select = h('select', { class: 'input min-w-0 truncate', name: 'version_id', disabled: true }, h('option', { value: '' }, 'Loading versions…'));
    const deps = h('p', { class: 'text-xs text-muted' });
    const error = h('p', { class: 'form-error', role: 'alert', hidden: true });
    const cancel = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Cancel');
    const ok = h('button', { class: 'btn btn-primary', type: 'submit', disabled: true }, 'Install');
    const versionField = field('Version', select);
    versionField.classList.add('min-w-0');
    const form = h('form', { class: 'flex min-w-0 flex-col gap-4', novalidate: true },
      h('div', { class: 'flex min-w-0 items-center gap-3' }, modIcon(p), h('p', { class: 'line-clamp-3 min-w-0 text-sm break-words text-muted' }, p.description)),
      versionField, deps, error,
      h('div', { class: 'flex justify-end gap-2' }, cancel, ok));
    const { close } = openDialog({ title: `Install ${p.title}`, content: form });
    cancel.addEventListener('click', close);

    let versions = [];
    const showDeps = () => {
      const v = versions.find((x) => x.id === select.value);
      const required = v?.dependencies.filter((d) => d.kind === 'required').length ?? 0;
      deps.textContent = required
        ? `This version needs ${required} other ${required === 1 ? 'mod' : 'mods'}; those not installed yet are downloaded too.`
        : '';
    };
    select.addEventListener('change', showDeps);

    api('GET', `/servers/${id}/mods/projects/${encodeURIComponent(p.id)}/versions`).then((out) => {
      versions = out.versions;
      if (!versions.length) {
        select.replaceChildren(h('option', { value: '' }, 'No version for this server'));
        return;
      }
      const preferred = versions.find((v) => v.type === 'release') ?? versions[0];
      select.replaceChildren(...versions.map((v) => h('option', { value: v.id, selected: v.id === preferred.id },
        `${v.number}${v.type === 'release' ? '' : ` (${v.type})`} · ${new Date(v.published_at).toLocaleDateString()}`)));
      select.disabled = false;
      ok.disabled = false;
      showDeps();
    }).catch((err) => {
      error.textContent = err.message;
      error.hidden = false;
    });

    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      if (!select.value) return;
      error.hidden = true;
      ok.disabled = true;
      ok.textContent = 'Installing…';
      try {
        const out = await api('POST', `/servers/${id}/mods`, { project_id: p.id, version_id: select.value });
        if (running) changed = true;
        const extra = out.installed.length - 1;
        toast(`${p.title} installed${extra > 0 ? ` with ${extra} ${extra === 1 ? 'dependency' : 'dependencies'}` : ''}.`, 'success');
        close();
        load();
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
        ok.disabled = false;
        ok.textContent = 'Install';
      }
    });
  }

  load();

  return {
    // setStatus tracks the server's state: the game loads the mods as it starts, so a start or
    // restart clears the restart note.
    setStatus(status) {
      running = ACTIVE.has(status);
      if (!running || status === 'starting' || status === 'restarting') changed = false;
      render();
    },
    refresh: load,
  };
}

// Catalog modpacks (Modrinth and CurseForge): a picker for the "New server" dialog and the
// pack card of the server page, which updates a stopped server to another pack version after a
// full backup. The game image installs the pack on start and keeps the world when the version
// changes. See docs/API.md, "Modpacks".
import { api, ApiError } from './api.js';
import { confirmDialog, openDialog } from './dialog.js';
import { field, h, toast } from './dom.js';
import { LOADERS, STOPPED } from './server-actions.js';

const compact = new Intl.NumberFormat(undefined, { notation: 'compact' });

export const SOURCES = { modrinth: 'Modrinth', curseforge: 'CurseForge' };

const versionLabel = (v) =>
  `${v.name}${v.type === 'release' ? '' : ` (${v.type})`} · ${LOADERS[v.loader] ?? v.loader} ${v.game_version} · ${new Date(v.published_at).toLocaleDateString()}`;

// packIcon is a project's icon (40 or 48 px), or its initial when it has none or the icon fails
// to load.
export function packIcon(source, project, size = 40) {
  const box = `${size === 48 ? 'size-12' : 'size-10'} shrink-0 rounded-md border border-border`;
  const fallback = () => h('span', {
    class: `${box} flex items-center justify-center bg-surface text-xs font-semibold text-muted`, 'aria-hidden': 'true',
  }, (project.title || project.name || '?').charAt(0).toUpperCase());
  if (project.has_icon === false) return fallback();
  const img = h('img', {
    class: `${box} bg-surface object-cover`, alt: '', width: String(size), height: String(size), loading: 'lazy', decoding: 'async',
    src: `/api/v1/modpacks/${source}/${encodeURIComponent(project.id ?? project.project_id)}/icon`,
  });
  img.addEventListener('error', () => img.replaceWith(fallback()), { once: true });
  return img;
}

// pickModpack opens a search over both catalogs. It resolves to
// { source, project, version } once a version is chosen, or null when cancelled.
export function pickModpack() {
  return new Promise((resolve) => {
    let result = null;
    let source = 'modrinth';
    let hits = [];
    let offset = 0;
    let total = 0;
    let q = '';

    const tabs = Object.entries(SOURCES).map(([key, label]) => h('button', {
      class: 'btn btn-ghost px-3 py-1 text-sm', type: 'button', role: 'tab', 'data-source': key,
      onclick: () => { source = key; markTabs(); runSearch(false); },
    }, label));
    const markTabs = () => tabs.forEach((t) => {
      const on = t.dataset.source === source;
      t.setAttribute('aria-selected', String(on));
      t.classList.toggle('btn-primary', on);
      t.classList.toggle('btn-ghost', !on);
    });
    const query = h('input', { class: 'input min-w-0 flex-1', type: 'search', name: 'q', placeholder: 'Search modpacks (empty: most popular)', autocomplete: 'off' });
    const searchBtn = h('button', { class: 'btn', type: 'submit' }, 'Search');
    const searchForm = h('form', { class: 'flex gap-2', role: 'search' }, query, searchBtn);
    const results = h('ul', { class: 'flex max-h-[55vh] min-w-0 flex-col divide-y divide-border overflow-y-auto pr-1' });
    const note = h('p', { class: 'text-sm text-muted', role: 'status', hidden: true });
    const moreBtn = h('button', { class: 'btn btn-ghost self-center', type: 'button', hidden: true }, 'Load more');
    const cancel = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Cancel');
    const { close } = openDialog({
      title: 'Choose a modpack',
      wide: true,
      content: h('div', { class: 'flex min-w-0 flex-col gap-3' },
        h('div', { class: 'flex gap-2', role: 'tablist' }, ...tabs),
        searchForm, note, results, moreBtn,
        h('p', { class: 'text-xs text-muted' }, 'Only packs for Fabric, Forge or NeoForge are listed. The server downloads the pack on its first start.'),
        h('div', { class: 'flex justify-end' }, cancel)),
      onClose: () => resolve(result),
    });
    cancel.addEventListener('click', close);

    function render() {
      results.replaceChildren(...hits.map((p) => h('li', { class: 'flex min-w-0 items-center justify-between gap-3 py-2' },
        h('div', { class: 'flex min-w-0 flex-1 items-center gap-3' },
          packIcon(source, p),
          h('div', { class: 'min-w-0' },
            h('p', { class: 'truncate font-medium' }, p.title),
            h('p', { class: 'line-clamp-2 text-xs text-muted' }, p.description),
            h('p', { class: 'text-xs text-muted' }, `${compact.format(p.downloads)} downloads`))),
        h('button', { class: 'btn btn-primary shrink-0 px-3 py-1 text-xs', type: 'button', onclick: () => chooseVersion(p) }, 'Choose…'))));
      moreBtn.hidden = hits.length === 0 || offset >= total;
    }

    async function runSearch(append) {
      const from = append ? offset : 0;
      searchBtn.disabled = moreBtn.disabled = true;
      if (!append) { hits = []; render(); }
      note.hidden = true;
      try {
        const out = await api('GET', `/modpacks/search?source=${source}&q=${encodeURIComponent(q)}&offset=${from}`);
        hits = append ? [...hits, ...out.hits] : out.hits;
        total = out.total;
        offset = from + out.hits.length;
        note.hidden = hits.length > 0;
        note.textContent = 'No modpack matches.';
      } catch (err) {
        note.hidden = false;
        note.replaceChildren(err instanceof ApiError && err.code === 'curseforge_not_configured'
          ? h('span', null, 'CurseForge needs an API key. Add one in ', h('a', { href: '#/settings', class: 'text-primary underline', onclick: close }, 'Settings'), '.')
          : `Search failed: ${err.message}`);
      } finally {
        searchBtn.disabled = moreBtn.disabled = false;
        render();
      }
    }

    function chooseVersion(p) {
      const select = h('select', { class: 'input min-w-0 truncate', name: 'version_id', disabled: true }, h('option', { value: '' }, 'Loading versions…'));
      const error = h('p', { class: 'form-error', role: 'alert', hidden: true });
      const back = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Cancel');
      const ok = h('button', { class: 'btn btn-primary', type: 'submit', disabled: true }, 'Use this version');
      const versionField = field('Version', select);
      versionField.classList.add('min-w-0');
      const form = h('form', { class: 'flex min-w-0 flex-col gap-4', novalidate: true },
        h('div', { class: 'flex min-w-0 items-center gap-3' }, packIcon(source, p),
          h('p', { class: 'line-clamp-3 min-w-0 text-sm break-words text-muted' }, p.description)),
        versionField, error, h('div', { class: 'flex justify-end gap-2' }, back, ok));
      const from = source;
      const dlg = openDialog({ title: p.title, content: form });
      back.addEventListener('click', dlg.close);
      let versions = [];
      api('GET', `/modpacks/${from}/${encodeURIComponent(p.id)}/versions`).then((out) => {
        versions = out.versions;
        if (!versions.length) {
          select.replaceChildren(h('option', { value: '' }, 'No version the panel can run'));
          return;
        }
        const preferred = versions.find((v) => v.type === 'release') ?? versions[0];
        select.replaceChildren(...versions.map((v) => h('option', { value: v.id, selected: v.id === preferred.id }, versionLabel(v))));
        select.disabled = ok.disabled = false;
      }).catch((err) => {
        error.textContent = err.message;
        error.hidden = false;
      });
      form.addEventListener('submit', (e) => {
        e.preventDefault();
        const version = versions.find((v) => v.id === select.value);
        if (!version) return;
        result = { source: from, project: p, version };
        dlg.close();
        close();
      });
    }

    searchForm.addEventListener('submit', (e) => {
      e.preventDefault();
      q = query.value.trim();
      runSearch(false);
    });
    moreBtn.addEventListener('click', () => runSearch(true));
    markTabs();
    query.focus();
    runSearch(false);
  });
}

// mountModpackCard shows a server's catalog modpack with a version picker. Updating needs the
// server stopped and makes a full backup first; onUpdated receives the updated server.
export function mountModpackCard(root, { server, onUpdated }) {
  const pack = server.modpack;
  const select = h('select', { class: 'input min-w-0 truncate', name: 'version_id', disabled: true }, h('option', { value: '' }, 'Loading versions…'));
  const updateBtn = h('button', { class: 'btn btn-primary', type: 'button', disabled: true }, 'Update');
  const note = h('p', { class: 'text-xs text-muted', role: 'status' });
  const error = h('p', { class: 'form-error', role: 'alert', hidden: true });
  const current = h('p', { class: 'text-sm text-muted' });
  let status = server.status;
  let versions = [];

  const versionField = field('Version', select);
  versionField.classList.add('min-w-0', 'flex-1');
  root.replaceChildren(h('div', { class: 'flex min-w-0 flex-col gap-3' },
    h('div', { class: 'flex min-w-0 items-center gap-3' },
      packIcon(pack.source, { project_id: pack.project_id, name: pack.name }, 48),
      h('div', { class: 'min-w-0' },
        h('h2', { class: 'truncate font-semibold' }, pack.name),
        current)),
    h('div', { class: 'flex min-w-0 flex-wrap items-end gap-2' }, versionField, updateBtn),
    note, error,
    pack.source === 'curseforge' && h('p', { class: 'text-xs text-muted' },
      'Some CurseForge mods cannot be downloaded automatically. If the console lists missing files, download them from CurseForge, upload them to the downloads folder in the Files tab and start the server again.')));

  function render() {
    const v = versions.find((x) => x.id === pack.version_id);
    current.textContent = `${SOURCES[pack.source]} · ${pack.version_name}${v ? ` · ${LOADERS[v.loader] ?? v.loader} ${v.game_version}` : ''}`;
    const chosen = versions.find((x) => x.id === select.value);
    const stopped = STOPPED.has(status);
    updateBtn.disabled = !chosen || chosen.id === pack.version_id || !stopped;
    if (!stopped) note.textContent = 'Stop the server to change the modpack version.';
    else if (chosen && v && compareMC(chosen.game_version, v.game_version) < 0) note.textContent = `Warning: ${chosen.game_version} is older than the world's Minecraft version (${v.game_version}). Worlds cannot be downgraded safely; the backup lets you go back.`;
    else note.textContent = 'A full backup is made first. The world is kept; the pack\'s mods and configs are replaced on the next start.';
  }

  select.addEventListener('change', render);
  updateBtn.addEventListener('click', async () => {
    const chosen = versions.find((x) => x.id === select.value);
    if (!chosen) return;
    const ok = await confirmDialog({
      title: `Update ${pack.name}?`,
      message: `The server is backed up, then switches to ${chosen.name}. The new version is installed on the next start; the world is kept.`,
      confirmLabel: 'Back up and update',
    });
    if (!ok) return;
    error.hidden = true;
    updateBtn.disabled = true;
    updateBtn.textContent = 'Backing up…';
    try {
      const updated = await api('POST', `/servers/${server.id}/modpack/version`, { version_id: chosen.id });
      toast(`${pack.name} switched to ${chosen.name}. Start the server to install it.`, 'success');
      onUpdated?.(updated);
    } catch (err) {
      error.textContent = err.message;
      error.hidden = false;
    } finally {
      updateBtn.textContent = 'Update';
      render();
    }
  });

  api('GET', `/servers/${server.id}/modpack/versions`).then((out) => {
    versions = out.versions;
    const options = versions.map((v) => h('option', { value: v.id, selected: v.id === pack.version_id },
      `${versionLabel(v)}${v.id === pack.version_id ? ' (installed)' : ''}`));
    if (!versions.some((v) => v.id === pack.version_id)) {
      options.unshift(h('option', { value: pack.version_id, selected: true }, `${pack.version_name} (installed)`));
    }
    select.replaceChildren(...options);
    select.disabled = false;
    render();
  }).catch((err) => {
    select.replaceChildren(h('option', { value: '' }, 'Versions unavailable'));
    error.textContent = err.message;
    error.hidden = false;
    render();
  });
  render();

  return {
    setStatus(s) {
      status = s;
      render();
    },
  };
}

function compareMC(a, b) {
  const pa = a.split('.').map(Number);
  const pb = b.split('.').map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (d) return d;
  }
  return 0;
}

// Server type → Minecraft version → loader build selects, filled from the official version
// lists through the panel. Shared by "New server" and "Change version". The inputs are named
// loader, version and loader_version, for formValues.

import { api } from './api.js';
import { field, h } from './dom.js';
import { LOADERS } from './server-actions.js';

// versionFields builds the fields. onBusy(busy) tells when a choice is still loading (the
// submit button should wait); onLoader(kind) runs when the type changes. initial preselects
// a server's current game when the lists contain it.
export function versionFields({ onBusy = () => {}, onLoader = () => {}, initial = null } = {}) {
  const loader = h('select', { class: 'input', name: 'loader' },
    Object.entries(LOADERS).map(([value, label]) => h('option', { value }, label)));
  if (initial?.loader in LOADERS) loader.value = initial.loader;
  const versionSlot = h('div');
  const loaderVersion = h('select', { class: 'input', name: 'loader_version', disabled: true });
  const loaderVersionField = field('Loader version', loaderVersion);
  loaderVersionField.hidden = true;
  const status = h('p', { class: 'text-xs text-muted', role: 'status', hidden: true });
  const root = h('div', { class: 'flex flex-col gap-4' },
    h('div', { class: 'grid grid-cols-2 gap-4' }, field('Server type', loader), versionSlot),
    loaderVersionField, status);

  // Each change bumps `request`, so a slow answer for a previous choice is ignored.
  let request = 0;
  const setStatus = (text) => {
    status.textContent = text;
    status.hidden = !text;
  };
  const options = (select, values, label = (v) => v) =>
    select.replaceChildren(...values.map((v) => h('option', { value: v }, label(v))));
  // The initial game applies only to the first list shown for the initial type.
  const wanted = (key) => (initial && loader.value === initial.loader ? initial[key] : undefined);

  async function loadVersions() {
    const id = ++request;
    const kind = loader.value;
    const modded = kind !== 'vanilla';
    onLoader(kind);
    loaderVersionField.hidden = !modded;
    loaderVersion.disabled = true;
    onBusy(true);
    setStatus('Loading versions…');
    try {
      const { versions } = await api('GET', `/installers/${kind}/versions`);
      if (id !== request) return;
      const select = h('select', { class: 'input', name: 'version' });
      options(select, versions, (v) => (v === 'LATEST' ? 'Latest release (updates on restart)' : v));
      if (versions.includes(wanted('version'))) select.value = wanted('version');
      if (modded) select.addEventListener('change', () => loadLoaderVersions(select.value));
      select.addEventListener('change', () => root.dispatchEvent(new Event('change', { bubbles: true })));
      versionSlot.replaceChildren(field('Minecraft version', select));
      if (modded) {
        if (!versions.length) throw new Error(`No ${LOADERS[kind]} versions are available.`);
        await loadLoaderVersions(select.value, id);
      } else {
        setStatus('');
        onBusy(false);
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
        h('input', { class: 'input', name: 'version', value: wanted('version') ?? 'LATEST', required: true, autocomplete: 'off' })));
      setStatus('The version list is unavailable; type "LATEST" or a release such as 1.21.4.');
      onBusy(false);
    }
  }

  async function loadLoaderVersions(mc, id = ++request) {
    loaderVersion.disabled = true;
    onBusy(true);
    setStatus('Loading loader versions…');
    try {
      const lv = await api('GET', `/installers/${loader.value}/versions/${encodeURIComponent(mc)}`);
      if (id !== request) return;
      options(loaderVersion, lv.loader_versions, (v) => (v === lv.recommended ? `${v} (recommended)` : v));
      const keep = mc === wanted('version') && lv.loader_versions.includes(wanted('loader_version'));
      loaderVersion.value = keep ? wanted('loader_version') : lv.recommended;
      loaderVersion.disabled = false;
      onBusy(false);
      setStatus('');
      root.dispatchEvent(new Event('change', { bubbles: true }));
    } catch (err) {
      if (id === request) setStatus(`Could not load loader versions: ${err.message}`);
    }
  }

  loader.addEventListener('change', loadVersions);
  loadVersions();
  return { root, loader };
}

// compareVersions orders Minecraft releases ("1.21.4" < "1.21.10" < "26.1"). LATEST is not
// comparable: null.
export function compareVersions(a, b) {
  if (a === 'LATEST' || b === 'LATEST') return null;
  const pa = a.split('.').map(Number);
  const pb = b.split('.').map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (d) return Math.sign(d);
  }
  return 0;
}

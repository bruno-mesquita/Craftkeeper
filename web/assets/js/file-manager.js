// File manager of one server (docs/API.md, "Files"): browse, upload (button or drag and
// drop), download (directories as .zip), edit text files, create folders, rename/move,
// extract .zip archives and delete. Paths are relative to the server's directory.
import { api, ApiError, upload } from './api.js';
import { confirmDialog, openDialog, promptDialog } from './dialog.js';
import { h, toast } from './dom.js';
import { bytes, dateTime } from './format.js';

const join = (dir, name) => (dir ? `${dir}/${name}` : name);
const parent = (p) => p.split('/').slice(0, -1).join('/');

export function mountFileManager(root, { id }) {
  const base = `/servers/${id}/files`;
  let cwd = '';
  let loading = 0; // guards against out-of-order listings

  const crumbs = h('nav', { class: 'flex min-w-0 flex-wrap items-center gap-1 text-sm', 'aria-label': 'Current folder' });
  const fileInput = h('input', { type: 'file', multiple: true, hidden: true });
  const uploadBtn = h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => fileInput.click() }, 'Upload');
  const folderBtn = h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => newFolder() }, 'New folder');
  const refreshBtn = h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => load() }, 'Refresh');
  const warning = h('p', { class: 'mb-3 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warning', hidden: true },
    'The server is running: configuration changes apply after a restart, and changing world files while it runs can corrupt them.');
  const status = h('p', { class: 'text-xs text-muted', role: 'status' });
  const tbody = h('tbody');
  const empty = h('p', { class: 'py-6 text-center text-muted', hidden: true }, 'This folder is empty. Drop files here to upload.');
  const drop = h('div', { class: 'rounded-md border border-transparent transition-colors' },
    h('div', { class: 'overflow-x-auto' },
      h('table', { class: 'w-full text-left' },
        h('thead', { class: 'text-xs text-muted' },
          h('tr', null, ['Name', 'Size', 'Modified', h('span', { class: 'sr-only' }, 'Actions')]
            .map((c) => h('th', { class: 'pb-2 pr-4 font-medium', scope: 'col' }, c)))),
        tbody)),
    empty);

  root.replaceChildren(
    warning,
    h('div', { class: 'mb-3 flex flex-wrap items-center justify-between gap-2' },
      crumbs,
      h('div', { class: 'flex gap-1' }, uploadBtn, folderBtn, refreshBtn, fileInput)),
    drop,
    status);

  // --- listing ------------------------------------------------------------------------

  async function load(dir = cwd) {
    const current = ++loading;
    try {
      const list = await api('GET', `${base}?path=${encodeURIComponent(dir)}`);
      if (current !== loading) return;
      cwd = list.path;
      render(list);
    } catch (err) {
      if (current !== loading) return;
      if (err instanceof ApiError && err.status === 404 && dir) return load(parent(dir));
      status.textContent = `Could not list the folder: ${err.message}`;
    }
  }

  function render(list) {
    crumbs.replaceChildren(...breadcrumbs());
    tbody.replaceChildren(...list.entries.map(row));
    empty.hidden = list.entries.length > 0;
    status.textContent = list.truncated
      ? `Showing the first ${list.entries.length} entries.`
      : `${list.entries.length} item${list.entries.length === 1 ? '' : 's'}`;
  }

  function breadcrumbs() {
    const parts = cwd ? cwd.split('/') : [];
    const out = [crumb('server', '', parts.length === 0)];
    parts.forEach((p, i) => {
      out.push(h('span', { class: 'text-muted', 'aria-hidden': 'true' }, '/'));
      out.push(crumb(p, parts.slice(0, i + 1).join('/'), i === parts.length - 1));
    });
    return out;
  }

  function crumb(label, dir, current) {
    return current
      ? h('span', { class: 'truncate font-medium', 'aria-current': 'location' }, label)
      : h('button', { class: 'truncate text-primary hover:underline', type: 'button', onclick: () => load(dir) }, label);
  }

  function row(e) {
    const p = join(cwd, e.name);
    const isDir = e.type === 'dir';
    const nameCell = isDir
      ? h('button', { class: 'font-medium text-primary hover:underline', type: 'button', onclick: () => load(p) }, `${e.name}/`)
      : h('span', { class: e.type === 'symlink' ? 'italic' : null, title: e.type === 'symlink' ? 'Symbolic link' : null }, e.name);
    const btn = (label, onclick, cls = '') =>
      h('button', { class: `btn btn-ghost px-2 py-1 text-xs ${cls}`, type: 'button', onclick, 'aria-label': `${label} ${e.name}` }, label);
    const actions = [];
    if (e.type === 'file') actions.push(btn('Edit', () => edit(p)));
    if (isDir || e.type === 'file' || e.type === 'symlink') {
      actions.push(h('a', {
        class: 'btn btn-ghost px-2 py-1 text-xs',
        href: `/api/v1${base}/download?path=${encodeURIComponent(p)}`,
        download: isDir ? `${e.name}.zip` : e.name,
        'aria-label': `Download ${e.name}`,
      }, isDir ? 'Download .zip' : 'Download'));
    }
    if (e.type === 'file' && e.name.toLowerCase().endsWith('.zip')) actions.push(btn('Extract', () => extract(p)));
    actions.push(btn('Rename', () => rename(p)));
    actions.push(btn('Delete', () => remove(p, isDir), 'text-danger'));
    return h('tr', { class: 'border-t border-border' },
      h('td', { class: 'max-w-xs truncate py-2 pr-4' }, nameCell),
      h('td', { class: 'py-2 pr-4 whitespace-nowrap tabular-nums text-muted' }, isDir ? '—' : bytes(e.size)),
      h('td', { class: 'py-2 pr-4 whitespace-nowrap text-muted' }, dateTime(e.modified_at)),
      h('td', { class: 'py-2' }, h('div', { class: 'flex justify-end gap-1' }, actions)));
  }

  // --- mutations ----------------------------------------------------------------------

  async function run(promise, success) {
    try {
      const out = await promise;
      if (success) toast(success, 'success');
      return out;
    } catch (err) {
      toast(err.message, 'error');
      return undefined;
    } finally {
      load();
    }
  }

  async function newFolder() {
    const name = await promptDialog({ title: 'New folder', label: 'Folder name', confirmLabel: 'Create' });
    if (name) run(api('POST', `${base}/directory`, { path: join(cwd, name) }));
  }

  async function rename(p) {
    const to = await promptDialog({ title: 'Rename or move', label: 'New path (relative to the server folder)', value: p, confirmLabel: 'Rename' });
    if (to && to !== p) run(api('POST', `${base}/rename`, { from: p, to }));
  }

  async function remove(p, isDir) {
    const ok = await confirmDialog({
      title: `Delete ${p}?`,
      message: isDir ? 'The folder and everything inside it are deleted. This cannot be undone.' : 'The file is deleted. This cannot be undone.',
      confirmLabel: 'Delete', danger: true,
    });
    if (ok) run(api('DELETE', `${base}?path=${encodeURIComponent(p)}`));
  }

  async function extract(p) {
    const ok = await confirmDialog({
      title: `Extract ${p}?`,
      message: `Its contents are extracted into /${cwd}. Symbolic links inside the archive are skipped.`,
      confirmLabel: 'Extract',
      checkbox: 'Overwrite existing files',
    });
    if (!ok) return;
    status.textContent = `Extracting ${p}…`;
    const out = await run(api('POST', `${base}/extract`, { path: p, destination: cwd, overwrite: ok.checked }));
    if (out) toast(`Extracted ${out.entries} entries.`, 'success');
  }

  async function uploadFiles(files, overwrite = false) {
    if (files.length === 0) return;
    const label = files.length === 1 ? files[0].name : `${files.length} files`;
    uploadBtn.disabled = true;
    try {
      await upload(`${base}/upload?path=${encodeURIComponent(cwd)}${overwrite ? '&overwrite=true' : ''}`, files,
        (f) => { status.textContent = `Uploading ${label}… ${Math.round(f * 100)}%`; });
      toast(`Uploaded ${label}.`, 'success');
    } catch (err) {
      if (err.code === 'file_exists' && !overwrite) {
        const ok = await confirmDialog({
          title: 'Replace existing files?',
          message: `A file named like one of the uploads already exists in /${cwd}. Replace it?`,
          confirmLabel: 'Replace', danger: true,
        });
        if (ok) return uploadFiles(files, true);
      } else {
        toast(`Upload failed: ${err.message}`, 'error');
      }
    } finally {
      uploadBtn.disabled = false;
      load();
    }
  }

  fileInput.addEventListener('change', () => {
    const files = [...fileInput.files];
    fileInput.value = '';
    uploadFiles(files);
  });
  const highlight = (on) => drop.classList.toggle('border-primary', on);
  drop.addEventListener('dragover', (e) => {
    if (!e.dataTransfer?.types.includes('Files')) return;
    e.preventDefault();
    highlight(true);
  });
  drop.addEventListener('dragleave', () => highlight(false));
  drop.addEventListener('drop', (e) => {
    e.preventDefault();
    highlight(false);
    uploadFiles([...(e.dataTransfer?.files ?? [])]);
  });

  // --- editor -------------------------------------------------------------------------

  async function edit(p) {
    let file;
    try {
      file = await api('GET', `${base}/content?path=${encodeURIComponent(p)}`);
    } catch (err) {
      toast(err.code === 'not_text' || err.code === 'file_too_large'
        ? `${p} cannot be edited here (${err.message}). Download it instead.`
        : err.message, 'error');
      return;
    }
    let version = file.version;
    const area = h('textarea', {
      class: 'input h-[60vh] resize-y font-mono text-xs leading-5', spellcheck: 'false', 'aria-label': `Contents of ${p}`,
    });
    area.value = file.content;
    const note = h('p', { class: 'text-xs text-muted', role: 'status' });
    const reload = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Reload');
    const cancel = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Close');
    const save = h('button', { class: 'btn btn-primary', type: 'button' }, 'Save');
    let dirty = false;
    area.addEventListener('input', () => { dirty = true; note.textContent = 'Unsaved changes'; });

    const { dialog, close } = openDialog({
      title: p,
      content: [area, h('div', { class: 'mt-4 flex flex-wrap items-center justify-between gap-2' },
        note, h('div', { class: 'flex gap-2' }, reload, cancel, save))],
    });
    dialog.classList.add('w-[min(72rem,calc(100%-2rem))]');
    dialog.addEventListener('cancel', (e) => { // Esc: same as Close
      e.preventDefault();
      cancel.click();
    });

    async function doSave(force) {
      save.disabled = true;
      try {
        const out = await api('PUT', `${base}/content`, { path: p, content: area.value, version: force ? '' : version });
        version = out.version;
        dirty = false;
        note.textContent = 'Saved.';
        load();
      } catch (err) {
        if (err.code === 'file_changed') {
          const ok = await confirmDialog({
            title: 'The file changed on disk',
            message: 'Someone (often the game itself) changed this file after you opened it. Overwrite it with your version? Cancel keeps editing; use Reload to see the new contents.',
            confirmLabel: 'Overwrite', danger: true,
          });
          if (ok) return doSave(true);
        } else {
          note.textContent = `Not saved: ${err.message}`;
        }
      } finally {
        save.disabled = false;
      }
    }

    save.addEventListener('click', () => doSave(false));
    cancel.addEventListener('click', async () => {
      if (dirty && !(await confirmDialog({ title: 'Discard changes?', message: 'Your unsaved changes are lost.', confirmLabel: 'Discard', danger: true }))) return;
      close();
    });
    reload.addEventListener('click', async () => {
      if (dirty && !(await confirmDialog({ title: 'Reload from disk?', message: 'Your unsaved changes are lost.', confirmLabel: 'Reload', danger: true }))) return;
      try {
        const fresh = await api('GET', `${base}/content?path=${encodeURIComponent(p)}`);
        area.value = fresh.content;
        version = fresh.version;
        dirty = false;
        note.textContent = 'Reloaded.';
      } catch (err) {
        note.textContent = `Could not reload: ${err.message}`;
      }
    });
    area.addEventListener('keydown', (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault();
        doSave(false);
      }
    });
    area.focus();
  }

  load();

  return {
    setRunning(running) {
      warning.hidden = !running;
    },
    refresh: () => load(),
  };
}

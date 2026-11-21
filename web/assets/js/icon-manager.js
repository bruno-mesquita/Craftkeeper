// Server list icon: the server-icon.png the game shows in the multiplayer list. Minecraft reads
// it from the server root at startup and accepts only a 64×64 PNG, so any image the user picks
// is cropped and scaled in the browser before upload. Reads and writes go through the file API
// (docs/API.md "Files"), which already confines, audits and writes atomically.
import { api, ApiError, upload } from './api.js';
import { confirmDialog } from './dialog.js';
import { h, toast } from './dom.js';

const ICON = 'server-icon.png';
const SIZE = 64; // the only size the game accepts
const MAX_SOURCE = 10 * 1024 * 1024; // larger pictures are refused before decoding

// serverIcon is a small tile with the server's current icon, empty when it has none. The
// download endpoint sends octet-stream, which <img> still decodes by content. reload() picks up
// a newly saved or removed icon.
export function serverIcon(id, sizeClass) {
  const img = h('img', { class: 'size-full', alt: '', hidden: true });
  img.addEventListener('load', () => { img.hidden = false; });
  img.addEventListener('error', () => { img.hidden = true; });
  const reload = () => {
    img.src = `/api/v1/servers/${id}/files/download?path=${encodeURIComponent(ICON)}&t=${Date.now()}`;
  };
  reload();
  return {
    el: h('span', { class: `${sizeClass} inline-flex shrink-0 overflow-hidden rounded-md border border-border bg-background` }, img),
    reload,
  };
}

export function mountIconManager(root, { id, onRestart, onChange }) {
  let running = false;
  let exists = false;
  let pending = null; // File ready to upload, after the user picked an image
  let busy = false;

  const preview = h('img', { class: 'size-full [image-rendering:pixelated]', alt: 'Server icon', hidden: true });
  const placeholder = h('span', { class: 'text-xs text-muted' }, 'No icon');
  const frame = h('div', {
    class: 'flex size-32 shrink-0 items-center justify-center overflow-hidden rounded-md border border-dashed border-border-strong bg-background',
  }, preview, placeholder);

  const picker = h('input', { type: 'file', accept: 'image/png,image/jpeg,image/webp,image/gif', hidden: true });
  const chooseBtn = h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => picker.click() }, 'Choose image…');
  const removeBtn = h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => remove() }, 'Remove');
  const cancelBtn = h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => refresh() }, 'Cancel');
  const saveBtn = h('button', { class: 'btn btn-primary', type: 'button', onclick: () => save(false) }, 'Save');
  const restartBtn = h('button', { class: 'btn btn-primary', type: 'button', onclick: () => save(true) }, 'Save and restart');
  const status = h('p', { class: 'text-xs text-muted', role: 'status' });

  root.replaceChildren(h('section', { class: 'mb-8 flex flex-wrap items-center gap-4 border-b border-border pb-6', 'aria-label': 'Server icon' },
    frame,
    h('div', { class: 'flex flex-col gap-2' },
      h('h2', { class: 'text-sm font-semibold' }, 'Server icon'),
      h('p', { class: 'text-xs text-muted' }, `Shown in the multiplayer server list. Saved as a ${SIZE}×${SIZE} PNG; applies on the next start.`),
      h('div', { class: 'flex flex-wrap gap-2' }, chooseBtn, removeBtn, cancelBtn, saveBtn, restartBtn),
      status),
    picker));

  function render() {
    chooseBtn.disabled = busy;
    removeBtn.hidden = Boolean(pending) || !exists;
    removeBtn.disabled = busy;
    for (const b of [cancelBtn, saveBtn]) {
      b.hidden = !pending;
      b.disabled = busy;
    }
    restartBtn.hidden = !pending || !running;
    restartBtn.disabled = busy;
  }

  function show(src) {
    preview.hidden = !src;
    placeholder.hidden = Boolean(src);
    if (src) preview.src = src;
    else preview.removeAttribute('src');
  }

  // The download is sent as application/octet-stream; re-type it as PNG and show it through a
  // data: URL, the only non-'self' image source the CSP allows.
  async function refresh() {
    pending = null;
    picker.value = '';
    status.textContent = '';
    render();
    try {
      const res = await fetch(`/api/v1/servers/${id}/files/download?path=${encodeURIComponent(ICON)}`, { credentials: 'same-origin' });
      if (res.status === 404) {
        exists = false;
        show(null);
      } else if (!res.ok) {
        const e = (await res.json().catch(() => null))?.error ?? {};
        if (res.status === 401 && e.code === 'unauthenticated') window.dispatchEvent(new CustomEvent('session-expired'));
        throw new ApiError(res.status, e.code ?? 'http_error', e.message ?? res.statusText);
      } else {
        exists = true;
        show(await dataURL(new Blob([await res.blob()], { type: 'image/png' })));
      }
    } catch (err) {
      status.textContent = `Could not load the icon: ${err.message}`;
    }
    render();
  }

  picker.addEventListener('change', async () => {
    const source = picker.files[0];
    if (!source) return;
    if (source.size > MAX_SOURCE) {
      toast('Choose an image up to 10 MiB.', 'error');
      picker.value = '';
      return;
    }
    try {
      const canvas = await toIcon(source);
      const blob = await new Promise((resolve) => canvas.toBlob(resolve, 'image/png'));
      if (!blob) throw new Error('could not encode the PNG');
      pending = new File([blob], ICON, { type: 'image/png' });
      show(canvas.toDataURL('image/png'));
      status.textContent = 'Preview — not saved yet.';
    } catch {
      toast('That file is not an image the browser can read.', 'error');
      picker.value = '';
    }
    render();
  });

  async function save(restart) {
    if (!pending || busy) return;
    busy = true;
    render();
    status.textContent = 'Saving…';
    try {
      await upload(`/servers/${id}/files/upload?path=&overwrite=true`, [pending]);
      toast(restart ? 'Icon saved. Restarting…' : 'Icon saved. It shows after the next start.', 'success');
      onChange?.();
      if (restart) onRestart?.();
      busy = false;
      await refresh();
    } catch (err) {
      busy = false;
      status.textContent = `Could not save the icon: ${err.message}`;
      render();
    }
  }

  async function remove() {
    const ok = await confirmDialog({
      title: 'Remove server icon',
      message: 'The server list will show the default icon after the next start.',
      confirmLabel: 'Remove',
      danger: true,
    });
    if (!ok) return;
    busy = true;
    render();
    try {
      await api('DELETE', `/servers/${id}/files?path=${encodeURIComponent(ICON)}`);
      toast('Icon removed.', 'success');
      onChange?.();
    } catch (err) {
      if (!(err instanceof ApiError && err.status === 404)) toast(`Could not remove the icon: ${err.message}`, 'error');
    }
    busy = false;
    await refresh();
  }

  refresh();

  return {
    refresh() {
      if (!pending && !busy) refresh();
    },
    setRunning(value) {
      running = value;
      render();
    },
  };
}

// adoptPackIcon turns the icon.png a server pack ships into the server-icon.png the game reads,
// placed next to it (the pack's own root, where its scripts start the game). dirs are the
// directories to look in, in order; the first icon.png found wins. An existing server-icon.png
// is never replaced. Returns whether an icon was written.
export async function adoptPackIcon(id, dirs) {
  const fileURL = (p) => `/api/v1/servers/${id}/files/download?path=${encodeURIComponent(p)}`;
  for (const dir of new Set(dirs)) {
    const at = (name) => (dir ? `${dir}/${name}` : name);
    const existing = await fetch(fileURL(at(ICON)), { credentials: 'same-origin' });
    if (existing.ok) return false;
    if (existing.status !== 404) continue;
    const res = await fetch(fileURL(at('icon.png')), { credentials: 'same-origin' });
    if (!res.ok) continue;
    const source = await res.blob();
    if (source.size > MAX_SOURCE) continue;
    let blob;
    try {
      const canvas = await toIcon(new Blob([source], { type: 'image/png' }));
      blob = await new Promise((resolve) => canvas.toBlob(resolve, 'image/png'));
    } catch {
      continue; // not an image the browser can read
    }
    if (!blob) continue;
    await upload(`/servers/${id}/files/upload?path=${encodeURIComponent(dir)}&overwrite=false`, [new File([blob], ICON, { type: 'image/png' })]);
    return true;
  }
  return false;
}

// toIcon center-crops the image to a square and scales it to SIZE×SIZE. Small sources (pixel
// art) are scaled without smoothing to stay crisp.
async function toIcon(file) {
  const bitmap = await createImageBitmap(file);
  try {
    const side = Math.min(bitmap.width, bitmap.height);
    const canvas = document.createElement('canvas');
    canvas.width = SIZE;
    canvas.height = SIZE;
    const ctx = canvas.getContext('2d');
    ctx.imageSmoothingEnabled = side > SIZE;
    ctx.imageSmoothingQuality = 'high';
    ctx.drawImage(bitmap, (bitmap.width - side) / 2, (bitmap.height - side) / 2, side, side, 0, 0, SIZE, SIZE);
    return canvas;
  } finally {
    bitmap.close();
  }
}

function dataURL(blob) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(blob);
  });
}

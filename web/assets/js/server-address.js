// Connection address of a game server: the panel's public host (Settings) plus the server's
// port, shown with a copy button. Without a public host, the host the panel is browsed on.
import { api } from './api.js';
import { h, toast } from './dom.js';
import { icons } from './icons.js';

let hostPromise = null;

// publicHost resolves to the saved public host, or location.hostname when none is saved
// (or it cannot be loaded). The request runs once per page load; refreshPublicHost resets it.
export function publicHost() {
  hostPromise ??= api('GET', '/settings/address')
    .then(({ host }) => host || location.hostname)
    .catch(() => location.hostname);
  return hostPromise;
}

export function refreshPublicHost() { hostPromise = null; }

// address joins host and port the way the game's Direct Connect field expects them.
export function address(host, port) {
  const name = host.includes(':') ? `[${host}]` : host; // IPv6
  return port === 25565 ? name : `${name}:${port}`;
}

// addressCopy returns { el, setPort }: the address as code plus a button that copies it.
// Port 0 renders nothing until setPort.
export function addressCopy(port) {
  let current = port;
  let text = '';
  const code = h('code', { class: 'font-mono text-xs' });
  const button = h('button', {
    class: 'btn btn-ghost p-1 text-muted hover:text-foreground', type: 'button',
    'aria-label': 'Copy address', title: 'Copy address', html: icons.copy,
  });
  button.addEventListener('click', async () => {
    if (!text) return;
    if (await copyText(text)) toast(`Copied ${text}`, 'success');
    else toast('Could not copy. Select the address and copy it by hand.', 'error');
  });
  const el = h('span', { class: 'inline-flex items-center gap-1' }, code, button);

  const render = () => publicHost().then((host) => {
    text = address(host, current);
    code.textContent = text;
  });
  if (current) render();
  return { el, setPort(p) { if (p !== current) { current = p; render(); } } };
}

// copyText uses the Clipboard API, which needs a secure context; a panel browsed over plain
// HTTP (the Tailscale IP) falls back to a hidden textarea and execCommand.
async function copyText(text) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch { /* fall back */ }
  const area = h('textarea', { class: 'sr-only', readonly: true, 'aria-hidden': 'true' });
  area.value = text;
  document.body.append(area);
  area.select();
  try {
    return document.execCommand('copy');
  } catch {
    return false;
  } finally {
    area.remove();
  }
}

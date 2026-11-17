// Players of one server (docs/API.md, "Players"): the whitelist with its on/off switch, banned
// players and banned IPs. A running server applies changes through its console; a stopped
// one has its files edited. The lists always show what is on disk.
import { api } from './api.js';
import { confirmDialog } from './dialog.js';
import { h, toast } from './dom.js';

const SETTLE_MS = 1000; // the game writes its files shortly after a console command

export function mountPlayerManager(root, { id }) {
  const base = `/servers/${id}/players`;
  let running = false;
  let loading = 0; // guards against out-of-order listings
  let settle = null;

  const note = h('p', { class: 'text-xs text-muted', role: 'status' });
  const status = h('p', { class: 'text-xs text-muted', role: 'status' });

  const toggle = h('button', {
    class: 'group relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border border-border-strong bg-surface transition-colors aria-checked:border-primary aria-checked:bg-primary',
    type: 'button', role: 'switch', 'aria-checked': 'false', 'aria-labelledby': 'whitelist-label', disabled: true,
    onclick: () => setWhitelist(toggle.getAttribute('aria-checked') !== 'true'),
  }, h('span', { class: 'inline-block size-4 translate-x-1 rounded-full bg-foreground transition-transform group-aria-checked:translate-x-6' }));

  // One list: add form, rows with a remove button. `cols` renders the row details.
  function section({ title, hint, inputs, addLabel, empty, rowLabel, details, onAdd, onRemove }) {
    const fields = inputs.map(({ name, label, placeholder, required, grow }) => h('input', {
      class: `input ${grow ? 'min-w-0 flex-1' : 'w-44'}`, name, placeholder, required, 'aria-label': label,
      autocomplete: 'off', spellcheck: 'false', maxlength: name === 'reason' ? '200' : '45',
    }));
    const addBtn = h('button', { class: 'btn btn-primary', type: 'submit' }, addLabel);
    const form = h('form', { class: 'mt-3 flex flex-wrap gap-2' }, fields, addBtn);
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const values = Object.fromEntries(fields.map((f) => [f.name, f.value.trim()]));
      addBtn.disabled = true;
      try {
        await onAdd(values);
        form.reset();
        changed();
      } catch (err) {
        toast(err.message, 'error');
        if (err.field) form.querySelector(`[name="${CSS.escape(err.field)}"]`)?.focus();
      } finally {
        addBtn.disabled = false;
      }
    });
    const list = h('ul', { class: 'mt-3 divide-y divide-border' });
    const none = h('p', { class: 'mt-3 text-muted', hidden: true }, empty);
    const el = h('section', { class: 'mt-6 border-t border-border pt-4' },
      h('h2', { class: 'text-sm font-semibold' }, title),
      hint && h('p', { class: 'text-xs text-muted' }, hint),
      form, list, none);
    return {
      el,
      render(items) {
        list.replaceChildren(...items.map((item) => h('li', { class: 'flex items-center justify-between gap-3 py-2' },
          h('div', { class: 'min-w-0' }, details(item)),
          h('button', {
            class: 'btn btn-ghost px-2 py-1 text-xs text-danger', type: 'button',
            'aria-label': `${rowLabel} ${item.name ?? item.ip}`, onclick: () => onRemove(item),
          }, rowLabel))));
        none.hidden = items.length > 0;
      },
    };
  }

  const detail = (main, ...rest) => [
    h('p', { class: 'truncate font-medium' }, main),
    ...rest.filter(Boolean).map((t) => h('p', { class: 'truncate text-xs text-muted' }, t)),
  ];

  const whitelist = section({
    title: 'Allowed players', addLabel: 'Add', empty: 'No players on the whitelist.', rowLabel: 'Remove',
    inputs: [{ name: 'name', label: 'Player name', placeholder: 'Player name', required: true }],
    details: (p) => detail(p.name, p.uuid),
    onAdd: ({ name }) => api('POST', `${base}/whitelist`, { name }),
    onRemove: (p) => send('DELETE', 'whitelist', { name: p.name }, `${p.name} removed from the whitelist.`),
  });
  const bans = section({
    title: 'Banned players', addLabel: 'Ban', empty: 'No banned players.', rowLabel: 'Unban',
    inputs: [
      { name: 'name', label: 'Player name', placeholder: 'Player name', required: true },
      { name: 'reason', label: 'Reason (optional)', placeholder: 'Reason (optional)', grow: true },
    ],
    details: (b) => detail(b.name, b.reason),
    onAdd: ({ name, reason }) => api('POST', `${base}/bans`, { name, reason }),
    onRemove: (b) => send('DELETE', 'bans', { name: b.name }, `${b.name} unbanned.`),
  });
  const ipBans = section({
    title: 'Banned IP addresses', addLabel: 'Ban', empty: 'No banned IP addresses.', rowLabel: 'Unban',
    inputs: [
      { name: 'ip', label: 'IP address', placeholder: 'IP address', required: true },
      { name: 'reason', label: 'Reason (optional)', placeholder: 'Reason (optional)', grow: true },
    ],
    details: (b) => detail(b.ip, b.reason),
    onAdd: ({ ip, reason }) => api('POST', `${base}/ip-bans`, { ip, reason }),
    onRemove: (b) => send('DELETE', 'ip-bans', { ip: b.ip }, `${b.ip} unbanned.`),
  });

  root.replaceChildren(
    h('div', { class: 'flex flex-wrap items-center justify-between gap-3' },
      h('div', { class: 'flex items-center gap-3' },
        toggle,
        h('div', null,
          h('p', { id: 'whitelist-label', class: 'font-medium' }, 'Whitelist'),
          h('p', { class: 'text-xs text-muted' }, 'Only the players below can join while it is on.'))),
      h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => load() }, 'Refresh')),
    note,
    whitelist.el, bans.el, ipBans.el,
    status);

  async function load() {
    const current = ++loading;
    try {
      const out = await api('GET', base);
      if (current !== loading) return;
      toggle.setAttribute('aria-checked', String(out.whitelist.enabled));
      toggle.disabled = false;
      whitelist.render(out.whitelist.players);
      bans.render(out.banned_players);
      ipBans.render(out.banned_ips);
      status.textContent = '';
    } catch (err) {
      if (current !== loading) return;
      status.textContent = `Could not load the player lists: ${err.message}`;
    }
  }

  // After a change: show the files now and, for a running game, again once it has written them.
  function changed() {
    load();
    clearTimeout(settle);
    if (running) settle = setTimeout(load, SETTLE_MS);
  }

  async function send(method, path, body, message) {
    try {
      await api(method, `${base}/${path}`, body);
      toast(message, 'success');
    } catch (err) {
      toast(err.message, 'error');
    }
    changed();
  }

  async function setWhitelist(enabled) {
    if (enabled) {
      const ok = await confirmDialog({
        title: 'Turn the whitelist on?',
        message: 'Players who are not on the list cannot join, and those online are kicked.',
        confirmLabel: 'Turn on',
      });
      if (!ok) return;
    }
    toggle.disabled = true;
    await send('PUT', 'whitelist', { enabled }, `Whitelist turned ${enabled ? 'on' : 'off'}.`);
  }

  function renderNote() {
    note.textContent = running
      ? 'The server is running: changes are applied through its console.'
      : 'The server is stopped: changes are written to its files. Adding players needs the server running when it checks accounts online (online-mode).';
  }
  renderNote();

  load();

  return {
    setRunning(value) {
      if (running === value) return;
      running = value;
      renderNote();
      load();
    },
    refresh: () => load(),
    destroy: () => clearTimeout(settle),
  };
}

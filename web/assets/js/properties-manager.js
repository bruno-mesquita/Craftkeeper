// server.properties as a form. Reads and writes the file through the file API
// (docs/API.md, "Files": GET/PUT /files/content), so writes stay atomic, confined to the
// server directory and guarded by the file version. Only the lines of changed keys are
// rewritten: comments, order and unknown lines are kept as they are.
import { api, ApiError } from './api.js';
import { confirmDialog } from './dialog.js';
import { h, toast } from './dom.js';

const FILE = 'server.properties';

// --- Java .properties format ---------------------------------------------------------

const ESCAPES = { t: '\t', n: '\n', r: '\r', f: '\f' };

function unescape(s) {
  return s.replace(/\\(u[0-9a-fA-F]{4}|.)/g, (_, c) =>
    c.length === 5 ? String.fromCharCode(parseInt(c.slice(1), 16)) : (ESCAPES[c] ?? c));
}

// escapeValue writes a value the way Java's Properties.store does: non-ASCII as \uXXXX,
// which every Minecraft version reads back correctly (e.g. § in the MOTD).
function escapeValue(v) {
  let out = '';
  for (let i = 0; i < v.length; i++) {
    const ch = v[i];
    const code = v.charCodeAt(i);
    if (ch === '\\') out += '\\\\';
    else if (ch === '\n') out += '\\n';
    else if (ch === '\r') out += '\\r';
    else if (ch === '\t') out += '\\t';
    else if (ch === ' ' && i === 0) out += '\\ ';
    else if ('=:#!'.includes(ch)) out += `\\${ch}`;
    else if (code < 0x20 || code > 0x7e) out += `\\u${code.toString(16).toUpperCase().padStart(4, '0')}`;
    else out += ch;
  }
  return out;
}

// parse splits the file into lines, remembering which key each line sets.
function parse(content) {
  const lines = content === '' ? [] : content.split('\n').map((raw) => {
    const bare = raw.endsWith('\r') ? raw.slice(0, -1) : raw;
    const text = bare.trimStart();
    if (text === '' || text[0] === '#' || text[0] === '!') return { raw };
    const m = /^((?:[^\\=:\s]|\\.)*)\s*[=:\s]?\s*(.*)$/.exec(text);
    return { raw, key: unescape(m[1]), value: unescape(m[2]), eol: raw.slice(bare.length) };
  });
  const values = new Map();
  for (const l of lines) if (l.key !== undefined) values.set(l.key, l.value);
  return { lines, values };
}

const escapeKey = (k) => k.replace(/[=:\s#!\\]/g, (c) => `\\${c}`);

// serialize rewrites the lines of changed keys and appends the keys the file lacked.
function serialize(lines, edits) {
  const pending = new Map(edits);
  const out = lines.map((l) => {
    if (l.key === undefined || !edits.has(l.key)) return l.raw;
    pending.delete(l.key);
    return `${escapeKey(l.key)}=${escapeValue(edits.get(l.key))}${l.eol}`;
  });
  if (out.length && out[out.length - 1] === '') out.pop(); // trailing newline
  for (const [k, v] of pending) out.push(`${escapeKey(k)}=${escapeValue(v)}`);
  return `${out.join('\n')}\n`;
}

// --- schema ----------------------------------------------------------------------------

const bool = (key, label, def, description, extra) => ({ key, label, type: 'bool', default: def, description, ...extra });
const int = (key, label, def, min, max, description, extra) => ({ key, label, type: 'int', default: def, min, max, description, ...extra });
const text = (key, label, def, description, extra) => ({ key, label, type: 'text', default: def, description, ...extra });
const choice = (key, label, def, options, description) => ({ key, label, type: 'enum', default: def, options, description });

const PERMISSION_LEVELS = [
  ['1', '1 · Bypass spawn protection'],
  ['2', '2 · Cheat commands and command blocks'],
  ['3', '3 · Multiplayer management (kick, ban, op)'],
  ['4', '4 · Everything, including /stop'],
];

const GROUPS = [
  {
    title: 'Server list',
    hint: 'How the server appears in the multiplayer list.',
    fields: [
      { key: 'motd', label: 'Message of the day', type: 'motd', default: 'A Minecraft Server', description: 'Shown under the server name. Up to two lines; use the buttons to add colors and styles.' },
      int('max-players', 'Max players', '20', 0, 2147483647, 'How many players can be online at the same time.'),
      bool('enable-status', 'Show in server list', 'true', 'When off, the server looks offline in the list but players can still join.'),
      bool('hide-online-players', 'Hide player names', 'false', 'Do not reveal who is online in the server list.'),
    ],
  },
  {
    title: 'Gameplay',
    fields: [
      choice('gamemode', 'Game mode', 'survival', [['survival', 'Survival'], ['creative', 'Creative'], ['adventure', 'Adventure'], ['spectator', 'Spectator']], 'Mode for new players.'),
      bool('force-gamemode', 'Force game mode', 'false', 'Put every player back in the game mode above whenever they join.'),
      choice('difficulty', 'Difficulty', 'easy', [['peaceful', 'Peaceful'], ['easy', 'Easy'], ['normal', 'Normal'], ['hard', 'Hard']], 'Affects mob damage, hunger and more.'),
      bool('hardcore', 'Hardcore', 'false', 'Hard difficulty and players become spectators when they die.'),
      bool('pvp', 'Player vs. player', 'true', 'Players can damage each other. Newer versions moved this to a game rule.'),
      bool('allow-flight', 'Allow flight', 'false', 'Do not kick players who fly in survival (needed by some mods).'),
      bool('allow-nether', 'Allow the Nether', 'true', 'Players can travel to the Nether.'),
      int('spawn-protection', 'Spawn protection radius', '16', 0, 2147483647, 'Blocks around spawn that only operators can change. 0 turns it off.'),
      bool('spawn-monsters', 'Spawn monsters', 'true', 'Hostile mobs can spawn.'),
      bool('spawn-animals', 'Spawn animals', 'true', 'Animals can spawn.'),
      bool('spawn-npcs', 'Spawn villagers', 'true', 'Villagers can spawn.'),
      bool('enable-command-block', 'Command blocks', 'false', 'Command blocks run their commands.'),
      int('player-idle-timeout', 'Idle kick (minutes)', '0', 0, 2147483647, 'Kick players idle for this long. 0 never kicks.'),
    ],
  },
  {
    title: 'World',
    hint: 'Generation settings only affect a world that does not exist yet.',
    fields: [
      text('level-name', 'World folder', 'world', 'Folder of the world. Pointing to a new name creates a new world.'),
      text('level-seed', 'Seed', '', 'Leave empty for a random world.'),
      choice('level-type', 'World type', 'minecraft:normal', [
        ['minecraft:normal', 'Normal'], ['minecraft:flat', 'Superflat'], ['minecraft:large_biomes', 'Large biomes'],
        ['minecraft:amplified', 'Amplified'], ['minecraft:single_biome_surface', 'Single biome'],
      ], 'Terrain generator for a new world.'),
      bool('generate-structures', 'Generate structures', 'true', 'Villages, temples, strongholds and so on.'),
      text('generator-settings', 'Generator settings', '{}', 'JSON options for Superflat and Single biome worlds.', { mono: true }),
      int('max-world-size', 'World border radius', '29999984', 1, 29999984, 'Largest possible world border, in blocks.'),
      text('initial-enabled-packs', 'Enabled data packs', 'vanilla', 'Comma-separated data packs turned on when the world is created.', { mono: true }),
      text('initial-disabled-packs', 'Disabled data packs', '', 'Comma-separated data packs left off when the world is created.', { mono: true }),
    ],
  },
  {
    title: 'Players & security',
    fields: [
      bool('online-mode', 'Verify accounts (online mode)', 'true', 'Only players with a real Minecraft account can join. Turning it off lets anyone use any name.', { warnWhen: 'false' }),
      bool('white-list', 'Whitelist', 'false', 'Only players on the whitelist can join (manage them in the Players tab).'),
      bool('enforce-whitelist', 'Enforce whitelist', 'false', 'Kick players who are not on the whitelist when it is reloaded.'),
      bool('enforce-secure-profile', 'Require signed chat', 'true', 'Players must have a Mojang-signed key to join.'),
      bool('prevent-proxy-connections', 'Block proxies', 'false', 'Kick players whose IP does not match the one Mojang saw.'),
      choice('op-permission-level', 'Operator level', '4', PERMISSION_LEVELS, 'What /op gives a player.'),
      choice('function-permission-level', 'Function level', '2', PERMISSION_LEVELS, 'Permission of commands run by functions.'),
      bool('accepts-transfers', 'Accept transfers', 'false', 'Accept players sent here by another server with /transfer.'),
    ],
  },
  {
    title: 'Performance',
    fields: [
      int('view-distance', 'View distance (chunks)', '10', 3, 32, 'How far players see. Lower uses less CPU and RAM.'),
      int('simulation-distance', 'Simulation distance (chunks)', '10', 3, 32, 'How far from players the world keeps running (mobs, crops, redstone).'),
      int('max-tick-time', 'Watchdog (ms)', '60000', -1, 9223372036854775807, 'Stop the server if one tick takes longer than this. -1 turns it off.'),
      int('network-compression-threshold', 'Compression threshold (bytes)', '256', -1, 2147483647, 'Compress packets larger than this. -1 turns compression off.'),
      int('entity-broadcast-range-percentage', 'Entity view range (%)', '100', 10, 1000, 'How far away entities are sent to players.'),
      bool('sync-chunk-writes', 'Synchronous chunk writes', 'true', 'Safer against data loss, slower to save.'),
      int('rate-limit', 'Packet rate limit', '0', 0, 2147483647, 'Kick players sending more packets per second. 0 turns it off.'),
      int('max-chained-neighbor-updates', 'Chained block updates', '1000000', -1, 2147483647, 'Limit of consecutive block updates. Negative means no limit.'),
      choice('region-file-compression', 'Region compression', 'deflate', [['deflate', 'Deflate (default)'], ['lz4', 'LZ4 (faster, larger)'], ['none', 'None']], 'How world chunks are compressed on disk.'),
      int('pause-when-empty-seconds', 'Pause when empty (seconds)', '60', 0, 2147483647, 'Pause the world after it has been empty this long. 0 never pauses.'),
      bool('use-native-transport', 'Native networking', 'true', 'Use Linux epoll for networking.'),
    ],
  },
  {
    title: 'Resource pack',
    fields: [
      text('resource-pack', 'Resource pack URL', '', 'Direct download link offered to players when they join.', { mono: true }),
      text('resource-pack-sha1', 'SHA-1', '', 'Checksum of the pack, so players re-download it only when it changes.', { mono: true }),
      text('resource-pack-id', 'Pack ID', '', 'UUID of the pack.', { mono: true }),
      bool('require-resource-pack', 'Require the pack', 'false', 'Players who decline the pack are disconnected.'),
      text('resource-pack-prompt', 'Prompt', '', 'Message shown when the pack is offered (JSON text).'),
    ],
  },
  {
    title: 'Advanced',
    fields: [
      text('server-ip', 'Bind address', '', 'Leave empty: inside the container the server must listen on every address.', { warnWhenNot: '' }),
      bool('log-ips', 'Log IP addresses', 'true', 'Write player IPs to the log when they join.'),
      bool('broadcast-console-to-ops', 'Show console commands to ops', 'true', 'Operators see the output of commands run from the console.'),
      bool('broadcast-rcon-to-ops', 'Show RCON commands to ops', 'true', 'Operators see the output of commands run over RCON.'),
      bool('enable-query', 'GameSpy query', 'false', 'The query port is not published by the container, so this has no effect outside it.'),
      int('query.port', 'Query port', '25565', 1, 65535, 'Port of the query protocol (not published).'),
      bool('enable-jmx-monitoring', 'JMX monitoring', 'false', 'Expose tick time beans over JMX.'),
      bool('enable-code-of-conduct', 'Code of conduct', 'false', 'Show the files in codeofconduct/ to players when they join.'),
      text('text-filtering-config', 'Text filtering config', '', 'Chat filter service configuration.', { mono: true }),
      int('text-filtering-version', 'Text filtering version', '0', 0, 1, 'Format of the text filtering config.'),
      text('bug-report-link', 'Bug report link', '', 'Link shown to players in the pause menu.', { mono: true }),
      text('management-server-host', 'Management API host', 'localhost', 'Host of the server management API.', { mono: true }),
      int('management-server-port', 'Management API port', '0', 0, 65535, 'Port of the server management API (not published). 0 picks one.'),
      bool('management-server-enabled', 'Management API', 'false', 'Turn on the server management API (not published by the container).'),
      bool('management-server-tls-enabled', 'Management API TLS', 'true', 'Serve the management API over TLS.'),
      text('management-server-secret', 'Management API secret', '', 'Secret clients must send to the management API.', { secret: true }),
      text('status-heartbeat-interval', 'Status heartbeat (seconds)', '0', 'Interval of management API status heartbeats. 0 turns them off.', { mono: true }),
    ],
  },
  {
    title: 'Managed by the panel',
    hint: 'The container depends on these values, so they are read-only here.',
    fields: [
      text('server-port', 'Server port', '25565', 'The game always listens on 25565 inside the container; players connect to the panel port shown in the header.', { readonly: true }),
      bool('enable-rcon', 'RCON', 'false', 'The panel sends commands through the console, so RCON is always off.', { readonly: true }),
      text('rcon.port', 'RCON port', '25575', 'Unused while RCON is off.', { readonly: true }),
      text('rcon.password', 'RCON password', '', 'Unused while RCON is off.', { readonly: true, secret: true }),
    ],
  },
];

const KNOWN = new Map(GROUPS.flatMap((g) => g.fields.map((f) => [f.key, f])));

// --- MOTD ----------------------------------------------------------------------------------

const MOTD_COLORS = {
  0: ['Black', '#000000'], 1: ['Dark blue', '#0000AA'], 2: ['Dark green', '#00AA00'], 3: ['Dark aqua', '#00AAAA'],
  4: ['Dark red', '#AA0000'], 5: ['Dark purple', '#AA00AA'], 6: ['Gold', '#FFAA00'], 7: ['Gray', '#AAAAAA'],
  8: ['Dark gray', '#555555'], 9: ['Blue', '#5555FF'], a: ['Green', '#55FF55'], b: ['Aqua', '#55FFFF'],
  c: ['Red', '#FF5555'], d: ['Light purple', '#FF55FF'], e: ['Yellow', '#FFFF55'], f: ['White', '#FFFFFF'],
};
const MOTD_STYLES = [['l', 'B', 'Bold'], ['o', 'I', 'Italic'], ['n', 'U', 'Underline'], ['m', 'S', 'Strikethrough'], ['r', 'R', 'Reset']];

// renderMotd turns § codes into styled spans, as the multiplayer list shows them.
function renderMotd(target, value) {
  const lines = value.split('\n').slice(0, 2);
  target.replaceChildren(...lines.map((line) => {
    const row = h('div', { class: 'min-h-5 truncate' });
    let style = {};
    for (const part of line.split(/(§[0-9a-fk-or])/i)) {
      if (/^§[0-9a-fk-or]$/i.test(part)) {
        const c = part[1].toLowerCase();
        if (MOTD_COLORS[c]) style = { color: MOTD_COLORS[c][1] };
        else if (c === 'r') style = {};
        else style = { ...style, [c]: true };
        continue;
      }
      if (!part) continue;
      const span = h('span', null, part);
      span.style.color = style.color ?? '#AAAAAA';
      if (style.l) span.style.fontWeight = '700';
      if (style.o) span.style.fontStyle = 'italic';
      span.style.textDecoration = [style.n && 'underline', style.m && 'line-through'].filter(Boolean).join(' ') || 'none';
      row.append(span);
    }
    return row;
  }));
}

// --- manager ---------------------------------------------------------------------------------

export function mountPropertiesManager(root, { id, onRestart }) {
  const base = `/servers/${id}/files/content`;
  let running = false;
  let file = null; // { exists, version, lines, values }
  let rows = []; // { def, control, row, original }
  const edits = new Map(); // key → new value, only keys that differ from `original`
  let loading = 0;
  let saving = false;

  const search = h('input', {
    class: 'input max-w-xs', type: 'search', placeholder: 'Search settings…', 'aria-label': 'Search settings',
    autocomplete: 'off', spellcheck: 'false',
  });
  const note = h('p', { class: 'text-xs text-muted', role: 'status' });
  const form = h('div', { class: 'flex flex-col gap-8' });
  const empty = h('p', { class: 'text-muted', hidden: true }, 'No setting matches your search.');
  const status = h('p', { class: 'text-xs text-muted', role: 'status' });

  const count = h('p', { class: 'text-sm font-medium' });
  const discardBtn = h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => discard() }, 'Discard');
  const saveBtn = h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => save(false) }, 'Save');
  const restartBtn = h('button', { class: 'btn btn-primary', type: 'button', onclick: () => save(true) }, 'Save and restart');
  const bar = h('div', {
    class: 'sticky bottom-0 z-10 -mx-5 -mb-5 mt-6 flex flex-wrap items-center justify-between gap-3 rounded-b-xl border-t border-border-strong bg-surface px-5 py-3',
    hidden: true,
  }, count, h('div', { class: 'flex flex-wrap gap-2' }, discardBtn, saveBtn, restartBtn));

  root.replaceChildren(
    h('div', { class: 'flex flex-wrap items-center justify-between gap-3' },
      search,
      h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => refresh(true) }, 'Reload')),
    h('div', { class: 'mt-2 mb-6' }, note),
    form, empty, status, bar);

  search.addEventListener('input', applyFilter);

  // --- controls -----------------------------------------------------------------------

  // Each control exposes { el, get(), set(v) } plus the element that takes the label.
  function control(def, inputId, describedBy) {
    const common = { id: inputId, 'aria-describedby': describedBy, disabled: def.readonly };
    if (def.type === 'bool') {
      const sw = h('button', {
        ...common, type: 'button', role: 'switch', 'aria-checked': 'false',
        class: 'group relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border border-border-strong bg-surface transition-colors aria-checked:border-primary aria-checked:bg-primary',
      }, h('span', { class: 'inline-block size-4 translate-x-1 rounded-full bg-foreground transition-transform group-aria-checked:translate-x-6' }));
      sw.addEventListener('click', () => {
        sw.setAttribute('aria-checked', String(sw.getAttribute('aria-checked') !== 'true'));
        sw.dispatchEvent(new Event('input', { bubbles: true }));
      });
      return {
        el: sw, focus: sw,
        get: () => sw.getAttribute('aria-checked'),
        // Anything but "false" reads as false in the game too; show it as such.
        set: (v) => sw.setAttribute('aria-checked', String(v.trim() === 'true')),
      };
    }
    if (def.type === 'enum') {
      const select = h('select', { ...common, class: 'input sm:w-72' });
      return {
        el: select, focus: select,
        get: () => select.value,
        set(v) {
          const opts = def.options.map(([value, label]) => h('option', { value }, label));
          // Values written in another form (older versions, typos) are kept as they are.
          if (!def.options.some(([value]) => value === v)) opts.push(h('option', { value: v }, `${v || '(empty)'} · custom`));
          select.replaceChildren(...opts);
          select.value = v;
        },
      };
    }
    if (def.type === 'motd') {
      const area = h('textarea', { ...common, class: 'input min-h-16 resize-y font-mono', rows: '2', spellcheck: 'false' });
      const preview = h('div', { class: 'rounded-md border border-border bg-black px-3 py-2 font-mono text-sm', 'aria-hidden': 'true' });
      const insert = (code) => {
        const { selectionStart: s, selectionEnd: e, value } = area;
        area.value = value.slice(0, s) + code + value.slice(e);
        area.setSelectionRange(s + code.length, s + code.length);
        area.focus();
        area.dispatchEvent(new Event('input', { bubbles: true }));
      };
      const swatches = Object.entries(MOTD_COLORS).map(([c, [name, hex]]) => {
        const b = h('button', { type: 'button', class: 'size-5 rounded-sm border border-border-strong', title: name, 'aria-label': `Insert ${name.toLowerCase()} color` });
        b.style.backgroundColor = hex;
        b.addEventListener('click', () => insert(`§${c}`));
        return b;
      });
      const styles = MOTD_STYLES.map(([c, label, name]) => h('button', {
        type: 'button', class: 'btn btn-ghost px-2 py-0.5 font-mono text-xs', title: name, 'aria-label': `Insert ${name.toLowerCase()} style`,
        onclick: () => insert(`§${c}`),
      }, label));
      area.addEventListener('input', () => {
        // The list shows two lines at most.
        const parts = area.value.split('\n');
        if (parts.length > 2) area.value = `${parts[0]}\n${parts.slice(1).join(' ')}`;
        renderMotd(preview, area.value);
      });
      return {
        el: h('div', { class: 'flex flex-col gap-2' },
          area,
          h('div', { class: 'flex flex-wrap items-center gap-1' }, swatches, h('span', { class: 'mx-1 h-5 w-px bg-border' }), styles),
          preview),
        focus: area,
        get: () => area.value,
        set(v) { area.value = v; renderMotd(preview, v); },
      };
    }
    const input = h('input', {
      ...common, class: `input sm:w-72 ${def.mono || def.type === 'int' ? 'font-mono' : ''}`,
      type: def.type === 'int' ? 'number' : (def.secret ? 'password' : 'text'),
      inputmode: def.type === 'int' ? 'numeric' : null, step: def.type === 'int' ? '1' : null,
      min: def.min, max: def.max, autocomplete: 'off', spellcheck: 'false',
    });
    return { el: input, focus: input, get: () => input.value, set: (v) => { input.value = v; } };
  }

  function buildRow(def, i) {
    const present = file.values.has(def.key);
    const original = present ? file.values.get(def.key) : (def.default ?? '');
    const inputId = `prop-${i}`;
    const descId = `prop-${i}-desc`;
    const c = control(def, inputId, descId);
    c.set(original);
    // What the control shows for the file's value (e.g. "TRUE" reads as on): untouched
    // controls compare equal to it, so they never rewrite their line.
    const baseline = c.get();

    const changed = h('span', { class: 'badge text-primary border-primary/40', hidden: true }, 'Changed');
    const warn = h('p', { class: 'text-xs text-warning', hidden: true });
    const reset = h('button', { class: 'btn btn-ghost px-2 py-1 text-xs', type: 'button', hidden: true }, 'Undo');
    const row = h('div', { class: 'grid gap-3 py-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start' },
      h('div', { class: 'min-w-0' },
        h('div', { class: 'flex flex-wrap items-center gap-2' },
          h('label', { class: 'font-medium', for: inputId }, def.label), changed),
        h('p', { id: descId, class: 'text-xs text-muted' },
          def.description ?? 'Not a setting the panel knows; edited as text.',
          !present && !def.readonly && def.description ? ` Not in the file yet: the game uses ${def.default === '' ? 'an empty value' : `"${def.default}"`}.` : ''),
        h('p', { class: 'mt-1 font-mono text-[11px] text-muted/70' }, def.key),
        warn),
      h('div', { class: `flex items-start gap-2 ${def.type === 'motd' ? 'sm:w-[28rem]' : ''}` }, c.el, reset));

    const r = { def, control: c, row, search: `${def.label} ${def.key} ${def.description ?? ''}`.toLowerCase() };
    const update = () => {
      const v = c.get();
      if (v === baseline) edits.delete(def.key);
      else edits.set(def.key, v);
      const dirty = edits.has(def.key);
      changed.hidden = !dirty;
      reset.hidden = !dirty;
      const risky = (def.warnWhen !== undefined && v === def.warnWhen) || (def.warnWhenNot !== undefined && v !== def.warnWhenNot);
      warn.hidden = !risky;
      warn.textContent = risky ? `Careful: ${def.description}` : '';
      renderBar();
    };
    row.addEventListener('input', update);
    row.addEventListener('change', update);
    reset.addEventListener('click', () => { c.set(original); update(); c.focus.focus(); });
    if (edits.has(def.key)) c.set(edits.get(def.key)); // keep unsaved edits across a reload
    update();
    return r;
  }

  function render() {
    rows = [];
    let i = 0;
    const other = [...file.values.keys()].filter((k) => !KNOWN.has(k))
      .map((key) => ({ key, label: key, type: 'text', mono: true }));
    const groups = [...GROUPS.slice(0, -1), { title: 'Other', hint: 'Settings from mods or newer versions that the panel does not describe.', fields: other }, GROUPS.at(-1)];
    form.replaceChildren(...groups.filter((g) => g.fields.length).map((g) => {
      const groupRows = g.fields.map((def) => buildRow(def, i++));
      rows.push(...groupRows);
      return h('section', { class: 'prop-group' },
        h('h2', { class: 'text-sm font-semibold' }, g.title),
        g.hint && h('p', { class: 'text-xs text-muted' }, g.hint),
        h('div', { class: 'mt-1 divide-y divide-border' }, groupRows.map((r) => r.row)));
    }));
    applyFilter();
  }

  function applyFilter() {
    const q = search.value.trim().toLowerCase();
    for (const r of rows) r.row.hidden = Boolean(q) && !r.search.includes(q);
    let any = false;
    for (const section of form.children) {
      const visible = [...section.querySelectorAll(':scope > div > div')].some((el) => !el.hidden);
      section.hidden = !visible;
      any ||= visible;
    }
    empty.hidden = any || rows.length === 0;
  }

  function renderBar() {
    bar.hidden = edits.size === 0;
    count.textContent = `${edits.size} unsaved ${edits.size === 1 ? 'change' : 'changes'}`;
    restartBtn.hidden = !running;
    saveBtn.className = `btn ${running ? 'btn-ghost' : 'btn-primary'}`;
    saveBtn.textContent = running ? 'Save only' : 'Save';
    for (const b of [discardBtn, saveBtn, restartBtn]) b.disabled = saving;
  }

  function renderNote() {
    const parts = [];
    if (file && !file.exists) parts.push('The game creates server.properties on its first start; only the settings you change are written now, the game fills in the rest.');
    parts.push(running
      ? 'The server is running: changes take effect after a restart. Commands such as /whitelist rewrite this file, so restart soon after saving.'
      : 'Changes take effect the next time the server starts.');
    note.textContent = parts.join(' ');
  }

  // --- loading and saving ---------------------------------------------------------------

  async function read() {
    try {
      const out = await api('GET', `${base}?path=${encodeURIComponent(FILE)}`);
      return { exists: true, version: out.version, ...parse(out.content) };
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return { exists: false, version: '', ...parse('') };
      throw err;
    }
  }

  async function load() {
    const current = ++loading;
    try {
      const f = await read();
      if (current !== loading) return;
      file = f;
      status.textContent = '';
      renderNote();
      render();
    } catch (err) {
      if (current !== loading) return;
      status.textContent = `Could not load ${FILE}: ${err.message}`;
    }
  }

  async function refresh(force) {
    if (edits.size && !force) return; // never drop unsaved edits behind the user's back
    if (edits.size) {
      const ok = await confirmDialog({
        title: 'Reload from disk?',
        message: `You have ${edits.size} unsaved ${edits.size === 1 ? 'change' : 'changes'}. They are kept and shown on top of what is on disk now.`,
        confirmLabel: 'Reload',
      });
      if (!ok) return;
    }
    await load();
  }

  async function discard() {
    edits.clear();
    render();
    renderBar();
  }

  function invalid() {
    for (const r of rows) {
      if (!edits.has(r.def.key) || r.def.type !== 'int') continue;
      const v = edits.get(r.def.key).trim();
      const n = Number(v);
      if (!/^-?\d+$/.test(v) || n < r.def.min || n > r.def.max) return { r, msg: `${r.def.label} must be a whole number from ${r.def.min} to ${r.def.max}.` };
    }
    return null;
  }

  async function write(f) {
    return api('PUT', base, { path: FILE, content: serialize(f.lines, edits), version: f.version });
  }

  async function save(restart) {
    const bad = invalid();
    if (bad) {
      search.value = '';
      applyFilter();
      bad.r.control.focus.setAttribute('aria-invalid', 'true');
      bad.r.control.focus.focus();
      toast(bad.msg, 'error');
      return;
    }
    for (const r of rows) r.control.focus.removeAttribute('aria-invalid');
    saving = true;
    renderBar();
    try {
      // A file that did not exist has no version to guard the write; make sure the game did
      // not create it meanwhile, or this partial file would replace the full one.
      let target = file.exists ? file : await read();
      if (!file.exists && target.exists) {
        file = target;
        throw new ApiError(409, 'file_changed', 'conflict');
      }
      try {
        await write(target);
      } catch (err) {
        if (!(err instanceof ApiError && err.code === 'file_changed')) throw err;
        target = await read();
        const ok = await confirmDialog({
          title: `${FILE} changed on disk`,
          message: 'Someone (often the game itself) changed the file after it was loaded. Apply your changes on top of the new version? Other settings keep what is on disk now.',
          confirmLabel: 'Apply my changes',
        });
        if (!ok) {
          file = target;
          render();
          return;
        }
        await write(target);
      }
      const n = edits.size;
      edits.clear();
      toast(`${n} ${n === 1 ? 'setting' : 'settings'} saved.`, 'success');
      await load();
      if (restart) onRestart?.();
    } catch (err) {
      if (err instanceof ApiError && err.code === 'file_changed') {
        toast(`${FILE} was just created by the game. Review the form and save again.`, 'error');
        render();
      } else {
        toast(err.message, 'error');
      }
    } finally {
      saving = false;
      renderBar();
    }
  }

  renderNote();
  load();

  return {
    setRunning(value) {
      if (running === value) return;
      running = value;
      renderNote();
      renderBar();
      refresh(false);
    },
    refresh: () => refresh(false),
  };
}

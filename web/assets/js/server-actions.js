// Server status labels, game version labels, lifecycle actions and limit fields shared by the
// server list and detail pages.
import { api } from './api.js';
import { confirmDialog } from './dialog.js';
import { field, h } from './dom.js';

export const STATUS = {
  running: { label: 'Running', tone: 'text-success border-success/40' },
  starting: { label: 'Starting', tone: 'text-warning border-warning/40' },
  restarting: { label: 'Restarting', tone: 'text-warning border-warning/40' },
  stopped: { label: 'Stopped', tone: 'text-muted' },
  missing: { label: 'Container missing', tone: 'text-danger border-danger/40' },
  unknown: { label: 'Unknown', tone: 'text-warning border-warning/40' },
  creating: { label: 'Creating…', tone: 'text-primary border-primary/40' },
  create_failed: { label: 'Create failed', tone: 'text-danger border-danger/40' },
};

// How often a page re-reads a server that is still being created.
export const CREATING_POLL_MS = 3000;

export const LOADERS = { vanilla: 'Vanilla', fabric: 'Fabric', forge: 'Forge', neoforge: 'NeoForge' };

// gameVersion describes what a server runs, e.g. "Forge 1.20.1 · 47.3.0".
export function gameVersion(s) {
  const version = s.version === 'LATEST' ? 'latest' : s.version;
  return `${LOADERS[s.loader] ?? s.loader} ${version}${s.loader_version ? ` · ${s.loader_version}` : ''}`;
}

export const ACTIVE = new Set(['running', 'starting', 'restarting']);

export const ACTIONS = {
  start: { label: 'Start', busy: 'Starting…' },
  stop: { label: 'Stop', busy: 'Stopping…' },
  restart: { label: 'Restart', busy: 'Restarting…' },
  kill: {
    label: 'Kill',
    busy: 'Killing…',
    confirm: (s) => ({
      title: `Kill ${s.name}?`,
      message: 'The game process is terminated immediately. Unsaved world changes are lost. Prefer Stop.',
      confirmLabel: 'Kill',
      danger: true,
    }),
  },
};

// lifecycleActions lists the actions that make sense for a status, in display order.
export function lifecycleActions(status) {
  if (status === 'stopped') return ['start'];
  if (ACTIVE.has(status)) return ['stop', 'restart', 'kill'];
  return [];
}

// confirmAction asks for confirmation when the action needs it. Resolves to true to proceed.
export async function confirmAction(s, action) {
  const confirm = ACTIONS[action].confirm;
  return !confirm || Boolean(await confirmDialog(confirm(s)));
}

// runAction performs a lifecycle action and resolves to the updated server.
export function runAction(s, action) {
  return api('POST', `/servers/${s.id}/${action}`);
}

// limitFields returns the memory and CPU form fields (bounds as in docs/API.md), filled with the
// server's current limits or the creation defaults.
export function limitFields(s) {
  return [
    field('Memory (MB)', h('input', { class: 'input', name: 'memory_mb', type: 'number', min: '512', max: '16384', step: '256', required: true, value: String(s?.memory_mb ?? 2048) })),
    field('CPU cores', h('input', { class: 'input', name: 'cpu_cores', type: 'number', min: '0.5', step: '0.5', required: true, value: String(s?.cpu_cores ?? 2) })),
  ];
}

// Server status labels and lifecycle actions shared by the server list and detail pages.
import { api } from './api.js';
import { confirmDialog } from './dialog.js';

export const STATUS = {
  running: { label: 'Running', tone: 'text-success border-success/40' },
  starting: { label: 'Starting', tone: 'text-warning border-warning/40' },
  restarting: { label: 'Restarting', tone: 'text-warning border-warning/40' },
  stopped: { label: 'Stopped', tone: 'text-muted' },
  missing: { label: 'Container missing', tone: 'text-danger border-danger/40' },
  unknown: { label: 'Unknown', tone: 'text-warning border-warning/40' },
};

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

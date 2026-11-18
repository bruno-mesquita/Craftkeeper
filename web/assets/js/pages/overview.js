// Dashboard: live host and container metrics (polled; moves to WebSocket in phase 4) and recent events.
import { api } from '../api.js';
import { confirmDialog } from '../dialog.js';
import { h, toast } from '../dom.js';
import { bytes, dateTime, percent, rate } from '../format.js';
import { icons } from '../icons.js';

const STATS_INTERVAL_MS = 3000;
const DASHBOARD_INTERVAL_MS = 30000;

function metricCard(icon, title) {
  const value = h('p', { class: 'font-mono text-3xl font-semibold tabular-nums' }, '—');
  const detail = h('p', { class: 'text-xs text-muted tabular-nums' }, ' ');
  // Width is set through CSSOM (allowed by the CSP), never via a style attribute.
  const bar = h('span', { class: 'w-0' });
  const meter = h('div', { class: 'meter', role: 'progressbar', 'aria-label': title, 'aria-valuemin': '0', 'aria-valuemax': '100' }, bar);
  const card = h('section', { class: 'card flex flex-col gap-3' },
    h('div', { class: 'flex items-center gap-2 text-muted' }, h('span', { html: icons[icon] }), h('h2', { class: 'text-sm font-semibold' }, title)),
    value, meter, detail);
  return {
    card,
    set(v, d, pct) {
      value.textContent = v;
      detail.textContent = d;
      if (pct == null) {
        meter.hidden = true;
        return;
      }
      bar.style.width = `${pct}%`;
      bar.classList.toggle('bg-danger', pct >= 90);
      bar.classList.toggle('bg-warning', pct >= 75 && pct < 90);
      meter.setAttribute('aria-valuenow', pct.toFixed(0));
    },
  };
}

export function mount(root) {
  const cpu = metricCard('cpu', 'CPU');
  const mem = metricCard('memory', 'Memory');
  const disk = metricCard('disk', 'Disk');
  const net = metricCard('network', 'Network');
  const updated = h('p', { class: 'text-xs text-muted', 'aria-live': 'off' });
  const docker = h('span', { class: 'badge' }, 'Docker: checking…');
  const serverCounts = h('a', { class: 'badge hover:bg-surface-hover', href: '#/servers' }, 'Servers: …');
  const events = h('tbody');
  const containers = h('tbody');

  root.replaceChildren(
    h('div', { class: 'mb-6 flex flex-wrap items-center justify-between gap-3' },
      h('h1', null, 'Overview'),
      h('div', { class: 'flex items-center gap-3' }, updated, serverCounts, docker)),
    h('div', { class: 'grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4' }, cpu.card, mem.card, disk.card, net.card),
    h('section', { class: 'card mt-6 overflow-x-auto' },
      h('h2', { class: 'mb-3 font-semibold' }, 'Servers'),
      h('table', { class: 'w-full text-left' },
        h('thead', { class: 'text-xs text-muted' },
          h('tr', null, ['Server', 'State', 'CPU', 'Memory', 'Network', ''].map((c) => h('th', { class: 'pb-2 font-medium', scope: 'col' }, c)))),
        containers)),
    h('section', { class: 'card mt-6 overflow-x-auto' },
      h('h2', { class: 'mb-3 font-semibold' }, 'Recent events'),
      h('table', { class: 'w-full text-left' },
        h('thead', { class: 'text-xs text-muted' },
          h('tr', null, ['Time', 'Action', 'Target', 'IP', 'Result'].map((c) => h('th', { class: 'pb-2 font-medium', scope: 'col' }, c)))),
        events)));

  async function refreshStats() {
    try {
      const s = await api('GET', '/system/stats');
      cpu.set(percent(s.cpu.usage_percent), `${s.cpu.cores} threads`, s.cpu.usage_percent);
      const memPct = (100 * s.memory.used_bytes) / s.memory.total_bytes;
      mem.set(percent(memPct), `${bytes(s.memory.used_bytes)} of ${bytes(s.memory.total_bytes)}`, memPct);
      const diskPct = (100 * s.disk.used_bytes) / s.disk.total_bytes;
      disk.set(percent(diskPct), `${bytes(s.disk.free_bytes)} free of ${bytes(s.disk.total_bytes)}`, diskPct);
      net.set(`↓ ${rate(s.network.rx_bytes_per_second)}`, `↑ ${rate(s.network.tx_bytes_per_second)}`, null);
      updated.textContent = `Updated ${new Date(s.collected_at).toLocaleTimeString()}`;
    } catch (err) {
      updated.textContent = `Metrics unavailable: ${err.message}`;
    }
  }

  const containerMessage = (text) => containers.replaceChildren(
    h('tr', null, h('td', { class: 'py-2 text-muted', colspan: '6' }, text)));

  // An orphan is a panel container whose server no longer exists; remove it from the host.
  async function removeOrphan(c) {
    const ok = await confirmDialog({
      title: `Remove container ${c.name}?`,
      message: 'This container has no server in the panel. It is stopped and removed from Docker; its files on the host are not touched.',
      confirmLabel: 'Remove', danger: true,
    });
    if (!ok) return;
    try {
      await api('DELETE', `/system/containers/${c.id}`);
      toast(`Container ${c.name} removed.`, 'success');
    } catch (err) {
      toast(`Could not remove container: ${err.message}`, 'error');
    }
    refreshContainers();
  }

  async function refreshContainers() {
    try {
      const [{ containers: list }, { servers }] = await Promise.all([
        api('GET', '/system/containers'),
        api('GET', '/servers'),
      ]);
      const names = new Map(servers.map((s) => [s.id, s.name]));
      if (!list.length) {
        containerMessage('No servers yet. Create one on the Servers page.');
        return;
      }
      containers.replaceChildren(...list.map((c) => {
        const running = c.state === 'running';
        const cell = (text, cls = '') => h('td', { class: `py-2 pr-4 whitespace-nowrap tabular-nums ${cls}` }, running ? text : '—');
        return h('tr', { class: 'border-t border-border' },
          h('td', { class: 'py-2 pr-4 font-medium' }, names.has(c.server_id)
            ? h('a', { class: 'hover:underline', href: `#/servers/${c.server_id}` }, names.get(c.server_id))
            : c.name),
          h('td', { class: `py-2 pr-4 ${running ? 'text-success' : 'text-muted'}` }, c.state),
          cell(percent(c.cpu_percent)),
          cell(`${bytes(c.memory.used_bytes)} of ${bytes(c.memory.limit_bytes)}`),
          cell(`↓ ${rate(c.network.rx_bytes_per_second)} ↑ ${rate(c.network.tx_bytes_per_second)}`),
          h('td', { class: 'py-2 text-right' }, names.has(c.server_id) ? null
            : h('button', { class: 'btn btn-danger px-2 py-1', type: 'button', 'aria-label': `Remove container ${c.name}`, onclick: () => removeOrphan(c) }, 'Remove')));
      }));
    } catch (err) {
      containerMessage(err.code === 'docker_unavailable' ? 'Docker unreachable.' : `Server metrics unavailable: ${err.message}`);
    }
  }

  async function refreshDashboard() {
    try {
      const d = await api('GET', '/dashboard');
      docker.textContent = d.docker.reachable ? 'Docker: connected' : 'Docker: unreachable';
      docker.className = `badge ${d.docker.reachable ? 'text-success' : 'text-warning'}`;
      serverCounts.textContent = `Servers: ${d.servers.running} running · ${d.servers.stopped} stopped`;
      events.replaceChildren(...(d.recent_events.length
        ? d.recent_events.map((e) => h('tr', { class: 'border-t border-border' },
          h('td', { class: 'py-2 pr-4 whitespace-nowrap tabular-nums' }, dateTime(e.created_at)),
          h('td', { class: 'py-2 pr-4 font-mono text-xs' }, e.action),
          h('td', { class: 'py-2 pr-4' }, e.target),
          h('td', { class: 'py-2 pr-4 font-mono text-xs text-muted' }, e.ip),
          h('td', { class: `py-2 ${e.success ? 'text-success' : 'text-danger'}` }, e.success ? 'OK' : 'Failed')))
        : [h('tr', null, h('td', { class: 'py-2 text-muted', colspan: '5' }, 'No events yet.'))]));
    } catch {
      docker.textContent = 'Docker: unknown';
    }
  }

  // Poll only while the tab is visible to keep idle cost near zero.
  let timers = [];
  const start = () => {
    stop();
    refreshStats();
    refreshContainers();
    refreshDashboard();
    timers = [
      setInterval(refreshStats, STATS_INTERVAL_MS),
      setInterval(refreshContainers, STATS_INTERVAL_MS),
      setInterval(refreshDashboard, DASHBOARD_INTERVAL_MS),
    ];
  };
  const stop = () => {
    timers.forEach(clearInterval);
    timers = [];
  };
  const onVisibility = () => (document.hidden ? stop() : start());

  document.addEventListener('visibilitychange', onVisibility);
  if (!document.hidden) start();

  return () => {
    stop();
    document.removeEventListener('visibilitychange', onVisibility);
  };
}

// Entry point: decides between setup, login and the authenticated shell, then routes by hash.
import { api, ApiError, setCsrfToken } from './api.js';
import { renderShell } from './shell.js';

const app = document.getElementById('app');

// Route table. Page modules are lazy-loaded and must export mount(container, ctx) -> cleanup.
const routes = {
  overview: { title: 'Overview', load: () => import('./pages/overview.js') },
  servers: { title: 'Servers', load: () => import('./pages/planned.js'), phase: 3 },
  backups: { title: 'Backups', load: () => import('./pages/planned.js'), phase: 6 },
  users: { title: 'Users', load: () => import('./pages/planned.js'), phase: null },
  settings: { title: 'Settings', load: () => import('./pages/settings.js') },
};

let session = null;
let cleanup = null;
let shell = null;
let navigation = 0; // guards against overlapping async route loads

async function boot() {
  try {
    const { required } = await api('GET', '/setup');
    if (required) return showPublicPage('./pages/setup.js');
    session = await api('GET', '/auth/me');
    setCsrfToken(session.csrf_token);
    shell = null;
    route();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return showPublicPage('./pages/login.js');
    app.replaceChildren(errorView(err));
  }
}

async function showPublicPage(module) {
  teardown();
  shell = null;
  session = null;
  setCsrfToken('');
  const page = await import(module);
  cleanup = page.mount(app, { onDone: boot }) ?? null;
}

async function route() {
  if (!session) return;
  const name = location.hash.replace(/^#\/?/, '') || 'overview';
  const def = routes[name];
  if (!def) {
    location.replace('#/overview');
    return;
  }
  const current = ++navigation;
  teardown();
  shell ??= renderShell(app, { routes, session, onLogout: logout });
  shell.setActive(name);
  document.title = `${def.title} · Mesquita Panel`;
  const page = await def.load();
  if (current !== navigation) return;
  cleanup = page.mount(shell.main, { session, route: def }) ?? null;
  shell.main.focus({ preventScroll: true });
}

function teardown() {
  if (cleanup) cleanup();
  cleanup = null;
}

async function logout() {
  try {
    await api('POST', '/auth/logout');
  } finally {
    boot();
  }
}

function errorView(err) {
  const p = document.createElement('p');
  p.className = 'p-6 text-danger';
  p.setAttribute('role', 'alert');
  p.textContent = `Failed to load the panel: ${err.message}`;
  return p;
}

window.addEventListener('hashchange', route);
window.addEventListener('session-expired', () => {
  if (session) showPublicPage('./pages/login.js');
});

boot();

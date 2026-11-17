// Import a server pack, choose its scripts, then start its one-time installer.
import { api, upload } from './api.js';
import { field, h, toast } from './dom.js';

export function mountModpackManager(root, { id, onInstall }) {
  const status = h('p', { class: 'text-sm text-muted', role: 'status' });
  const progress = h('progress', { class: 'w-full', max: '1', value: '0', hidden: true });
  const file = h('input', { class: 'input', type: 'file', accept: '.zip,application/zip' });
  const importButton = h('button', { class: 'btn btn-primary', type: 'button' }, 'Upload and extract ZIP');
  const installer = h('select', { class: 'input' });
  const launcher = h('select', { class: 'input' });
  const saveButton = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Save scripts');
  const installButton = h('button', { class: 'btn btn-primary', type: 'button', disabled: true }, 'Install modpack');
  const controls = h('div', { class: 'mt-4 grid gap-4 md:grid-cols-2' },
    field('Installation script', installer), field('Startup script', launcher));
  root.replaceChildren(
    h('h2', { class: 'mb-2 text-lg font-semibold' }, 'Modpack installation'),
    h('p', { class: 'mb-4 text-sm text-muted' }, 'Upload the server pack ZIP, select its scripts, then run the installer. Script output appears in the Console tab.'),
    h('p', { class: 'mb-4 text-sm text-warning' }, 'Create this server with the modpack’s exact Minecraft version first. The selected version determines the Java runtime.'),
    h('div', { class: 'flex flex-wrap items-center gap-3' }, file, importButton), progress, status,
    controls, h('div', { class: 'mt-4 flex flex-wrap gap-2' }, saveButton, installButton));

  let configured = false;
  let running = false;
  let lastInfo = null;
  function applyState() {
    if (!lastInfo) return;
    installButton.disabled = !configured || running;
    importButton.disabled = !lastInfo.supported || running;
    file.disabled = !lastInfo.supported || running;
    installer.disabled = running;
    launcher.disabled = running;
    saveButton.disabled = !lastInfo.supported || running || lastInfo.scripts.length === 0;
  }
  async function refresh() {
    try {
      const info = await api('GET', `/servers/${id}/modpack`);
      lastInfo = info;
      const options = info.scripts.map((p) => h('option', { value: p }, p));
      installer.replaceChildren(...options.map((o) => o.cloneNode(true)));
      launcher.replaceChildren(...options.map((o) => o.cloneNode(true)));
      installer.value = info.install_script || guess(info.scripts, /install|run/i);
      launcher.value = info.start_script || guess(info.scripts, /start|init|serverstart/i);
      configured = Boolean(info.supported && info.install_script && info.start_script);
      status.textContent = !info.supported
        ? 'This server was created before modpack support. Create a new server to use this feature.'
        : info.installed
          ? 'Installation completed. Start the server to launch the modpack.'
          : info.scripts.length
            ? 'Choose the installer and startup scripts from the extracted pack.'
            : 'Upload a server pack ZIP to find its scripts.';
      applyState();
      file.disabled = !info.supported || running;
      controls.hidden = !info.supported || info.scripts.length === 0;
      importButton.hidden = !info.supported;
      saveButton.hidden = !info.supported;
      installButton.hidden = !info.supported;
    } catch (err) {
      status.textContent = `Could not load modpack files: ${err.message}`;
    }
  }

  importButton.addEventListener('click', async () => {
    const pack = file.files?.[0];
    if (!pack) { toast('Choose a ZIP file first.', 'error'); return; }
    importButton.disabled = true;
    progress.hidden = false;
    status.textContent = 'Uploading ZIP…';
    try {
      await upload(`/servers/${id}/files/upload?overwrite=false`, [pack], (v) => { progress.value = v; });
      status.textContent = 'Extracting ZIP…';
      await api('POST', `/servers/${id}/files/extract`, { path: pack.name, destination: '', overwrite: false });
      await refresh();
    } catch (err) {
      status.textContent = `Modpack import failed: ${err.message}`;
    } finally {
      importButton.disabled = false;
      progress.hidden = true;
    }
  });

  saveButton.addEventListener('click', async () => {
    try {
      const info = await api('PUT', `/servers/${id}/modpack`, { install_script: installer.value, start_script: launcher.value });
      configured = Boolean(info.install_script && info.start_script);
      applyState();
      status.textContent = 'Scripts saved. Start installation when ready.';
    } catch (err) { status.textContent = `Could not save scripts: ${err.message}`; }
  });

  installButton.addEventListener('click', async () => {
    installButton.disabled = true;
    status.textContent = 'Starting installer… Switch to Console to follow its output.';
    try {
      await api('POST', `/servers/${id}/modpack/install`);
      status.textContent = 'Installation started. Follow progress in Console; the server will launch when installation completes.';
      onInstall?.();
    } catch (err) {
      status.textContent = `Could not start installation: ${err.message}`;
      installButton.disabled = false;
    }
  });

  refresh();
  return { refresh, setRunning(value) { if (running === value) return; running = value; applyState(); } };
}

function guess(scripts, re) {
  return scripts.find((s) => re.test(s.split('/').at(-1))) ?? scripts[0] ?? '';
}

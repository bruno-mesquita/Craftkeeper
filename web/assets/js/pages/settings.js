import { api } from '../api.js';
import { confirmDialog } from '../dialog.js';
import { clearFormError, field, formValues, h, showFormError, toast } from '../dom.js';

export function mount(root, { session }) {
  const button = h('button', { class: 'btn btn-primary self-start', type: 'submit' }, 'Change password');
  const form = h('form', { class: 'flex flex-col gap-4', novalidate: true },
    h('input', { type: 'text', name: 'username', autocomplete: 'username', value: session.user.username, hidden: true }),
    field('Current password', h('input', { class: 'input', name: 'current_password', type: 'password', required: true, autocomplete: 'current-password' })),
    field('New password', h('input', { class: 'input', name: 'new_password', type: 'password', required: true, autocomplete: 'new-password', minlength: '12' })),
    field('Confirm new password', h('input', { class: 'input', name: 'confirm', type: 'password', required: true, autocomplete: 'new-password' })),
    h('p', { class: 'form-error', 'data-error': true, role: 'alert', hidden: true }),
    button);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    clearFormError(form);
    const { current_password, new_password, confirm } = formValues(form);
    if (new_password !== confirm) {
      showFormError(form, { message: 'Passwords do not match.', field: 'confirm' });
      return;
    }
    button.disabled = true;
    try {
      await api('POST', '/auth/password', { current_password, new_password });
      form.reset();
      toast('Password changed. Other sessions were signed out.', 'success');
    } catch (err) {
      showFormError(form, err);
    } finally {
      button.disabled = false;
    }
  });

  root.replaceChildren(
    h('h1', { class: 'mb-6 text-xl font-semibold' }, 'Settings'),
    h('div', { class: 'flex flex-col gap-6' },
      h('section', { class: 'card max-w-md', 'aria-labelledby': 'pw-title' },
        h('h2', { id: 'pw-title', class: 'mb-4 font-semibold' }, 'Password'),
        form),
      curseForgeSection()));
}

// curseForgeSection stores the CurseForge API key that searching and installing CurseForge
// modpacks needs. The panel never shows the key again, only its last characters.
function curseForgeSection() {
  const status = h('p', { class: 'text-sm text-muted', role: 'status' }, 'Loading…');
  const input = h('input', { class: 'input', name: 'api_key', type: 'password', required: true, autocomplete: 'off', maxlength: '128', spellcheck: 'false' });
  const save = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Save key');
  const remove = h('button', { class: 'btn btn-ghost', type: 'button', hidden: true }, 'Remove key');
  const form = h('form', { class: 'flex flex-col gap-4', novalidate: true },
    h('p', { class: 'text-sm text-muted' }, 'Searching and installing CurseForge modpacks needs a free API key from ',
      h('a', { href: 'https://console.curseforge.com/', target: '_blank', rel: 'noopener noreferrer', class: 'text-primary underline' }, 'console.curseforge.com'),
      '. Modrinth needs none.'),
    status,
    field('API key', input),
    h('p', { class: 'form-error', 'data-error': true, role: 'alert', hidden: true }),
    h('div', { class: 'flex gap-2' }, save, remove));

  const show = (s) => {
    status.textContent = s.configured ? `A key ending in …${s.hint} is saved.` : 'No key saved.';
    remove.hidden = !s.configured;
    save.textContent = s.configured ? 'Replace key' : 'Save key';
  };
  api('GET', '/settings/curseforge').then(show).catch((err) => { status.textContent = `Could not load: ${err.message}`; });

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    clearFormError(form);
    save.disabled = true;
    try {
      show(await api('PUT', '/settings/curseforge', { api_key: input.value.trim() }));
      form.reset();
      toast('CurseForge key saved.', 'success');
    } catch (err) {
      showFormError(form, err);
    } finally {
      save.disabled = false;
    }
  });
  remove.addEventListener('click', async () => {
    const ok = await confirmDialog({
      title: 'Remove the CurseForge key?',
      message: 'CurseForge modpacks can no longer be searched or installed. Servers that already run one keep their key until their next pack update.',
      confirmLabel: 'Remove', danger: true,
    });
    if (!ok) return;
    try {
      await api('DELETE', '/settings/curseforge');
      show({ configured: false });
      toast('CurseForge key removed.', 'success');
    } catch (err) {
      toast(err.message, 'error');
    }
  });

  return h('section', { class: 'card max-w-md', 'aria-labelledby': 'cf-title' },
    h('h2', { id: 'cf-title', class: 'mb-4 font-semibold' }, 'CurseForge'),
    form);
}

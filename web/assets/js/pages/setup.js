// First-run administrator setup. Requires the one-time token printed in the service log.
import { api } from '../api.js';
import { clearFormError, field, formValues, h, showFormError, toast } from '../dom.js';
import { authLayout, errorSlot, submitButton } from './auth-layout.js';

export function mount(root, { onDone }) {
  const button = submitButton('Create administrator');
  const form = h('form', { class: 'flex flex-col gap-4', novalidate: true },
    h('p', { class: 'text-muted' },
      'Find the setup token in the service log: ',
      h('code', { class: 'font-mono text-foreground' }, 'journalctl -u craftkeeper')),
    field('Setup token', h('input', { class: 'input font-mono', name: 'token', required: true, autocomplete: 'off', spellcheck: 'false' })),
    field('Username', h('input', { class: 'input', name: 'username', required: true, autocomplete: 'username', minlength: '3', maxlength: '32' })),
    field('Password', h('input', { class: 'input', name: 'password', type: 'password', required: true, autocomplete: 'new-password', minlength: '12' })),
    field('Confirm password', h('input', { class: 'input', name: 'confirm', type: 'password', required: true, autocomplete: 'new-password' })),
    errorSlot(),
    button);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    clearFormError(form);
    const { token, username, password, confirm } = formValues(form);
    if (password !== confirm) {
      showFormError(form, { message: 'Passwords do not match.', field: 'confirm' });
      return;
    }
    button.disabled = true;
    try {
      await api('POST', '/setup', { token, username, password });
      toast('Administrator created. Sign in to continue.', 'success');
      onDone();
    } catch (err) {
      showFormError(form, err);
      button.disabled = false;
    }
  });

  root.replaceChildren(authLayout('Welcome', 'Create the administrator account', form));
  form.querySelector('input').focus();
}

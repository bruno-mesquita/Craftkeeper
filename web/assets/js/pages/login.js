import { api } from '../api.js';
import { clearFormError, field, formValues, h, showFormError } from '../dom.js';
import { authLayout, errorSlot, submitButton } from './auth-layout.js';

export function mount(root, { onDone }) {
  const button = submitButton('Sign in');
  const form = h('form', { class: 'flex flex-col gap-4', novalidate: true },
    field('Username', h('input', { class: 'input', name: 'username', required: true, autocomplete: 'username' })),
    field('Password', h('input', { class: 'input', name: 'password', type: 'password', required: true, autocomplete: 'current-password' })),
    errorSlot(),
    button);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    clearFormError(form);
    button.disabled = true;
    try {
      await api('POST', '/auth/login', formValues(form));
      onDone();
    } catch (err) {
      showFormError(form, err);
      form.elements.password.value = '';
      button.disabled = false;
    }
  });

  root.replaceChildren(authLayout('Mesquita Panel', 'Sign in to continue', form));
  form.elements.username.focus();
}

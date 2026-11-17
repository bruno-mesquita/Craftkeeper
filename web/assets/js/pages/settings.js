import { api } from '../api.js';
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
    h('section', { class: 'card max-w-md', 'aria-labelledby': 'pw-title' },
      h('h2', { id: 'pw-title', class: 'mb-4 font-semibold' }, 'Password'),
      form));
}

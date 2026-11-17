// Centered card layout shared by the setup and login screens.
import { h } from '../dom.js';

export function authLayout(title, subtitle, form) {
  return h('div', { class: 'flex min-h-dvh items-center justify-center p-4' },
    h('main', { class: 'card w-full max-w-sm p-6' },
      h('div', { class: 'mb-6 flex items-center gap-3' },
        h('img', { src: '/assets/icons/logo.svg', alt: '', width: '32', height: '32' }),
        h('div', null,
          h('h1', { class: 'text-lg font-semibold' }, title),
          h('p', { class: 'text-muted' }, subtitle))),
      form));
}

export function submitButton(label) {
  return h('button', { class: 'btn btn-primary w-full', type: 'submit' }, label);
}

export const errorSlot = () => h('p', { class: 'form-error', 'data-error': true, role: 'alert', hidden: true });

// Centered layout shared by the setup and login screens.
import { h } from '../dom.js';

export function authLayout(title, subtitle, form) {
  return h('div', { class: 'flex min-h-dvh items-center justify-center p-4' },
    h('main', { class: 'w-full max-w-sm' },
      h('img', { src: '/assets/icons/logo.svg', alt: '', width: '56', height: '56', class: 'mb-8' }),
      h('h1', { class: 'text-4xl' }, title),
      h('p', { class: 'mt-2 mb-8 text-muted' }, subtitle),
      h('div', { class: 'card' }, form)));
}

export function submitButton(label) {
  return h('button', { class: 'btn btn-primary w-full', type: 'submit' }, label);
}

export const errorSlot = () => h('p', { class: 'form-error', 'data-error': true, role: 'alert', hidden: true });

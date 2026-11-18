// Activity: the audit log of every server and of sign-ins, with filters.
import { h } from '../dom.js';
import { mountActivity } from '../activity.js';

export function mount(root) {
  const card = h('section', { class: 'card' });
  root.replaceChildren(h('h1', { class: 'mb-4' }, 'Activity'), card);
  mountActivity(card);
}

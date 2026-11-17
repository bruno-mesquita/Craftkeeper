// Placeholder for sections scheduled in later phases.
import { h } from '../dom.js';

export function mount(root, { route }) {
  const when = route.phase ? `It is planned for phase ${route.phase}.` : 'It is planned for a later phase.';
  root.replaceChildren(
    h('h1', { class: 'mb-4 text-xl font-semibold' }, route.title),
    h('div', { class: 'card text-muted' }, `This section is not available yet. ${when}`));
}

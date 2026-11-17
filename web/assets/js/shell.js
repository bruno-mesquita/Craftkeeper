// Authenticated layout: sidebar navigation + main content area.
import { h } from './dom.js';
import { icons } from './icons.js';

export function renderShell(root, { routes, session, onLogout }) {
  const links = new Map();
  const nav = h('nav', { class: 'flex flex-col gap-1', 'aria-label': 'Main' },
    Object.entries(routes).map(([name, def]) => {
      const a = h('a', { class: 'nav-link', href: `#/${name}` },
        h('span', { html: icons[name] }), def.title);
      links.set(name, a);
      return a;
    }));

  const main = h('main', { class: 'flex-1 overflow-y-auto p-4 md:p-8 outline-none', tabindex: '-1', id: 'main' });

  const sidebar = h('aside', { class: 'flex w-full shrink-0 flex-col gap-6 border-b border-border bg-surface p-4 md:sticky md:top-0 md:h-dvh md:w-60 md:border-r md:border-b-0' },
    h('div', { class: 'flex items-center gap-3 px-1' },
      h('img', { src: '/assets/icons/logo.svg', alt: '', width: '32', height: '32' }),
      h('span', { class: 'text-lg leading-none font-bold tracking-tight' }, 'Mesquita', h('span', { class: 'block pt-1 font-mono text-[0.65rem] font-medium text-muted' }, 'panel'))),
    nav,
    h('div', { class: 'mt-auto flex items-center justify-between gap-2 border-t border-border pt-4' },
      h('span', { class: 'truncate font-medium', title: session.user.username }, session.user.username),
      h('button', { class: 'btn btn-ghost', type: 'button', onclick: onLogout, 'aria-label': 'Sign out' },
        h('span', { html: icons.logout })),
    ));

  root.replaceChildren(
    h('a', { href: '#main', class: 'sr-only focus:not-sr-only focus:absolute focus:p-2' }, 'Skip to content'),
    h('div', { class: 'flex min-h-dvh flex-col md:flex-row' }, sidebar, main),
  );

  return {
    main,
    setActive(name) {
      for (const [n, a] of links) {
        if (n === name) a.setAttribute('aria-current', 'page');
        else a.removeAttribute('aria-current');
      }
    },
  };
}

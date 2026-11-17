// Modal dialogs on the native <dialog> element: showModal() traps focus, Esc closes, and
// focus returns to the opener on close. Content is built with h() (text only, never HTML).
import { h } from './dom.js';

let counter = 0;

// openDialog renders a modal and returns { dialog, close }. The dialog is removed from the
// DOM when it closes (button, Esc or close()); onClose runs once at that point.
export function openDialog({ title, content, onClose }) {
  const titleId = `dlg-title-${++counter}`;
  const dialog = h('dialog', { class: 'dialog', 'aria-labelledby': titleId },
    h('h2', { id: titleId, class: 'mb-4 text-base font-semibold' }, title),
    content);
  dialog.addEventListener('close', () => {
    dialog.remove();
    onClose?.();
  }, { once: true });
  document.body.append(dialog);
  dialog.showModal();
  return { dialog, close: () => dialog.close() };
}

// confirmDialog asks for confirmation. With `checkbox` (a label), the answer also says
// whether it was ticked. Resolves to null when cancelled, else { checked }.
export function confirmDialog({ title, message, confirmLabel = 'Confirm', danger = false, checkbox }) {
  return new Promise((resolve) => {
    let result = null;
    const box = checkbox ? h('input', { type: 'checkbox', class: 'size-4 accent-danger' }) : null;
    const cancel = h('button', { class: 'btn btn-ghost', type: 'button' }, 'Cancel');
    const ok = h('button', { class: `btn ${danger ? 'btn-danger' : 'btn-primary'}`, type: 'button' }, confirmLabel);
    const { close } = openDialog({
      title,
      content: [
        h('p', { class: 'text-muted' }, message),
        box && h('label', { class: 'mt-4 flex items-center gap-2' }, box, checkbox),
        h('div', { class: 'mt-6 flex justify-end gap-2' }, cancel, ok),
      ],
      onClose: () => resolve(result),
    });
    cancel.addEventListener('click', close);
    ok.addEventListener('click', () => {
      result = { checked: box?.checked ?? false };
      close();
    });
    cancel.focus(); // the safe choice is the default for destructive actions
  });
}

// Minimal DOM helpers. Text is always inserted as text nodes (never parsed as HTML);
// `html` is reserved for trusted, static markup such as the icon set.

export function h(tag, props, ...children) {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(props ?? {})) {
    if (value == null || value === false) continue;
    if (key === 'class') el.className = value;
    else if (key === 'html') el.innerHTML = value;
    else if (key.startsWith('on')) el.addEventListener(key.slice(2), value);
    else el.setAttribute(key, value === true ? '' : value);
  }
  for (const child of children.flat()) {
    if (child == null || child === false) continue;
    el.append(child instanceof Node ? child : String(child));
  }
  return el;
}

const toastRoot = () => document.getElementById('toasts');

export function toast(message, kind = 'info') {
  const tone = { info: 'border-border-strong', success: 'border-success/50', error: 'border-danger/50' }[kind];
  const el = h('div', {
    class: `pointer-events-auto rounded-md border ${tone} bg-surface px-4 py-3 text-sm shadow-lg`,
    role: kind === 'error' ? 'alert' : 'status',
  }, message);
  toastRoot().append(el);
  setTimeout(() => el.remove(), 4000);
}

// Reads a form's fields into a plain object.
export function formValues(form) {
  return Object.fromEntries(new FormData(form).entries());
}

// Shows an API error inside a form's [data-error] slot and focuses the offending field.
export function showFormError(form, err) {
  const slot = form.querySelector('[data-error]');
  slot.textContent = err.message;
  slot.hidden = false;
  if (err.field) form.querySelector(`[name="${CSS.escape(err.field)}"]`)?.focus();
}

export function clearFormError(form) {
  const slot = form.querySelector('[data-error]');
  slot.textContent = '';
  slot.hidden = true;
}

export function field(label, input) {
  const id = `f-${input.name}`;
  input.id = id;
  return h('div', { class: 'field' }, h('label', { class: 'label', for: id }, label), input);
}

// Resources tab: memory and CPU limits of a server, changed through PATCH /servers/{id}
// (docs/API.md). The API accepts new limits only while the server is stopped, so the form is
// enabled only in that state; the Java heap follows the memory limit on the next start.
import { api } from './api.js';
import { clearFormError, formValues, h, showFormError, toast } from './dom.js';
import { ACTIVE, limitFields } from './server-actions.js';

export function mountResourcesManager(root, { id, onSaved }) {
  let server = null;
  let saving = false;

  const fields = limitFields();
  const inputs = fields.map((f) => f.querySelector('input'));
  const [memory, cpu] = inputs;
  const note = h('p', { class: 'text-xs text-muted', role: 'status' });
  const saveBtn = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Save');
  const form = h('form', { class: 'flex max-w-md flex-col gap-4', novalidate: true },
    h('div', { class: 'grid grid-cols-2 gap-4' }, ...fields),
    h('p', { class: 'text-xs text-muted' }, 'The Java heap uses 75% of the memory limit. Changes apply on the next start.'),
    note,
    h('p', { class: 'form-error', 'data-error': true, role: 'alert', hidden: true }),
    h('div', { class: 'flex justify-end' }, saveBtn));
  root.replaceChildren(form);

  const editable = () => server?.status === 'stopped';
  const changed = () => server && (Number(memory.value) !== server.memory_mb || Number(cpu.value) !== server.cpu_cores);

  function fill() {
    memory.value = String(server.memory_mb);
    cpu.value = String(server.cpu_cores);
  }

  function render() {
    const on = editable() && !saving;
    for (const i of inputs) i.disabled = !on;
    saveBtn.disabled = !on || !changed();
    saveBtn.textContent = saving ? 'Saving…' : 'Save';
    if (!server || editable()) note.textContent = '';
    else if (ACTIVE.has(server.status)) note.textContent = 'Stop the server to change its memory or CPU limits.';
    else note.textContent = 'Limits can only be changed while the server is stopped.';
  }

  for (const i of inputs) i.addEventListener('input', render);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (!editable() || !changed() || saving) return;
    clearFormError(form);
    const v = formValues(form);
    saving = true;
    render();
    try {
      const view = await api('PATCH', `/servers/${id}`, { memory_mb: Number(v.memory_mb), cpu_cores: Number(v.cpu_cores) });
      server = view;
      fill();
      toast('Limits saved. They apply on the next start.', 'success');
      onSaved?.(view);
      saving = false;
      render();
    } catch (err) {
      saving = false;
      render(); // re-enable the inputs first so the error can focus the offending field
      showFormError(form, err);
    }
  });

  return {
    // setServer follows the page's server view; values the user is editing are kept.
    setServer(next) {
      const dirty = changed();
      server = next;
      if (!dirty) fill();
      render();
    },
  };
}

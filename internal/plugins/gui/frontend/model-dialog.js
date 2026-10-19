// One instance can be opened from any entry point with an explicit profile.
// The native dialog provides focus trapping, Escape and focus restoration.
let nextDialog = 0;
export function createModelDialog(bridge, doc = document) {
  const dialog = doc.createElement('dialog');
  dialog.className = 'model-dialog';
  // Opening focuses the dialog itself, not its first button, which would
  // otherwise wear the focus ring before anything was chosen; Tab reaches it.
  dialog.setAttribute('tabindex', '-1');
  const heading = doc.createElement('h2');
  heading.id = 'model-dialog-title-' + ++nextDialog;
  heading.textContent = 'Modell einstellen';
  dialog.setAttribute('aria-labelledby', heading.id);
  // A button that shows only a symbol; its name is for screen readers and the tooltip.
  const icon = (symbol, label) => {
    const b = doc.createElement('button');
    b.type = 'button';
    b.className = 'dialog-icon';
    b.textContent = symbol;
    b.setAttribute('aria-label', label);
    b.title = label;
    return b;
  };
  // Back is there when the dialog was opened from a page to go back to.
  const header = doc.createElement('div');
  header.className = 'dialog-header';
  const back = icon('‹', 'Zurück');
  const close = icon('×', 'Schließen');
  header.append(back);
  header.append(heading);
  header.append(close);
  const description = doc.createElement('p');
  description.className = 'model-dialog-description';
  const form = doc.createElement('form');
  const accessLabel = doc.createElement('label');
  accessLabel.textContent = 'Zugang';
  const access = doc.createElement('select');
  accessLabel.append(access);
  const modelLabel = doc.createElement('label');
  modelLabel.textContent = 'Modell';
  const model = doc.createElement('select');
  modelLabel.append(model);
  const status = doc.createElement('p');
  status.className = 'model-dialog-status';
  status.setAttribute('role', 'status');
  const error = doc.createElement('p');
  error.className = 'model-dialog-error';
  error.setAttribute('role', 'alert');
  error.hidden = true;
  // Asked when the dialog is left with a change that was not saved.
  const confirm = doc.createElement('div');
  confirm.className = 'model-dialog-confirm';
  confirm.setAttribute('role', 'alertdialog');
  confirm.hidden = true;
  const question = doc.createElement('p');
  question.textContent = 'Die Änderung ist noch nicht gespeichert.';
  const confirmActions = doc.createElement('div');
  confirmActions.className = 'model-dialog-actions';
  const keep = doc.createElement('button');
  keep.type = 'button';
  keep.textContent = 'Weiter bearbeiten';
  const discard = doc.createElement('button');
  discard.type = 'button';
  discard.textContent = 'Verwerfen';
  const saveAndLeave = doc.createElement('button');
  saveAndLeave.type = 'button';
  saveAndLeave.className = 'model-dialog-save';
  saveAndLeave.textContent = 'Speichern';
  confirmActions.append(keep);
  confirmActions.append(discard);
  confirmActions.append(saveAndLeave);
  confirm.append(question);
  confirm.append(confirmActions);
  const actions = doc.createElement('div');
  actions.className = 'model-dialog-actions';
  const save = doc.createElement('button');
  save.type = 'submit';
  save.className = 'model-dialog-save';
  save.textContent = 'Speichern';
  actions.append(save);
  form.append(accessLabel);
  form.append(modelLabel);
  form.append(status);
  form.append(error);
  form.append(confirm);
  form.append(actions);
  dialog.append(header);
  dialog.append(description);
  dialog.append(form);
  doc.body.append(dialog);
  let version = 0;
  let profile = -1;
  let data = null;
  let saving = false;
  // What the dialog showed once loaded, to tell a change from it.
  let shown = null;
  // Where the dialog goes once the owner decided about an unsaved change.
  let leaving = null;
  let goBack = null;
  const option = (select, value, label) => {
    const item = doc.createElement('option');
    item.value = value;
    item.textContent = label;
    select.append(item);
  };
  const fail = (cause) => {
    error.textContent = String(cause);
    error.hidden = false;
  };
  const changed = () => !!shown && (access.value !== shown.access || model.value !== shown.model);
  const ask = (to) => {
    leaving = to;
    confirm.hidden = false;
    actions.hidden = true;
    keep.focus();
  };
  const unask = () => {
    leaving = null;
    confirm.hidden = true;
    actions.hidden = false;
  };
  // Leaving by the X, back or Escape asks first when there is a change.
  const leave = (to) => {
    if (saving) return;
    if (changed()) ask(to);
    else to();
  };
  const closing = () => dialog.close();
  const backing = () => { const to = goBack; dialog.close(); to?.(); };
  const renderModels = () => {
    const entry = data?.accesses.find(item => item.id === access.value);
    model.replaceChildren();
    const models = [...(entry?.models || [])];
    if (access.value === data?.access && data.model && !models.includes(data.model)) models.unshift(data.model);
    for (const name of models) option(model, name, name);
    if (access.value === data?.access) model.value = data.model;
    model.disabled = models.length === 0;
    save.disabled = model.disabled;
    error.hidden = true;
    if (entry?.error) fail('Modellliste konnte nicht vollständig geladen werden: ' + entry.error);
    status.textContent = models.length ? '' : 'Für diesen Zugang sind keine Modelle verfügbar.';
  };
  const lock = (locked) => {
    access.disabled = model.disabled = save.disabled = back.disabled = close.disabled = locked;
    keep.disabled = discard.disabled = saveAndLeave.disabled = locked;
  };
  // Saves what is selected and goes where it was to go after: closed, or back.
  const store = async (then) => {
    if (saving || model.disabled || !data) return;
    const current = version;
    saving = true;
    lock(true);
    status.textContent = 'Modell wird gespeichert …';
    error.hidden = true;
    try {
      await bridge.SetModel(profile, access.value, model.value);
      if (current === version) { shown = null; then(); }
    } catch (cause) {
      if (current === version) {
        unask();
        fail('Modell konnte nicht gespeichert werden: ' + String(cause));
        status.textContent = '';
      }
    } finally {
      saving = false;
      if (current === version) lock(false);
    }
  };
  access.addEventListener('change', renderModels);
  close.addEventListener('click', () => leave(closing));
  back.addEventListener('click', () => leave(backing));
  keep.addEventListener('click', () => { unask(); model.focus(); });
  discard.addEventListener('click', () => { const to = leaving; shown = null; unask(); to?.(); });
  saveAndLeave.addEventListener('click', () => { void store(leaving || closing); });
  dialog.addEventListener('close', () => { version++; unask(); });
  dialog.addEventListener('cancel', event => {
    if (saving) { event.preventDefault(); return; }
    if (!confirm.hidden) { event.preventDefault(); unask(); return; }
    if (changed()) { event.preventDefault(); ask(closing); }
  });
  form.addEventListener('submit', event => {
    event.preventDefault();
    if (save.disabled) return;
    void store(closing);
  });
  return {
    // back, where given, is where the arrow in the header leads.
    async open(index, name = '', backTo = null) {
      if (dialog.open) return;
      profile = index;
      data = null;
      shown = null;
      goBack = backTo;
      back.hidden = !backTo;
      const current = ++version;
      description.textContent = 'Standardmodell für ' + (name || 'dieses Profil') + '. Gilt ab der nächsten Nachricht, auch in anderen Sitzungen.';
      access.replaceChildren();
      model.replaceChildren();
      lock(false);
      access.disabled = model.disabled = save.disabled = true;
      unask();
      error.hidden = true;
      status.textContent = 'Modelle werden geladen …';
      dialog.showModal();
      dialog.focus();
      try {
        const result = await bridge.Models(index);
        if (current !== version) return;
        data = result;
        for (const entry of data.accesses) option(access, entry.id, entry.label);
        if (data.accesses.some(entry => entry.id === data.access)) access.value = data.access;
        access.disabled = data.accesses.length === 0;
        renderModels();
        shown = { access: access.value, model: model.value };
        if (!data.accesses.length) status.textContent = 'Keine Zugänge eingerichtet. Bitte zuerst die Einrichtung in der TUI durchführen.';
      } catch (cause) {
        if (current === version) {
          status.textContent = '';
          fail('Modelle konnten nicht geladen werden: ' + String(cause));
        }
      }
    },
  };
}

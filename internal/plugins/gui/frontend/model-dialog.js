// One instance can be opened from any entry point with an explicit profile.
// The native dialog provides focus trapping, Escape and focus restoration.
let nextDialog = 0;
export function createModelDialog(bridge, doc = document) {
  const dialog = doc.createElement('dialog');
  dialog.className = 'model-dialog';
  const heading = doc.createElement('h2');
  heading.id = 'model-dialog-title-' + ++nextDialog;
  heading.textContent = 'Modell einstellen';
  dialog.setAttribute('aria-labelledby', heading.id);
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
  const actions = doc.createElement('div');
  actions.className = 'model-dialog-actions';
  const cancel = doc.createElement('button');
  cancel.type = 'button';
  cancel.textContent = 'Abbrechen';
  const save = doc.createElement('button');
  save.type = 'submit';
  save.className = 'model-dialog-save';
  save.textContent = 'Speichern';
  actions.append(cancel);
  actions.append(save);
  form.append(accessLabel);
  form.append(modelLabel);
  form.append(status);
  form.append(error);
  form.append(actions);
  dialog.append(heading);
  dialog.append(description);
  dialog.append(form);
  doc.body.append(dialog);
  let version = 0;
  let profile = -1;
  let data = null;
  let saving = false;
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
  access.addEventListener('change', renderModels);
  cancel.addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { version++; });
  dialog.addEventListener('cancel', event => { if (saving) event.preventDefault(); });
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (saving || save.disabled || !data) return;
    const current = version;
    saving = true;
    access.disabled = model.disabled = save.disabled = cancel.disabled = true;
    status.textContent = 'Modell wird gespeichert …';
    error.hidden = true;
    try {
      await bridge.SetModel(profile, access.value, model.value);
      if (current === version) dialog.close();
    } catch (cause) {
      if (current === version) {
        fail('Modell konnte nicht gespeichert werden: ' + String(cause));
        status.textContent = '';
      }
    } finally {
      saving = false;
      if (current === version) access.disabled = model.disabled = save.disabled = cancel.disabled = false;
    }
  });
  return {
    async open(index, name = '') {
      if (dialog.open) return;
      profile = index;
      data = null;
      const current = ++version;
      description.textContent = 'Standardmodell für ' + (name || 'dieses Profil') + '. Gilt ab der nächsten Nachricht, auch in anderen Sitzungen.';
      access.replaceChildren();
      model.replaceChildren();
      access.disabled = model.disabled = save.disabled = true;
      cancel.disabled = false;
      error.hidden = true;
      status.textContent = 'Modelle werden geladen …';
      dialog.showModal();
      try {
        const result = await bridge.Models(index);
        if (current !== version) return;
        data = result;
        for (const entry of data.accesses) option(access, entry.id, entry.label);
        if (data.accesses.some(entry => entry.id === data.access)) access.value = data.access;
        access.disabled = data.accesses.length === 0;
        renderModels();
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

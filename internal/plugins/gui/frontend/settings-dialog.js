import { createThemePreference } from './theme.js';
import { mountSpeechSettings } from './speech-dialog.js';

// Profile is captured on open; changing the main profile picker cannot redirect a save.
export function createSettingsDialog(bridge, modelDialog, doc = document, theme = createThemePreference(doc)) {
  const dialog = doc.createElement('dialog');
  dialog.className = 'model-dialog settings-dialog';
  dialog.setAttribute('aria-label', 'Settings');
  // Opening a page focuses the dialog itself, not its first button, which
  // would otherwise wear the focus ring before anything was chosen, and the
  // focus is not lost with the button of the page before; Tab reaches them.
  dialog.setAttribute('tabindex', '-1');
  doc.body.append(dialog);
  let profile = -1, name = '', version = 0, busy = false;
  let secrets = [], disposePage = () => {};
  const clearPassword = () => { for (const input of secrets) input.value = ''; };
  dialog.addEventListener('close', () => { disposePage(); clearPassword(); version++; });
  dialog.addEventListener('cancel', event => { clearPassword(); if (busy) event.preventDefault(); });
  const el = (tag, text = '') => { const e = doc.createElement(tag); e.textContent = text; return e; };
  const button = (text, run) => {
    const b = el('button', text); b.type = 'button';
    b.addEventListener('click', run); return b;
  };
  // A button that shows only a symbol; its name is for screen readers and the tooltip.
  const icon = (symbol, label, run) => {
    const b = button(symbol, () => { if (!busy) run(); });
    b.className = 'dialog-icon';
    b.setAttribute('aria-label', label); b.title = label;
    return b;
  };
  // Every page has a header: back to the page before where there is one,
  // its title, and the close button in the corner.
  const page = (title, back) => {
    disposePage(); disposePage = () => {};
    version++;
    clearPassword(); secrets = [];
    const header = el('div'); header.className = 'dialog-header';
    if (back) header.append(icon('‹', 'Back', back));
    header.append(el('h2', title));
    header.append(icon('×', 'Close', () => dialog.close()));
    dialog.replaceChildren(header);
    if (dialog.open) dialog.focus();
  };
  const home = () => {
    page('Settings · ' + name);
    dialog.append(button('Model', () => { dialog.close(); modelDialog.open(profile, name, () => show(profile, name)); }));
    dialog.append(button('Gateways', gateways));
    dialog.append(button('Speech recognition', () => {
      page('Settings › Speech recognition', home);
      disposePage = mountSpeechSettings(bridge, profile, dialog, doc, doc.defaultView?.setTimeout ? doc.defaultView : globalThis);
    }));
    const label = el('label', 'Darstellung');
    const select = el('select');
    for (const [value, text] of [['system', 'System'], ['light', 'Hell'], ['dark', 'Dunkel']]) {
      const option = el('option', text); option.value = value; select.append(option);
    }
    select.value = theme.value;
    select.addEventListener('change', () => theme.set(select.value));
    label.append(select); dialog.append(label);
  };
  const failure = () => {
    // Never reflect backend error strings: they may contain credentials.
    dialog.append(el('p', 'Gateway operation failed. Check the settings and try again.'));
  };
  const gateways = async () => {
    page('Settings › Gateways', home);
    const current = ++version;
    const status = el('p', 'Loading …'); dialog.append(status);
    try {
      const list = await bridge.Gateways(profile);
      if (current !== version || !dialog.open) return;
      status.textContent = list.length ? '' : 'No gateway plugins available for this profile.';
      status.hidden = !status.textContent;
      for (const item of list) dialog.append(button(item.Title || item.Name, () => gateway(item)));
    } catch { if (current === version) { status.textContent = ''; status.hidden = true; failure(); } }
  };
  const gateway = item => {
    page('Settings › Gateways › ' + (item.Title || item.Name), () => { clearPassword(); gateways(); });
    const form = el('form');
    const field = (label, value, type = 'text') => {
      const l = el('label', label), input = el('input');
      input.type = type;
      if (type === 'checkbox') input.checked = !!value; else input.value = value || '';
      l.append(input); form.append(l); return input;
    };
    const enabled = field('Enabled', item.Enabled, 'checkbox');
    const schema = item.Fields?.length ? item.Fields : null;
    const inputs = new Map();
    let homeserver, user, password, users;
    const secret = label => {
      const input = field(label, '', 'password');
      input.autocomplete = 'new-password'; secrets.push(input); return input;
    };
    if (schema) {
      for (const f of schema) {
        const input = f.Kind === 'secret' ? secret(f.Label || f.Key)
          : field(f.Label || f.Key, f.Kind === 'list' ? (f.List || []).join(', ') : f.Kind === 'bool' ? f.On : f.Value,
            f.Kind === 'bool' ? 'checkbox' : 'text');
        inputs.set(f.Key, input);
      }
    } else {
      homeserver = field('Homeserver', item.Homeserver);
      user = field('User ID', item.UserID);
      password = secret('Password (empty keeps current login)');
      users = field('Allowed user IDs (comma separated)', (item.AllowedUsers || []).join(', '));
    }
    form.append(el('p', 'Allowlisted users are delegated this profile’s agent tools, INCLUDING permission answers, not just chat. An empty allowlist denies all users.'));
    const status = el('p', (item.LoggedIn ? 'Logged in' : 'Not logged in') + (item.Status ? ' · ' + item.Status : ''));
    status.setAttribute('role', 'status'); form.append(status);
    const save = el('button', 'Login / Save'); save.type = 'submit'; form.append(save);
    const logout = button('Logout', () => submit(true)); form.append(logout);
    const cancel = button('Cancel', () => { clearPassword(); gateways(); }); form.append(cancel);
    const submit = async loggingOut => {
      if (busy) return;
      const splitList = value => value.split(/[,\n]/).map(s => s.trim()).filter(Boolean);
      const change = { Enabled: enabled.checked, Logout: loggingOut };
      if (schema) {
        change.Values = Object.create(null); change.Lists = Object.create(null);
        for (const f of schema) {
          const input = inputs.get(f.Key);
          if (f.Kind === 'list') change.Lists[f.Key] = splitList(input.value);
          else change.Values[f.Key] = f.Kind === 'bool' ? String(input.checked)
            : f.Kind === 'secret' && loggingOut ? '' : input.value;
        }
      } else Object.assign(change, { Homeserver: homeserver.value, UserID: user.value,
        Password: loggingOut ? '' : password.value, AllowedUsers: splitList(users.value) });
      clearPassword();
      const current = version;
      busy = true; save.disabled = logout.disabled = cancel.disabled = true;
      try {
        await bridge.SaveGateway(profile, item.Name, change);
        if (current === version && dialog.open) await gateways();
      } catch { if (current === version && dialog.open) failure(); }
      finally {
        if (schema) { for (const f of schema) if (f.Kind === 'secret') change.Values[f.Key] = ''; }
        else change.Password = '';
        busy = false; save.disabled = logout.disabled = cancel.disabled = false;
      }
    };
    form.addEventListener('submit', event => { event.preventDefault(); submit(false); });
    dialog.append(form);
  };
  const show = (index, label = '') => {
    if (dialog.open) return;
    profile = index; name = label; version++; home(); dialog.showModal(); dialog.focus();
  };
  return { open: show };
}

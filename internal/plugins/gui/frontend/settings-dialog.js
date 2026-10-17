// Profile is captured on open; changing the main profile picker cannot redirect a save.
export function createSettingsDialog(bridge, modelDialog, doc = document, timers = globalThis) {
  const dialog = doc.createElement('dialog');
  dialog.className = 'model-dialog';
  dialog.setAttribute('aria-label', 'Settings');
  doc.body.append(dialog);
  let profile = -1, name = '', version = 0, busy = false;
  let secrets = [], pollTimer;
  const stopPolling = () => { timers.clearTimeout(pollTimer); pollTimer = undefined; };
  const clearPassword = () => { for (const input of secrets) input.value = ''; };
  dialog.addEventListener('close', () => { stopPolling(); clearPassword(); version++; });
  dialog.addEventListener('cancel', event => { clearPassword(); if (busy) event.preventDefault(); });
  const el = (tag, text = '') => { const e = doc.createElement(tag); e.textContent = text; return e; };
  const button = (text, run) => {
    const b = el('button', text); b.type = 'button';
    b.addEventListener('click', run); return b;
  };
  const page = title => {
    stopPolling();
    version++;
    clearPassword(); secrets = [];
    dialog.replaceChildren(el('h2', title));
  };
  const home = () => {
    page('Settings · ' + name);
    dialog.append(button('Model', () => { dialog.close(); modelDialog.open(profile, name); }));
    dialog.append(button('Gateways', gateways));
    dialog.append(button('Close', () => dialog.close()));
  };
  const failure = () => {
    // Never reflect backend error strings: they may contain credentials.
    dialog.append(el('p', 'Gateway operation failed. Check the settings and try again.'));
  };
  const gateways = async () => {
    page('Settings › Gateways');
    dialog.append(button('Back', home));
    const current = ++version;
    const status = el('p', 'Loading …'); dialog.append(status);
    try {
      const list = await bridge.Gateways(profile);
      if (current !== version || !dialog.open) return;
      status.textContent = list.length ? '' : 'No gateway plugins available for this profile.';
      for (const item of list) dialog.append(button(item.Title || item.Name, () => gateway(item)));
    } catch { if (current === version) { status.textContent = ''; failure(); } }
  };
  const gateway = item => {
    page('Settings › Gateways › ' + (item.Title || item.Name));
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
    if (item.Verification) {
      const current = version, selectedProfile = profile;
      const active = () => current === version && dialog.open;
      const panel = el('section'); panel.setAttribute('aria-label', 'Device verification');
      const publicStatus = el('p'); publicStatus.setAttribute('role', 'status');
      const device = el('p'), transaction = el('p'), emojis = el('p');
      const hint = el('p', 'Compare every emoji with your other Matrix client. Confirm only if all emojis match.');
      const actionStatus = el('p'); actionStatus.setAttribute('role', 'status');
      let verification = item.Verification, pending = null, pendingAction = '', actionError = '', sending = false, refreshing = false;
      const key = v => JSON.stringify([v.Stage, v.TransactionID]);
      const render = () => {
        const v = verification;
        publicStatus.textContent = 'Verification: ' + (v.Stage || 'idle') + (v.Message ? ' · ' + v.Message : '');
        device.textContent = 'Device: ' + (v.DeviceID || '—');
        transaction.textContent = 'Transaction: ' + (v.TransactionID || '—');
        emojis.textContent = (v.Emojis || []).join(' ');
        const inProgress = ['requested', 'ready', 'comparing', 'confirmed'].includes(v.Stage);
        start.disabled = busy || sending || !!pending || !['idle', 'done', 'cancelled', 'failed'].includes(v.Stage || 'idle');
        confirm.hidden = v.Stage !== 'comparing';
        confirm.disabled = busy || sending || !!pending || v.Stage !== 'comparing' || !v.TransactionID || v.Emojis?.length !== 7;
        cancelVerification.disabled = busy || sending || (pending && pendingAction === 'cancel') || !inProgress || !v.TransactionID;
      };
      const refresh = async () => {
        if (!active() || refreshing) return;
        refreshing = true; refreshButton.disabled = true;
        try {
          const list = await bridge.Gateways(selectedProfile);
          if (!active()) return;
          const latest = list.find(g => g.Name === item.Name);
          if (!latest?.Verification) throw new Error('Verification unavailable');
          verification = latest.Verification;
          status.textContent = (latest.LoggedIn ? 'Logged in' : 'Not logged in') + (latest.Status ? ' · ' + latest.Status : '');
          if (pending && !sending && key(verification) !== pending) pending = null;
          actionStatus.textContent = pending ? 'Verification action pending …' : actionError;
          render();
        } catch {
          if (active()) actionStatus.textContent = 'Could not refresh verification status. Retrying …';
        } finally {
          refreshing = false; refreshButton.disabled = false;
          if (active()) { stopPolling(); pollTimer = timers.setTimeout(refresh, 1500); }
        }
      };
      const act = async action => {
        if (!active() || busy || sending || (pending && (action !== 'cancel' || pendingAction === 'cancel'))) return;
        const v = verification;
        if (action === 'confirm' && (v.Stage !== 'comparing' || !v.TransactionID || v.Emojis?.length !== 7)) return;
        if (action === 'cancel' && (!['requested', 'ready', 'comparing', 'confirmed'].includes(v.Stage) || !v.TransactionID)) return;
        if (action === 'start' && !['idle', 'done', 'cancelled', 'failed'].includes(v.Stage || 'idle')) return;
        // Send only an action, never the unsaved form or its credentials. The ID is
        // from the currently displayed SAS, not a later, separately fetched state.
        const change = { VerificationAction: action, VerificationTransactionID: action === 'start' ? '' : v.TransactionID || '' };
        pending = key(v); pendingAction = action; actionError = ''; sending = true; busy = true;
        save.disabled = logout.disabled = cancel.disabled = true;
        actionStatus.textContent = 'Verification action pending …'; render();
        try {
          await bridge.SaveGateway(selectedProfile, item.Name, change);
        } catch {
          if (active()) { pending = null; actionError = 'Verification action failed. Refresh status before trying again.'; actionStatus.textContent = actionError; }
        } finally {
          sending = false; busy = false;
          save.disabled = logout.disabled = cancel.disabled = false;
          if (active()) { render(); refresh(); }
        }
      };
      const start = button('Start verification', () => act('start'));
      const confirm = button('Emojis match', () => act('confirm'));
      const cancelVerification = button('Cancel verification', () => act('cancel'));
      const refreshButton = button('Refresh verification status', refresh);
      for (const node of [el('h3', 'Device verification'), publicStatus, device, transaction, emojis, hint,
        start, confirm, cancelVerification, refreshButton, actionStatus]) panel.append(node);
      form.append(panel);
      render(); pollTimer = timers.setTimeout(refresh, 1500);
    }
  };
  return { open(index, label = '') {
    if (dialog.open) return;
    profile = index; name = label; version++; home(); dialog.showModal();
  } };
}

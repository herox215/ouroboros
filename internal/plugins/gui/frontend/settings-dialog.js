import { createThemePreference } from './theme.js';
import { t } from './i18n.js';

// A server that was switched on or signed in to connects in the background, so
// the MCP page reads the list again ever later, and shows that it connects.
const mcpConnectDelays = [1000, 2000, 3000];

// Profile is captured on open; changing the main profile picker cannot redirect a save.
export function createSettingsDialog(bridge, modelDialog, doc = document, theme = createThemePreference(doc), retry = mcpConnectDelays) {
  const dialog = doc.createElement('dialog');
  dialog.className = 'model-dialog settings-dialog';
  dialog.setAttribute('aria-label', t('settings.title'));
  // Opening a page focuses the dialog itself, not its first button, which
  // would otherwise wear the focus ring before anything was chosen, and the
  // focus is not lost with the button of the page before; Tab reaches them.
  dialog.setAttribute('tabindex', '-1');
  doc.body.append(dialog);
  let profile = -1, name = '', version = 0, busy = false;
  let secrets = [];
  const clearPassword = () => { for (const input of secrets) input.value = ''; };
  dialog.addEventListener('close', () => { clearPassword(); version++; });
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
    version++;
    clearPassword(); secrets = [];
    const header = el('div'); header.className = 'dialog-header';
    if (back) header.append(icon('‹', t('dialog.back'), back));
    header.append(el('h2', title));
    header.append(icon('×', t('dialog.close'), () => dialog.close()));
    dialog.replaceChildren(header);
    if (dialog.open) dialog.focus();
  };
  const home = () => {
    page(t('settings.title') + ' · ' + name);
    dialog.append(button(t('settings.model'), () => { dialog.close(); modelDialog.open(profile, name, () => show(profile, name)); }));
    dialog.append(button(t('settings.gateways'), gateways));
    dialog.append(button(t('settings.mcp'), mcpServers));
    dialog.append(button(t('settings.memory'), memories));
    dialog.append(button(t('settings.thread'), threadWindow));
    const label = el('label', t('settings.appearance'));
    const select = el('select');
    for (const [value, text] of [['system', t('settings.system')], ['light', t('settings.light')], ['dark', t('settings.dark')]]) {
      const option = el('option', text); option.value = value; select.append(option);
    }
    select.value = theme.value;
    select.addEventListener('change', () => theme.set(select.value));
    label.append(select); dialog.append(label);
  };
  const failure = () => {
    // Never reflect backend error strings: they may contain credentials.
    dialog.append(el('p', t('settings.gatewayFailed')));
  };
  // The archivist revisits the memories: its interval, and the changes it
  // made, each of which can be taken back here.
  const memories = async () => {
    page(t('settings.title') + ' › ' + t('settings.memory'), home);
    const current = ++version;
    const form = el('form');
    const interval = el('label', t('settings.memoryInterval'));
    const input = el('input');
    input.type = 'text'; input.placeholder = '24h';
    interval.append(input); form.append(interval);
    form.append(el('p', t('settings.memoryHint')));
    const save = el('button', t('settings.save')); save.type = 'submit'; form.append(save);
    const status = el('p', ''); status.setAttribute('role', 'status'); form.append(status);
    const list = el('div'); form.append(list);
    const load = async () => {
      try {
        const value = await bridge.MemoryInterval(profile);
        if (current !== version || !dialog.open) return;
        input.value = value === 'off' ? '' : value;
        const changes = await bridge.MemoryChanges(profile, 20) || [];
        if (current !== version || !dialog.open) return;
        list.replaceChildren();
        if (!changes.length) { list.append(el('p', t('settings.noChanges'))); return; }
        for (const change of changes) {
          const line = el('div');
          line.append(el('span', `#${change.id} ${change.action} · ${change.reason}` +
            (change.undone ? ' · ' + t('settings.undone') : '')));
          if (!change.undone) {
            line.append(button(t('settings.undo'), async () => {
              if (busy) return;
              busy = true;
              try { await bridge.UndoMemoryChange(profile, change.id); await load(); }
              catch { if (current === version && dialog.open) status.textContent = t('settings.undoFailed'); }
              finally { busy = false; }
            }));
          }
          list.append(line);
        }
      } catch { if (current === version && dialog.open) status.textContent = t('settings.memoryFailed'); }
    };
    form.addEventListener('submit', async event => {
      event.preventDefault();
      if (busy) return;
      busy = true; save.disabled = true;
      try {
        await bridge.SetMemoryInterval(profile, input.value.trim());
        if (current === version && dialog.open) { status.textContent = t('settings.saved'); await load(); }
      } catch { if (current === version && dialog.open) status.textContent = t('settings.memoryFailed'); }
      finally { busy = false; save.disabled = false; }
    });
    dialog.append(form);
    await load();
  };
  // The thread hands messages to the sessions of the last few hours: how long
  // that is, the owner sets here.
  const threadWindow = async () => {
    page(t('settings.title') + ' › ' + t('settings.thread'), home);
    const current = ++version;
    const form = el('form');
    const label = el('label', t('settings.threadWindow'));
    const input = el('input');
    input.type = 'text'; input.placeholder = '3h';
    label.append(input); form.append(label);
    form.append(el('p', t('settings.threadHint')));
    const save = el('button', t('settings.save')); save.type = 'submit'; form.append(save);
    const status = el('p', ''); status.setAttribute('role', 'status'); form.append(status);
    const load = async () => {
      try {
        const value = await bridge.ThreadWindow(profile);
        if (current !== version || !dialog.open) return;
        input.value = value;
      } catch { if (current === version && dialog.open) status.textContent = t('settings.threadFailed'); }
    };
    form.addEventListener('submit', async event => {
      event.preventDefault();
      if (busy) return;
      busy = true; save.disabled = true;
      try {
        await bridge.SetThreadWindow(profile, input.value.trim());
        if (current === version && dialog.open) { status.textContent = t('settings.saved'); await load(); }
      } catch { if (current === version && dialog.open) status.textContent = t('settings.threadFailed'); }
      finally { busy = false; save.disabled = false; }
    });
    dialog.append(form);
    await load();
  };
  const gateways = async () => {
    page(t('settings.title') + ' › ' + t('settings.gateways'), home);
    const current = ++version;
    const status = el('p', t('dialog.loading')); dialog.append(status);
    try {
      const list = await bridge.Gateways(profile);
      if (current !== version || !dialog.open) return;
      status.textContent = list.length ? '' : t('settings.noGateways');
      status.hidden = !status.textContent;
      for (const item of list) dialog.append(button(item.Title || item.Name, () => gateway(item)));
    } catch { if (current === version) { status.textContent = ''; status.hidden = true; failure(); } }
  };
  const gateway = item => {
    page(t('settings.title') + ' › ' + t('settings.gateways') + ' › ' + (item.Title || item.Name), () => { clearPassword(); gateways(); });
    const form = el('form');
    const field = (label, value, type = 'text') => {
      const l = el('label', label), input = el('input');
      input.type = type;
      if (type === 'checkbox') input.checked = !!value; else input.value = value || '';
      l.append(input); form.append(l); return input;
    };
    const enabled = field(t('settings.enabled'), item.Enabled, 'checkbox');
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
      homeserver = field(t('settings.homeserver'), item.Homeserver);
      user = field(t('settings.user'), item.UserID);
      password = secret(t('settings.password'));
      users = field(t('settings.allowedUsers'), (item.AllowedUsers || []).join(', '));
    }
    form.append(el('p', t('settings.allowlistWarning')));
    const status = el('p', (item.LoggedIn ? t('settings.loggedIn') : t('settings.loggedOut')) + (item.Status ? ' · ' + item.Status : ''));
    status.setAttribute('role', 'status'); form.append(status);
    const save = el('button', t('settings.loginSave')); save.type = 'submit'; form.append(save);
    const logout = button(t('settings.logout'), () => submit(true)); form.append(logout);
    const cancel = button(t('dialog.cancel'), () => { clearPassword(); gateways(); }); form.append(cancel);
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
  // The MCP servers of the profile: how each is reached and how it is, a
  // switch for the server, the sign-in it may ask for, and below a connected
  // server its tools, each with a switch. A switch holds at once, so there is
  // no save.
  const mcpServers = async () => {
    page(t('settings.title') + ' › ' + t('settings.mcp'), home);
    const current = ++version;
    const status = el('p', t('dialog.loading'));
    status.setAttribute('role', 'status');
    const list = el('div'); list.className = 'mcp-list';
    dialog.append(status, list);
    // A sign-in opens the browser of the owner and waits for the server to
    // answer; the bridge carries it once the MCP plugin offers it, and until
    // then the page names the command that signs in.
    const login = typeof bridge.MCPLogin === 'function', logout = typeof bridge.MCPLogout === 'function';
    // A sign-in takes minutes, so it is told apart from the other calls: the
    // dialog stays closable while it runs, and only its own buttons are off.
    let signing = false;
    // The state of a server in the words of this language only: the plugin's
    // 'failed: …' carries the backend's error, which stays out of the page.
    const states = { connected: 'settings.mcpConnected', disabled: 'settings.mcpDisabled',
      'not connected': 'settings.mcpNotConnected', 'needs authentication': 'settings.mcpNeedsAuthentication' };
    const state = item => !item.Status ? '' : t(states[item.Status] || 'settings.mcpStatusFailed');
    const failed = () => {
      if (current !== version || !dialog.open) return;
      status.textContent = t('settings.mcpFailed'); status.hidden = false;
    };
    // A call holds the page back while it runs, keeps a second press from
    // running it again, and rereads the list after it, so the page shows what
    // the call did.
    const call = async (buttons, name, run, connecting) => {
      if (busy || signing) return;
      busy = true;
      for (const b of buttons) b.disabled = true;
      let done = false;
      try { await run(); done = true; }
      catch { failed(); }
      finally { busy = false; for (const b of buttons) b.disabled = false; }
      if (done) await refresh(name, connecting);
    };
    // The sign-in says that it waits for the browser, and does not set busy:
    // the X and Escape still close the dialog while the browser is open.
    const signIn = async (buttons, name) => {
      if (busy || signing) return;
      signing = true;
      for (const b of buttons) b.disabled = true;
      status.textContent = t('settings.mcpWaiting'); status.hidden = false;
      let done = false;
      try { await bridge.MCPLogin(profile, name); done = true; }
      catch { failed(); }
      finally { signing = false; for (const b of buttons) b.disabled = false; }
      if (done) await refresh(name, true);
    };
    const server = item => {
      const block = el('div'); block.className = 'mcp-server';
      block.append(el('strong', item.Name));
      block.append(el('p', [item.How, state(item)].filter(Boolean).join(' · ')));
      const actions = el('div'); actions.className = 'mcp-actions';
      const on = !item.Disabled, auth = item.Status === 'needs authentication';
      // The buttons of this server, off together while one of its calls runs.
      // Each is named after its server and tool: 'Turn off' alone tells a
      // screen reader nothing on a page of servers.
      const mine = [];
      const named = (text, label, run, connecting) => {
        const b = button(text, () => call(mine, item.Name, run, connecting));
        b.setAttribute('aria-label', label);
        mine.push(b); return b;
      };
      actions.append(named(on ? t('settings.mcpTurnOff') : t('settings.mcpTurnOn'),
        t(on ? 'settings.mcpTurnOffServer' : 'settings.mcpTurnOnServer', { name: item.Name }),
        () => bridge.MCPSetServer(profile, item.Name, !on), !on));
      if (auth && login) {
        const b = button(t('settings.mcpSignIn'), () => signIn(mine, item.Name));
        b.setAttribute('aria-label', t('settings.mcpSignInServer', { name: item.Name }));
        mine.push(b); actions.append(b);
      }
      // What the server signs in with can be dropped while it is switched off
      // too, so the sign-out is there whenever the bridge offers it.
      if (logout) actions.append(named(t('settings.mcpSignOut'),
        t('settings.mcpSignOutServer', { name: item.Name }),
        () => bridge.MCPLogout(profile, item.Name), false));
      block.append(actions);
      if (auth && !login) block.append(el('p', t('settings.mcpSignInHint', { name: item.Name })));
      if (item.Tools?.length) {
        const tools = el('div'); tools.className = 'mcp-tools';
        for (const tool of item.Tools) {
          const line = el('div');
          line.append(el('span', tool.Description ? `${tool.Name} · ${tool.Description}` : tool.Name));
          line.append(named(tool.On ? t('settings.mcpTurnOff') : t('settings.mcpTurnOn'),
            t(tool.On ? 'settings.mcpTurnOffTool' : 'settings.mcpTurnOnTool', { name: item.Name, tool: tool.Name }),
            () => bridge.MCPSetTool(profile, item.Name, tool.Name, !tool.On), false));
          tools.append(line);
        }
        block.append(tools);
      }
      return block;
    };
    const load = async () => {
      let found;
      try { found = await bridge.MCPServers(profile) || []; }
      catch { failed(); list.replaceChildren(); return null; }
      if (current !== version || !dialog.open) return null;
      status.textContent = found.length ? '' : t('settings.noMCP');
      status.hidden = !status.textContent;
      list.replaceChildren(...found.map(server));
      return found;
    };
    // A server that was switched on or signed in to connects in the
    // background, so a read right after it shows it not connected and without
    // tools. The page reads again, ever later, and says that it connects
    // meanwhile, so the tools and the real state appear without another click.
    const refresh = async (name, connecting) => {
      for (let attempt = 0; ; attempt++) {
        const found = await load();
        if (!found || current !== version || !dialog.open) return;
        const item = found.find(s => s.Name === name);
        if (!connecting || attempt === retry.length || !item || item.Status !== 'not connected') return;
        status.textContent = t('settings.mcpConnecting'); status.hidden = false;
        await new Promise(resolve => setTimeout(resolve, retry[attempt]));
        if (current !== version || !dialog.open) return;
      }
    };
    await load();
  };
  const show = (index, label = '') => {
    if (dialog.open) return;
    profile = index; name = label; version++; home(); dialog.showModal(); dialog.focus();
  };
  return { open: show };
}

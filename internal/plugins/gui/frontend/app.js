import { createModelDialog } from './model-dialog.js';
import { createSettingsDialog } from './settings-dialog.js';

// Session text is rendered literally, including questions and tool output.
export function mount(state, doc = document) {
  const picker = doc.getElementById('profile');
  picker.replaceChildren();
  state.profiles.forEach((profile, index) => {
    const option = doc.createElement('option');
    option.value = String(index);
    option.textContent = (index + 1) + ' · ' + profile.name;
    picker.append(option);
  });
  const render = () => {
    const profile = state.profiles[picker.selectedIndex];
    if (!profile) return;
    doc.getElementById('active').textContent = 'Profil: ' + profile.name;
    doc.getElementById('identity').textContent = profile.id || 'Profil noch nicht eingerichtet';
  };
  picker.selectedIndex = state.selected;
  picker.disabled = state.profiles.length < 2;
  picker.addEventListener('change', render);
  render();
}

export function dateGroup(value, now = new Date()) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return 'Ohne Datum';
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const yesterday = new Date(today);
  yesterday.setDate(yesterday.getDate() - 1);
  if (date >= today) return 'Heute';
  if (date >= yesterday) return 'Gestern';
  return 'Älter';
}

const dateFormat = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });
function formatDate(value) {
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? dateFormat.format(date) : '';
}

// Keep very large tool output out of layout until explicitly requested.
function appendText(parent, value, doc) {
  const limit = 8000;
  const text = doc.createElement('pre');
  text.textContent = value.slice(0, limit);
  parent.append(text);
  if (value.length <= limit) return;
  const expand = doc.createElement('button');
  expand.type = 'button';
  expand.className = 'expand-text';
  expand.textContent = 'Vollständigen Text anzeigen (' + value.length.toLocaleString('de-DE') + ' Zeichen)';
  expand.addEventListener('click', () => {
    const open = text.textContent.length > limit;
    text.textContent = open ? value.slice(0, limit) : value;
    expand.textContent = open ? 'Vollständigen Text anzeigen' : 'Text einklappen';
  });
  parent.append(expand);
}

// Session events are the same projection the terminal UI watches: never raw records.
function transcript(bridge, profile, id, content, doc, active, setQuestion) {
  const closeQuestion = () => setQuestion(null);
  const calls = new Map();
  let partial = null;
  const message = (kind, label, text, at) => {
    const item = doc.createElement('section');
    item.className = 'message message-' + kind;
    const heading = doc.createElement('h3');
    const time = formatDate(at);
    heading.textContent = label + (time ? ' · ' + time : '');
    item.append(heading);
    if (text) appendText(item, text, doc);
    content.append(item);
    return item;
  };
  const clearPartial = () => {
    if (partial) partial.item.hidden = true;
    partial = null;
  };
  const apply = (event) => {
    switch (event.kind) {
      case 'message': closeQuestion(); message('user', 'Du', event.text, event.at); break;
      case 'say':
        clearPartial();
        if (event.text?.trim()) message('assistant', 'Assistent', event.text, event.at);
        break;
      case 'answer':
        closeQuestion();
      case 'suggestion':
        clearPartial();
        if (event.text?.trim()) message('assistant', 'Assistent', event.text, event.at);
        break;
      case 'answering':
        if (!partial) {
          const item = message('assistant', 'Assistent · schreibt …', '', event.at);
          const text = doc.createElement('pre');
          item.append(text);
          partial = { item, text };
        }
        partial.text.textContent += event.text || '';
        break;
      case 'step': {
        clearPartial();
        const key = event.call_id || event.tool;
        let call = calls.get(key);
        if (!call || (!event.done && call.done)) {
          const item = message('tool', 'Tool', '', event.at);
          const details = doc.createElement('details');
          details.className = 'tool-call';
          const summary = doc.createElement('summary');
          details.append(summary);
          item.append(details);
          call = { details, summary, done: false, loaded: false, loading: false, note: null };
          calls.set(key, call);
          const ref = event.call_id;
          if (ref) details.addEventListener('toggle', async () => {
            if (!details.open || call.loaded || call.loading || !active()) return;
            call.loading = true;
            if (!call.note) {
              call.note = doc.createElement('p');
              details.append(call.note);
            }
            call.note.textContent = 'Tool-Ergebnis wird geladen …';
            try {
              const result = await bridge.ToolResult(profile, id, ref);
              if (!active()) return;
              if (result.found) {
                if (result.arguments) appendText(details, result.arguments, doc);
                appendText(details, result.content || '(Leeres Ergebnis)', doc);
                call.loaded = true;
              }
              call.note.textContent = result.found ? '' : 'Noch kein gespeichertes Ergebnis. Zum erneuten Laden auf- und zuklappen.';
            } catch (cause) {
              if (active()) call.note.textContent = 'Tool-Ergebnis konnte nicht geladen werden: ' + String(cause) + '. Zum Wiederholen auf- und zuklappen.';
            } finally {
              call.loading = false;
            }
          });
        }
        call.done = event.done;
        call.summary.textContent = (event.text || event.tool || 'Tool') +
          (event.call_id ? ' · Ergebnis aufklappen' : '');
        break;
      }
      case 'asked': {
        clearPartial();
        closeQuestion();
        const question = event.question;
        message('question', event.done ? 'Beantwortet' : 'Frage',
          question?.text || event.text || '', event.at);
        if (!event.done && id.startsWith('owner/')) {
          setQuestion({ ...(question || { text: event.text || '', options: [] }), at: event.at });
        }
        break;
      }
      case 'queued': message('other', 'Warteschlange', event.text || 'Nachricht eingereiht', event.at); break;
      case 'failed':
      case 'canceled':
        closeQuestion();
        clearPartial();
        message('end', 'Status', event.kind === 'canceled' ? 'Antwort gestoppt' : (event.text || 'Antwort fehlgeschlagen'), event.at);
        break;
      case 'task': message('other', 'Aufgabe', event.text, event.at); break;
      case 'thought': break;
    }
  };
  const render = async (events) => {
    for (let i = 0; i < events.length; i++) {
      if (!active()) return;
      apply(events[i]);
      // Yield during large replays, so switching sessions remains responsive.
      if (i % 40 === 39) await new Promise(resolve => setTimeout(resolve, 0));
    }
  };
  return render;
}

export function browse(bridge, doc = document, resume = '') {
  const profiles = doc.getElementById('profile');
  const sessions = doc.getElementById('sessions');
  const search = doc.getElementById('session-search');
  const content = doc.getElementById('content');
  const status = doc.getElementById('session-status');
  const title = doc.getElementById('session-title');
  const meta = doc.getElementById('session-meta');
  const error = doc.getElementById('error');
  const refresh = doc.getElementById('refresh');
  const composer = doc.getElementById('composer');
  const draft = doc.getElementById('draft');
  const send = doc.getElementById('send');
  const sendError = doc.getElementById('send-error');
  const sendStatus = doc.getElementById('send-status');
  const create = doc.getElementById('new-session');
  const drafts = new Map();
  const localEntries = new Map();
  // The composer is sticky: it is pinned to the viewport bottom, so asking the
  // browser to bring it into view moves nothing — it is already in view. The
  // page scrolls, not the transcript, so the page itself is moved to its end.
  const end = () => {
    const page = doc.scrollingElement;
    page.scrollTop = page.scrollHeight;
  };
  const atEnd = () => {
    const page = doc.scrollingElement;
    return page.scrollHeight - page.clientHeight - page.scrollTop <= 48;
  };
  // Whether new output is followed: so at the end of the page, not once the
  // owner scrolls up to read. It is kept from the scroll events rather than
  // measured when output comes, so that a window made smaller at the end does
  // not count as scrolling up. Whoever sends is taken to the end, see submit.
  let following = true;
  const stick = () => {
    following = true;
    end();
  };
  doc.addEventListener('scroll', () => { following = atEnd(); }, { passive: true });
  doc.defaultView?.addEventListener('resize', () => { if (following) end(); });
  let context = null;
  let ready = false;
  const questionPanel = doc.getElementById('question-panel');
  const questionText = doc.getElementById('question-text');
  const questionOptions = doc.getElementById('question-options');
  const questionHint = doc.getElementById('question-hint');
  let asking = null;
  const setQuestion = (question) => {
    if (asking?.buttons.includes(doc.activeElement)) draft.focus();
    asking = question ? { key: JSON.stringify(question), buttons: [] } : null;
    questionPanel.hidden = !question;
    questionOptions.replaceChildren();
    questionText.textContent = question?.text || '';
    if (question) {
      const current = asking;
      for (const option of question.options || []) {
        const button = doc.createElement('button');
        button.type = 'button';
        button.className = 'question-option';
        button.textContent = option + (option === question.recommended ? ' · Empfohlen' : '');
        if (option === question.recommended) button.className += ' recommended';
        button.addEventListener('click', () => {
          if (asking === current && !button.disabled) void submit(option, true);
        });
        current.buttons.push(button);
        questionOptions.append(button);
      }
    }
    updateComposer();
  };
  let creating = false;
  const sessionState = (profile, id) => {
    const key = profile + ':' + id;
    if (!drafts.has(key)) drafts.set(key, { text: '', error: '', pending: false, notice: '' });
    return drafts.get(key);
  };
  const writable = () => ready && context?.id.startsWith('owner/') && !context.state.pending;
  const updateComposer = () => {
    if (asking) {
      const sent = context?.state.sentQuestion === asking.key;
      asking.buttons.forEach(button => { button.disabled = !writable() || sent; });
      questionHint.textContent = sent ? 'Antwort übergeben – der Assistent fährt fort …' :
        'Wähle eine Antwort oder antworte unten in eigenen Worten.';
    }
    draft.disabled = !ready || !context?.id.startsWith('owner/');
    send.disabled = !writable() || !draft.value.trim();
    sendError.textContent = context?.state.error || '';
    sendError.hidden = !sendError.textContent;
    sendStatus.textContent = context?.state.pending ? 'Wird gesendet …' : (context?.state.notice || '');
  };
  const submit = async (text = draft.value, fromOption = false) => {
    if (!writable() || !text.trim()) return;
    // Who sends, also by an option of a question or while an answer runs, is
    // at the end, scrolled up or not: their message and the answer follow.
    stick();
    const target = context;
    const question = asking;
    const state = target.state;
    const original = state.text;
    state.pending = true;
    state.error = '';
    state.notice = '';
    updateComposer();
    try {
      await bridge.SendMessage(target.profile, target.id, text);
      if (question) state.sentQuestion = question.key;
      if (!fromOption && state.text === original) state.text = '';
      state.notice = 'Nachricht übergeben';
    } catch (cause) {
      state.error = 'Nachricht konnte nicht gesendet werden: ' + String(cause);
    } finally {
      state.pending = false;
      if (context?.state === state) {
        draft.value = state.text;
        updateComposer();
      }
    }
  };
  draft.addEventListener('input', () => {
    if (context) context.state.text = draft.value;
    updateComposer();
  });
  composer.addEventListener('submit', event => { event.preventDefault(); void submit(); });
  draft.addEventListener('keydown', event => {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      void submit();
    }
  });
  let generation = 0;
  let selection = 0;
  let entries = [];
  let selected = null;
  const fail = (message, cause) => {
    error.textContent = message + ': ' + String(cause);
    error.hidden = false;
  };
  const renderList = () => {
    sessions.replaceChildren();
    const query = search.value.trim().toLocaleLowerCase('de-DE');
    const groups = new Map();
    for (const entry of entries) {
      if (!((entry.title || '') + ' ' + entry.id).toLocaleLowerCase('de-DE').includes(query)) continue;
      const label = dateGroup(entry.modified);
      if (!groups.has(label)) groups.set(label, []);
      groups.get(label).push(entry);
    }
    for (const [label, items] of groups) {
      const group = doc.createElement('section');
      group.className = 'session-group';
      const heading = doc.createElement('h2');
      heading.textContent = label;
      group.append(heading);
      for (const entry of items) {
        const button = doc.createElement('button');
        button.type = 'button';
        button.className = 'session-item';
        button.setAttribute('aria-current', entry.id === selected ? 'true' : 'false');
        const name = doc.createElement('span');
        name.className = 'session-title';
        name.textContent = entry.title || entry.id;
        const subtitle = doc.createElement('span');
        subtitle.className = 'session-meta';
        subtitle.textContent = (entry.id.startsWith('owner/') ? 'Eigene Session' : 'Peer-Session') +
          (formatDate(entry.modified) ? ' · ' + formatDate(entry.modified) : '');
        button.title = entry.id;
        button.append(name);
        button.append(subtitle);
        button.addEventListener('click', () => { read(entry); });
        group.append(button);
      }
      sessions.append(group);
    }
    if (!groups.size) {
      const empty = doc.createElement('p');
      empty.className = 'list-empty';
      empty.textContent = query ? 'Keine passende Session gefunden.' : 'Noch keine Sessions.';
      sessions.append(empty);
    }
  };
  const read = async (entry) => {
    const current = ++selection;
    const version = generation;
    const profile = profiles.selectedIndex;
    if (selected !== entry.id) {
      selected = entry.id;
      renderList();
    }
    ready = false;
    setQuestion(null);
    context = { profile, id: entry.id, state: sessionState(profile, entry.id) };
    draft.value = context.state.text;
    updateComposer();
    content.replaceChildren();
    error.hidden = true;
    title.textContent = entry.title || entry.id;
    meta.textContent = entry.id + (formatDate(entry.modified) ? ' · ' + formatDate(entry.modified) : '');
    status.textContent = 'Verlauf wird geladen …';
    try {
      const snapshot = await bridge.OpenSession(profile, entry.id);
      const active = () => version === generation && current === selection;
      if (!active()) return;
      const render = transcript(bridge, profile, entry.id, content, doc, active, setQuestion);
      await render(snapshot.events || []);
      if (!active()) return;
      ready = true;
      updateComposer();
      status.textContent = entry.id.startsWith('owner/') ? 'Session · Bereit' : 'Peer-Session · Nur lesen';
      // Start at the end of the transcript after the full replay, following it.
      stick();
      // One outstanding poll per selected chat; the bridge batches the live stream.
      const follow = async () => {
        try {
          while (active() && snapshot.token && entry.id.startsWith('owner/')) {
            const events = await bridge.PollSession(snapshot.token);
            if (!active()) return;
            if (events === null) throw new Error('Session-Verbindung beendet');
            await render(events || []);
            if (active() && following) end();
          }
        } catch (cause) {
          if (active()) {
            ready = false;
            setQuestion(null);
            fail('Verbindung zur Session unterbrochen. Bitte aktualisieren', cause);
          }
        }
      };
      void follow();
    } catch (cause) {
      if (version !== generation || current !== selection) return;
      status.textContent = '';
      fail('Verlauf konnte nicht geladen werden', cause);
    }
  };
  const load = async (reset = false) => {
    const version = ++generation;
    ++selection;
    ready = false;
    context = null;
    setQuestion(null);
    draft.value = '';
    updateComposer();
    creating = false;
    create.disabled = true;
    const previous = reset ? null : (selected || resume);
    selected = null;
    entries = [];
    if (reset) search.value = '';
    sessions.replaceChildren();
    content.replaceChildren();
    error.hidden = true;
    title.textContent = 'Sessions';
    meta.textContent = 'Unterhaltungen des Nodes';
    status.textContent = 'Sessions werden geladen …';
    refresh.disabled = true;
    try {
      const result = await bridge.Sessions(profiles.selectedIndex) || [];
      if (version !== generation) return;
      // Work on a copy; never mutate data owned by the bridge.
      const locals = localEntries.get(profiles.selectedIndex) || new Map();
      for (const entry of result) locals.delete(entry.id);
      entries = [...result, ...locals.values()].sort((a, b) => (Date.parse(b.modified) || 0) - (Date.parse(a.modified) || 0));
      renderList();
      status.textContent = entries.length ? 'Wähle eine Session.' : 'Noch keine Sessions.';
      if (entries.length) await read(entries.find(entry => entry.id === previous) || entries[0]);
    } catch (cause) {
      if (version !== generation) return;
      status.textContent = '';
      fail('Sessions konnten nicht geladen werden', cause);
    } finally {
      if (version === generation) { refresh.disabled = false; create.disabled = false; }
    }
  };
  create.addEventListener('click', async () => {
    if (create.disabled || creating) return;
    const profile = profiles.selectedIndex;
    const version = generation;
    const current = selection;
    creating = true;
    create.disabled = true;
    error.hidden = true;
    try {
      const id = await bridge.NewSession(profile);
      const entry = { id, title: 'Neue Session', modified: new Date().toISOString() };
      if (!localEntries.has(profile)) localEntries.set(profile, new Map());
      localEntries.get(profile).set(id, entry);
      if (version !== generation) return;
      entries = [entry, ...entries.filter(item => item.id !== id)];
      renderList();
      if (current === selection) {
        search.value = '';
        await read(entry);
      }
    } catch (cause) {
      if (version === generation && current === selection) fail('Session konnte nicht erstellt werden', cause);
    } finally {
      if (version === generation) { creating = false; create.disabled = false; }
    }
  });
  profiles.addEventListener('change', () => { load(true); });
  search.addEventListener('input', renderList);
  refresh.addEventListener('click', () => { load(); });
  return load();
}

export async function start(bridge, doc = document) {
  const sidebar = doc.getElementById('sidebar');
  const toggle = doc.getElementById('sidebar-toggle');
  toggle.addEventListener('click', () => {
    sidebar.hidden = !sidebar.hidden;
    doc.body.setAttribute('data-sidebar-collapsed', String(sidebar.hidden));
    toggle.setAttribute('aria-expanded', String(!sidebar.hidden));
    toggle.textContent = sidebar.hidden ? '▶ Sitzungen einblenden' : '◀ Sitzungen ausblenden';
  });
  try {
    const state = await bridge.InitialState();
    mount(state, doc);
    const modelDialog = createModelDialog(bridge, doc);
    const settingsDialog = createSettingsDialog(bridge, modelDialog, doc);
    doc.getElementById('settings').addEventListener('click', () => {
      const index = doc.getElementById('profile').selectedIndex;
      settingsDialog.open(index, state.profiles[index]?.name);
    });
    await browse(bridge, doc, state.resume);
  } catch (error) {
    const message = doc.getElementById('error');
    message.textContent = 'Profile konnten nicht geladen werden: ' + String(error);
    message.hidden = false;
  }
}

if (typeof window !== 'undefined') start(window.go?.gui?.Window);

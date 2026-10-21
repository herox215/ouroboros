import { createModelDialog } from './model-dialog.js';
import { createSettingsDialog } from './settings-dialog.js';
import { createThemePreference } from './theme.js';

// Session text is rendered literally, including questions and tool output.
export function mount(state, doc = document) {
  const picker = doc.getElementById('profile');
  picker.replaceChildren();
  state.profiles.forEach((profile, index) => {
    const option = doc.createElement('option');
    option.value = String(index);
    option.textContent = (index + 1) + ' · ' + profile.name;
    option.setAttribute('data-name', profile.name);
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
const clockFormat = new Intl.DateTimeFormat('de-DE', { hour: '2-digit', minute: '2-digit' });

// The time above a message, as the terminal UI writes it: the clock today, the
// date with it before.
function clock(value, now = new Date()) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '';
  return date.toDateString() === now.toDateString() ? clockFormat.format(date) : dateFormat.format(date);
}
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
// What a transcript tells browse of its parts, where nothing listens.
const quietSections = { observe() {}, changed() {} };

// Messages are headed as in the terminal UI: "you", or the name of the agent.
function transcript(bridge, profile, id, content, doc, active, setQuestion, onEvent, inspect, agent = 'ouroboros', sections = quietSections) {
  const closeQuestion = () => setQuestion(null);
  const calls = new Map();
  let partial = null;
  // Each owner message opens a part of the thread; the sessions the thread
  // started or handed a message to on it belong to that part, as its Handed
  // events tell, see browse.
  let exchange = { at: null, sessions: new Set() };
  const message = (kind, label, text, at) => {
    const item = doc.createElement('section');
    item.className = 'message message-' + kind;
    const heading = doc.createElement('h3');
    const time = clock(at);
    heading.textContent = label + (time ? ' · ' + time : '');
    item.append(heading);
    if (text) appendText(item, text, doc);
    content.append(item);
    item.exchange = exchange;
    sections.observe(item);
    return item;
  };
  const clearPartial = () => {
    if (partial) partial.item.hidden = true;
    partial = null;
  };
  const apply = (event) => {
    onEvent(event);
    switch (event.kind) {
      case 'message': closeQuestion(); exchange = { at: event.at, sessions: new Set() }; message('user', 'you', event.text, event.at); break;
      case 'say':
        clearPartial();
        if (event.text?.trim()) message('assistant', agent, event.text, event.at);
        break;
      case 'answer':
        closeQuestion();
      case 'suggestion':
        clearPartial();
        if (event.text?.trim()) message('assistant', agent, event.text, event.at);
        break;
      case 'answering':
        if (!partial) {
          const item = message('assistant', agent + ' · schreibt …', '', event.at);
          const text = doc.createElement('pre');
          item.append(text);
          partial = { item, text };
        }
        partial.text.textContent += event.text || '';
        break;
      case 'handed':
        if (event.text && !exchange.sessions.has(event.text)) {
          exchange.sessions.add(event.text);
          sections.changed(exchange);
        }
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
            if (!details.open || !active()) return;
            if (call.loaded) { inspect(event.tool, call.result); return; }
            if (call.loading) return;
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
                call.result = result;
                inspect(event.tool, result);
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
      case 'task': message('task', '↳ Aufgabe', event.text, event.at); break;
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

export function browse(bridge, doc = document, resume = '', threadID = '') {
  const profiles = doc.getElementById('profile');
  // The name of the agent of the selected profile, as the terminal UI heads its messages.
  const agentName = () => profiles.children?.[profiles.selectedIndex]?.getAttribute?.('data-name') || 'ouroboros';
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
  const thread = doc.getElementById('thread');
  const back = doc.getElementById('back-to-thread');
  const stop = doc.getElementById('stop');
  const threadEntry = { id: threadID, title: 'Thread' };
  const contextTitle = doc.getElementById('context-title');
  const contextNote = doc.getElementById('context-note');
  const contextDetails = doc.getElementById('context-details');
  let inspecting = null;
  const inspect = (tool, result) => {
    inspecting = { profile: context.profile, id: context.id };
    contextTitle.textContent = '[' + (tool || 'Werkzeug') + ']';
    contextNote.textContent = 'Werkzeug-Ergebnis · ' + context.id;
    contextDetails.replaceChildren();
    if (result.arguments) appendText(contextDetails, result.arguments, doc);
    appendText(contextDetails, result.content || '(Leeres Ergebnis)', doc);
  };
  thread.hidden = !threadID;
  const drafts = new Map();
  const localEntries = new Map();
  // The history scrolls on its own, between the header and the composer, so it
  // is moved to its end, not the page.
  const scroller = doc.getElementById('transcript');
  const end = () => {
    scroller.scrollTop = scroller.scrollHeight;
  };
  // Whether new output is followed: so at the end of the history, not once the
  // owner scrolls up to read. Whoever sends is taken to the end, see submit.
  // It is told by a mark at the end of the history coming into view or going
  // out of it, not by reading the scroll position in each scroll event: such a
  // read, while the history bounces past its end, made WebKit draw a frame
  // without the bounce, so that a slow pull jerked back and forth.
  let following = true;
  const stick = () => {
    following = true;
    end();
  };
  const keep = () => { if (following) end(); };
  const Intersection = doc.defaultView?.IntersectionObserver;
  if (Intersection) {
    // Within 48 px of the end still counts as at the end.
    new Intersection(([mark]) => { following = mark.isIntersecting; pick(); },
      { root: scroller, rootMargin: '0px 0px 48px 0px' }).observe(doc.getElementById('transcript-end'));
    // Older messages fade out at the top only while there are older ones above:
    // at the start of a short thread the first lines stay readable.
    new Intersection(([mark]) => { scroller.classList?.toggle('at-start', mark.isIntersecting); },
      { root: scroller }).observe(doc.getElementById('transcript-start'));
  }
  doc.defaultView?.addEventListener('resize', keep);
  // Beside the thread, the sessions of the part of it in view show on the
  // left and their artefacts on the right, as in the demo: the part whose
  // messages cross the middle of the history, or the last one at its end.
  // Which sessions a part has, the Handed events of the thread tell, see
  // transcript; no scroll position is read for it, see following.
  let section = null, latest = null, seen = 0, soon = false;
  const visible = new Set();
  // A part names a session by its ID or by the handle the thread knows it by.
  const inSection = entry => !!section && [...section.sessions].some(handle => entry.id.slice(entry.id.indexOf('/') + 1).startsWith(handle));
  const show = part => {
    if (!part || part === section) return;
    section = part;
    renderList();
    void artefacts();
  };
  // Both marks decide anew whichever tells first: at the end of the history
  // the newest part is in view, though it may never reach the middle.
  const pick = () => {
    if (following) return show(latest);
    let first = null;
    for (const item of visible) if (!first || item.seq < first.seq) first = item;
    if (first) show(first.exchange);
  };
  const band = Intersection && new Intersection(items => {
    for (const item of items) item.isIntersecting ? visible.add(item.target) : visible.delete(item.target);
    pick();
  }, { root: scroller, rootMargin: '-45% 0px -45% 0px' });
  const sections = {
    observe(item) {
      item.seq = ++seen;
      latest = item.exchange;
      band?.observe(item);
      // At the end, the newest part is the one in view.
      if (soon) return;
      soon = true;
      queueMicrotask(() => { soon = false; if (following) show(latest); });
    },
    changed(part) {
      if (part !== section) return;
      renderList();
      void artefacts();
    },
  };
  // The artefacts of the sessions of the part in view: the last calls of
  // their tools, as the plugins of the tools tell them.
  const steps = new Map();
  let drawing = 0;
  const artefacts = async () => {
    const draw = ++drawing;
    if (!section || !threadID || context?.id !== threadID) return;
    const shown = entries.filter(inSection);
    contextTitle.textContent = '[Artefakte]';
    contextNote.textContent = shown.length ? 'T – ' + clock(section.at) : 'Noch keine';
    const blocks = [];
    for (const entry of shown) {
      let list = steps.get(entry.id);
      if (!list || entry.running) {
        try { list = await bridge.SessionSteps(context.profile, entry.id) || []; } catch { list = list || []; }
        if (draw !== drawing) return;
        steps.set(entry.id, list);
      }
      const block = doc.createElement('section');
      block.className = 'artefact';
      const title = doc.createElement('h3');
      title.textContent = (entry.title || entry.id) + ' · ' + (entry.running ? 'arbeitet' : 'fertig');
      block.append(title);
      const lines = doc.createElement('pre');
      lines.textContent = list.slice(-8).map(step => step.text || step.tool).join('\n') || 'Noch keine Werkzeuge';
      block.append(lines);
      blocks.push(block);
    }
    if (draw !== drawing) return;
    contextDetails.replaceChildren(...blocks);
  };
  // The history also changes its height by itself: as the sidebar slides and
  // its lines wrap anew, or as an answer grows while it streams in.
  const Resize = doc.defaultView?.ResizeObserver;
  if (Resize) {
    const sizes = new Resize(keep);
    sizes.observe(scroller);
    sizes.observe(doc.getElementById('content'));
  }
  // Expanding a text or a tool call is reading it: the history stays where it
  // is, not pulled to the end it grew, until the owner scrolls there again.
  doc.getElementById('content').addEventListener('click', event => {
    if (event.target?.closest?.('button, summary')) following = false;
  });
  let context = null;
  let ready = false;
  let activity = { busy: false, n: 0, own: false, stopping: false };
  const updateStatus = () => {
    if (!ready) return;
    const central = context?.id === threadID;
    const active = Math.max(0, activity.n - (activity.own ? 1 : 0));
    status.textContent = !context?.id.startsWith('owner/') ? 'Peer-Session · Nur lesen' :
      (central ? 'Thread' : 'Session') + ' · ' +
      (asking ? 'Braucht deine Antwort' : activity.busy ? 'Arbeitet …' : 'Bereit') +
      (central ? ' · ' + active + (active === 1 ? ' aktive Session' : ' aktive Sessions') : '');
  };
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
  // The one line of the composer grows with what is written, up to a limit in
  // the stylesheet, and shrinks again once it is sent.
  const grow = () => {
    if (!draft.style) return;
    // Empty, or not laid out yet (no width), it is the one line of rows="1";
    // measured without a width it would come out many lines tall.
    draft.style.height = '';
    if (!draft.value || !draft.offsetWidth) return;
    draft.style.height = draft.scrollHeight + 'px';
  };
  const updateComposer = () => {
    grow();
    if (asking) {
      const sent = context?.state.sentQuestion === asking.key;
      asking.buttons.forEach(button => { button.disabled = !writable() || sent; });
      questionHint.textContent = sent ? 'Antwort übergeben – der Assistent fährt fort …' :
        'Wähle eine Antwort oder antworte unten in eigenen Worten.';
    }
    draft.disabled = !ready || !context?.id.startsWith('owner/');
    send.disabled = !writable() || !draft.value.trim();
    stop.disabled = !ready || !context?.id.startsWith('owner/') || !activity.busy || activity.stopping;
    sendError.textContent = context?.state.error || '';
    sendError.hidden = !sendError.textContent;
    sendStatus.textContent = context?.state.pending ? 'Wird gesendet …' : (context?.state.notice || '');
    updateStatus();
  };
  stop.addEventListener('click', async () => {
    if (stop.disabled) return;
    const target = context;
    const current = activity;
    current.stopping = true;
    target.state.error = '';
    updateComposer();
    try {
      await bridge.StopSession(target.profile, target.id);
    } catch (cause) {
      target.state.error = 'Antwort konnte nicht gestoppt werden: ' + String(cause);
    } finally {
      current.stopping = false;
      if (context === target) updateComposer();
    }
  });
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
    thread.setAttribute('aria-current', selected === threadID ? 'true' : 'false');
    back.hidden = !threadID || selected === threadID;
    sessions.replaceChildren();
    const query = search.value.trim().toLocaleLowerCase('de-DE');
    if (threadID && !query && section?.at) {
      const when = doc.createElement('p');
      when.className = 'section-time';
      when.textContent = 'T – ' + clock(section.at);
      sessions.append(when);
    }
    const groups = new Map();
    for (const entry of [...entries].sort((a, b) => Number(!!b.running) - Number(!!a.running))) {
      if (!((entry.title || '') + ' ' + entry.id).toLocaleLowerCase('de-DE').includes(query)) continue;
      // Beside the thread, as in the demo, only the sessions of the part of it
      // in view show; a search finds the others.
      if (threadID && !query && !inSection(entry)) continue;
      const label = entry.running ? 'Aktiv' : dateGroup(entry.modified);
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
        button.setAttribute('data-running', String(!!entry.running));
        button.setAttribute('aria-current', entry.id === selected ? 'true' : 'false');
        const name = doc.createElement('span');
        name.className = 'session-title';
        name.textContent = entry.title || entry.id;
        const subtitle = doc.createElement('span');
        subtitle.className = 'session-meta';
        // As the sessions in the demo: what it works on, its state, and a bar that
        // runs while it works. No progress is made up: the bar only says running or not.
        subtitle.textContent = 'Zustand: ' + (entry.running ? 'arbeitet' : 'bereit') + ' · ' +
          (entry.id.startsWith('owner/') ? 'eigene' : 'peer') +
          (formatDate(entry.modified) ? ' · ' + formatDate(entry.modified) : '');
        const bar = doc.createElement('span');
        bar.className = 'session-bar';
        bar.setAttribute('aria-hidden', 'true');
        button.title = entry.id;
        button.append(name);
        button.append(subtitle);
        button.append(bar);
        button.addEventListener('click', () => { read(entry); });
        group.append(button);
      }
      sessions.append(group);
    }
    if (!groups.size) {
      const empty = doc.createElement('p');
      empty.className = 'list-empty';
      empty.textContent = query ? 'Keine passende Session gefunden.' : threadID ? 'Noch keine' : 'Noch keine Sessions.';
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
    activity = { busy: false, n: 0, own: false, stopping: false };
    setQuestion(null);
    context = { profile, id: entry.id, state: sessionState(profile, entry.id) };
    draft.value = context.state.text;
    updateComposer();
    content.replaceChildren();
    visible.clear();
    band?.disconnect();
    section = latest = null;
    // A deliberately opened result may stay beside the thread. It belongs to
    // its profile and is cleared when another individual session is selected.
    if (entry.id !== threadID || inspecting?.profile !== profile) {
      inspecting = null;
      contextTitle.textContent = '[' + (entry.id === threadID ? 'Thread' : 'Session') + ']';
      contextNote.textContent = 'Öffne ein Werkzeug-Ergebnis im Verlauf, um es hier zu lesen.';
      contextDetails.replaceChildren();
    }
    error.hidden = true;
    title.textContent = entry.title || entry.id;
    meta.textContent = entry.id + (formatDate(entry.modified) ? ' · ' + formatDate(entry.modified) : '');
    status.textContent = 'Verlauf wird geladen …';
    try {
      const snapshot = await bridge.OpenSession(profile, entry.id);
      const active = () => version === generation && current === selection;
      if (!active()) return;
      context.state.notice = snapshot.notice || '';
      const onEvent = event => {
        switch (event.kind) {
          case 'working':
            activity.n = event.n || 0;
            activity.own = !!event.own;
            activity.busy = activity.n > 0;
            break;
          case 'message': case 'task': case 'answering': case 'thought': case 'calling':
            activity.busy = true;
            break;
          case 'asked': if (!event.done) activity.busy = true; break;
          case 'answer': case 'failed': case 'canceled':
            activity.busy = entry.id === threadID && activity.n > (activity.own ? 1 : 0);
            break;
          case 'directory': meta.textContent = event.text || 'Profil-Workspace'; break;
          case 'step':
            if (!contextDetails.children.length) {
              contextTitle.textContent = '[' + (event.tool || 'Werkzeug') + ']';
              contextNote.textContent = event.text || event.tool || '';
            }
            break;
          case 'title':
            if (entry.id !== threadID && event.text) {
              entry.title = event.text;
              title.textContent = event.text;
              renderList();
            }
            break;
        }
        updateComposer();
      };
      const render = transcript(bridge, profile, entry.id, content, doc, active, setQuestion, onEvent, inspect, agentName(), sections);
      await render(snapshot.events || []);
      if (!active()) return;
      ready = true;
      updateComposer();
      // Start at the end of the transcript after the full replay, following it.
      stick();
      // One outstanding poll per selected chat; the bridge batches the live stream.
      const follow = async () => {
        try {
          while (active() && snapshot.token && entry.id.startsWith('owner/')) {
            const events = await bridge.PollSession(snapshot.token);
            if (!active()) return;
            if (events === null) throw new Error('Session-Verbindung beendet');
            // A poll comes back every 150 ms, mostly empty. Moving the history to
            // its end each time, at its end already, took back the bounce of a
            // pull past it: it is moved only when the history grew.
            if (!events?.length) continue;
            const height = scroller.scrollHeight;
            await render(events);
            if (active() && following && scroller.scrollHeight !== height) end();
          }
        } catch (cause) {
          if (active()) {
            ready = false;
            setQuestion(null);
            status.textContent = 'Verbindung unterbrochen';
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
    if (reset || !inspecting) {
      inspecting = null;
      contextTitle.textContent = '[Kontext]';
      contextNote.textContent = '';
      contextDetails.replaceChildren();
    }
    if (reset) search.value = '';
    sessions.replaceChildren();
    content.replaceChildren();
    error.hidden = true;
    title.textContent = 'Sessions';
    meta.textContent = 'Unterhaltungen des Nodes';
    status.textContent = 'Sessions werden geladen …';
    refresh.disabled = true;
    try {
      let result = [], listError;
      try { result = await bridge.Sessions(profiles.selectedIndex) || []; }
      catch (cause) { if (!threadID) throw cause; listError = cause; }
      if (version !== generation) return;
      // Work on a copy; never mutate data owned by the bridge.
      const locals = localEntries.get(profiles.selectedIndex) || new Map();
      for (const entry of result) locals.delete(entry.id);
      entries = [...result, ...locals.values()].filter(entry => entry.id !== threadID)
        .sort((a, b) => (Date.parse(b.modified) || 0) - (Date.parse(a.modified) || 0));
      renderList();
      status.textContent = entries.length ? 'Wähle eine Session.' : 'Noch keine Sessions.';
      const target = threadID && previous === threadID ? threadEntry : entries.find(entry => entry.id === previous);
      if (threadID || entries.length) await read(target || (threadID ? threadEntry : entries[0]));
      if (listError && version === generation) fail('Sessions konnten nicht geladen werden', listError);
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
  thread.addEventListener('click', () => { if (threadID) void read(threadEntry); });
  back.addEventListener('click', () => { if (threadID) void read(threadEntry); });
  search.addEventListener('input', renderList);
  refresh.addEventListener('click', () => { load(); });
  // Refresh only the existing session list, never the thread history/watch.
  // A late result belongs to its captured profile; it cannot change selection.
  let listing = false;
  doc.defaultView?.setInterval?.(async () => {
    if (listing || !ready || doc.hidden) return;
    const version = generation, profile = profiles.selectedIndex;
    listing = true;
    try {
      const result = await bridge.Sessions(profile) || [];
      if (version !== generation || profile !== profiles.selectedIndex) return;
      const locals = localEntries.get(profile) || new Map();
      for (const entry of result) locals.delete(entry.id);
      const nextEntries = [...result, ...locals.values()].filter(entry => entry.id !== threadID)
        .sort((a, b) => (Date.parse(b.modified) || 0) - (Date.parse(a.modified) || 0));
      if (JSON.stringify(nextEntries) === JSON.stringify(entries)) return;
      entries = nextEntries;
      renderList();
      if (entries.some(inSection)) void artefacts();
    } catch { /* Keep the last list; explicit refresh reports errors. */ }
    finally { listing = false; }
  }, 3000);
  return load();
}

export async function start(bridge, doc = document) {
  const theme = createThemePreference(doc);
  const sidebar = doc.getElementById('sidebar');
  const toggle = doc.getElementById('sidebar-toggle');
  // The sidebar is not hidden but slides out, see the stylesheet; inert keeps
  // what is out of view out of reach of the keyboard and screen readers.
  toggle.addEventListener('click', () => {
    const collapsed = !sidebar.inert;
    sidebar.inert = collapsed;
    doc.body.setAttribute('data-sidebar-collapsed', String(collapsed));
    toggle.setAttribute('aria-expanded', String(!collapsed));
    const label = collapsed ? 'Seitenleiste einblenden' : 'Seitenleiste ausblenden';
    toggle.setAttribute('aria-label', label);
    toggle.title = label;
  });
  try {
    const state = await bridge.InitialState();
    mount(state, doc);
    const modelDialog = createModelDialog(bridge, doc);
    const settingsDialog = createSettingsDialog(bridge, modelDialog, doc, theme);
    doc.getElementById('settings').addEventListener('click', () => {
      const index = doc.getElementById('profile').selectedIndex;
      settingsDialog.open(index, state.profiles[index]?.name);
    });
    await browse(bridge, doc, state.resume, state.thread);
  } catch (error) {
    const message = doc.getElementById('error');
    message.textContent = 'Profile konnten nicht geladen werden: ' + String(error);
    message.hidden = false;
  }
}

if (typeof window !== 'undefined') start(window.go?.gui?.Window);

import { t, locale, applyDocument } from './i18n.js';
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
  picker.selectedIndex = state.selected;
}

export function dateGroup(value, now = new Date()) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return t('date.undated');
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const yesterday = new Date(today);
  yesterday.setDate(yesterday.getDate() - 1);
  if (date >= today) return t('date.today');
  if (date >= yesterday) return t('date.yesterday');
  return t('date.older');
}

const dateFormat = () => new Intl.DateTimeFormat(locale(), { dateStyle: 'medium', timeStyle: 'short' });
const clockFormat = () => new Intl.DateTimeFormat(locale(), { hour: '2-digit', minute: '2-digit' });

// The time above a message, as the terminal UI writes it: the clock today, the
// date with it before.
function clock(value, now = new Date()) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '';
  return date.toDateString() === now.toDateString() ? clockFormat().format(date) : dateFormat().format(date);
}
function formatDate(value) {
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? dateFormat().format(date) : '';
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
  expand.textContent = t('transcript.expandCount', { n: value.length.toLocaleString(locale()) });
  expand.addEventListener('click', () => {
    const open = text.textContent.length > limit;
    text.textContent = open ? value.slice(0, limit) : value;
    expand.textContent = open ? t('transcript.expand') : t('transcript.collapse');
  });
  parent.append(expand);
}

// Session events are the same projection the terminal UI watches: never raw records.
// What a transcript tells browse of its parts, where nothing listens.
const quietSections = { observe() {}, changed() {} };

// How much of the thread shows, as in the terminal UI (tui.threadStart) and as
// its agent is told it (assistant.ThreadWindow): the archive keeps the rest.
const threadWindow = 6 * 60 * 60 * 1000;

// How long a window waits before each time it opens a session again whose
// live stream ended, in ms: about half a minute in all, long enough for a
// node to start anew from a new build, see browse.
const reconnectDelays = [250, 500, 1000, 2000, 4000, 8000, 8000];

// Messages are headed as in the terminal UI, in the language of the GUI:
// "Du", or the name of the agent.
function transcript(bridge, profile, id, content, doc, active, setQuestion, onEvent, inspect, agent = 'ouroboros', sections = quietSections) {
  const closeQuestion = () => setQuestion(null);
  const calls = new Map();
  let partial = null;
  // Of the thread only its last hours show: the expired prefix is left out,
  // and from the first message within them on everything shows, also one
  // without a time, as the terminal UI does.
  const cutoff = Date.now() - threadWindow;
  let shown = sections === quietSections;
  // Each owner message opens a part of the thread; the sessions the thread
  // started or handed a message to on it belong to that part, as its Handed
  // events tell, see browse.
  let exchange = { at: null, sessions: new Set() };
  const message = (kind, label, text, at) => {
    if (!shown && !(Date.parse(at) < cutoff)) shown = true;
    const item = doc.createElement('section');
    item.className = 'message message-' + kind;
    const heading = doc.createElement('h3');
    const time = clock(at);
    heading.textContent = label + (time ? ' · ' + time : '');
    item.append(heading);
    if (text) appendText(item, text, doc);
    if (!shown) return item;
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
      case 'message': closeQuestion(); exchange = { at: event.at, sessions: new Set() }; message('user', t('speaker.you'), event.text, event.at); break;
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
          const item = message('assistant', agent + ' · ' + t('transcript.writing'), '', event.at);
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
          const item = message('tool', t('speaker.tool'), '', event.at);
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
            call.note.textContent = t('tool.loading');
            try {
              const result = await bridge.ToolResult(profile, id, ref);
              if (!active()) return;
              if (result.found) {
                if (result.arguments) appendText(details, result.arguments, doc);
                appendText(details, result.content || t('tool.empty'), doc);
                call.loaded = true;
                call.result = result;
                inspect(event.tool, result);
              }
              call.note.textContent = result.found ? '' : t('tool.missing');
            } catch (cause) {
              if (active()) call.note.textContent = t('tool.failed', { error: String(cause) });
            } finally {
              call.loading = false;
            }
          });
        }
        call.done = event.done;
        call.summary.textContent = (event.text || event.tool || t('speaker.tool')) +
          (event.call_id ? t('tool.expandHint') : '');
        break;
      }
      case 'asked': {
        clearPartial();
        closeQuestion();
        const question = event.question;
        message('question', t(event.done ? 'speaker.answered' : 'speaker.question'),
          question?.text || event.text || '', event.at);
        if (!event.done && id.startsWith('owner/')) {
          setQuestion({ ...(question || { text: event.text || '', options: [] }), at: event.at });
        }
        break;
      }
      case 'queued': message('other', t('speaker.queue'), event.text || t('queue.text'), event.at); break;
      case 'failed':
      case 'canceled':
        closeQuestion();
        clearPartial();
        message('end', t('speaker.status'), event.kind === 'canceled' ? t('end.stopped') : (event.text || t('end.failed')), event.at);
        break;
      case 'task': message('task', t('speaker.task'), event.text, event.at); break;
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

// Where a session stands, from what its events tell: the phase of the loop to
// its answer, the call it is in, the one it did last, and what comes next. The
// counts come after this, as context, not before.
function sessionPhase(trace, running) {
  let phase = 'empty', since = '', call = null, last = null, question = '', jobs = '';
  for (const event of trace) {
    switch (event.kind) {
      case 'step':
        since = event.at || since;
        if (event.done) {
          last = event;
          call = null;
          if (phase !== 'asked') phase = 'thinking';
        } else {
          call = event;
          phase = 'tool';
        }
        break;
      case 'asked':
        since = event.at || since;
        if (event.done) {
          question = '';
          if (phase === 'asked') phase = 'thinking';
        } else {
          phase = 'asked';
          question = event.question?.text || '';
        }
        break;
      case 'answering':
        // A piece of the answer being written: that it is written, not what.
        since = event.at || since;
        if (phase !== 'asked') phase = 'answering';
        break;
      case 'jobs':
        jobs = String(event.text || '').trim();
        break;
      case 'message': case 'say': case 'task':
        since = event.at || since;
        if (phase !== 'asked') phase = 'thinking';
        break;
      case 'answer':
        phase = 'done'; since = event.at || since; call = null; question = '';
        break;
      case 'failed':
        phase = 'failed'; since = event.at || since; call = null;
        break;
      case 'canceled':
        phase = 'canceled'; since = event.at || since; call = null;
        break;
    }
  }
  const settled = phase === 'done' || phase === 'failed' || phase === 'canceled' || phase === 'empty';
  // A session the list does not know is taken as working while its phase says
  // so: better a state that may be stale than one that says "done" to a
  // session that works.
  const live = !settled && running !== false;
  const word = phase === 'asked' ? t('phase.asked')
    : phase === 'tool' ? (live ? t('phase.calling') : t('phase.ready'))
      : phase === 'thinking' ? (live ? t('phase.thinking') : t('phase.ready'))
        : phase === 'answering' ? (live ? t('phase.answering') : t('phase.ready'))
          : phase === 'done' ? t('phase.done')
            : phase === 'failed' ? t('phase.failed')
              : phase === 'canceled' ? t('phase.canceled')
                : t('phase.empty');
  // The call of a tool says its name and what it does with it.
  let what = '';
  if (phase === 'tool' && live && call) {
    const tool = call.tool || t('common.tool'), text = String(call.text || '');
    what = text.startsWith(tool + ' · ') ? text.slice(tool.length + 3) : text === tool ? '' : text;
  }
  const next = phase === 'asked' ? t('phase.nextAnswer')
    : phase === 'tool' && live ? t('phase.nextResult', { tool: call?.tool || t('common.tool') })
      : phase === 'answering' && live ? t('phase.nextEnd')
        : phase === 'thinking' && live ? t('phase.nextTool')
          : t('phase.nextNothing');
  return { phase, since, word, what, next, question, jobs, last, call, live: live || phase === 'asked' };
}

// The tools of a session as one picture instead of their calls one by one:
// one lane per tool, its calls where their time puts them, so that its mix and
// its rhythm read under the state. A call on its way pulses, and a dashed line
// marks where now is.
const lanesShown = 6;
function toolChart(doc, calls, pending, running) {
  const width = 240, labels = 64, counts = 16, row = 12, pad = 2;
  const name = step => step.tool || String(step.text || '').split(' · ')[0] || t('common.tool');
  const all = pending ? [...calls, pending] : calls;
  const tally = new Map();
  for (const step of all) tally.set(name(step), (tally.get(name(step)) || 0) + 1);
  const lanes = [...tally.entries()].sort((a, b) => b[1] - a[1]).slice(0, lanesShown);
  const lane = new Map(lanes.map(([tool], index) => [tool, index]));
  const height = lanes.length * row + 2 * pad;
  // A call sits where its time puts it, over the session up to now; without
  // stamps its order spreads it over the lane.
  const stamps = all.map(step => Date.parse(step.at)).filter(Number.isFinite);
  const first = Math.min(...stamps);
  const span = Math.max(...stamps, running ? Date.now() : -Infinity) - first;
  const at = (step, index) => {
    const stamp = Date.parse(step.at);
    const offset = span > 0 && Number.isFinite(stamp) ? (stamp - first) / span : index / Math.max(1, all.length);
    return Math.min(1, Math.max(0, offset));
  };
  const plot = width - counts - labels;
  const svg = doc.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('class', 'artefact-chart');
  svg.setAttribute('viewBox', '0 0 ' + width + ' ' + height);
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', all.length
    ? (running ? t('chart.running') : t('chart.done')) + all.length + (all.length === 1 ? t('chart.oneTool') : t('chart.tools')) +
      lanes.map(([tool, n]) => tool + ' ' + n).join(', ') +
      (tally.size > lanes.length ? t('chart.more', { n: tally.size - lanes.length }) : '')
    : t('chart.none'));
  const mark = (tag, attributes) => {
    const node = doc.createElementNS('http://www.w3.org/2000/svg', tag);
    for (const [key, value] of Object.entries(attributes)) node.setAttribute(key, value);
    svg.append(node);
    return node;
  };
  const text = (x, y, content, anchor) => {
    const node = mark('text', { x, y, 'text-anchor': anchor });
    node.textContent = content;
    return node;
  };
  lanes.forEach(([tool, n], index) => {
    const centre = pad + index * row + row / 2;
    text(0, centre + 2.8, tool.length > 12 ? tool.slice(0, 11) + '…' : tool);
    text(width, centre + 2.8, String(n), 'end');
    mark('line', { class: 'chart-lane lane-' + index, x1: labels, x2: labels + plot, y1: centre, y2: centre });
  });
  const tick = (step, index, extra) => {
    const shade = lane.get(name(step));
    if (shade === undefined) return null;
    const centre = pad + shade * row + row / 2;
    return mark('rect', {
      class: 'chart-tick lane-' + shade + (extra ? ' ' + extra : ''),
      x: (labels + at(step, index) * plot - 1.2).toFixed(2),
      y: centre - 3.5,
      width: 2.4,
      height: 7,
    });
  };
  calls.forEach((step, index) => tick(step, index, ''));
  // A call that was on its way when the session stopped does not pulse.
  if (pending) tick(pending, all.length - 1, running ? 'chart-live' : '');
  if (running) {
    mark('line', { class: 'chart-now', x1: labels + plot - 1.2, x2: labels + plot - 1.2, y1: pad, y2: height - pad });
  }
  return { svg, lanes, calls: all.length, kinds: tally.size };
}

// One session beside the thread: where it stands, what it did last and what it
// does next, with the chart of its tools under it.
function artefactBlock(doc, entry, trace) {
  const state = sessionPhase(trace, entry.running);
  const block = doc.createElement('section');
  block.className = 'artefact';
  const title = doc.createElement('h3');
  title.textContent = (entry.title || entry.id) + (state.live ? t('artefact.working') : '');
  block.append(title);
  const line = doc.createElement('p');
  line.className = 'artefact-state' + (state.live ? ' live' : '') + (state.phase === 'failed' ? ' bad' : '');
  const dot = doc.createElement('span');
  dot.className = 'artefact-dot';
  dot.setAttribute('aria-hidden', 'true');
  const word = doc.createElement('span');
  word.className = 'artefact-word';
  word.textContent = state.word;
  line.append(dot, word);
  if (state.what) {
    const what = doc.createElement('span');
    what.className = 'artefact-what';
    what.textContent = state.what;
    line.append(what);
  }
  if (state.since) {
    const time = doc.createElement('span');
    time.className = 'artefact-time';
    time.textContent = clock(state.since);
    line.append(time);
  }
  block.append(line);
  const fact = (key, value, time, extra) => {
    const row = doc.createElement('p');
    row.className = 'artefact-fact' + (extra ? ' ' + extra : '');
    const name = doc.createElement('span');
    name.className = 'artefact-key';
    name.textContent = key;
    const text = doc.createElement('span');
    text.className = 'artefact-value';
    text.textContent = value;
    row.append(name, text);
    if (time) {
      const at = doc.createElement('span');
      at.className = 'artefact-time';
      at.textContent = time;
      row.append(at);
    }
    return row;
  };
  if (state.question) block.append(fact(t('artefact.question'), state.question));
  block.append(fact(t('artefact.last'), state.last ? state.last.text : t('artefact.noCall'),
    state.last ? clock(state.last.at) : '', state.last?.failed ? 'bad' : ''));
  block.append(fact(t('artefact.next'), state.next));
  if (state.jobs) block.append(fact(t('artefact.running'), state.jobs.split('\n')[0] + (state.jobs.includes('\n') ? ' …' : '')));
  // Without a call there is nothing to draw; the stand above says it.
  const calls = trace.filter(step => step.kind === 'step' && step.done);
  const drawn = toolChart(doc, calls, state.call, state.live);
  if (drawn.calls) {
    block.append(drawn.svg);
    const summary = doc.createElement('p');
    summary.className = 'artefact-summary';
    summary.textContent = t(drawn.calls === 1 ? 'artefact.oneTool' : 'artefact.tools', { n: drawn.calls }) +
      drawn.kinds + t(drawn.kinds === 1 ? 'artefact.oneKind' : 'artefact.kinds') +
      (drawn.kinds > drawn.lanes.length ? t('artefact.lanes', { n: drawn.lanes.length }) : '');
    block.append(summary);
  }
  return block;
}

export function browse(bridge, doc = document, resume = '', threadID = '', retry = reconnectDelays) {
  const profiles = doc.getElementById('profile');
  // The name of the agent of the selected profile, as the terminal UI heads its messages.
  const agentName = () => profiles.children?.[profiles.selectedIndex]?.getAttribute?.('data-name') || 'ouroboros';
  const sessions = doc.getElementById('sessions');
  const content = doc.getElementById('content');
  const status = doc.getElementById('session-status');
  const title = doc.getElementById('session-title');
  const meta = doc.getElementById('session-meta');
  const error = doc.getElementById('error');
  const composer = doc.getElementById('composer');
  const draft = doc.getElementById('draft');
  const send = doc.getElementById('send');
  const sendError = doc.getElementById('send-error');
  const sendStatus = doc.getElementById('send-status');
  const back = doc.getElementById('back-to-thread');
  const stop = doc.getElementById('stop');
  const threadEntry = { id: threadID, title: 'Thread' };
  const contextTitle = doc.getElementById('context-title');
  const contextNote = doc.getElementById('context-note');
  const contextDetails = doc.getElementById('context-details');
  let inspecting = null;
  const inspect = (tool, result) => {
    inspecting = { profile: context.profile, id: context.id };
    contextTitle.textContent = '[' + (tool || t('common.tool')) + ']';
    contextNote.textContent = t('context.toolResult', { id: context.id });
    contextDetails.replaceChildren();
    if (result.arguments) appendText(contextDetails, result.arguments, doc);
    appendText(contextDetails, result.content || t('tool.empty'), doc);
  };
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
    // A result opened deliberately stays beside the thread until the owner
    // scrolls to another part of it; then the artefacts of that part show.
    if (section) inspecting = null;
    section = part;
    renderList();
    void artefacts();
  };
  // Both marks decide anew whichever tells first: at the end of the history
  // the newest part is in view, though it may never reach the middle.
  const pick = () => {
    // Only the thread has parts; an individual session keeps the one it was opened from.
    if (context?.id !== threadID) return;
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
  // A session opened on its own, not the thread: where it stands, as its
  // events tell it. Without any, the rail keeps its hint.
  const own = [];
  let heard = false; // an event of it came live, after its replay
  const ownArtefacts = () => {
    if (!context || context.id === threadID || inspecting || !own.length) return;
    // It works as the list says, or as its own events tell since it was
    // opened: the list learns of new work only with its next refresh.
    const listed = entries.find(item => item.id === context.id) || { id: context.id };
    const entry = { ...listed, running: !!listed.running || (heard && activity.busy) };
    contextTitle.textContent = t('context.stand');
    contextNote.textContent = 'T – ' + clock(own[own.length - 1].at);
    contextDetails.replaceChildren(artefactBlock(doc, entry, own));
  };
  // The artefacts of the sessions of the part in view: where each stands and
  // what its tools did, as the session tells it rather than call by call.
  const steps = new Map();
  let drawing = 0;
  const artefacts = async () => {
    const draw = ++drawing;
    if (!section || !threadID || context?.id !== threadID || inspecting) return;
    const profile = context.profile;
    // A render on its way is stale once another one began or the selection
    // changed, see read.
    const stale = () => draw !== drawing || context?.id !== threadID || context?.profile !== profile;
    const shown = entries.filter(inSection);
    contextTitle.textContent = t('context.stand');
    contextNote.textContent = shown.length ? 'T – ' + clock(section.at) : t('common.none');
    const blocks = [];
    for (const entry of shown) {
      // What was read while the session worked is read once more after it ended.
      const kept = steps.get(entry.id);
      let list = kept?.list;
      if (!kept || entry.running || kept.running) {
        try { list = await bridge.SessionTrace(profile, entry.id) || []; } catch { list = list || []; }
        if (stale()) return;
        steps.set(entry.id, { list, running: !!entry.running });
      }
      blocks.push(artefactBlock(doc, entry, list));
    }
    if (stale()) return;
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
  // Why the live stream of the shown session is lost for good, see read: said
  // beside the composer as well, where it is in view, since the error at the
  // start of the history scrolled away. A session that shows again clears it.
  let gone = '';
  let activity = { busy: false, n: 0, own: false, stopping: false };
  const updateStatus = () => {
    if (!ready) return;
    const central = context?.id === threadID;
    const active = Math.max(0, activity.n - (activity.own ? 1 : 0));
    status.textContent = !context?.id.startsWith('owner/') ? t('status.peer') :
      (central ? 'Thread' : 'Session') + ' · ' +
      (asking ? t('status.needsAnswer') : activity.busy ? t('status.working') : t('status.ready')) +
      (central ? t(active === 1 ? 'status.oneActive' : 'status.active', { n: active }) : '');
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
        button.textContent = option + (option === question.recommended ? t('question.recommended') : '');
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
      questionHint.textContent = sent ? t('question.sent') : t('question.hint');
    }
    draft.disabled = !ready || !context?.id.startsWith('owner/');
    send.disabled = !writable() || !draft.value.trim();
    stop.disabled = !ready || !context?.id.startsWith('owner/') || !activity.busy || activity.stopping;
    sendError.textContent = context?.state.error || gone;
    sendError.hidden = !sendError.textContent;
    sendStatus.textContent = context?.state.pending ? t('composer.sending') : (context?.state.notice || '');
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
      target.state.error = t('composer.stopFailed', { error: String(cause) });
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
      state.notice = t('composer.sent');
    } catch (cause) {
      state.error = t('composer.sendFailed', { error: String(cause) });
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
    back.hidden = !threadID || selected === threadID;
    sessions.replaceChildren();
    if (threadID && section?.at) {
      const when = doc.createElement('p');
      when.className = 'section-time';
      when.textContent = 'T – ' + clock(section.at);
      sessions.append(when);
    }
    const groups = new Map();
    for (const entry of [...entries].sort((a, b) => Number(!!b.running) - Number(!!a.running))) {
      // Beside the thread, as in the demo, only the sessions of the part of it
      // in view show.
      if (threadID && !inSection(entry)) continue;
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
        subtitle.textContent = t('session.state', {
          state: entry.running ? t('session.working') : t('phase.ready'),
          owner: entry.id.startsWith('owner/') ? t('session.own') : t('session.peer'),
        }) +
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
      empty.textContent = threadID ? t('common.none') : t('sessions.none');
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
    gone = '';
    activity = { busy: false, n: 0, own: false, stopping: false };
    setQuestion(null);
    context = { profile, id: entry.id, state: sessionState(profile, entry.id) };
    own.length = 0;
    heard = false;
    draft.value = context.state.text;
    updateComposer();
    // A deliberately opened result may stay beside the thread. It belongs to
    // its profile and is cleared when another individual session is selected.
    if (entry.id !== threadID || inspecting?.profile !== profile) {
      inspecting = null;
      contextTitle.textContent = '[' + (entry.id === threadID ? 'Thread' : 'Session') + ']';
      contextNote.textContent = t('context.hint');
      contextDetails.replaceChildren();
    }
    error.hidden = true;
    title.textContent = entry.title || entry.id;
    meta.textContent = entry.id + (formatDate(entry.modified) ? ' · ' + formatDate(entry.modified) : '');
    status.textContent = t('session.loading');
    const active = () => version === generation && current === selection;
    // What shows is cleared once the snapshot that replaces it is there: a
    // session opened again after its stream ended keeps its transcript until
    // then, see reconnect.
    const clear = () => {
      own.length = 0;
      heard = false;
      content.replaceChildren();
      drawing++; // artefacts on their way belong to what was shown before
      visible.clear();
      band?.disconnect();
      // The thread starts its parts anew; an individual session opened from it
      // keeps the part it was opened from, and its siblings beside it.
      if (entry.id === threadID) section = latest = null;
    };
    clear();
    // The stand of a session opened on its own follows its events; beside
    // the thread, the rail reads each session for itself.
    const stand = event => {
      if (entry.id === threadID) return;
      own.push(event);
      ownArtefacts();
    };
    const onEvent = event => {
      if (ready) heard = true;
      switch (event.kind) {
        case 'working':
          activity.n = event.n || 0;
          activity.own = !!event.own;
          activity.busy = activity.n > 0;
          break;
        case 'message': case 'task':
          activity.busy = true;
          stand(event);
          break;
        case 'answering':
          activity.busy = true;
          // One piece of the answer being written is enough to tell it.
          if (own[own.length - 1]?.kind !== 'answering') stand(event);
          break;
        case 'thought': case 'calling':
          activity.busy = true;
          break;
        case 'asked': if (!event.done) activity.busy = true; stand(event); break;
        case 'answer': case 'failed': case 'canceled':
          activity.busy = entry.id === threadID && activity.n > (activity.own ? 1 : 0);
          stand(event);
          break;
        case 'directory': meta.textContent = event.text || t('session.workspace'); break;
        case 'step': stand(event); break;
        case 'jobs': case 'say': stand(event); break;
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
    // follow replays a snapshot of the session in full, then follows its live
    // stream, see poll.
    const follow = async (snapshot) => {
      context.state.notice = snapshot.notice || '';
      const render = transcript(bridge, profile, entry.id, content, doc, active, setQuestion, onEvent, inspect, agentName(), entry.id === threadID ? sections : undefined);
      await render(snapshot.events || []);
      if (!active()) return;
      ready = true;
      gone = '';
      updateComposer();
      // Start at the end of the transcript after the full replay, following it.
      stick();
      void poll(snapshot.token, render);
    };
    // One outstanding poll per selected chat; the bridge batches the live stream.
    const poll = async (token, render) => {
      try {
        while (active() && token && entry.id.startsWith('owner/')) {
          const events = await bridge.PollSession(token);
          if (!active()) return;
          // The stream ended, as when the node starts anew: the session is
          // opened again.
          if (events === null) return await reconnect();
          // A stream that answers earns its retries back; one that ends
          // right after each opening uses them up instead of spinning.
          attempts = 0;
          // A poll comes back every 150 ms, mostly empty. Moving the history to
          // its end each time, at its end already, took back the bounce of a
          // pull past it: it is moved only when the history grew.
          if (!events?.length) continue;
          const height = scroller.scrollHeight;
          await render(events);
          if (active() && following && scroller.scrollHeight !== height) end();
        }
      } catch (cause) {
        if (active()) lost(cause);
      }
    };
    const lost = (cause, message = t('session.disconnectedHint')) => {
      ready = false;
      gone = message + ': ' + String(cause);
      setQuestion(null);
      updateComposer();
      status.textContent = t('session.disconnected');
      fail(message, cause);
    };
    // reconnect opens the session again a bounded number of times, ever
    // later, while the node starts anew: the window of a new build replaces
    // this one, see Window.OpenSession, or the node gives the whole session
    // anew, which replaces the transcript without doubling it. A node that
    // does not come back is said once, and the transcript stays.
    let attempts = 0;
    const reconnect = async () => {
      ready = false;
      setQuestion(null);
      activity = { busy: false, n: 0, own: false, stopping: false };
      updateComposer();
      status.textContent = t('session.reconnecting');
      let cause = new Error(t('session.ended'));
      while (attempts < retry.length) {
        await new Promise(resolve => setTimeout(resolve, retry[attempts++]));
        if (!active()) return;
        let snapshot;
        try { snapshot = await bridge.OpenSession(profile, entry.id); }
        catch (failed) { cause = failed; continue; }
        if (!active()) return;
        error.hidden = true;
        clear();
        return follow(snapshot);
      }
      if (active()) lost(cause, t('session.gone'));
    };
    try {
      const snapshot = await bridge.OpenSession(profile, entry.id);
      if (!active()) return;
      await follow(snapshot);
    } catch (cause) {
      if (!active()) return;
      status.textContent = '';
      fail(t('session.loadFailed'), cause);
    }
  };
  const load = async (reset = false) => {
    const version = ++generation;
    ++selection;
    ready = false;
    gone = '';
    context = null;
    setQuestion(null);
    draft.value = '';
    updateComposer();
    const previous = reset ? null : (selected || resume);
    selected = null;
    entries = [];
    if (reset || !inspecting) {
      inspecting = null;
      contextTitle.textContent = t('context.title');
      contextNote.textContent = '';
      contextDetails.replaceChildren();
    }
    sessions.replaceChildren();
    content.replaceChildren();
    error.hidden = true;
    title.textContent = t('sessions.title');
    meta.textContent = t('sessions.subtitle');
    status.textContent = t('sessions.loading');
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
      status.textContent = entries.length ? t('sessions.choose') : t('sessions.none');
      const target = threadID && previous === threadID ? threadEntry : entries.find(entry => entry.id === previous);
      if (threadID || entries.length) await read(target || (threadID ? threadEntry : entries[0]));
      if (listError && version === generation) fail(t('sessions.loadFailed'), listError);
    } catch (cause) {
      if (version !== generation) return;
      status.textContent = '';
      fail(t('sessions.loadFailed'), cause);
    }
  };
  profiles.addEventListener('change', () => { load(true); });
  back.addEventListener('click', () => { if (threadID) void read(threadEntry); });
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
      ownArtefacts();
      if (entries.some(inSection)) void artefacts();
    } catch { /* Keep the last list; explicit refresh reports errors. */ }
    finally { listing = false; }
  }, 3000);
  return load();
}

export async function start(bridge, doc = document) {
  const theme = createThemePreference(doc);
  try {
    applyDocument(doc);
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
    message.textContent = t('profiles.loadFailed', { error: String(error) });
    message.hidden = false;
  }
}

if (typeof window !== 'undefined') start(window.go?.gui?.Window);

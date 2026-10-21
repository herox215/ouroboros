import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

// A data URL keeps this test independent of package.json's module mode.
const source = await readFile(new URL('./frontend/app.js', import.meta.url), 'utf8');
const dialogSource = await readFile(new URL('./frontend/model-dialog.js', import.meta.url), 'utf8');
const dialogURL = 'data:text/javascript;base64,' + Buffer.from(dialogSource).toString('base64');
const { createModelDialog } = await import(dialogURL);
const themeSource = await readFile(new URL('./frontend/theme.js', import.meta.url), 'utf8');
const themeURL = 'data:text/javascript;base64,' + Buffer.from(themeSource).toString('base64');
const { createThemePreference } = await import(themeURL);
const settingsSource = await readFile(new URL('./frontend/settings-dialog.js', import.meta.url), 'utf8');
const settingsURL = 'data:text/javascript;base64,' + Buffer.from(settingsSource.replace("'./theme.js'", JSON.stringify(themeURL))).toString('base64');
const { createSettingsDialog } = await import(settingsURL);
const { mount, start, browse, dateGroup } = await import(
  'data:text/javascript;base64,' + Buffer.from(source.replace("'./model-dialog.js'", JSON.stringify(dialogURL)).replace("'./settings-dialog.js'", JSON.stringify(settingsURL)).replace("'./theme.js'", JSON.stringify(themeURL))).toString('base64')
);

class Element extends EventTarget {
  constructor(tagName) {
    super();
    this.tagName = tagName;
    this.children = [];
    this.textContent = '';
    this.value = '';
    this.selectedIndex = -1;
    this.disabled = false;
    this.hidden = false;
    this.className = '';
    this.title = '';
    this.attributes = new Map();
  }

  showModal() { this.open = true; }

  close() { this.open = false; this.dispatchEvent(new Event('close')); }

  focus() { this.focused = true; }

  // No scrollIntoView stub: the history is moved to its end itself, see the
  // test on end scrolling.
  scrollIntoView() { assert.fail('the frontend must move the history, not scroll an element into view'); }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }

  set innerHTML(value) {
    assert.fail('Frontend must use textContent, not innerHTML');
  }

  replaceChildren(...children) {
    this.children = children;
    this.selectedIndex = -1;
  }

  append(child) {
    this.children.push(child);
    if (this.tagName === 'select' && this.selectedIndex === -1) {
      this.selectedIndex = 0;
    }
  }
}

// A scroller that clamps scrollTop at both ends, as a browser does.
class Scroller extends Element {
  constructor(tagName) {
    super(tagName);
    this.scrollHeight = 0;
    this.clientHeight = 0;
    this.top = 0;
  }

  get scrollTop() { return this.top; }

  set scrollTop(value) {
    this.top = Math.max(0, Math.min(value, Math.max(0, this.scrollHeight - this.clientHeight)));
  }
}

function documentFixture() {
  const elements = {
    settings: new Element('button'),
    sidebar: new Element('aside'),
    profile: new Element('select'),
    error: new Element('p'),
    sessions: new Element('div'),
    'session-title': new Element('h2'),
    'session-meta': new Element('p'),
    content: new Element('div'),
    'session-status': new Element('p'),
    transcript: new Scroller('div'),
    'transcript-end': new Element('div'),
    'transcript-start': new Element('div'),
    'back-to-thread': new Element('button'),
    stop: new Element('button'),
    'context-title': new Element('h2'),
    'context-note': new Element('p'),
    'context-details': new Element('div'),
    'question-panel': new Element('section'),
    'question-text': new Element('p'),
    'question-options': new Element('div'),
    'question-hint': new Element('p'),
    composer: new Element('form'),
    draft: new Element('textarea'),
    send: new Element('button'),
    'send-error': new Element('p'),
    'send-status': new Element('p'),
  };
  elements.profile.disabled = true;
  elements.error.hidden = true;
  // The document gets the scroll events of the page, its window the resizes.
  const events = new EventTarget();
  // Its window tells whether the mark at the end of the history is in view, see scrollPage.
  const view = new EventTarget(), marks = [];
  view.IntersectionObserver = class {
    constructor(run, options) { this.run = run; this.options = options; marks.push(this); }
    observe(target) { this.target ??= target; (this.targets ??= []).push(target); }
    disconnect() { this.targets = []; }
  };
  return {
    ...elements,
    body: new Element('body'),
    documentElement: new Element('html'),
    defaultView: view,
    marks,
    addEventListener: events.addEventListener.bind(events),
    dispatchEvent: events.dispatchEvent.bind(events),
    getElementById(id) {
      assert.ok(Object.hasOwn(elements, id), 'Unexpected element: ' + id);
      return elements[id];
    },
    createElement(tagName) {
      assert.ok(['option', 'section', 'h2', 'h3', 'pre', 'p', 'button', 'span', 'details', 'summary', 'dialog', 'form', 'label', 'select', 'div', 'input'].includes(tagName));
      return new Element(tagName);
    },
  };
}

function select(doc, index) {
  doc.profile.selectedIndex = index;
  doc.profile.dispatchEvent(new Event('change'));
}

test('HTML supplies the module and accessible profile/error elements', async () => {
  const html = await readFile(new URL('./frontend/index.html', import.meta.url), 'utf8');
  assert.match(html, /<script\b[^>]*type="module"[^>]*src="\/app\.js"[^>]*><\/script>/);
  for (const id of ['profile', 'error', 'sessions', 'content', 'session-status', 'session-title', 'session-meta', 'back-to-thread', 'stop', 'composer', 'draft', 'send', 'send-error', 'send-status', 'question-panel', 'question-text', 'question-options', 'question-hint']) {
    assert.match(html, new RegExp('id="' + id + '"'));
  }
  // Beside the thread only its sessions: no navigation above them, and the
  // profile, chosen at start, is not shown.
  for (const id of ['active', 'identity', 'session-search', 'refresh', 'new-session', 'thread', 'sidebar-toggle']) {
    assert.doesNotMatch(html, new RegExp('id="' + id + '"'));
  }
  assert.doesNotMatch(html, /class="brand"/);
  assert.match(html, /<select\b(?=[^>]*id="profile")(?=[^>]*aria-label="Profil")(?=[^>]*hidden)(?=[^>]*disabled)[^>]*>/);
  assert.match(html, /<[^>]+(?=[^>]*id="error")(?=[^>]*role="alert")(?=[^>]*hidden)[^>]*>/);
  assert.match(html, /<[^>]+(?=[^>]*id="session-status")(?=[^>]*role="status")[^>]*>/);
});

// The owner scrolls the history, as the browser reports it.
function scrollPage(doc, top) {
  const page = doc.transcript;
  page.scrollTop = top;
  const near = page.scrollHeight - page.clientHeight - page.scrollTop <= 48;
  for (const mark of doc.marks) if (mark.target === doc['transcript-end']) mark.run([{ isIntersecting: near, target: mark.target }]);
}

// An open owner session on a page of 3200 px in a window of 900 px, where
// every rendered message makes the transcript 400 px taller.
async function liveSession(events = []) {
  const doc = archiveFixture();
  const page = doc.transcript;
  page.scrollHeight = 3200;
  page.clientHeight = 900;
  const append = doc.content.append.bind(doc.content);
  doc.content.append = child => { append(child); page.scrollHeight += 400; };
  const pending = [], sent = [];
  await browse({
    async Sessions() { return [{ id: 'owner/live' }]; },
    async OpenSession() { return { token: 1, events }; },
    PollSession() { const next = deferred(); pending.push(next); return next.promise; },
    async SendMessage(...args) { sent.push(args); },
  }, doc);
  return { doc, page, pending, sent };
}

test('live messages keep the page at its end unless the user has scrolled up', async () => {
  const { doc, page, pending } = await liveSession();
  assert.equal(doc.marks.length, 3, 'a mark at the end, one at the start, and the band in the middle');
  assert.equal(doc.marks[0].target, doc['transcript-end']);
  assert.deepEqual(doc.marks[0].options, { root: doc.transcript, rootMargin: '0px 0px 48px 0px' });
  assert.equal(page.scrollTop, 2300, 'the empty transcript still sits at its end');
  pending[0].resolve([{ kind: 'answer', text: 'new' }]);
  await settle();
  assert.equal(doc.content.children.length, 1);
  assert.equal(page.scrollTop, 2700, 'the answer is followed to the new end');
  // The owner scrolls up to read earlier messages: new output must not pull the page down.
  scrollPage(doc, 100);
  pending[1].resolve([{ kind: 'answer', text: 'more' }]);
  await settle();
  assert.equal(doc.content.children.length, 2);
  assert.equal(page.scrollTop, 100, 'reading earlier messages is not interrupted');
  // Scrolled back down, close enough to the end, output is followed again.
  scrollPage(doc, 4000 - 900 - 40);
  pending[2].resolve([{ kind: 'answer', text: 'again' }]);
  await settle();
  assert.equal(page.scrollTop, 4400 - 900);
});

test('who sends is taken to the end and follows the answer, also when scrolled up', async () => {
  const { doc, page, pending, sent } = await liveSession();
  scrollPage(doc, 100);
  typeDraft(doc, 'hello');
  submit(doc);
  assert.deepEqual(sent, [[0, 'owner/live', 'hello']]);
  assert.equal(page.scrollTop, 2300, 'sending takes the owner to the end at once');
  await settle();
  pending[0].resolve([{ kind: 'message', text: 'hello' }, { kind: 'answer', text: 'reply' }]);
  await settle();
  assert.equal(page.scrollTop, 4000 - 900, 'the message and its answer are followed');
  // An option of a question sends as well, while the agent waits for it.
  pending[1].resolve([{ kind: 'asked', question: { text: 'Which?', options: ['a', 'b'] } }]);
  await settle();
  scrollPage(doc, 100);
  doc['question-options'].children[0].dispatchEvent(new Event('click'));
  assert.deepEqual(sent[1], [0, 'owner/live', 'a']);
  assert.equal(page.scrollTop, page.scrollHeight - 900, 'answering a question takes the owner to the end');
});

test('a blank or refused send does not move the page', async () => {
  const { doc, page, sent } = await liveSession();
  scrollPage(doc, 100);
  typeDraft(doc, '   ');
  submit(doc);
  assert.deepEqual(sent, []);
  assert.equal(page.scrollTop, 100);
});

test('a window made smaller at the end stays at the end, scrolled up it stays where it is', async () => {
  const { doc, page, pending } = await liveSession();
  page.clientHeight = 600;
  doc.defaultView.dispatchEvent(new Event('resize'));
  assert.equal(page.scrollTop, 3200 - 600, 'the end stays in view');
  pending[0].resolve([{ kind: 'answer', text: 'new' }]);
  await settle();
  assert.equal(page.scrollTop, 3600 - 600, 'and output is still followed');
  scrollPage(doc, 100);
  page.clientHeight = 500;
  doc.defaultView.dispatchEvent(new Event('resize'));
  assert.equal(page.scrollTop, 100);
  pending[1].resolve([{ kind: 'answer', text: 'more' }]);
  await settle();
  assert.equal(page.scrollTop, 100);
});

test('transcript layout uses real message heights for reliable end scrolling', async () => {
  const css = await readFile(new URL('./frontend/style.css', import.meta.url), 'utf8');
  assert.doesNotMatch(css, /content-visibility\s*:\s*auto|contain-intrinsic-size/);
});

// The history is its own scroller, the composer below it and the sidebar beside
// it: only the history bounces at its ends, and nothing in it is sticky, so the
// bounce moves it as one rather than jerking a pinned composer along.
test('only the history scrolls and bounces; composer and sidebar stay out of it', async () => {
  const html = await readFile(new URL('./frontend/index.html', import.meta.url), 'utf8');
  const scroller = html.slice(html.indexOf('id="transcript"'), html.indexOf('class="composer-dock"'));
  assert.match(scroller, /id="content"/);
  assert.doesNotMatch(scroller, /id="composer"/);
  assert.doesNotMatch(scroller, /id="session-status"/, 'thread activity stays visible above the scrolling history');
  const css = await readFile(new URL('./frontend/style.css', import.meta.url), 'utf8');
  assert.match(css, /\.transcript-scroller \{[^}]*overflow-y: auto; overscroll-behavior-y: contain;/);
  assert.match(css, /html, body \{ overscroll-behavior: none; \}/);
  assert.match(css, /\.sidebar \{[^}]*overscroll-behavior: none/);
  assert.doesNotMatch(css, /position: sticky; bottom/);
  // At the end or not is told by the mark coming into view, not by reading the
  // scroll position in scroll events, which jerked the bounce.
  assert.doesNotMatch(source, /addEventListener\('scroll'/);
  assert.match(html, /id="content"[^>]*><\/div>\s*<div id="transcript-end" aria-hidden="true">/);
  assert.doesNotMatch(source, /scrollIntoView|scrollingElement/);
});

test('initial selection uses the bridge snapshot, including a non-first profile', async () => {
  const doc = documentFixture();
  let calls = 0;
  await start({
    async Sessions() { return []; },
    async InitialState() {
      calls++;
      return {
        profiles: [{ name: 'Luna', id: 'aaa' }, { name: 'Other', id: 'bbb' }],
        selected: 1,
      };
    },
  }, doc);
  assert.equal(calls, 1);
  assert.deepEqual(doc.profile.children.map(option => [option.value, option.textContent]), [
    ['0', '1 · Luna'], ['1', '2 · Other'],
  ]);
  assert.equal(doc.profile.selectedIndex, 1);
  assert.equal(doc.error.hidden, true);
});

test('switching profiles reads archives without snapshot writes', async () => {
  const doc = documentFixture();
  const state = Object.freeze({
    profiles: Object.freeze([
      Object.freeze({ name: 'Luna', id: 'aaa' }),
      Object.freeze({ name: 'Other', id: 'bbb' }),
    ]),
    selected: 0,
  });
  let calls = 0;
  const bridge = new Proxy({
    async Sessions() { return []; },
    async InitialState() {
      calls++;
      return state;
    },
  }, {
    get(target, key) {
      assert.ok(['InitialState', 'Sessions'].includes(key), 'Only read-only capabilities should be used');
      return target[key];
    },
  });
  await start(bridge, doc);
  select(doc, 1);
  select(doc, 0);
  assert.equal(state.selected, 0);
  assert.equal(calls, 1);
});

test('a profile list without an entry mounts without touching the view', () => {
  const doc = documentFixture();
  mount({ profiles: [], selected: 0 }, doc);
  assert.equal(doc.profile.selectedIndex, 0);
  assert.deepEqual(doc.profile.children, []);
});

test('duplicate names remain distinct options selected by index', () => {
  const doc = documentFixture();
  mount({
    profiles: [{ name: 'Same', id: 'first' }, { name: 'Same', id: 'second' }],
    selected: 0,
  }, doc);
  assert.deepEqual(doc.profile.children.map(option => [option.value, option.textContent]), [
    ['0', '1 · Same'], ['1', '2 · Same'],
  ]);
  select(doc, 1);
  select(doc, 0);
});

test('HTML in profile names is displayed literally through textContent', () => {
  const doc = documentFixture();
  const name = '<img src=x onerror="alert(1)"> & <script>bad()</script>';
  mount({ profiles: [{ name, id: 'safe-id' }], selected: 0 }, doc);
  assert.equal(doc.profile.children[0].textContent, '1 · ' + name);
  assert.deepEqual(doc.profile.children[0].children, []);
});

test('an asynchronous bridge rejection reveals the error instead of rejecting start', async () => {
  const doc = documentFixture();
  await assert.doesNotReject(() => start({
    async InitialState() {
      await Promise.resolve();
      throw new Error('bridge unavailable');
    },
  }, doc));
  assert.equal(doc.error.hidden, false);
  assert.equal(doc.error.textContent, 'Profile konnten nicht geladen werden: Error: bridge unavailable');
  assert.deepEqual(doc.profile.children, []);
});

const settle = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
function archiveFixture() {
  const doc = documentFixture();
  mount({ profiles: [{ name: 'One' }, { name: 'Two' }], selected: 0 }, doc);
  return doc;
}

function buttons(doc) {
  return doc.sessions.children.flatMap(group => group.children.filter(child => child.tagName === 'button'));
}
function choose(doc, id) {
  const button = buttons(doc).find(button => button.title === id);
  assert.ok(button, 'Missing session: ' + id);
  button.dispatchEvent(new Event('click'));
}
function current(doc) {
  return buttons(doc).filter(button => button.getAttribute('aria-current') === 'true').map(button => button.title);
}

test('dateGroup uses local calendar boundaries, including invalid dates', () => {
  const now = new Date(2026, 0, 1, 12);
  assert.equal(dateGroup(new Date(2026, 0, 1, 0), now), 'Heute');
  assert.equal(dateGroup(new Date(2026, 0, 1, 23, 59), now), 'Heute');
  assert.equal(dateGroup(new Date(2025, 11, 31, 0), now), 'Gestern');
  assert.equal(dateGroup(new Date(2025, 11, 31, 23, 59), now), 'Gestern');
  assert.equal(dateGroup(new Date(2025, 11, 30, 23, 59), now), 'Älter');
  assert.equal(dateGroup('not a date', now), 'Ohne Datum');
  assert.equal(dateGroup(undefined, now), 'Ohne Datum');
});

test('sessions are sorted newest first and grouped without mutating bridge data', async () => {
  const doc = archiveFixture();
  const day = new Date();
  day.setHours(12, 0, 0, 0);
  const yesterday = new Date(day);
  yesterday.setDate(day.getDate() - 1);
  const older = new Date(day);
  older.setDate(day.getDate() - 7);
  const entries = Object.freeze([
    { id: 'owner/invalid', modified: 'invalid' },
    { id: 'owner/old', modified: older.toISOString() },
    { id: 'owner/yesterday', modified: yesterday.toISOString() },
    { id: 'owner/early', modified: new Date(day.getTime() - 3600000).toISOString() },
    { id: 'owner/latest', modified: day.toISOString() },
  ].map(Object.freeze));
  await browse({ async Sessions() { return entries; }, async OpenSession() { return { token: 0, events: [] }; } }, doc);
  assert.deepEqual(doc.sessions.children.map(group => [group.tagName, group.children[0].textContent]),
    [['section', 'Heute'], ['section', 'Gestern'], ['section', 'Älter'], ['section', 'Ohne Datum']]);
  assert.deepEqual(buttons(doc).map(button => button.title),
    ['owner/latest', 'owner/early', 'owner/yesterday', 'owner/old', 'owner/invalid']);
  assert.equal(entries[0].id, 'owner/invalid');
  assert.deepEqual(current(doc), ['owner/latest']);
  assert.match(doc['session-status'].textContent, /Session · Bereit/);
});

test('opening and switching sessions scrolls to the end only after the full replay', async () => {
  const doc = archiveFixture();
  const page = doc.transcript;
  page.scrollHeight = 5000;
  page.clientHeight = 800;
  const scrolls = [];
  let top = 0;
  Object.defineProperty(page, 'scrollTop', {
    get: () => top,
    set(value) { top = value; scrolls.push({ top: value, messages: doc.content.children.length }); },
  });
  const old = deferred();
  const events = Array.from({ length: 85 }, (_, i) => ({ kind: 'message', text: String(i) }));
  await browse({
    async Sessions() { return [{ id: 'owner/first' }, { id: 'peer/second' }, { id: 'owner/stale' }]; },
    async OpenSession(_, id) {
      if (id === 'owner/stale') return old.promise;
      return { token: 0, events };
    },
  }, doc);
  assert.deepEqual(scrolls, [{ top: 5000, messages: 85 }]);
  choose(doc, 'owner/stale');
  await settle();
  assert.equal(scrolls.length, 1);
  choose(doc, 'peer/second');
  // Large replays yield to timers between batches.
  while (scrolls.length < 2) await new Promise(resolve => setTimeout(resolve, 5));
  assert.deepEqual(scrolls[1], scrolls[0]);
  old.resolve({ token: 0, events: [] });
  await settle();
  assert.equal(scrolls.length, 2);
});

test('late list responses or errors cannot leak into the newly selected profile', async () => {
  for (const reject of [false, true]) {
    const doc = archiveFixture();
    const old = deferred();
    const reads = [];
    const loading = browse({
      Sessions(profile) { return profile === 0 ? old.promise : Promise.resolve([{ id: 'owner/new' }]); },
      async OpenSession(profile, id) { reads.push([profile, id]); return { token: 0, events: [{ kind: 'message', text: 'new profile' }] }; },
    }, doc);
    select(doc, 1);
    await settle();
    if (reject) old.reject(new Error('stale list failure'));
    else old.resolve([{ id: 'owner/old' }]);
    await loading;
    assert.deepEqual(buttons(doc).map(button => button.title), ['owner/new']);
    assert.equal(doc.content.children[0].children[1].textContent, 'new profile');
    assert.deepEqual(reads, [[1, 'owner/new']]);
    assert.equal(doc.error.hidden, true);
  }
});

test('late snapshots and errors cannot replace a newer session or profile', async () => {
  for (const switchProfile of [false, true]) {
    for (const reject of [false, true]) {
      const doc = archiveFixture();
      const old = deferred();
      const loading = browse({
        async Sessions(profile) { return profile === 0 ? [{ id: 'owner/old' }, { id: 'owner/new' }] : [{ id: 'peer/new' }]; },
        OpenSession(profile, id) {
          return id === 'owner/old' ? old.promise : Promise.resolve({ token: 0, events: [{ kind: 'answer', text: 'latest' }] });
        },
      }, doc);
      await settle();
      if (switchProfile) select(doc, 1);
      else choose(doc, 'owner/new');
      await settle();
      if (reject) old.reject(new Error('stale failure'));
      else old.resolve({ token: 0, events: [{ kind: 'message', text: 'stale content' }] });
      await loading;
      assert.equal(doc.content.children[0].children[1].textContent, 'latest');
      assert.deepEqual(current(doc), [switchProfile ? 'peer/new' : 'owner/new']);
      assert.equal(doc.error.hidden, true);
    }
  }
});


function descendants(element, tag) {
  return element.children.flatMap(child => [
    ...(child.tagName === tag ? [child] : []), ...descendants(child, tag),
  ]);
}
function visibleTexts(element) {
  if (element.hidden) return [];
  return [
    ...(element.tagName === 'pre' ? [element.textContent] : []),
    ...element.children.flatMap(visibleTexts),
  ];
}
function toggle(detail, open) {
  detail.open = open;
  detail.dispatchEvent(new Event('toggle'));
}
const toolEvents = [
  { kind: 'step', tool: 'files.read', text: 'Reading file', call_id: 'call-1', done: false },
  { kind: 'step', tool: 'files.read', text: 'File read', call_id: 'call-1', done: true },
];

test('whole conversation replays projected events literally, with paired tools and no eager details', async () => {
  const doc = archiveFixture();
  const unsafe = '<img src=x onerror="bad()"> & <script>literal</script>';
  const calls = [];
  const events = [
    { kind: 'message', text: unsafe, at: '2026-01-01T12:00:00Z' },
    { kind: 'thought', text: 'private thought' },
    { kind: 'answering', text: 'partial ' },
    { kind: 'answering', text: 'answer' },
    { kind: 'answer', text: unsafe },
    ...toolEvents,
    { kind: 'say', text: 'Spoken answer' },
    { kind: 'suggestion', text: 'Suggested answer' },
    { kind: 'answer', text: '  ' },
    { kind: 'task', text: unsafe },
    { kind: 'failed', text: unsafe },
    { kind: 'failed' },
    { kind: 'canceled' },
  ];
  const entries = Array.from({ length: 65 }, (_, i) => ({ id: 'owner/' + i, title: unsafe }));
  await browse(new Proxy({
    async Sessions() { return entries; },
    async OpenSession(profile, id) { calls.push([profile, id]); return { token: 0, events }; },
    ToolResult() { assert.fail('Details must not load during replay'); },
  }, {
    get(target, key) {
      assert.ok(['Sessions', 'OpenSession', 'ToolResult'].includes(key), 'Only conversation reads allowed');
      return target[key];
    },
  }), doc);
  assert.equal(buttons(doc).length, 65);
  assert.equal(buttons(doc)[0].children[0].textContent, unsafe);
  assert.equal(doc['session-title'].textContent, unsafe);
  assert.match(doc['session-meta'].textContent, /owner\/0/);
  const texts = descendants(doc.content, 'pre');
  assert.deepEqual(visibleTexts(doc.content),
    [unsafe, unsafe, 'Spoken answer', 'Suggested answer', unsafe, unsafe, 'Antwort fehlgeschlagen', 'Antwort gestoppt']);
  for (const text of texts) assert.deepEqual(text.children, []);
  assert.ok(!visibleTexts(doc.content).includes('partial answer'));
  assert.ok(doc.content.children.some(item => item.hidden && descendants(item, 'pre').some(text => text.textContent === 'partial answer')));
  assert.match(doc.content.children[0].children[0].textContent, /^Du · /);
  const details = descendants(doc.content, 'details');
  assert.equal(details.length, 1);
  assert.equal(details[0].children.length, 1);
  assert.match(details[0].children[0].textContent, /File read.*Ergebnis aufklappen/);
  assert.equal(doc['session-status'].textContent, 'Session · Bereit');
  choose(doc, 'owner/64');
  await settle();
  assert.deepEqual(current(doc), ['owner/64']);
  assert.deepEqual(calls, [[0, 'owner/0'], [0, 'owner/64']]);
});

test('projected events without their optional fields still render literally', async () => {
  const doc = archiveFixture();
  await browse({
    async Sessions() { return [{ id: 'owner/plain' }]; },
    async OpenSession() {
      return { token: 0, events: [
        { kind: 'answering' },
        { kind: 'step', tool: 'files.read' },
        { kind: 'step' },
        { kind: 'asked' },
        { kind: 'queued' },
      ] };
    },
  }, doc);
  assert.deepEqual(descendants(doc.content, 'summary').map(item => item.textContent), ['files.read', 'Tool']);
  assert.deepEqual(visibleTexts(doc.content), ['Nachricht eingereiht']);
  assert.equal(doc['question-panel'].hidden, false);
  assert.equal(doc['question-text'].textContent, '');
  assert.deepEqual(doc['question-options'].children, []);
});

test('a tool call that starts again after its result opens a second detail', async () => {
  const doc = archiveFixture();
  await browse({
    async Sessions() { return [{ id: 'owner/again' }]; },
    async OpenSession() {
      return { token: 0, events: [
        { kind: 'step', tool: 'files.read', text: 'Reading file', call_id: 'call-9', done: false },
        { kind: 'step', tool: 'files.read', text: 'File read', call_id: 'call-9', done: true },
        { kind: 'step', tool: 'files.read', text: 'Reading it again', call_id: 'call-9', done: false },
      ] };
    },
  }, doc);
  assert.deepEqual(descendants(doc.content, 'summary').map(item => item.textContent),
    ['File read · Ergebnis aufklappen', 'Reading it again · Ergebnis aufklappen']);
  assert.equal(descendants(doc.content, 'details').length, 2);
});

test('tool arguments and content load only on open, once, and huge results expand and collapse', async () => {
  const doc = archiveFixture();
  const args = '{"path":"<script>literal</script>"}';
  const content = '<b>literal result & unsafe</b>'.repeat(1000);
  const pending = deferred();
  const calls = [];
  await browse({
    async Sessions() { return [{ id: 'owner/tool' }]; },
    async OpenSession() { return { token: 0, events: toolEvents }; },
    ToolResult(...args) { calls.push(args); return pending.promise; },
  }, doc);
  const [detail] = descendants(doc.content, 'details');
  assert.deepEqual(calls, []);
  toggle(detail, false);
  assert.deepEqual(calls, []);
  toggle(detail, true);
  toggle(detail, true);
  assert.deepEqual(calls, [[0, 'owner/tool', 'call-1']]);
  assert.equal(descendants(detail, 'pre').length, 0);
  pending.resolve({ arguments: args, content, found: true });
  await settle();
  const [argumentsText, result] = descendants(detail, 'pre');
  assert.equal(argumentsText.textContent, args);
  assert.equal(result.textContent, content.slice(0, 8000));
  const [expand] = descendants(detail, 'button');

  for (let i = 0; i < 2; i++) {
    expand.dispatchEvent(new Event('click'));
    assert.equal(result.textContent, content);
    assert.equal(expand.textContent, 'Text einklappen');
    expand.dispatchEvent(new Event('click'));
    assert.equal(result.textContent, content.slice(0, 8000));
    assert.match(expand.textContent, /Vollständigen Text anzeigen/);
    toggle(detail, false);
    toggle(detail, true);
    await settle();
    assert.equal(calls.length, 1);
    assert.equal(descendants(detail, 'pre').length, 2);
  }
  assert.deepEqual(argumentsText.children, []);
  assert.deepEqual(result.children, []);
});

test('tool detail errors and not-yet-found results retry after close and reopen', async () => {
  const doc = archiveFixture();
  let calls = 0;
  await browse({
    async Sessions() { return [{ id: 'owner/tool' }]; },
    async OpenSession() { return { token: 0, events: toolEvents }; },
    async ToolResult() {
      calls++;
      if (calls === 1) throw new Error('detail failed');
      if (calls === 2) return { found: false };
      return { found: true, arguments: '', content: '' };
    },
  }, doc);
  const [detail] = descendants(doc.content, 'details');
  toggle(detail, true);
  await settle();
  assert.match(detail.children[1].textContent, /detail failed.*Wiederholen/);
  assert.equal(descendants(detail, 'pre').length, 0);
  toggle(detail, false);
  toggle(detail, true);
  await settle();
  assert.match(detail.children[1].textContent, /Noch kein gespeichertes Ergebnis/);
  toggle(detail, false);
  toggle(detail, true);
  await settle();
  assert.equal(descendants(detail, 'pre')[0].textContent, '(Leeres Ergebnis)');
  assert.equal(detail.children[1].textContent, '');
  toggle(detail, false);
  toggle(detail, true);
  assert.equal(calls, 3);
});

test('stale tool detail success or failure is ignored after session or profile switch', async () => {
  for (const switchProfile of [false, true]) {
    for (const reject of [false, true]) {
      const doc = archiveFixture();
      const old = deferred();
      let calls = 0;
      await browse({
        async Sessions(profile) { return profile ? [{ id: 'peer/new' }] : [{ id: 'owner/old' }, { id: 'owner/new' }]; },
        async OpenSession(_, id) { return { token: 0, events: id === 'owner/old' ? toolEvents : [{ kind: 'answer', text: 'latest' }] }; },
        ToolResult() { calls++; return old.promise; },
      }, doc);
      const [detail] = descendants(doc.content, 'details');
      toggle(detail, true);
      const loadingNote = detail.children[1].textContent;
      if (switchProfile) select(doc, 1);
      else choose(doc, 'owner/new');
      await settle();
      if (reject) old.reject(new Error('stale details'));
      else old.resolve({ found: true, arguments: 'stale args', content: 'stale result' });
      await settle();
      assert.deepEqual(descendants(doc.content, 'pre').map(text => text.textContent), ['latest']);
      assert.equal(descendants(detail, 'pre').length, 0);
      assert.equal(detail.children[1].textContent, loadingNote);
      toggle(detail, false);
      toggle(detail, true);
      assert.equal(calls, 1);
      assert.equal(doc.error.hidden, true);
    }
  }
});

test('live polling appends events, pairs start/done across batches and exposes connection failure', async () => {
  const doc = archiveFixture();
  const polls = [];
  const pending = [];
  let detailReads = 0;
  await browse({
    async Sessions() { return [{ id: 'owner/live' }]; },
    async OpenSession() { return { token: 17, events: [{ kind: 'message', text: 'hello' }] }; },
    PollSession(token) { polls.push(token); const next = deferred(); pending.push(next); return next.promise; },
    async ToolResult() { detailReads++; return { found: true, arguments: 'args', content: 'result' }; },
  }, doc);
  assert.deepEqual(polls, [17]);
  pending[0].resolve([toolEvents[0], { kind: 'answering', text: 'A' }]);
  await settle();
  assert.deepEqual(polls, [17, 17]);

  pending[1].resolve([toolEvents[1], { kind: 'answer', text: 'done' }]);
  await settle();
  assert.deepEqual(polls, [17, 17, 17]);
  const [detail] = descendants(doc.content, 'details');
  assert.equal(descendants(doc.content, 'summary')[0].textContent, 'File read · Ergebnis aufklappen');
  toggle(detail, true);
  await settle();
  assert.equal(detailReads, 1);
  assert.equal(descendants(detail, 'pre')[0].textContent, 'args');
  pending[2].reject(new Error('disconnected'));
  await settle();
  assert.equal(doc.error.hidden, false);
  assert.match(doc.error.textContent, /Verbindung.*disconnected/);
});

test('closed live streams stop polling instead of spinning', async () => {
  const doc = archiveFixture();
  let polls = 0;
  await browse({
    async Sessions() { return [{ id: 'owner/closed' }]; },
    async OpenSession() { return { token: 5, events: [] }; },
    async PollSession() { polls++; return null; },
  }, doc);
  await settle();
  assert.equal(polls, 1);
  assert.equal(doc.error.hidden, false);
  assert.match(doc.error.textContent, /Verbindung.*beendet/);
});

test('a live poll without events leaves the transcript alone and keeps following', async () => {
  const doc = archiveFixture();
  let polls = 0;
  await browse({
    async Sessions() { return [{ id: 'owner/quiet' }]; },
    async OpenSession() { return { token: 4, events: [{ kind: 'message', text: 'hello' }] }; },
    PollSession() { polls++; return polls === 1 ? undefined : new Promise(() => {}); },
  }, doc);
  await settle();
  assert.equal(polls, 2);
  assert.deepEqual(visibleTexts(doc.content), ['hello']);
  assert.equal(doc.error.hidden, true);
});

test('pending live poll responses and failures cannot affect a newer session or profile', async () => {
  for (const switchProfile of [false, true]) {
    for (const reject of [false, true]) {
      const doc = archiveFixture();
      const pending = [];
      await browse({
        async Sessions(profile) { return profile ? [{ id: 'peer/new' }] : [{ id: 'owner/old' }, { id: 'owner/new' }]; },
        async OpenSession(_, id) {
          return id === 'owner/old'
            ? { token: 7, events: [{ kind: 'message', text: 'old' }] }
            : { token: 3, events: [{ kind: 'answer', text: 'latest' }] };
        },
        PollSession() { const next = deferred(); pending.push(next); return next.promise; },
      }, doc);
      assert.equal(pending.length, 1);
      if (switchProfile) select(doc, 1);
      else choose(doc, 'owner/new');
      await settle();
      assert.equal(pending.length, switchProfile ? 1 : 2);
      if (reject) pending[0].reject(new Error('stale poll'));
      else pending[0].resolve([{ kind: 'message', text: 'stale' }, toolEvents[0]]);
      await settle();
      assert.deepEqual(visibleTexts(doc.content), ['latest']);
      assert.equal(doc.error.hidden, true);
    }
  }
});

test('large replay yields to session switching and cancels the remaining old events', async () => {
  const doc = archiveFixture();
  const events = Array.from({ length: 120 }, (_, index) => ({ kind: 'message', text: 'alt ' + index }));
  const loading = browse({
    async Sessions() {
      return [{ id: 'owner/large', modified: '2026-10-15T10:00:00Z' }, { id: 'owner/other', modified: '2026-10-15T09:00:00Z' }];
    },
    async OpenSession(_, id) {
      return { token: 0, events: id === 'owner/large' ? events : [{ kind: 'answer', text: 'neu' }] };
    },
  }, doc);
  await new Promise(resolve => setTimeout(resolve, 0));
  // The replay pauses after its first batch, so only 40 of 120 events are on screen.
  assert.equal(doc.content.children.length, 40);
  choose(doc, 'owner/other');
  await loading;
  await settle();
  assert.deepEqual(visibleTexts(doc.content), ['neu']);
  assert.equal(descendants(doc.content, 'section').length, 1);
});

function typeDraft(doc, text) {
  doc.draft.value = text;
  doc.draft.dispatchEvent(new Event('input'));
}
function enter(doc, extra = {}) {
  const event = new Event('keydown', { cancelable: true });
  Object.assign(event, { key: 'Enter', shiftKey: false, isComposing: false }, extra);
  doc.draft.dispatchEvent(event);
  return event;
}
function submit(doc) {
  doc.composer.dispatchEvent(new Event('submit', { cancelable: true }));
}

test('composer sends Enter/form, not Shift+Enter, IME, blank or duplicate submissions', async () => {
  const doc = archiveFixture();
  const calls = [];
  const pending = deferred();
  await browse({
    async Sessions() { return [{ id: 'owner/a' }, { id: 'peer/a' }]; },
    async OpenSession() { return { events: [] }; },
    SendMessage(...args) { calls.push(args); return calls.length === 1 ? pending.promise : Promise.resolve(); },
  }, doc);
  assert.equal(doc.draft.disabled, false);
  typeDraft(doc, '  ');
  assert.equal(doc.send.disabled, true);
  enter(doc);
  typeDraft(doc, 'hello\nworld');
  assert.equal(enter(doc, { shiftKey: true }).defaultPrevented, false);
  assert.equal(enter(doc, { isComposing: true }).defaultPrevented, false);
  assert.deepEqual(calls, []);
  assert.equal(enter(doc).defaultPrevented, true);
  assert.equal(doc.send.disabled, true);
  submit(doc);
  assert.deepEqual(calls, [[0, 'owner/a', 'hello\nworld']]);
  assert.match(doc['send-status'].textContent, /gesendet/);
  typeDraft(doc, 'new draft while sending');
  pending.resolve();
  await settle();
  assert.equal(doc.draft.value, 'new draft while sending');
  submit(doc);
  await settle();
  assert.equal(doc.draft.value, '');
  choose(doc, 'peer/a');
  await settle();
  assert.equal(doc.draft.disabled, true);
  typeDraft(doc, 'not allowed');
  submit(doc);
  assert.equal(doc.send.disabled, true);
  assert.equal(calls.length, 2);
});

test('send outcomes and drafts stay with their session/profile, including returning while pending', async () => {
  for (const switchProfile of [false, true]) for (const reject of [false, true]) {
    const doc = archiveFixture();
    const pending = deferred();
    const calls = [];
    await browse({
      async Sessions() { return [{ id: 'owner/a' }, { id: 'owner/b' }]; },
      async OpenSession() { return { events: [] }; },
      SendMessage(...args) { calls.push(args); return pending.promise; },
    }, doc);
    typeDraft(doc, 'original');
    submit(doc);
    if (switchProfile) select(doc, 1);
    else choose(doc, 'owner/b');
    await settle();
    typeDraft(doc, 'other draft');
    if (reject) pending.reject(new Error('offline'));
    else pending.resolve();
    await settle();
    assert.equal(doc.draft.value, 'other draft');
    assert.equal(doc['send-error'].hidden, true);
    if (switchProfile) select(doc, 0);
    else choose(doc, 'owner/a');
    await settle();
    assert.equal(doc.draft.value, reject ? 'original' : '');
    assert.equal(doc['send-error'].hidden, !reject);
    if (reject) assert.match(doc['send-error'].textContent, /offline/);
    assert.deepEqual(calls, [[0, 'owner/a', 'original']]);
  }
  const doc = archiveFixture();
  const pending = deferred();
  await browse({
    async Sessions() { return [{ id: 'owner/a' }, { id: 'owner/b' }]; },
    async OpenSession() { return { events: [] }; },
    SendMessage() { return pending.promise; },
  }, doc);
  typeDraft(doc, 'hello');
  submit(doc);
  choose(doc, 'owner/b');
  await settle();
  choose(doc, 'owner/a');
  await settle();
  assert.equal(doc.send.disabled, true);
  pending.reject(new Error('retry me'));
  await settle();
  assert.equal(doc.send.disabled, false);
  assert.equal(doc.draft.value, 'hello');
  assert.match(doc['send-error'].textContent, /retry me/);
});

test('questions render literally with recommendations, free replies, queued and resolved states', async () => {
  const doc = archiveFixture();
  const pending = deferred();
  const poll = deferred();
  const calls = [];
  let polls = 0;
  const question = { kind: 'asked', question: { text: '<b>Choose?</b>', options: ['<yes>', 'no'], recommended: '<yes>' } };
  await browse({
    async Sessions() { return [{ id: 'owner/a' }, { id: 'peer/a' }]; },
    async OpenSession(_, id) { return { token: id.startsWith('owner/') ? 1 : 0, events: [question] }; },
    PollSession() { return ++polls === 1 ? poll.promise : new Promise(() => {}); },
    SendMessage(...args) { calls.push(args); return pending.promise; },
  }, doc);
  assert.deepEqual(visibleTexts(doc.content), ['<b>Choose?</b>']);
  const [yes, no] = doc['question-options'].children;
  assert.equal(yes.textContent, '<yes> · Empfohlen');
  typeDraft(doc, 'free form draft');
  yes.dispatchEvent(new Event('click'));
  no.dispatchEvent(new Event('click'));
  assert.deepEqual(calls, [[0, 'owner/a', '<yes>']]);
  assert.equal(yes.disabled, true);
  pending.resolve();
  await settle();
  assert.equal(doc.draft.value, 'free form draft');
  submit(doc);
  await settle();
  assert.deepEqual(calls[1], [0, 'owner/a', 'free form draft']);
  poll.resolve([{ kind: 'queued', text: 'Queued reply' }, { kind: 'asked', done: true, text: '<yes>' },
    { kind: 'asked', question: { text: 'Free reply?' } }]);
  await settle();
  assert.deepEqual(visibleTexts(doc.content), ['<b>Choose?</b>', 'Queued reply', '<yes>', 'Free reply?']);
  assert.equal(yes.disabled, true);
  choose(doc, 'peer/a');
  await settle();
  assert.deepEqual(visibleTexts(doc.content), ['<b>Choose?</b>']);
  assert.equal(doc.draft.disabled, true);
  await settle();
  assert.equal(doc['question-panel'].hidden, true);
  assert.deepEqual(doc['question-options'].children, []);
  yes.dispatchEvent(new Event('click'));
  assert.equal(calls.length, 2);
});

test('permission choices send exact values, retry failures and clear on completion', async () => {
  for (const option of ['Run it this time', 'Always allow it', "Don't run it"]) {
    const doc = archiveFixture();
    const poll = deferred();
    const calls = [];
    let first = true;
    await browse({
      async Sessions() { return [{ id: 'owner/permission' }]; },
      async OpenSession() { return { token: 1, events: [{ kind: 'asked', question: {
        text: 'Allow bash: echo <safe>', options: ['Run it this time', 'Always allow it', "Don't run it"],
      } }] }; },
      PollSession() { return first ? (first = false, poll.promise) : new Promise(() => {}); },
      async SendMessage(...args) {
        calls.push(args);
        if (calls.length === 1) throw new Error('offline');
      },
    }, doc);
    const button = doc['question-options'].children.find(button => button.textContent === option);
    assert.equal(doc['question-text'].textContent, 'Allow bash: echo <safe>');
    button.dispatchEvent(new Event('click'));
    await settle();
    assert.equal(button.disabled, false);
    assert.match(doc['send-error'].textContent, /offline/);
    button.dispatchEvent(new Event('click'));
    await settle();
    assert.deepEqual(calls, [[0, 'owner/permission', option], [0, 'owner/permission', option]]);
    assert.equal(button.disabled, true);
    poll.resolve([{ kind: 'asked', done: true, text: option }]);
    await settle();
    assert.equal(doc['question-panel'].hidden, true);
    assert.deepEqual(doc['question-options'].children, []);
  }
});

test('historical and canceled questions leave no active dialog; switching invalidates old buttons', async () => {
  const doc = archiveFixture();
  let sends = 0;
  const q = { kind: 'asked', question: { text: 'Continue?', options: ['Yes', 'No'] } };
  await browse({
    async Sessions() { return [{ id: 'owner/open' }, { id: 'owner/history' }, { id: 'owner/canceled' }]; },
    async OpenSession(_, id) {
      return { token: 0, events: [q, ...(id.endsWith('history') ? [{ kind: 'asked', done: true, text: 'Yes' }] :
        id.endsWith('canceled') ? [{ kind: 'canceled' }] : [])] };

    },
    SendMessage() { sends++; return new Promise(() => {}); },
  }, doc);
  assert.equal(doc['question-panel'].hidden, false);
  const old = doc['question-options'].children[0];
  choose(doc, 'owner/history');
  await settle();

  assert.equal(doc['question-panel'].hidden, true);
  old.dispatchEvent(new Event('click'));
  assert.equal(sends, 0);
  choose(doc, 'owner/canceled');
  await settle();
  assert.equal(doc['question-panel'].hidden, true);
});

test('closing a question while its button still has focus returns focus to the draft', async () => {
  const doc = archiveFixture();
  const poll = deferred();
  let polls = 0;
  await browse({
    async Sessions() { return [{ id: 'owner/focus' }]; },
    async OpenSession() { return { token: 1, events: [{ kind: 'asked', question: { text: 'Weiter?', options: ['Ja'] } }] }; },
    PollSession() { return ++polls === 1 ? poll.promise : new Promise(() => {}); },
  }, doc);
  assert.equal(doc['question-panel'].hidden, false);
  doc.activeElement = doc['question-options'].children[0];
  poll.resolve([{ kind: 'canceled' }]);
  await settle();
  assert.equal(doc['question-panel'].hidden, true);
  assert.equal(doc.draft.focused, true);
});

test('reopening while a choice is sending keeps it locked; disconnect clears the dialog', async () => {
  const doc = archiveFixture();
  const sent = deferred();
  const polls = [];
  const question = { kind: 'asked', at: '2026-10-15T12:00:00Z', question: { text: 'Allow?', options: ['Yes', 'No'] } };
  let sends = 0;
  await browse({
    async Sessions() { return [{ id: 'owner/a' }]; },
    async OpenSession() { return { token: 1, events: [question] }; },
    PollSession() { const p = deferred(); polls.push(p); return p.promise; },
    SendMessage() { sends++; return sent.promise; },
  }, doc);
  doc['question-options'].children[0].dispatchEvent(new Event('click'));
  choose(doc, 'owner/a');
  await settle();
  sent.resolve();
  await settle();
  const button = doc['question-options'].children[0];
  assert.equal(button.disabled, true);
  button.dispatchEvent(new Event('click'));
  assert.equal(sends, 1);
  polls.at(-1).reject(new Error('offline'));
  await settle();
  assert.equal(doc['question-panel'].hidden, true);
  assert.equal(doc.draft.disabled, true);
});

function dialogElements(doc) {
  const dialog = doc.body.children.find(e => e.className === 'model-dialog');
  const [header, , form] = dialog.children;
  const [back, title, close] = header.children;
  const confirm = form.children[4];
  const [keep, discard, saveAndLeave] = confirm.children[1].children;
  return { dialog, form, title, back, close, access: form.children[0].children[0], model: form.children[1].children[0],
    status: form.children[2], error: form.children[3], confirm, keep, discard, saveAndLeave,
    actions: form.children[5], save: form.children[5].children[0] };
}

test('reusable model dialog loads and saves for its explicit profile, and opens again', async () => {
  const doc = documentFixture();
  const saved = [];
  const loaded = [];
  const picker = createModelDialog({
    Models: async index => { loaded.push(index); return { access: 'a', model: 'current', accesses: [
      { id: 'a', label: 'Mein Zugang', models: ['other'] },
    ] }; },
    SetModel: async (...args) => { saved.push(args); },
  }, doc);
  await picker.open(1, 'Arbeit');
  const el = dialogElements(doc);
  assert.equal(el.dialog.open, true);
  assert.equal(el.access.value, 'a');
  assert.equal(el.model.value, 'current');
  assert.deepEqual(el.model.children.map(item => item.value), ['current', 'other']);
  assert.equal(el.save.disabled, false);
  el.model.value = 'other';
  el.form.dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  assert.deepEqual(saved, [[1, 'a', 'other']]);
  assert.equal(el.dialog.open, false);
  await picker.open(0, 'Privat');
  assert.deepEqual(loaded, [1, 0]);
  assert.equal(doc.body.children.length, 1);
  assert.equal(el.close.disabled, false);
});

test('model dialog ignores a response after close and reports load/save errors', async () => {
  const doc = documentFixture();
  let resolve;
  let request = new Promise(done => { resolve = done; });
  const picker = createModelDialog({
    Models: () => request,
    SetModel: async () => { throw new Error('Speichern fehlgeschlagen'); },
  }, doc);
  const pending = picker.open(0);
  const el = dialogElements(doc);
  el.close.dispatchEvent(new Event('click'));
  resolve({ access: 'a', model: 'stale', accesses: [{ id: 'a', models: ['stale'] }] });
  await pending;
  assert.equal(el.access.children.length, 0);
  request = Promise.reject(new Error('Offline'));
  await picker.open(1);
  assert.match(el.error.textContent, /Offline/);
  assert.equal(el.save.disabled, true);
  el.dialog.close();
  request = Promise.resolve({ access: 'a', model: 'm', accesses: [{ id: 'a', label: 'A', models: ['m'] }] });
  await picker.open(1);
  el.form.dispatchEvent(new Event('submit', { cancelable: true }));
  assert.equal(el.close.disabled, true);
  await settle();
  assert.equal(el.dialog.open, true);
  assert.match(el.error.textContent, /Speichern fehlgeschlagen/);
  assert.equal(el.save.disabled, false);
});

test('a second open is ignored and an access whose model list failed is reported', async () => {
  const doc = documentFixture();
  const loaded = [];
  const picker = createModelDialog({
    Models: async index => {
      loaded.push(index);
      return { access: 'b', model: 'old', accesses: [
        { id: 'a', label: 'A', models: ['other'] },
        { id: 'b', label: 'B', models: ['old'], error: 'Zeitüberschreitung' },
      ] };
    },
    SetModel: async () => {},
  }, doc);
  await picker.open(0, 'Privat');
  await picker.open(1, 'Arbeit');
  assert.deepEqual(loaded, [0]);
  const el = dialogElements(doc);
  assert.equal(el.dialog.open, true);
  assert.equal(el.access.value, 'b');
  assert.equal(el.model.value, 'old');
  assert.equal(el.error.hidden, false);
  assert.match(el.error.textContent, /Modellliste konnte nicht vollständig geladen werden: Zeitüberschreitung/);
  assert.equal(el.save.disabled, false);
});

test('a running save refuses a second submit and Escape, and locks the dialog', async () => {
  const doc = documentFixture();
  const saved = deferred();
  const calls = [];
  const picker = createModelDialog({
    Models: async () => ({ access: 'a', model: 'm', accesses: [{ id: 'a', label: 'A', models: ['m', 'n'] }] }),
    SetModel: (...args) => { calls.push(args); return saved.promise; },
  }, doc);
  await picker.open(0, 'Privat');
  const el = dialogElements(doc);
  el.model.value = 'n';
  el.form.dispatchEvent(new Event('submit', { cancelable: true }));
  assert.equal(el.close.disabled, true);
  assert.equal(el.status.textContent, 'Modell wird gespeichert …');
  el.form.dispatchEvent(new Event('submit', { cancelable: true }));
  const escape = new Event('cancel', { cancelable: true });
  el.dialog.dispatchEvent(escape);
  assert.equal(escape.defaultPrevented, true);
  assert.deepEqual(calls, [[0, 'a', 'n']]);
  saved.resolve();
  await settle();
  assert.equal(el.dialog.open, false);
  await picker.open(1, 'Arbeit');
  assert.equal(el.dialog.open, true);
  assert.equal(el.close.disabled, false);
});

test('appearance defaults to system and restores only valid saved preferences', () => {
  const doc = documentFixture(), stored = new Map();
  doc.defaultView.localStorage = { getItem: key => stored.get(key), setItem: (key, value) => stored.set(key, value) };
  const theme = createThemePreference(doc);
  assert.equal(theme.value, 'system');
  assert.equal(doc.documentElement.getAttribute('data-theme'), 'system');
  theme.set('dark');
  assert.equal(doc.documentElement.getAttribute('data-theme'), 'dark');
  assert.equal(createThemePreference(doc).value, 'dark', 'reopening restores the preference');
  theme.set('light');
  assert.equal(createThemePreference(doc).value, 'light');
  theme.set('system');
  assert.equal(createThemePreference(doc).value, 'system');
  theme.set('invalid');
  assert.equal(theme.value, 'system');
  doc.defaultView.localStorage.getItem = () => 'invalid';
  assert.equal(createThemePreference(doc).value, 'system', 'unknown saved values follow the OS');
});

test('appearance still switches if local storage is unavailable', () => {
  const doc = documentFixture();
  Object.defineProperty(doc.defaultView, 'localStorage', { get() { throw new Error('Storage blocked'); } });
  const theme = createThemePreference(doc);
  assert.equal(theme.value, 'system');
  theme.set('dark');
  assert.equal(theme.value, 'dark');
  assert.equal(doc.documentElement.getAttribute('data-theme'), 'dark');
});

test('the gear changes appearance immediately and keeps it across settings and profile changes', async () => {
  const doc = documentFixture();
  await start({
    InitialState: async () => ({ profiles: [{ name: 'Privat' }, { name: 'Arbeit' }], selected: 0 }),
    Sessions: async () => [],
  }, doc);
  doc.settings.dispatchEvent(new Event('click'));
  const dialog = doc.body.children.at(-1);
  const appearance = () => dialog.children.find(e => e.tagName === 'label').children[0];
  assert.equal(appearance().value, 'system');
  assert.deepEqual(appearance().children.map(e => [e.value, e.textContent]), [['system', 'System'], ['light', 'Hell'], ['dark', 'Dunkel']]);
  appearance().value = 'dark';
  appearance().dispatchEvent(new Event('change'));
  assert.equal(doc.documentElement.getAttribute('data-theme'), 'dark');
  dialog.close();
  select(doc, 1);
  await settle();
  doc.settings.dispatchEvent(new Event('click'));
  assert.equal(appearance().value, 'dark');
  appearance().value = 'light';
  appearance().dispatchEvent(new Event('change'));
  assert.equal(doc.documentElement.getAttribute('data-theme'), 'light');
  appearance().value = 'system';
  appearance().dispatchEvent(new Event('change'));
  assert.equal(doc.documentElement.getAttribute('data-theme'), 'system');
});

test('settings opens on the dialog, not with the focus ring on its first button', async () => {
  const doc = documentFixture();
  createSettingsDialog({}, {}, doc).open(0, 'node');
  const dialog = doc.body.children.at(-1);
  assert.equal(dialog.getAttribute('tabindex'), '-1');
  assert.equal(dialog.focused, true);
  assert.match(dialog.className, /\bsettings-dialog\b/);
  const css = await readFile(new URL('./frontend/style.css', import.meta.url), 'utf8');
  assert.match(css, /\.settings-dialog > button[^{]*\{[^}]*border-radius/);
});

test('settings icon navigates to the model dialog for the selected profile', async () => {
  const doc = documentFixture();
  const loaded = [];
  await start({
    InitialState: async () => ({ profiles: [{ name: 'Privat' }, { name: 'Arbeit' }], selected: 1 }),
    Sessions: async () => [],
    Models: async index => { loaded.push(index); return { accesses: [] }; },
  }, doc);
  doc.settings.dispatchEvent(new Event('click'));
  const settings = doc.body.children.at(-1);
  assert.equal(settings.open, true);
  settings.children.find(e => e.textContent === 'Model').dispatchEvent(new Event('click'));
  await settle();
  assert.deepEqual(loaded, [1]);
  assert.equal(dialogElements(doc).dialog.open, true);
  assert.match(dialogElements(doc).status.textContent, /Keine Zugänge/);
  // The arrow leads back to the settings it was opened from.
  const el = dialogElements(doc);
  assert.equal(el.back.hidden, false);
  el.back.dispatchEvent(new Event('click'));
  assert.equal(el.dialog.open, false);
  assert.equal(settings.open, true);
});

test('gateway navigation saves to captured profile, masks and clears credentials, and logs out', async () => {
  const doc = documentFixture(), saved = [], loaded = [];
  const picker = createSettingsDialog({
    Gateways: async index => { loaded.push(index); return [{ Name: 'fixture', Title: 'Matrix', Enabled: true, UserID: '@owner:example', AllowedUsers: ['@friend:example'], LoggedIn: true, Status: 'Ready' }]; },
    SaveGateway: async (...args) => { saved.push([args[0], args[1], { ...args[2] }]); },
  }, { open() { assert.fail('unexpected model picker'); } }, doc);
  picker.open(1, 'Work');
  const dialog = doc.body.children.at(-1);
  const click = text => dialog.children.find(e => e.textContent === text).dispatchEvent(new Event('click'));
  click('Gateways'); await settle(); click('Matrix');
  let form = dialog.children.at(-1);
  let password = form.children[3].children[0];
  assert.equal(password.type, 'password');
  password.value = 'secret';
  form.dispatchEvent(new Event('submit', { cancelable: true }));
  assert.equal(password.value, '');
  await settle();
  assert.equal(saved[0][0], 1); assert.equal(saved[0][1], 'fixture');
  assert.equal(saved[0][2].Password, 'secret'); assert.deepEqual(saved[0][2].AllowedUsers, ['@friend:example']);
  click('Matrix'); form = dialog.children.at(-1); password = form.children[3].children[0];
  password.value = 'discard'; form.children.find(e => e.textContent === 'Cancel').dispatchEvent(new Event('click'));
  assert.equal(password.value, ''); await settle(); assert.equal(saved.length, 1);
  click('Matrix'); form = dialog.children.at(-1);
  form.children.find(e => e.textContent === 'Logout').dispatchEvent(new Event('click')); await settle();
  assert.equal(saved[1][2].Logout, true); assert.equal(saved[1][2].Password, '');
  assert.ok(loaded.every(index => index === 1));
  click('Matrix'); password = dialog.children.at(-1).children[3].children[0]; password.value = 'close';
  dialog.close(); assert.equal(password.value, '');
});

test('gateway failures do not display credential-bearing backend errors', async () => {
  const doc = documentFixture();
  const picker = createSettingsDialog({
    Gateways: async () => [{ Name: 'fixture' }],
    SaveGateway: async () => { throw Error('secret'); },
  }, {}, doc);
  picker.open(0); const dialog = doc.body.children.at(-1);
  dialog.children.find(e => e.textContent === 'Gateways').dispatchEvent(new Event('click')); await settle();
  dialog.children.find(e => e.textContent === 'fixture').dispatchEvent(new Event('click'));
  const form = dialog.children.at(-1), password = form.children[3].children[0]; password.value = 'secret';
  form.dispatchEvent(new Event('submit', { cancelable: true })); await settle();
  assert.equal(password.value, '');
  assert.match(dialog.children.at(-1).textContent, /operation failed/);
  assert.ok(!dialog.children.at(-1).textContent.includes('secret'));
});

test('start goes on with the session the window before showed', async () => {
  const doc = documentFixture();
  const opened = [];
  await start({
    async InitialState() { return { profiles: [{ name: 'Luna' }], selected: 0, resume: 'owner/old' }; },
    async Sessions() { return [{ id: 'owner/new' }, { id: 'owner/old' }]; },
    async OpenSession(profile, id) { opened.push([profile, id]); return { token: 0, events: [] }; },
  }, doc);
  assert.deepEqual(opened, [[0, 'owner/old']]);
  assert.equal(doc['session-title'].textContent, 'owner/old');
});



test('gateway schema renders arbitrary fields without Matrix and clears all secrets', async () => {
  const doc = documentFixture(), saved = [], payloads = [];
  const item = { Name: 'custom', Status: 'Ready', Fields: [
    { Key: 'token', Label: 'Bot token', Kind: 'secret', Value: 'ignore' },
    { Key: 'region', Label: 'Region', Kind: 'text', Value: 'west' },
    { Key: 'members', Label: 'Members', Kind: 'list', List: ['one'] },
    { Key: 'quiet', Label: 'Quiet', Kind: 'bool', On: true },
    { Key: 'other', Label: 'Other secret', Kind: 'secret' },
  ] };
  const picker = createSettingsDialog({
    Gateways: async () => [item],
    SaveGateway: async (profile, name, change) => { payloads.push(change); saved.push(JSON.parse(JSON.stringify(change))); },
  }, {}, doc);
  picker.open(2);
  const dialog = doc.body.children.at(-1);
  const click = text => dialog.children.find(e => e.textContent === text).dispatchEvent(new Event('click'));
  click('Gateways'); await settle(); click('custom');
  let form = dialog.children.at(-1);
  const fields = () => form.children.filter(e => e.tagName === 'label').map(e => e.children[0]);
  // Fixture tag names are kept as supplied by createElement.
  let inputs = fields();
  assert.equal(inputs.length, 6);
  assert.equal(inputs[1].type, 'password'); assert.equal(inputs[1].value, '');
  assert.equal(inputs[5].type, 'password');
  assert.ok(!form.children.some(e => /Homeserver|User ID|Password/.test(e.textContent)));
  assert.ok(form.children.some(e => /INCLUDING permission answers, not just chat/.test(e.textContent)));
  inputs[0].checked = true; inputs[1].value = 'token-secret'; inputs[2].value = 'east';
  inputs[3].value = 'two, three\nfour'; inputs[4].checked = false; inputs[5].value = 'other-secret';
  form.dispatchEvent(new Event('submit', { cancelable: true }));
  assert.equal(inputs[1].value, ''); assert.equal(inputs[5].value, '');
  await settle();
  assert.deepEqual(saved[0].Values, { token: 'token-secret', region: 'east', quiet: 'false', other: 'other-secret' });
  assert.deepEqual(saved[0].Lists, { members: ['two', 'three', 'four'] });
  assert.equal(saved[0].Homeserver, undefined); assert.equal(saved[0].Password, undefined);
  assert.equal(payloads[0].Values.token, ''); assert.equal(payloads[0].Values.other, '');
  click('custom'); form = dialog.children.at(-1); inputs = fields();
  inputs[1].value = inputs[5].value = 'discard';
  form.children.find(e => e.textContent === 'Cancel').dispatchEvent(new Event('click'));
  assert.equal(inputs[1].value, ''); assert.equal(inputs[5].value, ''); await settle();
  assert.equal(saved.length, 1);
  click('custom'); form = dialog.children.at(-1); inputs = fields();
  inputs[1].value = inputs[5].value = 'logout';
  form.children.find(e => e.textContent === 'Logout').dispatchEvent(new Event('click')); await settle();
  assert.equal(saved[1].Logout, true); assert.equal(saved[1].Values.token, ''); assert.equal(saved[1].Values.other, '');
  click('custom'); form = dialog.children.at(-1); inputs = fields();
  inputs[1].value = inputs[5].value = 'close'; dialog.close();
  assert.equal(inputs[1].value, ''); assert.equal(inputs[5].value, '');
});

test('settings pages close with the X in the corner and go back with the arrow', async () => {
  const doc = documentFixture();
  createSettingsDialog({ Gateways: async () => [{ Name: 'matrix', Title: 'Matrix' }] }, {}, doc).open(0, 'node');
  const dialog = doc.body.children.at(-1);
  const header = () => dialog.children[0];
  const named = label => header().children.find(e => e.getAttribute('aria-label') === label);
  const title = () => header().children.find(e => e.tagName === 'h2').textContent;
  assert.equal(title(), 'Settings · node');
  assert.equal(named('Back'), undefined, 'the first page has nowhere to go back to');
  assert.equal(named('Close').textContent, '×');
  assert.ok(!dialog.children.some(e => e.textContent === 'Close'), 'no Close button besides the X');
  dialog.children.find(e => e.textContent === 'Gateways').dispatchEvent(new Event('click')); await settle();
  assert.equal(title(), 'Settings › Gateways');
  assert.equal(named('Back').textContent, '‹');
  assert.ok(!dialog.children.some(e => e.tagName === 'p' && !e.hidden), 'no empty status line before the list');
  dialog.children.find(e => e.textContent === 'Matrix').dispatchEvent(new Event('click'));
  assert.equal(title(), 'Settings › Gateways › Matrix');
  named('Back').dispatchEvent(new Event('click')); await settle();
  assert.equal(title(), 'Settings › Gateways');
  named('Back').dispatchEvent(new Event('click'));
  assert.equal(title(), 'Settings · node');
  named('Close').dispatchEvent(new Event('click'));
  assert.equal(dialog.open, false);
});

test('model dialog has an X and, opened from a page, a back arrow instead of a cancel button', async () => {
  const doc = documentFixture();
  const picker = createModelDialog({ Models: async () => ({ access: 'a', model: 'm', accesses: [{ id: 'a', label: 'A', models: ['m'] }] }) }, doc);
  await picker.open(0, 'Privat');
  const el = dialogElements(doc);
  assert.equal(el.title.textContent, 'Modell einstellen');
  assert.equal(el.close.getAttribute('aria-label'), 'Schließen');
  assert.equal(el.back.hidden, true, 'nowhere to go back to');
  assert.deepEqual(el.actions.children.map(b => b.textContent), ['Speichern']);
  assert.equal(el.dialog.focused, true);
  el.close.dispatchEvent(new Event('click'));
  assert.equal(el.dialog.open, false, 'nothing changed, so it closes at once');
});

test('model dialog asks to save or discard a change left by the X, back or Escape', async () => {
  const doc = documentFixture();
  const saved = [], backs = [];
  let refuse = false;
  const picker = createModelDialog({
    Models: async () => ({ access: 'a', model: 'm', accesses: [{ id: 'a', label: 'A', models: ['m', 'n'] }] }),
    SetModel: async (...args) => { if (refuse) throw new Error('Offline'); saved.push(args); },
  }, doc);
  const el = dialogElements(doc);
  const asking = () => !el.confirm.hidden && el.actions.hidden;
  const escape = () => { const e = new Event('cancel', { cancelable: true }); el.dialog.dispatchEvent(e); return e.defaultPrevented; };
  await picker.open(0, 'Privat', () => backs.push('settings'));
  el.model.value = 'n';
  el.close.dispatchEvent(new Event('click'));
  assert.equal(el.dialog.open, true);
  assert.ok(asking());
  el.keep.dispatchEvent(new Event('click'));
  assert.ok(!asking());
  assert.equal(el.model.value, 'n', 'editing goes on with the change');
  assert.equal(escape(), true, 'Escape asks as well');
  assert.ok(asking());
  assert.equal(escape(), true, 'and a second Escape takes the question back');
  assert.ok(!asking());
  el.back.dispatchEvent(new Event('click'));
  assert.ok(asking());
  el.discard.dispatchEvent(new Event('click'));
  assert.equal(el.dialog.open, false);
  assert.deepEqual(backs, ['settings'], 'discarding goes where the owner was going');
  assert.deepEqual(saved, []);
  // Saving from the question saves and goes on.
  await picker.open(0, 'Privat', () => backs.push('settings'));
  assert.ok(!asking(), 'a new open starts without the question');
  el.model.value = 'n';
  el.back.dispatchEvent(new Event('click'));
  el.saveAndLeave.dispatchEvent(new Event('click'));
  await settle();
  assert.deepEqual(saved, [[0, 'a', 'n']]);
  assert.equal(el.dialog.open, false);
  assert.deepEqual(backs, ['settings', 'settings']);
  // A save that fails keeps the dialog open with the error.
  refuse = true;
  await picker.open(0, 'Privat');
  el.model.value = 'n';
  el.close.dispatchEvent(new Event('click'));
  el.saveAndLeave.dispatchEvent(new Event('click'));
  await settle();
  assert.equal(el.dialog.open, true);
  assert.ok(!asking());
  assert.match(el.error.textContent, /Offline/);
  // Changed back to what was shown, there is nothing to ask about.
  el.model.value = 'm';
  el.close.dispatchEvent(new Event('click'));
  assert.equal(el.dialog.open, false);
});

test('a history that grows or wraps anew stays at its end; expanding something is reading', async () => {
  const doc = archiveFixture();
  const observed = [];
  doc.defaultView.ResizeObserver = class { constructor(run) { this.run = run; } observe(target) { observed.push([target, this.run]); } };
  const page = doc.transcript;
  page.scrollHeight = 3200;
  page.clientHeight = 900;
  await browse({
    async Sessions() { return [{ id: 'owner/live' }]; },
    async OpenSession() { return { token: 0, events: [] }; },
  }, doc);
  assert.deepEqual(observed.map(([target]) => target), [doc.transcript, doc.content]);
  const resized = () => { for (const [, run] of observed) run([]); };
  assert.equal(page.scrollTop, 2300);
  // The sidebar slides in: the lines wrap anew and the history gets taller.
  page.scrollHeight = 3600;
  resized();
  assert.equal(page.scrollTop, 2700, 'the end stays in view');
  // Expanding a text or a tool call is reading: the history stays where it is.
  const expand = new Element('button');
  expand.closest = () => expand;
  const click = new Event('click');
  Object.defineProperty(click, 'target', { value: expand });
  doc.content.dispatchEvent(click);
  page.scrollHeight = 4400;
  resized();
  assert.equal(page.scrollTop, 2700);
});

test('empty polls and output that does not grow the history leave it alone, so a bounce is not taken back', async () => {
  const { doc, page, pending } = await liveSession();
  const writes = [];
  let top = page.scrollTop;
  Object.defineProperty(page, 'scrollTop', { get: () => top, set(value) { writes.push(value); top = value; } });
  pending[0].resolve([]);
  await settle();
  pending[1].resolve([]);
  await settle();
  assert.deepEqual(writes, [], 'an empty poll moves nothing');
  const append = doc.content.append;
  doc.content.append = child => { doc.content.children.push(child); };
  pending[2].resolve([{ kind: 'answer', text: 'same height' }]);
  await settle();
  assert.deepEqual(writes, [], 'output that does not grow the history moves nothing');
  doc.content.append = append;
  pending[3].resolve([{ kind: 'answer', text: 'grows' }]);
  await settle();
  assert.equal(writes.length, 1, 'output that grows it is followed');
});

test('the thread opens by default, including an empty profile, through the ordinary session bridge', async () => {
  for (const entries of [[], [{ id: 'owner/topic', title: 'Topic' }]]) {
    const doc = archiveFixture(), opened = [], sent = [];
    await start({
      InitialState: async () => ({ profiles: [{ name: 'Luna' }], selected: 0, thread: 'owner/thread' }),
      Sessions: async () => entries,
      OpenSession: async (...args) => { opened.push(args); return { token: 0, events: [] }; },
      SendMessage: async (...args) => { sent.push(args); },
    }, doc);
    assert.deepEqual(opened, [[0, 'owner/thread']]);
    assert.equal(doc['back-to-thread'].hidden, true);
    assert.equal(doc['session-title'].textContent, 'Thread');
    assert.equal(doc.draft.disabled, false);
    typeDraft(doc, 'two different topics'); submit(doc); await settle();
    assert.deepEqual(sent, [[0, 'owner/thread', 'two different topics']]);
  }
});

test('restart resumes a thread or explicit session; missing resumed sessions return to the thread', async () => {
  for (const [resume, expected] of [['owner/thread', 'owner/thread'], ['owner/topic', 'owner/topic'], ['owner/gone', 'owner/thread']]) {
    const doc = archiveFixture(), opened = [];
    await browse({
      Sessions: async () => [{ id: 'owner/topic' }],
      OpenSession: async (...args) => { opened.push(args); return { token: 0, events: [] }; },
    }, doc, resume, 'owner/thread');
    assert.deepEqual(opened, [[0, expected]]);
  }
});

test('a failed session list does not prevent opening the thread', async () => {
  const doc = archiveFixture();
  await browse({
    Sessions: async () => { throw Error('list unavailable'); },
    OpenSession: async (_, id) => { assert.equal(id, 'owner/thread'); return { token: 0, events: [] }; },
  }, doc, '', 'owner/thread');
  assert.equal(doc.draft.disabled, false);
  assert.equal(doc['session-title'].textContent, 'Thread');
  assert.match(doc.error.textContent, /list unavailable/);
});

test('thread work, relayed questions, replies and stopping use the shared session events', async () => {
  const doc = archiveFixture(), pending = [], sent = [], stopped = [];
  await browse({
    Sessions: async () => [],
    OpenSession: async () => ({ token: 1, notice: 'keeps project', events: [
      { kind: 'message', text: 'two topics' },
      { kind: 'answer', text: 'first result' },
      { kind: 'directory', text: '/project' },
      { kind: 'working', n: 3, own: true },
    ] }),
    PollSession: () => { const d = deferred(); pending.push(d); return d.promise; },
    SendMessage: async (...args) => { sent.push(args); },
    StopSession: async (...args) => { stopped.push(args); },
  }, doc, '', 'owner/thread');
  assert.match(doc['session-status'].textContent, /Arbeitet.*2 aktive Sessions/);
  assert.equal(doc['session-meta'].textContent, '/project');
  assert.equal(doc['send-status'].textContent, 'keeps project');
  assert.equal(doc.stop.disabled, false);
  pending[0].resolve([{ kind: 'working', n: 2, own: false }, { kind: 'asked', question: { text: 'Which topic?', options: ['first', 'second'] } }]);
  await settle();
  assert.match(doc['session-status'].textContent, /Braucht deine Antwort.*2 aktive Sessions/);
  doc['question-options'].children[0].dispatchEvent(new Event('click')); await settle();
  assert.deepEqual(sent, [[0, 'owner/thread', 'first']]);
  pending[1].resolve([{ kind: 'asked', done: true, text: 'first' }, { kind: 'answer', text: 'done' }]);
  await settle();
  assert.equal(doc['question-panel'].hidden, true);
  assert.match(doc['session-status'].textContent, /Arbeitet/);
  doc.stop.dispatchEvent(new Event('click')); await settle();
  assert.deepEqual(stopped, [[0, 'owner/thread']]);
  pending[2].resolve([{ kind: 'working', n: 0, own: false }]); await settle();
  assert.match(doc['session-status'].textContent, /Bereit.*0 aktive Sessions/);
  assert.equal(doc.stop.disabled, true);
});

test('late stop failures stay with the original session and peers cannot be stopped', async () => {
  const doc = archiveFixture(), stopping = deferred(), stopped = [];
  await browse({
    Sessions: async () => [{ id: 'peer-key/topic' }],
    OpenSession: async (_, id) => ({ token: 0, events: id === 'owner/thread' ? [{ kind: 'message', text: 'go' }, { kind: 'handed', text: 'topic' }, { kind: 'working', n: 1 }] : [] }),
    StopSession: (...args) => { stopped.push(args); return stopping.promise; },
  }, doc, '', 'owner/thread');
  doc.stop.dispatchEvent(new Event('click'));
  doc.stop.dispatchEvent(new Event('click'));
  assert.deepEqual(stopped, [[0, 'owner/thread']]);
  choose(doc, 'peer-key/topic'); await settle();
  assert.equal(doc.stop.disabled, true);
  stopping.reject(Error('cannot stop')); await settle();
  assert.equal(doc['send-error'].hidden, true);
  doc['back-to-thread'].dispatchEvent(new Event('click')); await settle();
  assert.match(doc['send-error'].textContent, /cannot stop/);
  assert.equal(doc.stop.disabled, false);
});

test('the context rail reuses explicitly loaded tool results and clears them when switching', async () => {
  const doc = archiveFixture(); let requests = 0;
  await browse({
    Sessions: async () => [{ id: 'owner/a' }, { id: 'owner/b' }],
    OpenSession: async (_, id) => ({token: 0, events: id === 'owner/a' ? [{kind: 'step', tool: 'read', call_id: 'call', done: true, text: 'read file'}]
      : id === 'owner/thread' ? [{kind: 'message', text: 'go'}, {kind: 'handed', text: 'a'}, {kind: 'handed', text: 'b'}] : []}),
    ToolResult: async () => { requests++; return {found: true, arguments: '<input>', content: '<output>'}; },
  }, doc, 'owner/a', 'owner/thread');
  assert.equal(requests, 0);
  assert.equal(doc['context-details'].children.length, 0);
  const detail = descendants(doc.content, 'details')[0];
  detail.open = true; detail.dispatchEvent(new Event('toggle')); await settle();
  assert.equal(requests, 1);
  assert.equal(doc['context-title'].textContent, '[read]');
  assert.deepEqual(visibleTexts(doc['context-details']), ['<input>', '<output>']);
  detail.open = false; detail.dispatchEvent(new Event('toggle'));
  detail.open = true; detail.dispatchEvent(new Event('toggle')); await settle();
  assert.equal(requests, 1, 'the inspector reuses the same request and cache');
  doc['back-to-thread'].dispatchEvent(new Event('click')); await settle();
  assert.deepEqual(visibleTexts(doc['context-details']), ['<input>', '<output>'], 'the result remains beside the central thread');
  assert.match(doc['context-note'].textContent, /owner\/a/);
  choose(doc, 'owner/b'); await settle();
  assert.equal(doc['context-details'].children.length, 0);
});

test('live session overview adds delegated topics without reopening the thread and ignores stale profiles', async () => {
  const doc = archiveFixture(), opened = [];
  let tick, refresh = false, late;
  doc.defaultView.setInterval = (run, ms) => { assert.equal(ms, 3000); tick = run; };
  await browse({
    Sessions: async profile => {
      if (late && profile === 0) return late.promise;
      return profile === 1 ? [{id: 'owner/other', running: true}] : refresh ? [{id: 'owner/idle'}, {id: 'owner/new', running: true}] : [];
    },
    OpenSession: async (...args) => { opened.push(args); return {token: 0, events: [{kind: 'message', text: 'go'}, {kind: 'handed', text: 'new'}, {kind: 'handed', text: 'idle'}, {kind: 'handed', text: 'other'}]}; },
  }, doc, '', 'owner/thread');
  refresh = true; await tick();
  // Beside the thread the sessions of the part in view show.
  assert.deepEqual(buttons(doc).map(b => b.title), ['owner/new', 'owner/idle']);
  assert.equal(buttons(doc)[0].getAttribute('data-running'), 'true');
  assert.deepEqual(opened, [[0, 'owner/thread']], 'refresh changes the list, not the watched conversation');
  late = deferred();
  const pending = tick();
  select(doc, 1); await settle();
  assert.deepEqual(opened.at(-1), [1, 'owner/thread']);
  late.resolve([{id: 'owner/stale', running: true}]); await pending;
  assert.deepEqual(buttons(doc).map(b => b.title), ['owner/other']);
});

test('the thread reads as in the terminal UI and the demo: you, the agent by name, the clock today', async () => {
  const doc = documentFixture();
  mount({ profiles: [{ name: 'luna' }], selected: 0 }, doc);
  const now = new Date();
  await browse({
    async Sessions() { return [{ id: 'owner/a', running: true, title: 'Matrix-Eingang' }]; },
    async OpenSession() { return { token: 0, events: [
      { kind: 'message', text: 'hi', at: now.toISOString() },
      { kind: 'answer', text: 'hallo', at: now.toISOString() },
      { kind: 'task', text: 'S/02 · Matrix-Eingang', at: '2026-01-01T12:00:00Z' },
    ] }; },
  }, doc);
  const [mine, theirs, task] = doc.content.children;
  const time = new Intl.DateTimeFormat('de-DE', { hour: '2-digit', minute: '2-digit' }).format(now);
  assert.equal(mine.children[0].textContent, 'Du · ' + time);
  assert.equal(theirs.children[0].textContent, 'luna · ' + time);
  assert.equal(task.className, 'message message-task');
  assert.match(task.children[0].textContent, /^↳ Aufgabe · 01\.01\.2026/);
  // A session tells its state and runs its bar while it works, without a made-up progress.
  const session = doc.sessions.children[0].children.find(e => e.tagName === 'button');
  assert.equal(session.getAttribute('data-running'), 'true');
  assert.match(session.children[1].textContent, /^Zustand: arbeitet · eigene/);
  assert.equal(session.children[2].className, 'session-bar');
});

test('the composer is one line behind a prompt with a round send button, as in the demo', async () => {
  const html = await readFile(new URL('./frontend/index.html', import.meta.url), 'utf8');
  assert.match(html, /<div class="composer-row">\s*<span class="composer-prompt" aria-hidden="true">›<\/span>\s*<textarea id="draft" rows="1"/);
  assert.match(html, /<button id="send" type="submit" aria-label="Senden"[^>]*><span aria-hidden="true">↑<\/span><\/button>/);
  const css = await readFile(new URL('./frontend/style.css', import.meta.url), 'utf8');
  // The fade is a still layer over the top of the history, not a mask on the
  // scrolling history, which WebKit composited anew each frame of a scroll.
  assert.match(html, /<\/div>\s*<div class="transcript-fade" aria-hidden="true"><\/div>\s*<\/div>\s*<div class="composer-dock">/);
  assert.match(css, /\.transcript-fade \{[^}]*position: absolute;[^}]*pointer-events: none;[^}]*background: linear-gradient\(to bottom, var\(--background\) 0, color-mix\(in srgb, var\(--background\) 70%, transparent\) 14%, transparent 100%\)/);
  assert.doesNotMatch(css, /mask-image/);
});

test('the fade at the top is off while the start of the thread is in view, so a short thread reads in full', async () => {
  const { doc } = await liveSession();
  const toggles = [];
  doc.transcript.classList = { toggle: (name, on) => toggles.push([name, on]) };
  const start = doc.marks.find(mark => mark.target === doc['transcript-start']);
  assert.deepEqual(start.options, { root: doc.transcript });
  start.run([{ isIntersecting: true }]);
  start.run([{ isIntersecting: false }]);
  assert.deepEqual(toggles, [['at-start', true], ['at-start', false]]);
  const css = await readFile(new URL('./frontend/style.css', import.meta.url), 'utf8');
  assert.match(css, /\.transcript-scroller\.at-start \+ \.transcript-fade \{ opacity: 0; \}/);
});

test('beside the thread show the sessions of the part in view, else "Noch keine"', async () => {
  const doc = archiveFixture();
  const now = new Date().toISOString();
  const stepsOf = { 'owner/aaaa1111-x': [{ kind: 'step', done: true, tool: 'browser_use', text: 'browser_use · navigate spec.matrix.org' }] };
  await browse({
    async Sessions() { return [{ id: 'owner/aaaa1111-x', title: 'Recherche', running: true }, { id: 'owner/bbbb2222-y', title: 'B' }, { id: 'owner/cccc3333-z', title: 'C' }]; },
    async OpenSession() { return { token: 0, events: [
      { kind: 'message', text: 'recherchier mal', at: now },
      { kind: 'handed', text: 'aaaa1111-x', at: now },
      { kind: 'answer', text: 'läuft', at: now },
      { kind: 'message', text: 'und B?', at: now },
      { kind: 'handed', text: 'bbbb2222', at: now },
      { kind: 'handed', text: 'bbbb2222', at: now },
    ] }; },
    async SessionSteps(profile, id) { return stepsOf[id] || []; },
  }, doc, '', 'owner/thread');
  await settle();
  const [band] = doc.marks.filter(mark => mark.options.rootMargin === '-45% 0px -45% 0px');
  const items = doc.content.children;
  // Scrolled up from the end: the part in the middle of the history counts.
  doc.marks.find(mark => mark.target === doc['transcript-end']).run([{ isIntersecting: false }]);
  // The first part is in the middle of the history: its new session shows, and its tool calls on the right.
  band.run([{ isIntersecting: true, target: items[0] }]);
  await settle();
  assert.deepEqual(buttons(doc).map(b => b.title), ['owner/aaaa1111-x']);
  assert.match(doc.sessions.children[0].textContent, /^T – /);
  assert.equal(doc['context-title'].textContent, '[Artefakte]');
  assert.match(doc['context-details'].children[0].children[1].textContent, /browser_use · navigate spec\.matrix\.org/);
  // Scrolled on to the second part, the session it addressed shows instead.
  band.run([{ isIntersecting: false, target: items[0] }, { isIntersecting: false, target: items[1] }, { isIntersecting: true, target: items[2] }]);
  assert.equal(items[2].exchange.sessions.size, 1, 'a session handed twice in a part counts once');
  await settle();
  assert.deepEqual(buttons(doc).map(b => b.title), ['owner/bbbb2222-y']);
  // Back at the end, the newest part is in view, though it never reached the middle.
  doc.marks.find(mark => mark.target === doc['transcript-end']).run([{ isIntersecting: true }]);
  await settle();
  assert.deepEqual(buttons(doc).map(b => b.title), ['owner/bbbb2222-y']);
});

// Review of #54: an opened session keeps the part of the thread and its
// siblings beside it; a session that ends is read once more; a render on its
// way does not write into the session selected meanwhile.
test('beside an opened session its siblings stay, ended sessions are read anew, stale artefacts are dropped', async () => {
  const doc = archiveFixture();
  let tick, running = true, slow = null;
  doc.defaultView.setInterval = (run) => { tick = run; };
  const now = new Date().toISOString();
  const reads = [];
  const list = () => [{ id: 'owner/aaaa1111-x', title: 'A', running }, { id: 'owner/bbbb2222-y', title: 'B' }];
  await browse({
    async Sessions() { return list(); },
    async OpenSession(profile, id) {
      return { token: 0, events: id === 'owner/thread'
        ? [{ kind: 'message', text: 'go', at: now }, { kind: 'handed', text: 'aaaa1111' }, { kind: 'handed', text: 'bbbb2222' }]
        : [{ kind: 'message', text: 'inside', at: now }, { kind: 'answer', text: 'done', at: now }] };
    },
    async SessionSteps(profile, id) {
      reads.push(id);
      if (slow) return slow.promise;
      return [{ text: id === 'owner/aaaa1111-x' && !running ? 'FINAL TOOL RESULT' : 'tool still starting' }];
    },
  }, doc, '', 'owner/thread');
  await settle();
  const rail = () => doc['context-details'].children.map(block => block.children[1].textContent).join(' | ');
  assert.deepEqual(buttons(doc).map(b => b.title), ['owner/aaaa1111-x', 'owner/bbbb2222-y']);
  assert.match(rail(), /tool still starting/);
  // A ends: the next overview reads its last steps once more.
  running = false;
  await tick(); await settle();
  assert.match(rail(), /FINAL TOOL RESULT/);
  // Opening A keeps the part of the thread beside it, with B.
  choose(doc, 'owner/aaaa1111-x'); await settle();
  assert.deepEqual(buttons(doc).map(b => b.title), ['owner/aaaa1111-x', 'owner/bbbb2222-y']);
  // Back in the thread, a slow read is dropped once another session is selected.
  doc['back-to-thread'].dispatchEvent(new Event('click')); await settle();
  slow = deferred();
  running = true;
  await tick();
  choose(doc, 'owner/bbbb2222-y'); await settle();
  const before = doc['context-details'].children.length;
  slow.resolve([{ text: 'OLD THREAD RESULT' }]); await settle();
  assert.equal(doc['context-details'].children.length, before);
  assert.doesNotMatch(rail(), /OLD THREAD RESULT/);
});

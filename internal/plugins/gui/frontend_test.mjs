import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

// A data URL keeps this test independent of package.json's module mode.
const source = await readFile(new URL('./frontend/app.js', import.meta.url), 'utf8');
const dialogSource = await readFile(new URL('./frontend/model-dialog.js', import.meta.url), 'utf8');
const dialogURL = 'data:text/javascript;base64,' + Buffer.from(dialogSource).toString('base64');
const { createModelDialog } = await import(dialogURL);
const settingsSource = await readFile(new URL('./frontend/settings-dialog.js', import.meta.url), 'utf8');
const settingsURL = 'data:text/javascript;base64,' + Buffer.from(settingsSource).toString('base64');
const { createSettingsDialog } = await import(settingsURL);
const { mount, start, browse, dateGroup } = await import(
  'data:text/javascript;base64,' + Buffer.from(source.replace("'./model-dialog.js'", JSON.stringify(dialogURL)).replace("'./settings-dialog.js'", JSON.stringify(settingsURL))).toString('base64')
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

  scrollIntoView(options) {
    this.scrolls ??= [];
    this.scrolls.push(options);
  }

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

function documentFixture() {
  const elements = {
    settings: new Element('button'),
    sidebar: new Element('aside'),
    'sidebar-toggle': new Element('button'),
    profile: new Element('select'),
    active: new Element('p'),
    identity: new Element('p'),
    error: new Element('p'),
    sessions: new Element('div'),
    'session-search': new Element('input'),
    'session-title': new Element('h2'),
    'session-meta': new Element('p'),
    content: new Element('div'),
    'session-status': new Element('p'),
    refresh: new Element('button'),
    'new-session': new Element('button'),
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
  return {
    ...elements,
    body: new Element('body'),
    scrollingElement: { scrollHeight: 0, clientHeight: 0, scrollTop: 0 },
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
  for (const id of ['profile', 'active', 'identity', 'error', 'sessions', 'content', 'session-status', 'session-search', 'session-title', 'session-meta', 'refresh', 'new-session', 'composer', 'draft', 'send', 'send-error', 'send-status', 'question-panel', 'question-text', 'question-options', 'question-hint']) {
    assert.match(html, new RegExp('id="' + id + '"'));
  }
  assert.match(html, /<label\b[^>]*for="profile"/);
  assert.match(html, /<select\b(?=[^>]*id="profile")(?=[^>]*disabled)[^>]*>/);
  assert.match(html, /<[^>]+(?=[^>]*id="active")(?=[^>]*aria-live="polite")[^>]*>/);
  assert.match(html, /<[^>]+(?=[^>]*id="error")(?=[^>]*role="alert")(?=[^>]*hidden)[^>]*>/);
  assert.match(html, /<[^>]+(?=[^>]*id="session-status")(?=[^>]*role="status")[^>]*>/);
});

test('live messages keep the composer visible unless the user has scrolled up', async () => {
  const doc = archiveFixture();
  const pending = [];
  await browse({
    async Sessions() { return [{ id: 'owner/live' }]; },
    async OpenSession() { return { token: 1, events: [] }; },
    PollSession() { const next = deferred(); pending.push(next); return next.promise; },
  }, doc);
  assert.equal(doc.composer.scrolls.length, 1);
  pending[0].resolve([{ kind: 'answer', text: 'new' }]);
  await settle();
  assert.equal(doc.composer.scrolls.length, 2);
  doc.scrollingElement.scrollHeight = 2000;
  doc.scrollingElement.clientHeight = 900;
  pending[1].resolve([{ kind: 'answer', text: 'more' }]);
  await settle();
  assert.equal(doc.composer.scrolls.length, 2);
});

test('transcript layout uses real message heights for reliable end scrolling', async () => {
  const css = await readFile(new URL('./frontend/style.css', import.meta.url), 'utf8');
  assert.doesNotMatch(css, /content-visibility\s*:\s*auto|contain-intrinsic-size/);
});

test('sidebar toggle hides and restores navigation without changing the session', async () => {
  const doc = documentFixture();
  await start({
    async InitialState() { return { profiles: [{ name: 'Luna' }], selected: 0 }; },
    async Sessions() { return []; },
  }, doc);
  const button = doc['sidebar-toggle'];
  const title = doc['session-title'].textContent;
  for (let i = 0; i < 2; i++) {
    button.dispatchEvent(new Event('click'));
    assert.equal(doc.sidebar.hidden, true);
    assert.equal(doc.body.getAttribute('data-sidebar-collapsed'), 'true');
    assert.equal(button.getAttribute('aria-expanded'), 'false');
    assert.equal(button.textContent, '▶ Sitzungen einblenden');
    button.dispatchEvent(new Event('click'));
    assert.equal(doc.sidebar.hidden, false);
    assert.equal(doc.body.getAttribute('data-sidebar-collapsed'), 'false');
    assert.equal(button.getAttribute('aria-expanded'), 'true');
    assert.equal(button.textContent, '◀ Sitzungen ausblenden');
    assert.equal(doc['session-title'].textContent, title);
  }
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
  assert.equal(doc.profile.disabled, false);
  assert.equal(doc.active.textContent, 'Profil: Other');
  assert.equal(doc.identity.textContent, 'bbb');
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
  assert.equal(doc.active.textContent, 'Profil: Other');
  assert.equal(doc.identity.textContent, 'bbb');
  select(doc, 0);
  assert.equal(doc.active.textContent, 'Profil: Luna');
  assert.equal(doc.identity.textContent, 'aaa');
  assert.equal(state.selected, 0);
  assert.equal(calls, 1);
});

test('one unnamed profile disables the picker and shows the setup fallback', () => {
  const doc = documentFixture();
  // The Go bridge supplies "Not set up" for an unnamed profile.
  mount({ profiles: [{ name: 'Not set up', id: '' }], selected: 0 }, doc);
  assert.equal(doc.profile.disabled, true);
  assert.equal(doc.profile.selectedIndex, 0);
  assert.equal(doc.profile.children.length, 1);
  assert.equal(doc.profile.children[0].textContent, '1 · Not set up');
  assert.equal(doc.active.textContent, 'Profil: Not set up');
  assert.equal(doc.identity.textContent, 'Profil noch nicht eingerichtet');
});

test('a profile list without an entry mounts without touching the view', () => {
  const doc = documentFixture();
  mount({ profiles: [], selected: 0 }, doc);
  assert.equal(doc.profile.selectedIndex, 0);
  assert.deepEqual(doc.profile.children, []);
  assert.equal(doc.active.textContent, '');
  assert.equal(doc.identity.textContent, '');
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
  assert.equal(doc.active.textContent, 'Profil: Same');
  assert.equal(doc.identity.textContent, 'second');
  select(doc, 0);
  assert.equal(doc.identity.textContent, 'first');
});

test('HTML in profile names is displayed literally through textContent', () => {
  const doc = documentFixture();
  const name = '<img src=x onerror="alert(1)"> & <script>bad()</script>';
  mount({ profiles: [{ name, id: 'safe-id' }], selected: 0 }, doc);
  assert.equal(doc.profile.children[0].textContent, '1 · ' + name);
  assert.equal(doc.active.textContent, 'Profil: ' + name);
  assert.deepEqual(doc.profile.children[0].children, []);
  assert.deepEqual(doc.active.children, []);
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
  assert.equal(doc.profile.disabled, true);
  assert.deepEqual(doc.profile.children, []);
  assert.equal(doc.active.textContent, '');
  assert.equal(doc.identity.textContent, '');
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
function search(doc, query) {
  doc['session-search'].value = query;
  doc['session-search'].dispatchEvent(new Event('input'));
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
  const old = deferred();
  const events = Array.from({ length: 85 }, (_, i) => ({ kind: 'message', text: String(i) }));
  const scrolls = [];
  doc.composer.scrollIntoView = options => {
    scrolls.push({ options, messages: doc.content.children.length });
  };
  await browse({
    async Sessions() { return [{ id: 'owner/first' }, { id: 'peer/second' }, { id: 'owner/stale' }]; },
    async OpenSession(_, id) {
      if (id === 'owner/stale') return old.promise;
      return { token: 0, events };
    },
  }, doc);
  assert.deepEqual(scrolls, [{ options: { block: 'end', behavior: 'instant' }, messages: 85 }]);
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

test('search matches title and ID case insensitively without any backend calls', async () => {
  const doc = archiveFixture();
  const calls = [];
  await browse({
    async Sessions() { calls.push('list'); return [
      { id: 'owner/Alpha-ID', title: 'Äpfel Meeting' }, { id: 'peer/Beta', title: 'Other' },
    ]; },
    async OpenSession(_, id) { calls.push(id); return { token: 0, events: [{ kind: 'message', text: 'unchanged' }] }; },
  }, doc);
  for (const query of [' äPFEL ', 'MEETING', 'ALPHA-id']) {
    search(doc, query);
    assert.deepEqual(buttons(doc).map(button => button.title), ['owner/Alpha-ID']);
  }
  search(doc, 'PEER/bETA');
  assert.deepEqual(buttons(doc).map(button => button.title), ['peer/Beta']);
  search(doc, 'missing');
  assert.deepEqual(buttons(doc), []);
  assert.equal(doc.sessions.children[0].className, 'list-empty');
  assert.equal(doc.content.children[0].children[1].textContent, 'unchanged');
  search(doc, '');
  assert.equal(buttons(doc).length, 2);
  assert.deepEqual(current(doc), ['owner/Alpha-ID']);
  assert.deepEqual(calls, ['list', 'owner/Alpha-ID']);
});

test('refresh preserves selected session despite reordered entries and falls back if removed', async () => {
  const doc = archiveFixture();
  let entries = [{ id: 'owner/a' }, { id: 'owner/b' }];
  const reads = [];
  await browse({
    async Sessions() { return entries; },
    async OpenSession(_, id) { reads.push(id); return { token: 0, events: [] }; },
  }, doc);
  choose(doc, 'owner/b');
  await settle();
  entries = [{ id: 'owner/new' }, { id: 'owner/b' }, { id: 'owner/a' }];
  search(doc, 'b');
  doc.refresh.dispatchEvent(new Event('click'));
  await settle();
  assert.deepEqual(current(doc), ['owner/b']);
  assert.equal(doc['session-search'].value, 'b');
  assert.equal(doc['session-title'].textContent, 'owner/b');
  entries = [{ id: 'owner/new' }];
  search(doc, '');
  doc.refresh.dispatchEvent(new Event('click'));
  await settle();
  assert.deepEqual(current(doc), ['owner/new']);
  assert.deepEqual(reads, ['owner/a', 'owner/b', 'owner/b', 'owner/new']);
});

test('empty archives and list/open errors recover via refresh', async () => {
  const doc = archiveFixture();
  let mode = 'empty';
  await browse({
    async Sessions() {
      if (mode === 'list-error') throw new Error('list failed');
      return mode === 'empty' ? null : [{ id: 'owner/a' }];
    },
    async OpenSession() {
      if (mode === 'read-error') throw new Error('read failed');
      return { token: 0, events: mode === 'null-events' ? null : [{ kind: 'message', text: 'recovered' }] };
    },
  }, doc);
  assert.deepEqual(buttons(doc), []);
  assert.equal(doc.sessions.children[0].className, 'list-empty');
  assert.match(doc['session-status'].textContent, /Noch keine Sessions/);
  for (const next of ['list-error', 'read-error', 'success', 'null-events', 'empty']) {
    mode = next;
    doc.refresh.dispatchEvent(new Event('click'));
    await settle();
    assert.equal(doc.refresh.disabled, false);
    if (mode.endsWith('error')) {
      assert.equal(doc.error.hidden, false);
      assert.match(doc.error.textContent, mode === 'list-error' ? /list failed/ : /read failed/);
    } else {
      assert.equal(doc.error.hidden, true);
      assert.equal(doc.content.children.length, mode === 'success' ? 1 : 0);
    }
  }
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
    search(doc, 'old');
    select(doc, 1);
    await settle();
    assert.equal(doc['session-search'].value, '');
    if (reject) old.reject(new Error('stale list failure'));
    else old.resolve([{ id: 'owner/old' }]);
    await loading;
    assert.deepEqual(buttons(doc).map(button => button.title), ['owner/new']);
    assert.equal(doc.content.children[0].children[1].textContent, 'new profile');
    assert.deepEqual(reads, [[1, 'owner/new']]);
    assert.equal(doc.error.hidden, true);
    assert.equal(doc.refresh.disabled, false);
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

test('new sessions open even when empty archives omit them, and survive refresh', async () => {
  const doc = archiveFixture();
  const opened = [];
  let creates = 0;
  await browse({
    async Sessions() { return []; },
    async NewSession(profile) { assert.equal(profile, 0); creates++; return 'owner/new'; },
    async OpenSession(...args) { opened.push(args); return { events: [] }; },
  }, doc);
  doc['new-session'].dispatchEvent(new Event('click'));
  doc['new-session'].dispatchEvent(new Event('click'));
  await settle();
  assert.equal(creates, 1);
  assert.deepEqual(current(doc), ['owner/new']);
  assert.equal(doc.draft.disabled, false);
  typeDraft(doc, 'unsent');
  doc.refresh.dispatchEvent(new Event('click'));
  assert.equal(doc.draft.disabled, true);
  await settle();
  assert.deepEqual(current(doc), ['owner/new']);
  assert.equal(doc.draft.value, 'unsent');
  assert.deepEqual(opened, [[0, 'owner/new'], [0, 'owner/new']]);
});

test('late creation success/failure cannot steal a newer session or profile', async () => {
  for (const switchProfile of [false, true]) for (const reject of [false, true]) {
    const doc = archiveFixture();
    const pending = deferred();
    await browse({
      async Sessions() { return [{ id: 'owner/a' }, { id: 'owner/b' }]; },
      NewSession() { return pending.promise; },
      async OpenSession() { return { events: [] }; },
    }, doc);
    doc['new-session'].dispatchEvent(new Event('click'));
    if (switchProfile) select(doc, 1);
    else choose(doc, 'owner/b');
    await settle();
    typeDraft(doc, 'keep');
    if (reject) pending.reject(new Error('stale create'));
    else pending.resolve('owner/new');
    await settle();
    assert.deepEqual(current(doc), [switchProfile ? 'owner/a' : 'owner/b']);
    assert.equal(doc.draft.value, 'keep');
    assert.equal(doc.error.hidden, true);
    assert.equal(doc['new-session'].disabled, false);
  }
});

test('new session errors recover; loading transcripts cannot send', async () => {
  const doc = archiveFixture();
  const pending = deferred();
  let creates = 0;
  const loading = browse({
    async Sessions() { return [{ id: 'owner/a' }]; },
    OpenSession() { return pending.promise; },
    async NewSession() { creates++; throw new Error('create failed'); },
    SendMessage() { assert.fail('Loading sessions must not send'); },
  }, doc);
  await settle();
  typeDraft(doc, 'wait');
  submit(doc);
  assert.equal(doc.draft.disabled, true);
  pending.resolve({ events: [] });
  await loading;
  for (let i = 0; i < 2; i++) {
    doc['new-session'].dispatchEvent(new Event('click'));
    await settle();
    assert.match(doc.error.textContent, /create failed/);
    assert.equal(doc['new-session'].disabled, false);
  }
  assert.equal(creates, 2);
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
  const dialog = doc.body.children.find(e => e.children[0]?.textContent === 'Modell einstellen');
  const form = dialog.children[2];
  return { dialog, form, access: form.children[0].children[0], model: form.children[1].children[0],
    status: form.children[2], error: form.children[3], cancel: form.children[4].children[0], save: form.children[4].children[1] };
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
  assert.equal(el.cancel.disabled, false);
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
  el.cancel.dispatchEvent(new Event('click'));
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
  assert.equal(el.cancel.disabled, true);
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
  assert.equal(el.cancel.disabled, true);
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
  assert.equal(el.cancel.disabled, false);
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

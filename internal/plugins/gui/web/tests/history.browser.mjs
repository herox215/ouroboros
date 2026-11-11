// Dependency-free Chromium/CDP integration test. Start `pnpm dev --port 5179`
// first, then `node tests/history.browser.mjs`. No node or owner is contacted:
// all Wails bindings are replaced before the application loads.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const profile = await mkdtemp(join(tmpdir(), 'ouro-history-browser-'));
const browser = spawn(process.env.CHROMIUM || 'chromium', [
  '--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank',
], { stdio: ['ignore', 'ignore', 'pipe'] });
let socket;
try {
  const endpoint = await new Promise((resolve, reject) => {
    let output = '';
    const timer = setTimeout(() => reject(new Error('Chromium startup timeout')), 15000);
    browser.stderr.on('data', (chunk) => {
      output += chunk;
      const match = output.match(/DevTools listening on (ws:\/\/\S+)/);
      if (match) { clearTimeout(timer); resolve(match[1]); }
    });
    browser.once('exit', (code) => { clearTimeout(timer); reject(new Error(`Chromium exited ${code}: ${output.slice(-2000)}`)); });
  });
  socket = new WebSocket(endpoint);
  await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });
  let sequence = 0;
  const waiting = new Map();
  const errors = [];
  socket.onmessage = ({ data }) => {
    const message = JSON.parse(data);
    if (message.method === 'Runtime.exceptionThrown') errors.push(message.params.exceptionDetails.text);
    if (!message.id) return;
    const callback = waiting.get(message.id);
    waiting.delete(message.id);
    if (message.error) callback.reject(new Error(JSON.stringify(message.error)));
    else callback.resolve(message.result);
  };
  function call(method, params = {}, sessionId) {
    const id = ++sequence;
    return new Promise((resolve, reject) => {
      waiting.set(id, { resolve, reject });
      socket.send(JSON.stringify({ id, method, params, sessionId }));
    });
  }
  const { targetId } = await call('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await call('Target.attachToTarget', { targetId, flatten: true });
  const send = (method, params) => call(method, params, sessionId);
  await send('Page.enable');
  // Vite injects CSS inline; production checks keep the strict CSP enabled.
  if (process.env.HISTORY_TEST_PRODUCTION !== '1') {
    await send('Page.setBypassCSP', { enabled: true });
  }
  await send('Runtime.enable');
  await send('Emulation.setDeviceMetricsOverride', { width: 1000, height: 800, deviceScaleFactor: 1, mobile: false });
  await send('Page.addScriptToEvaluateOnNewDocument', { source: `
    const handlers = {};
    const events = Array.from({length: 600}, (_, i) => ({
      kind: i % 2 ? 'answer' : 'message',
      text: 'Fixture row ' + i + ': ' + ('some harmless wrapping content '.repeat(i % 7 + 1)),
    }));
    window.__fixture = {
      calls: [],
      resolve: null,
      emit(batch) { handlers['ouro:events']?.(batch); },
      state() {
        const s = document.querySelector('.transcript');
        const rect = s.getBoundingClientRect();
        const rows = [...s.querySelectorAll('[data-entry-id]')];
        const anchor = rows.find(r => r.getBoundingClientRect().bottom > rect.top + 1);
        return { top:s.scrollTop, height:s.scrollHeight, viewport:s.clientHeight,
          count: rows.length, loaded: Number(rows[0]?.getAttribute('aria-setsize')),
          id: anchor?.dataset.entryId, offset: anchor?.getBoundingClientRect().top - rect.top };
      }
    };
    window.runtime = { EventsOn(name, handler) { handlers[name] = handler; return () => {}; } };
    window.go = { gui: { Window: {
      InitialState: async () => ({profile:'fixture', thread:'fixture'}),
      Open: async () => ({events:events.slice(400), before:400, has_more:true}),
      History: (before) => {
        __fixture.calls.push(before);
        return new Promise(resolve => { __fixture.resolve = () => resolve({
          events: events.slice(Math.max(0,before-200),before),
          before:Math.max(0,before-200), has_more:before>200
        }); });
      },
      Send: async () => { throw new Error('Sending is forbidden in this fixture'); },
      Stop: async () => { throw new Error('Stopping is forbidden in this fixture'); }
    } } };
  ` });
  async function evaluate(expression) {
    const response = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (response.exceptionDetails) throw new Error(JSON.stringify(response.exceptionDetails));
    return response.result.value;
  }
  const sleep = (ms) => new Promise(resolve => setTimeout(resolve, ms));
  async function until(expression, message) {
    for (let i = 0; i < 100; i++) {
      if (await evaluate(expression)) return;
      await sleep(50);
    }
    throw new Error(`Timed out: ${message}; ${JSON.stringify(await evaluate('({state:__fixture.state(), calls:__fixture.calls, active:document.activeElement?.className, errors:document.body.innerText.slice(-400)})'))}`);
  }
  const settle = () => sleep(350);
  const state = () => evaluate('__fixture.state()');
  const anchorEqual = async (before, _after, operation) => {
    const after = await evaluate(`(() => {
      const row = document.querySelector('[data-entry-id="${before.id}"]');
      const s = document.querySelector('.transcript');
      return {...__fixture.state(), id:row?.dataset.entryId,
        offset:row?.getBoundingClientRect().top-s.getBoundingClientRect().top};
    })()`);
    assert.equal(after.id, before.id, `${operation}: visible row ID ${JSON.stringify({before, after})}`);
    assert.ok(Math.abs(after.offset - before.offset) < 2, `${operation}: offset changed ${JSON.stringify({before, after})}`);
  };
  await send('Page.navigate', { url: process.env.HISTORY_TEST_URL || 'http://127.0.0.1:5179/' });
  await until(`document.querySelectorAll('[data-entry-id]').length > 0`, 'transcript mounts');
  await settle();
  let current = await state();
  assert.equal(current.loaded, 200);
  assert.ok(current.count < 50, 'initial loaded history is virtualized');
  assert.ok(current.height - current.top - current.viewport < 2, 'opens at latest');
  assert.deepEqual(await evaluate('__fixture.calls'), [], 'mount must not page');

  const rect = await evaluate(`(() => {const r=document.querySelector('.transcript').getBoundingClientRect();return {x:r.left+100,y:r.top+100}})()`);
  await send('Input.dispatchMouseEvent', { type: 'mouseWheel', ...rect, deltaX: 0, deltaY: -(current.top - 160) });
  await until('__fixture.calls.length === 1', 'upward scroll requests history');
  await settle();
  const before = await state();
  await evaluate('__fixture.resolve()');
  await until(`__fixture.state().loaded === 400`, 'older page prepends');
  await settle();
  await anchorEqual(before, await state(), 'prepend');
  assert.deepEqual(await evaluate('__fixture.calls'), [400], 'one page per interaction');

  const reading = await state();
  await evaluate(`__fixture.emit([{kind:'answering',text:'streamed fixture '.repeat(300)}])`);
  await settle();
  await anchorEqual(reading, await state(), 'live output while reading');
  await send('Emulation.setDeviceMetricsOverride', { width: 650, height: 650, deviceScaleFactor: 1, mobile: false });
  await settle();
  await anchorEqual(reading, await state(), 'width and height resize');

  await evaluate(`document.querySelector('.jump').click()`);
  await settle();
  current = await state();
  assert.ok(current.height-current.top-current.viewport < 2, `jump latest reaches bottom: ${JSON.stringify(current)}`);
  assert.ok(current.count < 60, 'jump latest does not mount all rows');
  await evaluate(`__fixture.emit([{kind:'answering',text:' more output'.repeat(300)}])`);
  await settle();
  current = await state();
  assert.ok(current.height-current.top-current.viewport < 2, 'streaming height follows bottom');

  await evaluate(`document.querySelector('.transcript').focus()`);
  await send('Input.dispatchKeyEvent', { type:'keyDown', key:'Home', code:'Home', windowsVirtualKeyCode:36 });
  await send('Input.dispatchKeyEvent', { type:'keyUp', key:'Home', code:'Home', windowsVirtualKeyCode:36 });
  await until('__fixture.calls.length === 2', 'keyboard pages at top');
  await settle();
  const second = await state();
  await evaluate('__fixture.resolve()');
  await until('__fixture.state().loaded === 601', 'final page loaded');
  await settle();
  await anchorEqual(second, await state(), 'second prepend');
  assert.deepEqual(await evaluate('__fixture.calls'), [400,200]);
  assert.equal(await evaluate(`document.querySelector('.history-control') === null`), true, 'no more history');
  const down = (await state()).top;
  await send('Input.dispatchKeyEvent', { type:'keyDown', key:'PageDown', code:'PageDown', windowsVirtualKeyCode:34 });
  await settle();
  assert.ok((await state()).top > down, 'PageDown scrolls downward');
  assert.ok((await state()).count < 60, 'downward scrolling remains virtualized');
  const up = (await state()).top;
  await send('Input.dispatchKeyEvent', { type:'keyDown', key:'PageUp', code:'PageUp', windowsVirtualKeyCode:33 });
  await settle();
  assert.ok((await state()).top < up, 'PageUp scrolls upward');
  await send('Input.dispatchKeyEvent', { type:'keyDown', key:'End', code:'End', windowsVirtualKeyCode:35 });
  await settle();
  current = await state();
  assert.ok(current.height-current.top-current.viewport < 2, 'End key jumps latest');
  assert.deepEqual(errors, [], 'no browser exceptions');
  console.log('PASS: native History fixture; demand-only paging; two prepend anchors (<2px); virtualization up/down; jump/End; resize; live streaming follow and non-follow.');
} finally {
  socket?.close();
  browser.kill();
  await new Promise(resolve => browser.exitCode !== null ? resolve() : browser.once('exit', resolve));
  await rm(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
}

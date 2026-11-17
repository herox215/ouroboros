// The host the page of the thread needs in a browser: what Wails is to the
// window, this is to the same app.js. It binds the methods of the window
// to the server of the node, and its events to the stream the node sends.
//
// Nothing of it is secret: the session cookie the browser carries is what
// lets the requests through, and it is not readable from here.
(() => {
  'use strict';

  // The name of this tab. A cookie is shared by every tab of one browser, and
  // one history cursor belongs to one tab, so the page picks a name per tab
  // and carries it in a header, or in the query of the stream, which is a
  // request an EventSource makes and cannot give a header to. The name lives
  // in this page alone and is not kept: a duplicated tab, and one opened from
  // this one, copies what the page keeps, and would otherwise share the name
  // — and the history cursor — with the tab it came from.
  const client = (() => {
    const raw = new Uint8Array(32);
    crypto.getRandomValues(raw);
    return btoa(String.fromCharCode(...raw)).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
  })();

  const json = (body) => (body === undefined ? {} : { 'Content-Type': 'application/json' });

  const fail = async (res) => {
    const text = (await res.text()).trim();
    return new Error(text || res.statusText || `HTTP ${res.status}`);
  };

  const read = (res) => {
    const kind = res.headers.get('content-type') || '';
    return kind.includes('application/json') ? res.json() : null;
  };

  const signedOut = () => {
    // The session ended, or the node started anew with a new one: the login
    // asks again rather than the page showing a thread it cannot reach.
    location.replace('./login');
  };

  const request = (path, options) =>
    fetch(path, options).then(async (res) => {
      if (res.status === 401) {
        signedOut();
        throw new Error('signed out');
      }
      if (!res.ok) throw await fail(res);
      return read(res);
    });

  const get = (path) => request(path, { credentials: 'same-origin', headers: { 'X-Ouro-Client': client } });

  const post = (path, body) =>
    request(path, {
      method: 'POST',
      credentials: 'same-origin',
      headers: Object.assign({ 'X-Ouro-Client': client, 'X-Ouro-Web': '1' }, json(body)),
      body: body === undefined ? undefined : JSON.stringify(body),
    });

  // What the page sends of an agent, see agentBody in webserver.go.
  const agentBody = (id, change) => ({
    id: String(id),
    name: String((change && change.name) || ''),
    description: String((change && change.description) || ''),
    soul: String((change && change.soul) || ''),
    access: String((change && change.access) || ''),
    model: String((change && change.model) || ''),
    effort: String((change && change.effort) || ''),
  });

  const bridge = {
    InitialState: () => get('./api/initial'),
    Open: () => post('./api/open'),
    History: (before) => get('./api/history?before=' + encodeURIComponent(String(before))),
    Send: (text) => post('./api/send', { text: String(text) }),
    Stop: () => post('./api/stop'),
    Board: () => get('./api/board'),
    Ticket: (id) => get('./api/ticket?id=' + encodeURIComponent(String(id))),
    Sessions: () => get('./api/sessions'),
    Session: (id) => get('./api/session?id=' + encodeURIComponent(String(id))),
    Agents: () => get('./api/agents'),
    UpdateAgent: (id, change) => post('./api/agent/update', agentBody(id, change)),
    CreateAgent: (change) => post('./api/agent/create', agentBody('', change)),
    DeleteAgent: (id) => post('./api/agent/delete', { id: String(id) }),
    AgentModels: () => get('./api/agent/models'),
    AgentJobs: () => get('./api/jobs'),
    // The options of the node, see webproviders.go.
    Providers: () => get('./api/providers'),
    LLMs: () => get('./api/llms'),
    SetLLM: (scope, access, model) => post('./api/llm', { scope: String(scope), access: String(access || ''), model: String(model || '') }),
    ToggleFallback: (access, model) => post('./api/fallback', { access: String(access), model: String(model) }),
    SetEffort: (effort) => post('./api/effort', { effort: String(effort || '') }),
    RenameAccess: (id, name) => post('./api/access/rename', { id: String(id), name: String(name || '') }),
    RemoveAccess: (id) => post('./api/access/remove', { id: String(id) }).then((answer) => (answer && answer.note) || ''),
    StartLogin: (provider, again) =>
      post('./api/login/start', { provider: String(provider || ''), again: String(again || '') }).then((answer) => answer.id),
    LoginStep: (id) => post('./api/login/step', { id: String(id) }),
    AnswerLogin: (id, answer) => post('./api/login/answer', { id: String(id), answer: String(answer) }).then(() => undefined),
    CancelLogin: (id) => post('./api/login/cancel', { id: String(id) }).then(() => undefined),
    Assets: () => get('./assets').then((answer) => (answer && answer.version) || ''),
  };

  // What the window tells, and where the app listens for it.
  const listeners = { events: [], ended: [], restarting: [] };
  let stream = null;
  let connected = false; // the stream worked once, and what ends it is news
  // lost is set while the page waits out a stream it gave up on: the app may
  // have given up reconnecting by then, so the stream that comes back is what
  // tells it to take the thread again.
  let lost = false;
  let retry = 0; // the timer that opens another stream, 0 while none waits
  // webReopen is how long the page waits for a stream it lost: the node is
  // starting anew, or the proxy in front of it is away.
  const webReopen = 5000;

  // signedIn reports whether the node still knows this browser: one that
  // answers 401 is a login that ended, and one that does not answer at all, or
  // answers what a proxy says while the node is away, is not. It names the tab
  // as every other request does, so it opens no window of its own.
  const signedIn = () =>
    fetch('./api/initial', { credentials: 'same-origin', headers: { 'X-Ouro-Client': client } }).then(
      (res) => res.status !== 401,
      () => true,
    );

  const open = () => {
    if (retry) {
      clearTimeout(retry);
      retry = 0;
    }
    const source = new EventSource('./api/events?client=' + encodeURIComponent(client));
    stream = source;
    source.addEventListener('events', (message) => {
      let batch;
      try {
        batch = JSON.parse(message.data);
      } catch {
        return;
      }
      if (Array.isArray(batch)) for (const listener of listeners.events) listener(batch);
    });
    source.addEventListener('ended', () => {
      for (const listener of listeners.ended) listener();
    });
    source.addEventListener('restarting', () => {
      for (const listener of listeners.restarting) listener();
    });
    source.addEventListener('open', () => {
      if (connected || lost) {
        // The stream dropped and is back, or a page that could not be reached
        // is there again. What it told in between is in the snapshot of an
        // Open, so the page takes the thread again rather than carrying on
        // with a gap in it — or staying offline once its own retries ran out.
        for (const listener of listeners.ended) listener();
      }
      connected = true;
      lost = false;
    });
    source.addEventListener('error', () => {
      // A stream that drops reconnects by itself. One the browser gives up on
      // for good is the login that ended (401) — and also a page that cannot
      // be reached at all, which a node starting anew, or a proxy in front of
      // it, answers with. Only the first is a reason to ask who is there: the
      // other is waited out, with the page saying for itself that the thread
      // is not there.
      if (source.readyState !== EventSource.CLOSED || stream !== source) return;
      void signedIn().then((known) => {
        if (stream !== source) return; // another stream took over meanwhile
        if (!known) {
          signedOut();
          return;
        }
        source.close();
        stream = null;
        lost = true;
        if (connected) {
          connected = false;
          for (const listener of listeners.ended) listener();
        }
        if (!retry) retry = setTimeout(() => open(), webReopen);
      });
    });
  };

  const runtime = {
    EventsOn(name, listener) {
      const list = name === 'ouro:events' ? listeners.events : name === 'ouro:ended' ? listeners.ended : name === 'ouro:restarting' ? listeners.restarting : null;
      if (!list) return () => {};
      list.push(listener);
      if (!stream) open();
      return () => {
        const at = list.indexOf(listener);
        if (at >= 0) list.splice(at, 1);
      };
    },
  };

  // The two globals the app looks for, as the window has them.
  globalThis.go = { gui: { Window: bridge } };
  globalThis.runtime = runtime;

  // Installed on a phone, the page opens without the browser around it. The
  // worker keeps the shell of the page, never a message of the thread.
  if ('serviceWorker' in navigator) {
    window.addEventListener('load', () => {
      navigator.serviceWorker.register('./sw.js').catch(() => {});
    });
  }
})();

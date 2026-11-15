// The host the page of the thread needs in a browser: what Wails is to the
// window, this is to the same app.js. It binds the five methods of the window
// to the server of the node, and its events to the stream the node sends.
//
// Nothing of it is secret: the session cookie the browser carries is what
// lets the requests through, and it is not readable from here.
(() => {
  'use strict';

  // The name of this tab. A cookie is shared by every tab of one browser, and
  // one history cursor belongs to one tab, so the page picks a name per tab
  // and carries it in a header, or in the query of the stream, which is a
  // request an EventSource makes and cannot give a header to.
  const TAB = 'ouro_web_client';
  const TAB_NAME = /^[A-Za-z0-9_-]{43}$/;

  const tabID = () => {
    let id = '';
    try {
      id = sessionStorage.getItem(TAB) || '';
    } catch {
      // No storage: every load of the page is a tab of its own.
    }
    if (TAB_NAME.test(id)) return id;
    const raw = new Uint8Array(32);
    crypto.getRandomValues(raw);
    id = btoa(String.fromCharCode(...raw)).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
    try {
      sessionStorage.setItem(TAB, id);
    } catch {
      // No storage: the name lives as long as this page does.
    }
    return id;
  };

  const client = tabID();

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

  const bridge = {
    InitialState: () => get('./api/initial'),
    Open: () => post('./api/open'),
    History: (before) => get('./api/history?before=' + encodeURIComponent(String(before))),
    Send: (text) => post('./api/send', { text: String(text) }),
    Stop: () => post('./api/stop'),
  };

  // What the window tells, and where the app listens for it.
  const listeners = { events: [], ended: [], restarting: [] };
  let stream = null;
  let connected = false;

  const open = () => {
    stream = new EventSource('./api/events?client=' + encodeURIComponent(client));
    stream.addEventListener('events', (message) => {
      let batch;
      try {
        batch = JSON.parse(message.data);
      } catch {
        return;
      }
      if (Array.isArray(batch)) for (const listener of listeners.events) listener(batch);
    });
    stream.addEventListener('ended', () => {
      for (const listener of listeners.ended) listener();
    });
    stream.addEventListener('restarting', () => {
      for (const listener of listeners.restarting) listener();
    });
    stream.addEventListener('open', () => {
      if (connected) {
        // The stream dropped and is back. What it told in between is in the
        // snapshot of an Open, so the page takes the thread again rather than
        // carrying on with a gap in it.
        for (const listener of listeners.ended) listener();
      }
      connected = true;
    });
    stream.addEventListener('error', () => {
      // A stream that drops reconnects by itself. One the browser gives up on
      // for good is the login that is no longer there, which is what a 401
      // does, and then the page asks for it again.
      if (stream.readyState === EventSource.CLOSED) signedOut();
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

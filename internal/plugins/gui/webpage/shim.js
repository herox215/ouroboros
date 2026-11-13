// The host the page of the thread needs in a browser: what Wails is to the
// window, this is to the same app.js. It binds the five methods of the window
// to the server of the node, and its events to the stream the node sends.
//
// Nothing of it is secret: the session cookie the browser carries is what
// lets the requests through, and it is not readable from here.
(() => {
  'use strict';

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

  const get = (path) =>
    fetch(path, { credentials: 'same-origin' }).then(async (res) => {
      if (res.status === 401) {
        signedOut();
        throw new Error('signed out');
      }
      if (!res.ok) throw await fail(res);
      return read(res);
    });

  const post = (path, body) =>
    fetch(path, {
      method: 'POST',
      credentials: 'same-origin',
      headers: Object.assign({ 'X-Ouro-Web': '1' }, json(body)),
      body: body === undefined ? undefined : JSON.stringify(body),
    }).then(async (res) => {
      if (res.status === 401) {
        signedOut();
        throw new Error('signed out');
      }
      if (!res.ok) throw await fail(res);
      return read(res);
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

  const open = () => {
    stream = new EventSource('./api/events');
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
    // A stream that drops reconnects by itself, as the window reopens the
    // thread: its snapshot says what was missed.
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

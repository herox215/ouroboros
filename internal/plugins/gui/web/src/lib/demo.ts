// A scripted thread for looking at the page in a browser while it is built
// (pnpm dev). The window itself never uses it: connect picks it only when no
// Wails host is there and the bundle is a dev build.

import type { Link, WireEvent } from './wire';

const wait = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

export function demoLink(): Link {
  const handlers: ((batch: WireEvent[]) => void)[] = [];
  const timers = new Set<ReturnType<typeof setTimeout>>();
  let busy = false;
  let scripted = false;
  let question: string | null = null;

  const send = (batch: WireEvent[]) => {
    for (const handler of handlers) handler(batch);
  };
  const later = (ms: number, run: () => void) => {
    const timer = setTimeout(() => {
      timers.delete(timer);
      run();
    }, ms);
    timers.add(timer);
  };
  const stream = (list: WireEvent[], gap = 140) => {
    list.forEach((event, index) => later(index * gap, () => send([event])));
  };
  const stopTimers = () => {
    for (const timer of timers) clearTimeout(timer);
    timers.clear();
  };

  const reply = async (body: string) => {
    send([{ kind: 'say', text: 'Ich sehe mir das an.' }]);
    await wait(220);
    send([{ kind: 'step', tool: 'bash', call_id: 'demo-1', text: 'bash · go test ./…' }]);
    await wait(480);
    send([{ kind: 'step', tool: 'bash', call_id: 'demo-1', text: 'bash · go test ./… · ok', done: true }]);
    for (const word of body.split(/(?= )/)) {
      if (!busy) return;
      send([{ kind: 'answering', text: word }]);
      await wait(12);
    }
    send([{ kind: 'answer', text: body }, { kind: 'working', n: 0 }]);
    busy = false;
  };

  return {
    bridge: {
      async InitialState() {
        return { profile: 'demo', thread: 'thread' };
      },
      async Open() {
        if (!scripted) {
          scripted = true;
          later(500, () => {
            stream([
              { kind: 'working', n: 1 },
              { kind: 'step', tool: 'session_status', call_id: 'demo-0', text: 'session_status · status' },
              { kind: 'step', tool: 'session_status', call_id: 'demo-0', text: 'session_status · status · 2 laufen', done: true },
              { kind: 'say', text: 'Zwei Sessions arbeiten noch: eine prüft die Scroll-Logik, die andere schreibt Tests.' },
              { kind: 'asked', text: 'Soll ich eine davon stoppen?', options: ['Ja, beide stoppen', 'Nein, laufen lassen'], recommended: 'Nein, laufen lassen' },
              { kind: 'working', n: 0 },
            ]);
            question = 'Soll ich eine davon stoppen?';
          });
        }
        return {
          before: 0,
          has_more: false,
          events: [
            { kind: 'message', text: 'Was hängt gerade?', at: new Date(Date.now() - 90000).toISOString() },
            {
              kind: 'answer',
              at: new Date(Date.now() - 85000).toISOString(),
              text: 'Nichts. Ich bin bereit.\n\n## Zuletzt\n\n- `go test ./...` lief durch\n- der Build ist aktuell\n\n```go\nfunc main() {\n\tprintln("bereit")\n}\n```',
            },
          ],
        };
      },
      async History() {
        return { events: [], before: 0, has_more: false };
      },
      async Send(text: string) {
        send([{ kind: 'message', text, at: new Date().toISOString() }, { kind: 'working', n: 1, own: true }]);
        busy = true;
        if (question) {
          question = null;
          send([{ kind: 'asked', text: 'Soll ich eine davon stoppen?', done: true }]);
          await wait(400);
          send([{ kind: 'answer', text: text === 'Ja, beide stoppen' ? 'Beide gestoppt. Der Thread ist wieder frei.' : 'Gut, sie laufen weiter.' }, { kind: 'working', n: 0 }]);
          busy = false;
          return;
        }
        await reply(
          `## Klar\n\nDas ist eine Demo-Antwort auf **${text}**.\n\n- der Verlauf rendert Markdown\n- Werkzeuge erscheinen als schmale Zeilen\n- Antworten wachsen beim Streamen\n\n\`\`\`go\nfunc main() {\n\tprintln("hallo")\n}\n\`\`\`\n\nEin [Link](https://example.com) bleibt Text.`,
        );
      },
      async Stop() {
        busy = false;
        stopTimers();
        send([{ kind: 'canceled', text: 'gestoppt' }, { kind: 'working', n: 0 }]);
      },
    },
    transport: {
      onEvents(handler) {
        handlers.push(handler);
      },
      onEnded() {},
      onRestarting() {},
    },
  };
}

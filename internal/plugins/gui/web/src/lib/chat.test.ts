import { describe, expect, it } from 'vitest';
import { Chat } from './chat.svelte';
import type { Link, Snapshot, State, Transport, WireEvent } from './wire';

interface Script {
  link: Link;
  events: ((batch: WireEvent[]) => void)[];
  ended: (() => void)[];
  restarting: (() => void)[];
}

function script(open: () => Promise<Snapshot>, send: (text: string) => Promise<void> = async () => {}): Script {
  const events: ((batch: WireEvent[]) => void)[] = [];
  const ended: (() => void)[] = [];
  const restarting: (() => void)[] = [];
  const transport: Transport = {
    onEvents: (handler) => events.push(handler),
    onEnded: (handler) => ended.push(handler),
    onRestarting: (handler) => restarting.push(handler),
  };
  const link: Link = {
    bridge: {
      InitialState: async (): Promise<State> => ({ profile: 'Luna', thread: 'thread' }),
      Open: open,
      Send: send,
      Stop: async () => {},
    },
    transport,
  };
  return { link, events, ended, restarting };
}

const snapshot = (...events: WireEvent[]): Snapshot => ({ events });

describe('Chat', () => {
  it('opens the thread and shows what it told, and where it stands', async () => {
    const { link } = script(async () =>
      snapshot(
        { kind: 'message', text: 'hello', at: '2026-01-01T10:00:00Z' },
        { kind: 'say', text: 'looking' },
        { kind: 'answer', text: 'done' },
      ),
    );
    const chat = new Chat(link);
    await chat.start();
    expect(chat.state).toEqual({ profile: 'Luna', thread: 'thread' });
    expect(chat.status).toBe('ready');
    expect(chat.entries.map((entry) => [entry.role, entry.text])).toEqual([
      ['owner', 'hello'],
      ['agent', 'looking'],
      ['agent', 'done'],
    ]);
  });

  it('grows one answer from its pieces and replaces it with the whole', async () => {
    const { link, events } = script(async () => snapshot());
    const chat = new Chat(link);
    await chat.start();
    events[0]([{ kind: 'answering', text: 'Hel' }]);
    events[0]([{ kind: 'answering', text: 'lo' }]);
    expect(chat.entries).toHaveLength(1);
    expect(chat.entries[0]).toMatchObject({ role: 'agent', text: 'Hello', streaming: true });
    events[0]([{ kind: 'answer', text: 'Hello **world**' }]);
    expect(chat.entries[0]).toMatchObject({ role: 'agent', text: 'Hello **world**', streaming: false });
    // A say after an answer is a line of its own, not appended to it.
    events[0]([{ kind: 'say', text: 'next round' }]);
    expect(chat.entries).toHaveLength(2);
    expect(chat.entries[1].text).toBe('next round');
  });

  it('updates a call of a tool once it ended', async () => {
    const { link, events } = script(async () => snapshot());
    const chat = new Chat(link);
    await chat.start();
    events[0]([{ kind: 'step', tool: 'bash', call_id: 'c1', text: 'bash · go test' }]);
    expect(chat.entries[0]).toMatchObject({ role: 'tool', done: false, failed: false });
    events[0]([{ kind: 'step', tool: 'bash', call_id: 'c1', text: 'bash · go test · 2s', done: true }]);
    expect(chat.entries).toHaveLength(1);
    expect(chat.entries[0]).toMatchObject({ role: 'tool', text: 'bash · go test · 2s', done: true });
    events[0]([{ kind: 'step', tool: 'bash', call_id: 'c2', text: 'bash · false', done: true, failed: true }]);
    expect(chat.entries[1]).toMatchObject({ failed: true });
  });

  it('shows a question until it is answered, and waits for the owner', async () => {
    const { link, events } = script(async () => snapshot());
    const chat = new Chat(link);
    await chat.start();
    events[0]([
      { kind: 'asked', text: 'Which one?', options: ['a', 'b'], recommended: 'b' },
      { kind: 'working', n: 0 },
    ]);
    expect(chat.entries[0]).toMatchObject({ role: 'question', open: true, options: ['a', 'b'], recommended: 'b' });
    expect(chat.busy).toBe(false);
    events[0]([{ kind: 'asked', text: 'b', done: true }]);
    expect(chat.entries[0]).toMatchObject({ open: false, answered: 'b' });
  });

  it('shows who works, and what waits to be taken', async () => {
    const { link, events } = script(async () => snapshot());
    const chat = new Chat(link);
    await chat.start();
    events[0]([{ kind: 'working', n: 2, own: true }]);
    expect(chat.busy && chat.own).toBe(true);
    events[0]([{ kind: 'working', n: 1, own: false }]);
    expect(chat.busy && !chat.own).toBe(true);
    events[0]([{ kind: 'queued', text: 'second' }]);
    expect(chat.queued).toEqual(['second']);
    events[0]([{ kind: 'unqueued' }]);
    expect(chat.queued).toEqual([]);
    events[0]([{ kind: 'message', text: 'first' }, { kind: 'working', n: 0 }]);
    expect(chat.busy).toBe(false);
  });

  it('names the thread by its title', async () => {
    const { link, events } = script(async () => snapshot());
    const chat = new Chat(link);
    await chat.start();
    events[0]([{ kind: 'title', text: 'Scrolling' }]);
    expect(chat.title).toBe('Scrolling');
  });

  it('sends what the owner wrote, and reports what fails', async () => {
    const sent: string[] = [];
    const { link } = script(
      async () => snapshot(),
      async (text) => {
        sent.push(text);
        if (text === 'bad') throw new Error('provider gone');
      },
    );
    const chat = new Chat(link);
    await chat.start();
    await expect(chat.send('  hello  ')).resolves.toBe(true);
    expect(sent).toEqual(['hello']);
    await expect(chat.send(' \n ')).resolves.toBe(false);
    expect(sent).toEqual(['hello']);
    await expect(chat.send('bad')).resolves.toBe(false);
    expect(chat.error).toBe('provider gone');
    expect(chat.sending).toBe(false);
  });

  it('stops the answer on its way', async () => {
    let stopped = 0;
    const { link } = script(async () => snapshot());
    link.bridge.Stop = async () => {
      stopped++;
    };
    const chat = new Chat(link);
    await chat.start();
    await chat.stop();
    expect(stopped).toBe(1);
  });

  it('reopens the thread when the stream ended, once the node came back', async () => {
    let opens = 0;
    const { link, ended } = script(async () => {
      opens++;
      if (opens === 1) throw new Error('node gone');
      return snapshot({ kind: 'answer', text: 'back' });
    });
    const chat = new Chat(link, [1, 1]);
    await chat.start();
    expect(chat.status).toBe('offline');
    expect(chat.error).toBe('node gone');
    ended[0]();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(chat.status).toBe('ready');
    expect(chat.entries.at(-1)?.text).toBe('back');
  });

  it('reopens when the stream ended before the snapshot was shown', async () => {
    let opens = 0;
    const { link, ended } = script(async () => {
      opens++;
      if (opens === 1) queueMicrotask(() => ended[0]());
      return snapshot({ kind: 'answer', text: 'back' });
    });
    const chat = new Chat(link, [1, 1]);
    await chat.start();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(opens).toBe(2);
    expect(chat.status).toBe('ready');
    expect(chat.entries.at(-1)?.text).toBe('back');
  });

  it('gives up after the last wait, and lets the owner try again', async () => {
    let opens = 0;
    const { link, ended } = script(async () => {
      opens++;
      if (opens > 1) throw new Error('still gone');
      return snapshot();
    });
    const chat = new Chat(link, [1]);
    await chat.start();
    ended[0]();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(chat.status).toBe('offline');
    link.bridge.Open = async () => snapshot({ kind: 'answer', text: 'back' });
    await chat.open();
    expect(chat.status).toBe('ready');
  });

  it('starts anew, and does not reopen the thread by itself', async () => {
    let opens = 0;
    const { link, ended, restarting } = script(async () => {
      opens++;
      return snapshot();
    });
    const chat = new Chat(link, [1]);
    await chat.start();
    restarting[0]();
    expect(chat.status).toBe('restarting');
    ended[0]();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(opens).toBe(1);
    expect(chat.status).toBe('restarting');
  });

  it('does not reconnect once the page is gone', async () => {
    let opens = 0;
    const { link, ended } = script(async () => {
      opens++;
      throw new Error('gone');
    });
    const chat = new Chat(link, [1]);
    await chat.start();
    chat.dispose();
    ended[0]();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(opens).toBe(1);
  });
});

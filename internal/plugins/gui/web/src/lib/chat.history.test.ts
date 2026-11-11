import { describe, expect, it, vi } from 'vitest';
import { Chat } from './chat.svelte';
import type { Link, Snapshot, WireEvent } from './wire';

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
const page = (before: number, has_more: boolean, ...events: WireEvent[]): Snapshot => ({ before, has_more, events });
function fixture(initial = page(100, true, { kind: 'message', text: 'current' })) {
  let receive!: (events: WireEvent[]) => void;
  const history = vi.fn(async (_before: number) => page(0, false));
  const link: Link = {
    bridge: {
      InitialState: async () => ({ profile: 'fixture', thread: 'fixture' }),
      Open: async () => initial,
      History: history,
      Send: async () => { throw new Error('No messages allowed in fixture'); },
      Stop: async () => {},
    },
    transport: { onEvents: (handler) => { receive = handler; }, onEnded() {}, onRestarting() {} },
  };
  return { link, history, receive: (events: WireEvent[]) => receive(events), chat: new Chat(link, []) };
}

describe('history paging', () => {
  it('requests only on demand, serializes pages and preserves live objects, state and IDs', async () => {
    const f = fixture();
    await f.chat.start();
    expect(f.history).not.toHaveBeenCalled();
    f.receive([
      { kind: 'title', text: 'live title' }, { kind: 'queued', text: 'next' },
      { kind: 'asked', text: 'live question', options: ['yes'] },
      { kind: 'answering', text: 'live ' }, { kind: 'working', n: 1, own: true },
    ]);
    const live = [...f.chat.entries];
    const pending = deferred<Snapshot>();
    f.history.mockImplementationOnce(() => pending.promise);
    const loading = f.chat.loadOlder();
    await f.chat.loadOlder();
    expect(f.history).toHaveBeenCalledExactlyOnceWith(100);
    f.receive([{ kind: 'answering', text: 'output' }]);
    pending.resolve(page(40, true,
      { kind: 'title', text: 'old title' }, { kind: 'queued', text: 'old queue' },
      { kind: 'message', text: 'old' }, { kind: 'asked', text: 'old question' },
      { kind: 'answer', text: 'old answer' }, { kind: 'working', n: 0 },
    ));
    await loading;
    expect(f.chat.entries.slice(-live.length).every((entry, i) => entry === live[i])).toBe(true);
    expect(f.chat.entries.at(-1)?.text).toBe('live output');
    expect(f.chat.entries.filter((e) => e.role === 'question' && e.open).map((e) => e.text)).toEqual(['live question']);
    expect([f.chat.busy, f.chat.own, f.chat.title, [...f.chat.queued]]).toEqual([true, true, 'live title', ['next']]);
    f.receive([{ kind: 'say', text: 'newest' }]);
    expect(new Set(f.chat.entries.map((e) => e.id)).size).toBe(f.chat.entries.length);
    await f.chat.loadOlder();
    expect(f.history).toHaveBeenLastCalledWith(40);
    expect(f.chat.hasMore).toBe(false);
    await f.chat.loadOlder();
    expect(f.history).toHaveBeenCalledTimes(2);
  });

  it('leaves history and cursor intact on errors and supports retry', async () => {
    const f = fixture();
    await f.chat.start();
    const current = f.chat.entries[0];
    f.history.mockRejectedValueOnce(new Error('fixture unavailable'));
    await f.chat.loadOlder();
    expect(f.chat.historyError).toBe('fixture unavailable');
    expect(f.chat.status).toBe('ready');
    expect(f.chat.entries[0]).toBe(current);
    expect(f.chat.loadingOlder).toBe(false);
    f.history.mockResolvedValueOnce(page(100, true, { kind: 'message', text: 'duplicate' }));
    await f.chat.loadOlder();
    expect(f.chat.historyError).toContain('cursor');
    expect(f.chat.entries).toHaveLength(1);
    await f.chat.loadOlder();
    expect(f.history.mock.calls.map(([cursor]) => cursor)).toEqual([100, 100, 100]);
    expect(f.chat.historyError).toBe('');
  });

  it('ignores a stale page after reopening or disposal', async () => {
    const f = fixture();
    await f.chat.start();
    const pending = deferred<Snapshot>();
    f.history.mockImplementationOnce(() => pending.promise);
    const loading = f.chat.loadOlder();
    await f.chat.open();
    pending.resolve(page(0, false, { kind: 'message', text: 'stale' }));
    await loading;
    expect(f.chat.entries.map((e) => e.text)).toEqual(['current']);
    expect(f.chat.hasMore).toBe(true);
    const disposed = deferred<Snapshot>();
    f.history.mockImplementationOnce(() => disposed.promise);
    const more = f.chat.loadOlder();
    f.chat.dispose();
    disposed.resolve(page(0, false, { kind: 'message', text: 'stale too' }));
    await more;
    expect(f.chat.entries.map((e) => e.text)).toEqual(['current']);
  });

  it('replays events received while Open is pending after the snapshot', async () => {
    const f = fixture();
    const pending = deferred<Snapshot>();
    const entered = deferred<void>();
    f.link.bridge.Open = () => { entered.resolve(); return pending.promise; };
    const starting = f.chat.start();
    await entered.promise;
    f.receive([{ kind: 'message', text: 'pending live' }, { kind: 'answering', text: 'pending answer' }]);
    pending.resolve(page(50, true, { kind: 'message', text: 'snapshot' }, { kind: 'answer', text: 'snapshot answer' }));
    await starting;
    expect(f.chat.entries.map((e) => e.text)).toEqual(['snapshot', 'snapshot answer', 'pending live', 'pending answer']);
    expect(f.chat.entries.at(-1)?.streaming).toBe(true);
  });
});

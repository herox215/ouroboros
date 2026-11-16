import { afterEach, describe, expect, it, vi } from 'vitest';
import { Board } from './board.svelte';
import type { BoardBridge, BoardView, Ticket } from './wire';

const view = (pending = false): BoardView => ({
  lanes: [
    { column: 'new', cards: [{ id: 2, title: 'Export', column: 'new', changed: '2026-11-16T09:00:00Z', notes: 0 }] },
    { column: 'ready', cards: [] },
    { column: 'progress', cards: [] },
    { column: 'blocked', cards: [] },
    { column: 'done', cards: [{ id: 1, title: 'Typo', column: 'done', changed: '2026-11-16T08:00:00Z', notes: 2 }] },
  ],
  pending,
});

const ticket = (id: number, title = 'Export'): Ticket => ({
  id,
  title,
  text: 'as CSV',
  column: 'new',
  created: '2026-11-16T09:00:00Z',
  changed: '2026-11-16T09:00:00Z',
});

/** A promise the test settles when it wants to. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function bridge(over: Partial<BoardBridge> = {}): BoardBridge & { boards: number; tickets: number[] } {
  const calls = { boards: 0, tickets: [] as number[] };
  return Object.assign(calls, {
    Board: async () => {
      calls.boards++;
      return view();
    },
    Ticket: async (id: number) => {
      calls.tickets.push(id);
      return ticket(id);
    },
    ...over,
  });
}

describe('Board', () => {
  afterEach(() => vi.useRealTimers());

  it('reads the board when it starts, and again every few seconds until it stops', async () => {
    vi.useFakeTimers();
    const b = bridge();
    const board = new Board(b, 1000);
    expect(board.view).toBeNull();
    board.start();
    board.start(); // it reads already
    await vi.advanceTimersByTimeAsync(0);
    expect(b.boards).toBe(1);
    expect(board.view?.lanes.map((lane) => lane.cards.length)).toEqual([1, 0, 0, 0, 1]);
    await vi.advanceTimersByTimeAsync(2000);
    expect(b.boards).toBe(3);
    board.stop();
    board.stop(); // nothing to stop
    await vi.advanceTimersByTimeAsync(5000);
    expect(b.boards).toBe(3);
    expect(board.view).not.toBeNull();
    board.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(b.boards).toBe(4);
    board.stop();
  });

  it('keeps what it showed while the board cannot be read, and says why', async () => {
    let fail = false;
    const b = bridge({
      Board: async () => {
        if (fail) throw new Error('the board cannot be read');
        return view(true);
      },
    });
    const board = new Board(b);
    await board.refresh();
    expect(board.error).toBe('');
    expect(board.view?.pending).toBe(true);
    fail = true;
    await board.refresh();
    expect(board.error).toBe('the board cannot be read');
    expect(board.view?.pending).toBe(true);
    fail = false;
    await board.refresh();
    expect(board.error).toBe('');
  });

  it('says what went wrong when that is no error', async () => {
    const board = new Board(
      bridge({
        Board: () => Promise.reject('no board here'),
      }),
    );
    await board.refresh();
    expect(board.error).toBe('no board here');
  });

  it('sets its clock with every reading', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-11-16T12:00:00Z'));
    const board = new Board(bridge());
    vi.setSystemTime(new Date('2026-11-16T12:05:00Z'));
    await board.refresh();
    expect(board.now).toBe(Date.parse('2026-11-16T12:05:00Z'));
  });

  it('opens a ticket, reads it again with the board, and closes it', async () => {
    const b = bridge();
    const board = new Board(b);
    await board.open(2);
    expect(board.opened).toBe(2);
    expect(board.ticket?.id).toBe(2);
    expect(b.tickets).toEqual([2]);
    await board.refresh();
    expect(b.tickets).toEqual([2, 2]);
    board.close();
    expect(board.opened).toBeNull();
    expect(board.ticket).toBeNull();
    await board.refresh();
    expect(b.tickets).toEqual([2, 2]);
  });

  it('shows no ticket of another while the one opened is on its way', async () => {
    const second = deferred<Ticket>();
    const b = bridge({
      Ticket: (id: number) => (id === 1 ? second.promise : Promise.resolve(ticket(id))),
    });
    const board = new Board(b);
    await board.open(2);
    const opening = board.open(1);
    expect(board.opened).toBe(1);
    expect(board.ticket).toBeNull();
    second.resolve(ticket(1, 'Typo'));
    await opening;
    expect(board.ticket?.title).toBe('Typo');
  });

  it('keeps the ticket it shows while it is read again', async () => {
    const again = deferred<Ticket>();
    let reads = 0;
    const board = new Board(
      bridge({
        Ticket: (id: number) => (++reads === 1 ? Promise.resolve(ticket(id)) : again.promise),
      }),
    );
    await board.open(2);
    const reading = board.open(2);
    expect(board.ticket?.id).toBe(2);
    again.resolve(ticket(2, 'Export, renamed'));
    await reading;
    expect(board.ticket?.title).toBe('Export, renamed');
  });

  it('drops the answer for a ticket that was left meanwhile', async () => {
    const slow = deferred<Ticket>();
    const failing = deferred<Ticket>();
    const b = bridge({
      Ticket: (id: number) => (id === 1 ? slow.promise : id === 3 ? failing.promise : Promise.resolve(ticket(id))),
    });
    const board = new Board(b);
    const first = board.open(1);
    await board.open(2);
    slow.resolve(ticket(1, 'Typo'));
    await first;
    expect(board.opened).toBe(2);
    expect(board.ticket?.id).toBe(2);
    // The same holds for one that fails, and for one that was closed.
    const third = board.open(3);
    board.close();
    failing.reject(new Error('gone'));
    await third;
    expect(board.ticketError).toBe('');
    expect(board.opened).toBeNull();
    const fourth = deferred<Ticket>();
    const late = new Board(bridge({ Ticket: () => fourth.promise }));
    const opening = late.open(4);
    late.close();
    fourth.resolve(ticket(4));
    await opening;
    expect(late.ticket).toBeNull();
  });

  it('says why a ticket cannot be read, and forgets that once it can', async () => {
    let gone = true;
    const board = new Board(
      bridge({
        Ticket: async (id: number) => {
          if (gone) throw new Error('the board has no ticket 9 any more');
          return ticket(id);
        },
      }),
    );
    await board.open(9);
    expect(board.ticket).toBeNull();
    expect(board.ticketError).toBe('the board has no ticket 9 any more');
    gone = false;
    await board.refresh();
    expect(board.ticketError).toBe('');
    expect(board.ticket?.id).toBe(9);
    // Another ticket starts without the error of the one before.
    gone = true;
    await board.open(9);
    expect(board.ticketError).not.toBe('');
    const other = board.open(8);
    expect(board.ticketError).toBe('');
    await other;
  });
});

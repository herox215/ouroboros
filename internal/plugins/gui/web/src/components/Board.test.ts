import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import BoardView from './Board.svelte';
import { Board } from '../lib/board.svelte';
import { setLocale } from '../lib/i18n';
import type { BoardBridge, BoardView as View, Ticket } from '../lib/wire';

const at = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();

const full = (): View => ({
  lanes: [
    {
      column: 'new',
      cards: [
        { id: 4, title: 'Search', column: 'new', changed: at(1), notes: 0 },
        { id: 1, title: 'Dark mode', column: 'new', role: 'refinement', session: '0123456789abcdef', since: at(5), changed: at(5), notes: 1 },
      ],
    },
    { column: 'ready', cards: [] },
    { column: 'progress', cards: [] },
    { column: 'blocked', cards: [{ id: 2, title: 'Export', column: 'blocked', from: 'new', reason: '1. Which separator?', told: true, changed: at(90), notes: 3 }] },
    { column: 'done', cards: [{ id: 3, title: 'Typo', column: 'done', changed: at(3000), notes: 2 }] },
  ],
  looked: { at: at(2), session: 'look-session', result: 'Told the thread.' },
  pending: true,
});

const blocked = (): Ticket => ({
  id: 2,
  title: 'Export',
  text: 'The table as **CSV**.',
  directory: '/srv/app',
  column: 'blocked',
  from: 'new',
  reason: '1. Which separator?\n2. Which encoding?',
  told: true,
  created: at(200),
  changed: at(90),
  runs: [
    { session: 'aaaaaaaa11111111', role: 'refinement', began: at(120), ended: at(100), outcome: 'blocked' },
    { session: 'bbbbbbbb22222222', role: 'refinement', began: at(95) },
  ],
  notes: [
    { at: at(100), by: 'session aaaaaaaa11111111', text: 'moved from new to blocked' },
    { at: at(95), by: 'thread', text: 'a semicolon,\nand UTF-8' },
  ],
});

function mount(bridge: Partial<BoardBridge>) {
  const board = new Board({
    Board: async () => full(),
    Ticket: async () => blocked(),
    ...bridge,
  });
  const { container } = render(BoardView, { props: { board } });
  return { board, container };
}

const texts = (container: Element, selector: string) =>
  [...container.querySelectorAll(selector)].map((node) => (node.textContent ?? '').replace(/\s+/g, ' ').trim());

describe('Board', () => {
  beforeEach(() => setLocale('en'));

  it('says that it reads the board until it has it', async () => {
    const { board, container } = mount({});
    expect(container.querySelector('[role="status"]')?.textContent).toBe('Reading the board …');
    expect(container.querySelector('.lanes')).toBeNull();
    await board.refresh();
    await tick();
    expect(container.querySelector('[role="status"]')).toBeNull();
    expect(container.querySelector('.lanes')).not.toBeNull();
  });

  it('shows a lane a column with its tickets, and how each stands', async () => {
    const { board, container } = mount({});
    await board.refresh();
    await tick();
    expect(texts(container, '.lane-head h2')).toEqual(['New', 'Ready', 'In progress', 'Blocked', 'Done']);
    expect(texts(container, '.lane-count')).toEqual(['2', '0', '0', '1', '1']);
    expect(texts(container, '.lane[data-column="new"] .card-title')).toEqual(['Search', 'Dark mode']);
    expect(texts(container, '.lane[data-column="ready"] .lane-empty')).toEqual(['Nothing here']);
    // The lane names its heading, for who does not see the columns.
    const lane = container.querySelector('.lane[data-column="blocked"]') as HTMLElement;
    expect(document.getElementById(lane.getAttribute('aria-labelledby') ?? '')?.textContent).toBe('Blocked');
    const [search, dark] = [...container.querySelectorAll('.lane[data-column="new"] .card')];
    expect(texts(search, '.card-id')).toEqual(['#4']);
    expect(search.querySelector('.card-work')).toBeNull();
    expect(texts(search, '.card-meta')).toEqual(['changed 1 min. ago']);
    expect(texts(dark, '.card-work')).toEqual(['Refinement · 5 min. ago']);
    expect(texts(dark, '.card-meta')).toEqual(['changed 5 min. ago 1 note']);
    const export_ = container.querySelector('.lane[data-column="blocked"] .card') as HTMLElement;
    expect(texts(export_, '.card-reason')).toEqual(['1. Which separator?']);
    expect(texts(export_, '.card-meta')).toEqual(['changed 1 hr. ago 3 notes ✓ You were told']);
    expect(texts(container, '.lane[data-column="done"] .card-meta')).toEqual(['changed 2 days ago 2 notes']);
  });

  it('says when the orchestrator looked, and that it looks again', async () => {
    const { board, container } = mount({});
    await board.refresh();
    await tick();
    expect(texts(container, '.board-foot')).toEqual([
      'The orchestrator last looked 2 min. ago: Told the thread. The board changed since: it looks again within a minute.',
    ]);
  });

  it('says that the orchestrator has not looked yet', async () => {
    const view = full();
    delete view.looked;
    view.pending = false;
    const { board, container } = mount({ Board: async () => view });
    await board.refresh();
    await tick();
    expect(texts(container, '.board-foot')).toEqual(['The orchestrator has not looked yet.']);
  });

  it('ends what the orchestrator said nothing of with a full stop', async () => {
    const view = full();
    view.looked = { at: at(2), session: 'look-session' };
    view.pending = false;
    const { board, container } = mount({ Board: async () => view });
    await board.refresh();
    await tick();
    expect(texts(container, '.board-foot')).toEqual(['The orchestrator last looked 2 min. ago.']);
  });

  it('invites to the first ticket on a board that has none', async () => {
    const empty: View = { lanes: full().lanes.map((lane) => ({ ...lane, cards: [] })), pending: false };
    const { board, container } = mount({ Board: async () => empty });
    await board.refresh();
    await tick();
    expect(container.querySelector('.lanes')).toBeNull();
    expect(texts(container, '.empty-title')).toEqual(['No tickets yet']);
  });

  it('says why the board cannot be read, and keeps what it showed', async () => {
    let fail = true;
    const { board, container } = mount({
      Board: async () => {
        if (fail) throw new Error('the board is broken');
        return full();
      },
    });
    await board.refresh();
    await tick();
    expect(texts(container, '[role="alert"]')).toEqual(['The board cannot be read: the board is broken']);
    expect(container.querySelector('[role="status"]')).toBeNull();
    fail = false;
    await board.refresh();
    fail = true;
    await board.refresh();
    await tick();
    expect(container.querySelector('[role="alert"]')).not.toBeNull();
    expect(container.querySelectorAll('.card')).toHaveLength(4);
  });

  it('opens a ticket from its card, and closes it with Escape or its button', async () => {
    const { board, container } = mount({});
    await board.refresh();
    await tick();
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    const card = container.querySelector('.lane[data-column="blocked"] .card') as HTMLButtonElement;
    expect(card.getAttribute('aria-expanded')).toBe('false');
    await fireEvent.click(card);
    await waitFor(() => expect(container.querySelector('#ticket-title')?.textContent).toBe('Export'));
    expect(board.opened).toBe(2);
    expect(card.getAttribute('aria-expanded')).toBe('true');
    const dialog = container.querySelector('[role="dialog"]') as HTMLElement;
    expect(document.getElementById(dialog.getAttribute('aria-labelledby') ?? '')?.textContent).toBe('Export');
    // The panel takes the focus, and gives it back to the card it came from.
    const closer = dialog.querySelector('.ticket-close') as HTMLButtonElement;
    expect(document.activeElement).toBe(closer);
    expect(closer.getAttribute('aria-label')).toBe('Close the ticket');
    await fireEvent.keyDown(window, { key: 'a' });
    expect(container.querySelector('[role="dialog"]')).not.toBeNull();
    await fireEvent.keyDown(window, { key: 'Escape' });
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(board.opened).toBeNull();
    expect(document.activeElement).toBe(card);
    await fireEvent.click(card);
    await waitFor(() => expect(container.querySelector('.ticket-close')).not.toBeNull());
    await fireEvent.click(container.querySelector('.ticket-close') as HTMLButtonElement);
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(card);
  });

  it('shows the ticket in full: why it is blocked, what is to be done, its sessions and notes', async () => {
    const { board, container } = mount({});
    await board.refresh();
    await board.open(2);
    await tick();
    const dialog = container.querySelector('[role="dialog"]') as HTMLElement;
    expect(texts(dialog, '.ticket-id')).toEqual(['#2 Blocked']);
    expect(dialog.querySelector('.chip')?.getAttribute('data-column')).toBe('blocked');
    expect(texts(dialog, '.ticket-meta')).toEqual(['created 3 hr. ago changed 1 hr. ago ✓ You were told']);
    expect(texts(dialog, '.ticket-part h3')).toEqual(['Why it does not go on', 'What is to be done', 'Project', 'Sessions', 'Notes']);
    expect(dialog.querySelector('.ticket-part.blocked .plain')?.textContent).toBe('1. Which separator?\n2. Which encoding?');
    expect(texts(dialog, '.ticket-from')).toEqual(['came from New']);
    // What is to be done is formatted; what was noted is shown as written.
    expect(dialog.querySelector('.ticket-part .markdown strong')?.textContent).toBe('CSV');
    expect(texts(dialog, '.ticket-directory')).toEqual(['/srv/app']);
    // The session that works comes first, as the latest note does.
    expect(texts(dialog, '.run')).toEqual(['Refinement bbbbbbbb working · 1 hr. ago', 'Refinement aaaaaaaa ended 1 hr. ago · Blocked']);
    expect([...dialog.querySelectorAll('.run')].map((run) => run.classList.contains('working'))).toEqual([true, false]);
    expect(dialog.querySelector('.run-session')?.getAttribute('title')).toBe('bbbbbbbb22222222');
    expect(texts(dialog, '.note-head')).toEqual(['Thread 1 hr. ago', 'Session aaaaaaaa 1 hr. ago']);
    expect(dialog.querySelector('.ticket-note .plain')?.textContent).toBe('a semicolon,\nand UTF-8');
    expect([...dialog.querySelectorAll('.ticket-note')].map((note) => note.getAttribute('data-by'))).toEqual(['thread', 'session']);
  });

  it('shows the result of a ticket that is done, and says what a ticket lacks', async () => {
    const done: Ticket = { id: 3, title: 'Typo', text: 'In the footer.', column: 'done', result: 'Fixed in `footer.html`.', created: at(3100), changed: at(3000), runs: [{ session: 'cccccccc', role: 'implementation', began: at(3010), ended: at(3000) }] };
    const { board, container } = mount({ Ticket: async () => done });
    await board.refresh();
    await board.open(3);
    await tick();
    const dialog = container.querySelector('[role="dialog"]') as HTMLElement;
    expect(texts(dialog, '.ticket-part h3')).toEqual(['Result', 'What is to be done', 'Sessions', 'Notes']);
    expect(dialog.querySelector('.ticket-part.result code')?.textContent).toBe('footer.html');
    expect(texts(dialog, '.ticket-meta')).toEqual(['created 2 days ago changed 2 days ago']);
    expect(texts(dialog, '.run')).toEqual(['Implementation cccccccc ended 2 days ago ·']);
    expect(texts(dialog, '.ticket-none')).toEqual(['Nothing was noted yet.']);
  });

  it('says that no session worked on a ticket yet, and nothing of a block that has no reason', async () => {
    const fresh: Ticket = { id: 4, title: 'Search', text: 'Over all notes.', column: 'blocked', created: at(1), changed: at(1) };
    const { board, container } = mount({ Ticket: async () => fresh });
    await board.refresh();
    await board.open(4);
    await tick();
    const dialog = container.querySelector('[role="dialog"]') as HTMLElement;
    expect(dialog.querySelector('.ticket-part.blocked')).toBeNull();
    expect(texts(dialog, '.ticket-none')).toEqual(['No session has worked on it yet.', 'Nothing was noted yet.']);
  });

  it('says that it reads the ticket, and why it cannot', async () => {
    let fail: Error | null = null;
    let release!: (ticket: Ticket) => void;
    const { board, container } = mount({
      Ticket: () => (fail ? Promise.reject(fail) : new Promise<Ticket>((resolve) => (release = resolve))),
    });
    await board.refresh();
    const opening = board.open(2);
    await tick();
    const dialog = container.querySelector('[role="dialog"]') as HTMLElement;
    expect(texts(dialog, '.ticket-id')).toEqual(['#2']);
    expect(texts(dialog, '[role="status"]')).toEqual(['Reading the ticket …']);
    expect(dialog.querySelector('#ticket-title')?.textContent).toBe('');
    release(blocked());
    await opening;
    await tick();
    expect(dialog.querySelector('[role="status"]')).toBeNull();
    // A ticket that is gone says so, over what was shown of it.
    fail = new Error('the board has no ticket 2 any more');
    await board.refresh();
    await tick();
    expect(texts(dialog, '[role="alert"]')).toEqual(['the board has no ticket 2 any more']);
    expect(dialog.querySelector('#ticket-title')?.textContent).toBe('Export');
    // One that never came says only why.
    board.close();
    await board.open(9);
    await tick();
    const gone = container.querySelector('[role="dialog"]') as HTMLElement;
    expect(texts(gone, '[role="alert"]')).toEqual(['the board has no ticket 2 any more']);
    expect(gone.querySelector('[role="status"]')).toBeNull();
    expect(gone.querySelector('.ticket-part')).toBeNull();
  });

  it('gives no focus back when the ticket was opened by another way', async () => {
    const { board, container } = mount({});
    await board.refresh();
    await board.open(2);
    await tick();
    await fireEvent.click(container.querySelector('.ticket-close') as HTMLButtonElement);
    expect(board.opened).toBeNull();
  });

  it('says which agent works on a ticket, by the name it has now', async () => {
    const view = full();
    view.lanes[0].cards[1] = { ...view.lanes[0].cards[1], agent: 'agent-2f288108', agent_name: 'Jeff' };
    view.lanes[0].cards.push({ id: 5, title: 'Pears', column: 'new', role: 'refinement', session: 'ccc', since: at(1), agent: 'thread', changed: at(1), notes: 0 });
    const ticket = blocked();
    ticket.runs = [
      { session: 'aaaaaaaa11111111', role: 'refinement', agent: 'agent-2f288108', agent_name: 'Jeff', began: at(120), ended: at(100), outcome: 'blocked' },
      { session: 'bbbbbbbb22222222', role: 'refinement', began: at(95) },
    ];
    const board = new Board({ Board: async () => view, Ticket: async () => ticket });
    const shown = render(BoardView, { props: { board, names: { thread: 'pythia' } } });
    await board.refresh();
    await waitFor(() => expect(shown.container.querySelectorAll('.card-agent')).toHaveLength(2));
    // As it was called when it began, while the names do not know it; the
    // agent of the thread by the name of the profile.
    expect(texts(shown.container, '.card-agent')).toEqual(['as Jeff', 'as pythia']);
    // A card nobody works on names no agent.
    expect(shown.container.querySelector('.card .card-title')?.textContent).toBe('Search');
    await shown.rerender({ board, names: { thread: 'pythia', 'agent-2f288108': 'Jeffrey' } });
    expect(texts(shown.container, '.card-agent')).toEqual(['as Jeffrey', 'as pythia']);

    await board.open(2);
    await waitFor(() => expect(shown.container.querySelectorAll('.run')).toHaveLength(2));
    // The latest session comes first; one from before the board knew its agent names none.
    expect([...shown.container.querySelectorAll('.run')].map((run) => run.querySelector('.run-agent')?.textContent ?? '')).toEqual(['', 'Jeffrey']);
  });
});

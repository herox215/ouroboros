import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import App from './App.svelte';
import { Chat } from './lib/chat.svelte';
import { Board } from './lib/board.svelte';
import { Agents } from './lib/agents.svelte';
import { Jobs } from './lib/jobs.svelte';
import { Sessions } from './lib/sessions.svelte';
import { SYSTEM, Themes } from './lib/themes.svelte';
import { setLocale } from './lib/i18n';
import type { BoardBridge, Link } from './lib/wire';

const link = (): Link => ({
  bridge: {
    InitialState: async () => ({ profile: 'Luna', thread: 'thread' }),
    Open: async () => ({ events: [{ kind: 'answer', text: 'ready when you are' }], before: 0, has_more: false }),
    History: async () => ({ events: [], before: 0, has_more: false }),
    Send: async () => {},
    Stop: async () => {},
  },
  transport: { onEvents() {}, onEnded() {}, onRestarting() {} },
});

function boardBridge(): BoardBridge & { reads: number } {
  const calls = { reads: 0 };
  return Object.assign(calls, {
    Board: async () => {
      calls.reads++;
      return {
        lanes: [{ column: 'new' as const, cards: [{ id: 1, title: 'Dark mode', column: 'new' as const, changed: new Date().toISOString(), notes: 0 }] }],
        pending: false,
      };
    },
    Ticket: async () => {
      throw new Error('not asked for');
    },
  });
}

const pressed = (container: Element) =>
  [...container.querySelectorAll('.views button')].map((button) => `${button.textContent}:${button.getAttribute('aria-pressed')}`);

describe('App', () => {
  beforeEach(() => setLocale('en'));
  afterEach(() => vi.useRealTimers());

  it('shows the thread, and the board beside it while it is chosen', async () => {
    const bridge = boardBridge();
    const { container } = render(App, { props: { chat: new Chat(link()), board: new Board(bridge, 1000) } });
    await waitFor(() => expect(container.querySelector('.transcript')).not.toBeNull());
    expect(pressed(container)).toEqual(['Thread:true', 'Board:false']);
    expect(container.querySelector('.views')?.getAttribute('aria-label')).toBe('View');
    expect(container.querySelector('.board')).toBeNull();
    expect(container.querySelector('.shell')?.classList.contains('wide')).toBe(false);
    expect(bridge.reads).toBe(0);
    // What is typed stays in the field while the board is shown.
    const area = container.querySelector('textarea') as HTMLTextAreaElement;
    await fireEvent.input(area, { target: { value: 'a draft' } });
    const [thread, board] = [...container.querySelectorAll('.views button')] as HTMLButtonElement[];
    await fireEvent.click(board);
    await waitFor(() => expect(container.querySelector('.card-title')?.textContent).toBe('Dark mode'));
    expect(pressed(container)).toEqual(['Thread:false', 'Board:true']);
    expect(container.querySelector('.transcript')).toBeNull();
    expect(container.querySelector('.shell')?.classList.contains('wide')).toBe(true);
    expect((container.querySelector('.compose') as HTMLElement).hidden).toBe(true);
    expect(bridge.reads).toBe(1);
    await fireEvent.click(thread);
    await waitFor(() => expect(container.querySelector('.transcript')).not.toBeNull());
    expect(container.querySelector('.board')).toBeNull();
    expect((container.querySelector('.compose') as HTMLElement).hidden).toBe(false);
    expect((container.querySelector('textarea') as HTMLTextAreaElement).value).toBe('a draft');
  });

  it('reads the board only while it shows it', async () => {
    vi.useFakeTimers();
    const bridge = boardBridge();
    const { container } = render(App, { props: { chat: new Chat(link()), board: new Board(bridge, 1000) } });
    await vi.advanceTimersByTimeAsync(3000);
    expect(bridge.reads).toBe(0);
    const [thread, board] = [...container.querySelectorAll('.views button')] as HTMLButtonElement[];
    await fireEvent.click(board);
    await vi.advanceTimersByTimeAsync(2000);
    expect(bridge.reads).toBe(3);
    await fireEvent.click(thread);
    await vi.advanceTimersByTimeAsync(5000);
    expect(bridge.reads).toBe(3);
  });

  it('lists the agents while they are chosen, and names the profile anew once the agent of the thread is renamed', async () => {
    let name = 'Luna';
    let reads = 0;
    const agents = new Agents({
      Agents: async () => {
        reads++;
        return [{ id: 'thread', kind: 'thread', name }];
      },
      UpdateAgent: async (id, change) => {
        name = change.name;
        return { id, kind: 'thread', name };
      },
      CreateAgent: async () => {
        throw new Error('not asked for');
      },
      DeleteAgent: async () => {
        throw new Error('not asked for');
      },
    });
    const { container } = render(App, { props: { chat: new Chat(link()), agents } });
    await waitFor(() => expect(container.querySelector('.profile')?.textContent).toBe('Luna'));
    expect(pressed(container)).toEqual(['Thread:true', 'Agents:false']);
    expect(reads).toBe(0);
    const [, view] = [...container.querySelectorAll('.views button')] as HTMLButtonElement[];
    await fireEvent.click(view);
    await waitFor(() => expect(container.querySelector('.agent-name')?.textContent).toBe('Luna'));
    expect(reads).toBe(1);
    expect(container.querySelector('.transcript')).toBeNull();
    expect((container.querySelector('.compose') as HTMLElement).hidden).toBe(true);
    const edit = [...container.querySelectorAll('.agent-button')].find((button) => button.textContent?.trim() === 'Edit') as HTMLButtonElement;
    await fireEvent.click(edit);
    await fireEvent.input(container.querySelector('.agent-input') as HTMLInputElement, { target: { value: 'Delphi' } });
    await fireEvent.submit(container.querySelector('form') as HTMLFormElement);
    await waitFor(() => expect(container.querySelector('.profile')?.textContent).toBe('Delphi'));
    expect(container.querySelector('.agent-name')?.textContent).toBe('Delphi');
  });

  it('lists the scheduled jobs in a view of their own, each named by the agent it runs as', async () => {
    vi.useFakeTimers();
    let reads = 0;
    const agents = new Agents({
      Agents: async () => [
        { id: 'thread', kind: 'thread', name: 'Luna' },
        { id: 'archivist', kind: 'archivist', name: 'Mnemosyne' },
        { id: 'orchestrator', kind: 'orchestrator', name: 'Orchestrator' },
      ],
      UpdateAgent: async () => {
        throw new Error('not asked for');
      },
      CreateAgent: async () => {
        throw new Error('not asked for');
      },
      DeleteAgent: async () => {
        throw new Error('not asked for');
      },
    });
    const jobs = new Jobs(
      {
        AgentJobs: async () => {
          reads++;
          return [{ id: 'a1', name: 'Archive memories', agent: 'archivist', cron: '0 4 * * *' }];
        },
      },
      1000,
    );
    const sessions = new Sessions({
      Sessions: async () => [{ id: 's1', title: 'Archive memories', running: false, last: new Date().toISOString(), agent: 'archivist' }],
      Session: async () => {
        throw new Error('not asked for');
      },
    });
    const { container } = render(App, { props: { chat: new Chat(link()), sessions, agents, jobs } });
    await vi.advanceTimersByTimeAsync(0);
    expect(pressed(container)).toEqual(['Thread:true', 'Sessions:false', 'Jobs:false', 'Agents:false']);
    const buttons = [...container.querySelectorAll('.views button')] as HTMLButtonElement[];
    // The jobs are a view of their own, not one beneath the agents.
    expect(buttons.map((button) => button.className.includes('sub'))).toEqual([false, false, false, false]);
    expect(reads).toBe(0);
    await fireEvent.click(buttons[2]);
    await vi.advanceTimersByTimeAsync(0);
    expect(container.querySelector('.job-name')?.textContent).toBe('Archive memories');
    expect(container.querySelector('.job-agent-name')?.textContent).toBe('Mnemosyne');
    expect((container.querySelector('.compose') as HTMLElement).hidden).toBe(true);
    await vi.advanceTimersByTimeAsync(2000);
    expect(reads).toBe(3);
    // A session says which agent it runs as.
    await fireEvent.click(buttons[1]);
    await vi.advanceTimersByTimeAsync(0);
    expect(container.querySelector('.row-agent')?.textContent).toBe('Mnemosyne');
    await vi.advanceTimersByTimeAsync(5000);
    expect(reads).toBe(3);
  });

  it('keeps the views in a sidebar that folds to icons, and remembers it', async () => {
    localStorage.removeItem('ouro.sidebar');
    const { container, unmount } = render(App, { props: { chat: new Chat(link()), board: new Board(boardBridge(), 1000) } });
    await waitFor(() => expect(container.querySelector('.transcript')).not.toBeNull());
    const sidebar = container.querySelector('aside.sidebar') as HTMLElement;
    expect(sidebar.querySelector('.views')).not.toBeNull();
    expect(sidebar.querySelector('.brand')?.textContent).toBe('Ouroboros');
    expect(container.querySelector('.top .brand')).toBeNull();
    const frame = container.querySelector('.frame') as HTMLElement;
    const toggle = container.querySelector('.side-toggle') as HTMLButtonElement;
    expect(frame.classList.contains('collapsed')).toBe(false);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(toggle.getAttribute('aria-label')).toBe('Collapse sidebar');
    expect(container.querySelector('.views button')?.getAttribute('title')).toBeNull();
    await fireEvent.click(toggle);
    expect(frame.classList.contains('collapsed')).toBe(true);
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(toggle.getAttribute('aria-label')).toBe('Expand sidebar');
    // Folded, a view still says its name: to the reader and on hover.
    expect(pressed(container)).toEqual(['Thread:true', 'Board:false']);
    expect(container.querySelector('.views button')?.getAttribute('title')).toBe('Thread');
    expect(localStorage.getItem('ouro.sidebar')).toBe('collapsed');
    unmount();
    const again = render(App, { props: { chat: new Chat(link()), board: new Board(boardBridge(), 1000) } });
    expect(again.container.querySelector('.frame')?.classList.contains('collapsed')).toBe(true);
    await fireEvent.click(again.container.querySelector('.side-toggle') as HTMLButtonElement);
    expect(localStorage.getItem('ouro.sidebar')).toBe('open');
    localStorage.removeItem('ouro.sidebar');
  });

  it('keeps the options at the foot of the sidebar, also where there is no other view', async () => {
    const themes = new Themes(null, document.documentElement);
    const { container } = render(App, { props: { chat: new Chat(link()), themes } });
    await waitFor(() => expect(container.querySelector('.transcript')).not.toBeNull());
    expect(pressed(container)).toEqual(['Thread:true', 'Options:false']);
    const foot = container.querySelector('.views-foot button') as HTMLButtonElement;
    await fireEvent.click(foot);
    expect(pressed(container)).toEqual(['Thread:false', 'Options:true']);
    expect(container.querySelector('section.prefs')).not.toBeNull();
    expect((container.querySelector('.compose') as HTMLElement).hidden).toBe(true);

    await fireEvent.click(container.querySelector('button[aria-label="Copy: Nord"]') as HTMLButtonElement);
    expect(document.documentElement.style.getPropertyValue('--bg')).toBe('#2e3440');
    // Leaving the view leaves the theme that was being edited unworn.
    await fireEvent.click(container.querySelector('.views button') as HTMLButtonElement);
    expect(container.querySelector('section.prefs')).toBeNull();
    expect(document.documentElement.style.getPropertyValue('--bg')).toBe('');
    expect(themes.selected).toBe(SYSTEM);
  });

  it('offers no board where the host has none', async () => {
    const { container } = render(App, { props: { chat: new Chat(link()) } });
    await waitFor(() => expect(container.querySelector('.transcript')).not.toBeNull());
    expect(container.querySelector('.views')).toBeNull();
    expect(container.querySelector('.sidebar')).toBeNull();
    expect(container.querySelector('.top .brand')?.textContent).toBe('Ouroboros');
    expect((container.querySelector('.compose') as HTMLElement).hidden).toBe(false);
  });

  it('says that ouro starts anew, keeps the thread readable, and holds the field', async () => {
    const host = link();
    let restart: () => void = () => {};
    host.transport.onRestarting = (handler) => {
      restart = handler;
    };
    const chat = new Chat(host, [60000]);
    const { container } = render(App, { props: { chat } });
    await waitFor(() => expect(container.textContent).toContain('ready when you are'));
    restart();
    await waitFor(() => expect(container.querySelector('.notice')).not.toBeNull());
    expect(container.querySelector('.state')?.textContent).toContain('Restarting');
    // No veil over the conversation: it stays readable while the node starts.
    expect(container.querySelector('.veil')).toBeNull();
    expect(container.textContent).toContain('ready when you are');
    expect(container.querySelector('.notice')?.textContent).toContain('starts anew');
    expect((container.querySelector('textarea') as HTMLTextAreaElement).disabled).toBe(true);
    chat.dispose();
  });

  it('says where it runs when it has no host at all', () => {
    const { container } = render(App, { props: { chat: null } });
    expect(container.textContent).toContain('This page runs only in the window of ouro.');
  });
});

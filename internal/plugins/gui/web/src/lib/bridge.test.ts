import { afterEach, expect, it, vi } from 'vitest';
import { connect } from './bridge';

afterEach(() => vi.unstubAllGlobals());
it('maps numeric History cursors to the native Window binding without converting them', async () => {
  const snapshot = { events: [], before: 12, has_more: true };
  const History = vi.fn(async (_before: number) => snapshot);
  vi.stubGlobal('go', { gui: { Window: { History } } });
  vi.stubGlobal('runtime', { EventsOn: vi.fn() });
  expect(await connect()!.bridge.History(345)).toBe(snapshot);
  expect(History).toHaveBeenCalledExactlyOnceWith(345);
});

it('reads the board through the native Window binding', async () => {
  const board = { lanes: [], pending: false };
  const ticket = { id: 7 };
  const Board = vi.fn(async () => board);
  const Ticket = vi.fn(async (_id: number) => ticket);
  vi.stubGlobal('go', { gui: { Window: { Board, Ticket } } });
  vi.stubGlobal('runtime', { EventsOn: vi.fn() });
  const host = connect()!;
  expect(await host.board.Board()).toBe(board);
  expect(await host.board.Ticket(7)).toBe(ticket);
  expect(Ticket).toHaveBeenCalledExactlyOnceWith(7);
});

it('says that a window from before the board has none', async () => {
  vi.stubGlobal('go', { gui: { Window: {} } });
  vi.stubGlobal('runtime', { EventsOn: vi.fn() });
  const host = connect()!;
  await expect(host.board.Board()).rejects.toThrow('this ouro has no board');
  await expect(host.board.Ticket(1)).rejects.toThrow('this ouro has no board');
});

it('lists the jobs of the agents through the native Window binding, and none from a window before them', async () => {
  const jobs = [{ id: 'board', kind: 'board', agent: 'orchestrator' }];
  const AgentJobs = vi.fn(async () => jobs);
  vi.stubGlobal('go', { gui: { Window: { AgentJobs } } });
  vi.stubGlobal('runtime', { EventsOn: vi.fn() });
  expect(await connect()!.agents.AgentJobs()).toBe(jobs);
  vi.stubGlobal('go', { gui: { Window: {} } });
  await expect(connect()!.agents.AgentJobs()).rejects.toThrow('this ouro lists no jobs');
});

it('is no host outside the window', () => {
  expect(connect()).toBeNull();
});

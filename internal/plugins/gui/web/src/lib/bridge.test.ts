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

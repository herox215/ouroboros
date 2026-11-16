import { describe, expect, it } from 'vitest';
import { Sessions } from './sessions.svelte';
import type { SessionBridge, SessionInfo, SessionTrack } from './wire';

function bridgeOf(list: SessionInfo[], tracks: Record<string, SessionTrack | Error> = {}): SessionBridge {
  return {
    async Sessions() {
      return list;
    },
    async Session(id: string) {
      const track = tracks[id];
      if (!track) throw new Error(`there is no session ${id}`);
      if (track instanceof Error) throw track;
      return track;
    },
  };
}

describe('Sessions', () => {
  it('reads the list, and what one session did when it is opened', async () => {
    const list: SessionInfo[] = [
      { id: 'aaaa', title: 'Dark mode', running: true, last: '2026-11-16T09:00:00Z' },
      { id: 'bbbb', title: 'Export', running: false, last: '2026-11-16T08:00:00Z' },
    ];
    const track: SessionTrack = { ...list[0], events: [{ kind: 'message', text: 'Mach das', at: '2026-11-16T09:00:00Z' }] };
    const sessions = new Sessions(bridgeOf(list, { aaaa: track }));

    await sessions.refresh();
    expect(sessions.list?.length).toBe(2);
    expect(sessions.error).toBe('');
    expect(sessions.opened).toBeNull();

    await sessions.open('aaaa');
    expect(sessions.opened).toBe('aaaa');
    expect(sessions.track?.events.length).toBe(1);
    expect(sessions.trackError).toBe('');

    sessions.close();
    expect(sessions.opened).toBeNull();
    expect(sessions.track).toBeNull();
  });

  it('reports what cannot be read and keeps what was shown', async () => {
    const list: SessionInfo[] = [{ id: 'aaaa', title: 'Dark mode', running: true, last: '2026-11-16T09:00:00Z' }];
    const failing = new Error('the node does not answer');
    const sessions = new Sessions({
      Sessions: async () => {
        throw failing;
      },
      Session: async () => {
        throw failing;
      },
    });
    await sessions.refresh();
    expect(sessions.error).toBe('the node does not answer');
    expect(sessions.list).toBeNull();

    const working = new Sessions(bridgeOf(list, { aaaa: failing }));
    await working.refresh();
    await working.open('aaaa');
    expect(working.trackError).toBe('the node does not answer');
    expect(working.list?.length).toBe(1);
  });

  it('drops the answer for a session that was left meanwhile', async () => {
    let release: (track: SessionTrack) => void = () => {};
    const sessions = new Sessions({
      Sessions: async () => [],
      Session: () =>
        new Promise<SessionTrack>((resolve) => {
          release = resolve;
        }),
    });
    const opened = sessions.open('aaaa');
    sessions.close();
    release({ id: 'aaaa', title: 'Alt', running: false, last: '', events: [] });
    await opened;
    expect(sessions.track).toBeNull();
  });
});

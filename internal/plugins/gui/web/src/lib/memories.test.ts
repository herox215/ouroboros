import { afterEach, describe, expect, it, vi } from 'vitest';
import { filtered, Memories, topicsOf } from './memories.svelte';
import type { Memory, MemoryList } from './wire';

const at = '2026-11-20T08:00:00Z';
const memory = (id: string, topic: string, key: string, content = ''): Memory => ({ id, topic, key, content, updated: at });

const list: MemoryList = {
  memories: [
    memory('1', 'matrix', 'room', '!abc:server'),
    memory('2', 'zebra', 'stripes'),
    memory('3', 'core', 'Sprache', 'Deutsch'),
    memory('4', 'general', 'style', 'short answers'),
    memory('5', 'matrix', 'user', '@lu:server'),
    memory('6', 'Core', 'tone', 'dry'),
    memory('7', 'apple', 'tree'),
  ],
  total: 9,
  hidden: 2,
};

describe('topicsOf', () => {
  it('puts core first, then general, then the rest by how many they hold and by name', () => {
    expect(topicsOf(list)).toEqual([
      { name: 'core', count: 2 },
      { name: 'general', count: 1 },
      { name: 'matrix', count: 2 },
      { name: 'apple', count: 1 },
      { name: 'zebra', count: 1 },
    ]);
  });

  it('knows no topic of an empty list', () => {
    expect(topicsOf({ memories: [] })).toEqual([]);
  });
});

describe('filtered', () => {
  it('keeps every memory without a topic and a query, in the order of the list', () => {
    expect(filtered(list, '', '').map((m) => m.id)).toEqual(['1', '2', '3', '4', '5', '6', '7']);
  });

  it('narrows to a topic whatever its case', () => {
    expect(filtered(list, 'matrix', '').map((m) => m.id)).toEqual(['1', '5']);
    expect(filtered(list, 'CORE', '').map((m) => m.id)).toEqual(['3', '6']);
  });

  it('searches key, content and topic whatever the case, and trims the query', () => {
    expect(filtered(list, '', ' SPRACHE ').map((m) => m.id)).toEqual(['3']);
    expect(filtered(list, '', 'server').map((m) => m.id)).toEqual(['1', '5']);
    expect(filtered(list, '', 'ZEBRA').map((m) => m.id)).toEqual(['2']);
    expect(filtered(list, 'matrix', 'user').map((m) => m.id)).toEqual(['5']);
    expect(filtered(list, 'general', 'server')).toEqual([]);
  });
});

describe('Memories store', () => {
  afterEach(() => vi.useRealTimers());

  it('reads the memories while it is started, and keeps what it read while they cannot be read', async () => {
    vi.useFakeTimers();
    let reads = 0;
    let fail = false;
    const store = new Memories(
      {
        Memories: async () => {
          reads++;
          if (fail) throw new Error('gone');
          return list;
        },
      },
      1000,
    );
    store.start();
    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(reads).toBe(1);
    expect(store.list).toEqual(list);
    await vi.advanceTimersByTimeAsync(2000);
    expect(reads).toBe(3);
    fail = true;
    await vi.advanceTimersByTimeAsync(1000);
    expect(store.error).toBe('gone');
    expect(store.list).toEqual(list);
    store.stop();
    await vi.advanceTimersByTimeAsync(5000);
    expect(reads).toBe(4);
    store.stop();
  });

  it('drops an answer a newer read overtook', async () => {
    const answers: ((memories: MemoryList) => void)[] = [];
    const store = new Memories({ Memories: () => new Promise((resolve) => answers.push(resolve)) });
    const first = store.refresh();
    const second = store.refresh();
    const newer = { memories: [list.memories[0]], total: 1, hidden: 0 };
    answers[1](newer);
    answers[0](list);
    await Promise.all([first, second]);
    expect(store.list).toEqual(newer);
  });
});

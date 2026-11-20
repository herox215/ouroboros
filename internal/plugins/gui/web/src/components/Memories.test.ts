import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render } from '@testing-library/svelte';
import MemoriesView from './Memories.svelte';
import { Memories } from '../lib/memories.svelte';
import { setLocale } from '../lib/i18n';
import type { MemoryList } from '../lib/wire';

const updated = '2026-11-20T08:00:00Z';
const list: MemoryList = {
  memories: [
    { id: 'a', topic: 'matrix', key: 'room', content: '!abc:server', updated },
    { id: 'b', topic: 'core', key: 'Sprache', content: 'Deutsch\nund knapp', updated },
    { id: 'c', topic: 'matrix', key: 'user', content: '@lu:server', updated },
    { id: 'd', topic: 'general', key: 'style', content: 'short answers', updated },
  ],
  total: 4,
  hidden: 0,
};

async function shown(answer: MemoryList = list) {
  const store = new Memories({ Memories: async () => answer });
  await store.refresh();
  return render(MemoriesView, { props: { memories: store } });
}

const keys = (container: Element) => [...container.querySelectorAll('.memory-key')].map((key) => key.textContent);
const chips = (container: Element) =>
  [...container.querySelectorAll('.memory-topic')].map((chip) => `${chip.textContent?.trim()}:${chip.getAttribute('aria-pressed')}`);

describe('Memories view', () => {
  beforeEach(() => setLocale('en'));

  it('lists every memory flat, with its topic, its text as it was written and when it changed', async () => {
    const { container } = await shown();
    expect(keys(container)).toEqual(['room', 'Sprache', 'user', 'style']);
    const memories = [...container.querySelectorAll('.memory')] as HTMLElement[];
    expect(memories.map((m) => m.querySelector('.memory-tag')?.textContent)).toEqual(['matrix', 'core', 'matrix', 'general']);
    expect(memories[1].querySelector('.memory-tag')?.classList.contains('core')).toBe(true);
    expect(memories[1].querySelector('.memory-content')?.textContent).toBe('Deutsch\nund knapp');
    expect(memories[1].querySelector('.memory-meta')?.textContent).toContain('changed');
    expect(container.querySelector('.memory-foot')).toBeNull();
    // Nothing here changes a memory.
    expect(container.querySelector('.memory button, .memory input, .memory textarea')).toBeNull();
  });

  it('counts the memories per topic, core first, and narrows to the topic a chip picks', async () => {
    const { container } = await shown();
    expect(chips(container)).toEqual(['All (4):true', 'core (1):false', 'general (1):false', 'matrix (2):false']);
    const chip = [...container.querySelectorAll('.memory-topic')].find((c) => c.textContent?.includes('matrix')) as HTMLElement;
    await fireEvent.click(chip);
    expect(keys(container)).toEqual(['room', 'user']);
    expect(chips(container)).toEqual(['All (4):false', 'core (1):false', 'general (1):false', 'matrix (2):true']);
    // The topic is said by the chip, not by every memory.
    expect(container.querySelector('.memory-tag')).toBeNull();
    await fireEvent.click(container.querySelector('.memory-topic') as HTMLElement);
    expect(keys(container)).toHaveLength(4);
  });

  it('searches key, content and topic, and says when nothing matches', async () => {
    const { container } = await shown();
    const search = container.querySelector('.memory-search') as HTMLInputElement;
    expect(search.placeholder).toBe('Search …');
    await fireEvent.input(search, { target: { value: 'SERVER' } });
    expect(keys(container)).toEqual(['room', 'user']);
    await fireEvent.input(search, { target: { value: 'nothing like this' } });
    expect(keys(container)).toEqual([]);
    expect(container.querySelector('.memory-nomatch')?.textContent).toBe('No memory matches.');
  });

  it('says what is left out: the expired, and what lies beyond the latest', async () => {
    const { container } = await shown({ ...list, total: 9, hidden: 2 });
    expect(container.querySelector('.memory-foot')?.textContent?.replace(/\s+/g, ' ').trim()).toBe('The latest 4 of 7 2 expired hidden');
    const { container: expired } = await shown({ ...list, total: 5, hidden: 1 });
    expect(expired.querySelector('.memory-foot')?.textContent?.trim()).toBe('1 expired hidden');
  });

  it('speaks German', async () => {
    setLocale('de');
    const { container } = await shown({ ...list, total: 5, hidden: 1 });
    expect(container.querySelector('.memory-topic')?.textContent?.trim()).toBe('Alle (4)');
    expect(container.querySelector('.memory-foot')?.textContent?.trim()).toBe('1 abgelaufene ausgeblendet');
  });

  it('says that there are no memories', async () => {
    const { container } = await shown({ memories: [], total: 0, hidden: 0 });
    expect(container.querySelector('.empty-title')?.textContent).toBe('No memories yet');
    expect(container.querySelector('.memory-search')).toBeNull();
  });

  it('waits for the first answer, and says why the memories cannot be read', async () => {
    const store = new Memories({ Memories: () => Promise.reject(new Error('no store')) });
    const { container } = render(MemoriesView, { props: { memories: store } });
    expect(container.querySelector('[role="status"]')?.textContent).toBe('Reading the memories …');
    await store.refresh();
    await Promise.resolve();
    expect(container.querySelector('[role="alert"]')?.textContent).toBe('The memories cannot be read: no store');
  });
});

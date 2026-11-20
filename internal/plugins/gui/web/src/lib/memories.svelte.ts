import type { Memory, MemoryBridge, MemoryList } from './wire';

/** How often the window reads the memories again while it shows them. */
const EVERY = 10_000;

function reason(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

/** A topic of the memories, with how many of them the list holds. */
export interface TopicCount {
  name: string;
  count: number;
}

/** The topics of the memories with their counts: core first, as it goes into
 * every prompt, then general, then the others by how many they hold, the
 * name deciding between equals. Topics are the same whatever their case; the
 * first spelling read is the one shown. */
export function topicsOf(list: Pick<MemoryList, 'memories'>): TopicCount[] {
  const counted = new Map<string, TopicCount>();
  for (const memory of list.memories) {
    const key = memory.topic.toLowerCase();
    const topic = counted.get(key);
    if (topic) topic.count++;
    else counted.set(key, { name: memory.topic, count: 1 });
  }
  const rank = (topic: TopicCount) => {
    const key = topic.name.toLowerCase();
    return key === 'core' ? 0 : key === 'general' ? 1 : 2;
  };
  return [...counted.values()].sort(
    (a, b) => rank(a) - rank(b) || b.count - a.count || a.name.localeCompare(b.name),
  );
}

/** The memories of topic ('' for every topic) whose topic, key or content hold
 * query, whatever the case. The order of the list stays. */
export function filtered(list: Pick<MemoryList, 'memories'>, topic: string, query: string): Memory[] {
  const wanted = topic.toLowerCase();
  const needle = query.trim().toLowerCase();
  return list.memories.filter((memory) => {
    if (wanted && memory.topic.toLowerCase() !== wanted) return false;
    if (!needle) return true;
    return [memory.key, memory.content, memory.topic].some((text) => text.toLowerCase().includes(needle));
  });
}

/**
 * The memories of the agent, as the window shows them: what it kept of its
 * owner, by topic, the latest changed first. The thread keeps them while it
 * works, so the store asks again now and then while they are shown. Nothing
 * of them is changed from here.
 */
export class Memories {
  readonly #bridge: MemoryBridge;
  readonly #every: number;
  #timer: ReturnType<typeof setInterval> | null = null;
  /** Counts the reads: an answer that a newer read overtook is dropped. */
  #asked = 0;

  /** The memories as they were read last, null until the first answer. */
  list = $state<MemoryList | null>(null);
  /** Why the memories could not be read, '' when they could. */
  error = $state('');

  constructor(bridge: MemoryBridge, every = EVERY) {
    this.#bridge = bridge;
    this.#every = every;
  }

  /** Reads the memories now, and again now and then until stop. */
  start(): void {
    if (this.#timer) return;
    void this.refresh();
    this.#timer = setInterval(() => void this.refresh(), this.#every);
  }

  /** Ends the reading: the memories stay as they were read last. */
  stop(): void {
    if (this.#timer) clearInterval(this.#timer);
    this.#timer = null;
  }

  /** Reads the memories anew. What was shown stays while they cannot be read. */
  async refresh(): Promise<void> {
    const asked = ++this.#asked;
    try {
      const list = await this.#bridge.Memories();
      if (asked !== this.#asked) return;
      this.list = list;
      this.error = '';
    } catch (error) {
      if (asked !== this.#asked) return;
      this.error = reason(error);
    }
  }
}

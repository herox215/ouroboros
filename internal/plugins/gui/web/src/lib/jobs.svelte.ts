import type { AgentBridge, AgentJob } from './wire';

/** How often the window reads the jobs again while it shows them. */
const EVERY = 10_000;

function reason(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

/**
 * The scheduled jobs of the profile, as the window shows them: what is still
 * to run, each as its agent. The node runs them and the thread plans them, so
 * the store asks again now and then while they are shown.
 */
export class Jobs {
  readonly #bridge: Pick<AgentBridge, 'AgentJobs'>;
  readonly #every: number;
  #timer: ReturnType<typeof setInterval> | null = null;
  /** Counts the reads: an answer that a newer read overtook is dropped. */
  #asked = 0;

  /** The jobs as they were read last, null until the first answer. */
  list = $state<AgentJob[] | null>(null);
  /** Why the jobs could not be read, '' when they could. */
  error = $state('');

  constructor(bridge: Pick<AgentBridge, 'AgentJobs'>, every = EVERY) {
    this.#bridge = bridge;
    this.#every = every;
  }

  /** Reads the jobs now, and again now and then until stop. */
  start(): void {
    if (this.#timer) return;
    void this.refresh();
    this.#timer = setInterval(() => void this.refresh(), this.#every);
  }

  /** Ends the reading: the jobs stay as they were read last. */
  stop(): void {
    if (this.#timer) clearInterval(this.#timer);
    this.#timer = null;
  }

  /** Reads the jobs anew. What was shown stays while they cannot be read. */
  async refresh(): Promise<void> {
    const asked = ++this.#asked;
    try {
      const list = await this.#bridge.AgentJobs();
      if (asked !== this.#asked) return;
      this.list = list;
      this.error = '';
    } catch (error) {
      if (asked !== this.#asked) return;
      this.error = reason(error);
    }
  }
}

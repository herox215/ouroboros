import type { SessionBridge, SessionInfo, SessionTrack } from './wire';

/** How often the window reads the sessions again while it shows them. */
const EVERY = 4000;

function reason(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

/**
 * The sessions of the profile, as the window shows them: the list, and the one
 * session the owner opened with what it told lately. The window only reads
 * them: they run in the node, so the store asks again every few seconds while
 * the view is shown.
 */
export class Sessions {
  readonly #bridge: SessionBridge;
  readonly #every: number;
  #timer: ReturnType<typeof setInterval> | null = null;
  /** Counts the sessions asked for: an answer for one that was left is dropped. */
  #asked = 0;

  /** The sessions as they were read last, null until the first answer. */
  list = $state<SessionInfo[] | null>(null);
  /** Why the sessions could not be read, '' when they could. */
  error = $state('');
  /** The session the owner opened, and all of it once it came. */
  opened = $state<string | null>(null);
  track = $state<SessionTrack | null>(null);
  trackError = $state('');
  /** When the list was read last, for what says how long ago something was. */
  now = $state(Date.now());

  constructor(bridge: SessionBridge, every = EVERY) {
    this.#bridge = bridge;
    this.#every = every;
  }

  /** Reads the sessions now, and again every few seconds until stop. */
  start(): void {
    if (this.#timer) return;
    void this.refresh();
    this.#timer = setInterval(() => void this.refresh(), this.#every);
  }

  /** Ends the reading: the sessions stay as they were read last. */
  stop(): void {
    if (this.#timer) clearInterval(this.#timer);
    this.#timer = null;
  }

  /** Reads the list, and the session that is open, anew. What was shown stays
   * while the sessions cannot be read. */
  async refresh(): Promise<void> {
    try {
      this.list = await this.#bridge.Sessions();
      this.error = '';
    } catch (error) {
      this.error = reason(error);
    }
    this.now = Date.now();
    if (this.opened !== null) await this.#load(this.opened);
  }

  /** Opens the session id: the detail says which, and what it told follows. */
  async open(id: string): Promise<void> {
    if (this.opened !== id) {
      this.track = null;
      this.trackError = '';
    }
    this.opened = id;
    await this.#load(id);
  }

  /** Closes the session that is open. */
  close(): void {
    this.#asked++;
    this.opened = null;
    this.track = null;
    this.trackError = '';
  }

  async #load(id: string): Promise<void> {
    const asked = ++this.#asked;
    try {
      const track = await this.#bridge.Session(id);
      if (asked !== this.#asked) return;
      this.track = track;
      this.trackError = '';
    } catch (error) {
      if (asked !== this.#asked) return;
      this.trackError = reason(error);
    }
  }
}

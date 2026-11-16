import type { BoardBridge, BoardView, Ticket } from './wire';

/** How often the window reads the board again while it shows it. */
const EVERY = 4000;

function reason(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

/**
 * The board, as the window shows it: its lanes, and the ticket the owner
 * opened. The window only reads it: the node keeps it, and what changes it is
 * the thread, the orchestrator and the sessions that work on tickets. So the
 * store asks again every few seconds while the board is shown.
 */
export class Board {
  readonly #bridge: BoardBridge;
  readonly #every: number;
  #timer: ReturnType<typeof setInterval> | null = null;
  /** Counts the tickets asked for: an answer for one that was left is dropped. */
  #asked = 0;

  /** The board as it was read last, null until the first answer. */
  view = $state<BoardView | null>(null);
  /** Why the board could not be read, '' when it could. */
  error = $state('');
  /** The ticket the owner opened: its number, and all of it once it came. */
  opened = $state<number | null>(null);
  ticket = $state<Ticket | null>(null);
  ticketError = $state('');
  /** When the board was read last, for what says how long ago something was. */
  now = $state(Date.now());

  constructor(bridge: BoardBridge, every = EVERY) {
    this.#bridge = bridge;
    this.#every = every;
  }

  /** Reads the board now, and again every few seconds until stop. */
  start(): void {
    if (this.#timer) return;
    void this.refresh();
    this.#timer = setInterval(() => void this.refresh(), this.#every);
  }

  /** Ends the reading: the board stays as it was read last. */
  stop(): void {
    if (this.#timer) clearInterval(this.#timer);
    this.#timer = null;
  }

  /** Reads the board, and the ticket that is open, anew. What was shown
   * stays while the board cannot be read. */
  async refresh(): Promise<void> {
    try {
      this.view = await this.#bridge.Board();
      this.error = '';
    } catch (error) {
      this.error = reason(error);
    }
    this.now = Date.now();
    if (this.opened !== null) await this.#load(this.opened);
  }

  /** Opens the ticket id: the lane shows which, and all of it follows. */
  async open(id: number): Promise<void> {
    if (this.opened !== id) {
      this.ticket = null;
      this.ticketError = '';
    }
    this.opened = id;
    await this.#load(id);
  }

  /** Closes the ticket that is open. */
  close(): void {
    this.#asked++;
    this.opened = null;
    this.ticket = null;
    this.ticketError = '';
  }

  async #load(id: number): Promise<void> {
    const asked = ++this.#asked;
    try {
      const ticket = await this.#bridge.Ticket(id);
      if (asked !== this.#asked) return;
      this.ticket = ticket;
      this.ticketError = '';
    } catch (error) {
      if (asked !== this.#asked) return;
      this.ticketError = reason(error);
    }
  }
}

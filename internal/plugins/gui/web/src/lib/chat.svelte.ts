import type { Link, State, WireEvent } from './wire';

/** What a line of the transcript is. */
export type Role = 'owner' | 'agent' | 'tool' | 'error' | 'note' | 'question';

/** One line of the transcript. */
export interface Entry {
  id: number;
  role: Role;
  text: string;
  at?: string;
  /** The answer is still growing: its pieces arrived one by one. */
  streaming?: boolean;
  tool?: string;
  callId?: string;
  done?: boolean;
  failed?: boolean;
  /** A question the thread asks: the answers to choose from. */
  options?: string[];
  recommended?: string;
  /** A question that waits for the owner, or what they answered. */
  open?: boolean;
  answered?: string;
}

/** Whether the window can reach the node, and whether it is starting anew. */
export type Status = 'loading' | 'ready' | 'offline' | 'restarting';

const RECONNECT = [300, 600, 1200, 2400, 4800, 9600];

const wait = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

function reason(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

/**
 * The thread, as the window shows it: the events the Go side passes on, folded
 * into the lines of a chat, and what the owner does with it.
 *
 * The store owns no transport of its own: it is handed the link to the native
 * window, so that a test drives it with a scripted one.
 */
export class Chat {
  readonly #link: Link;
  readonly #delays: number[];
  #next = 1;
  #retrying = false;
  #disposed = false;
  #ends = 0;

  state = $state<State | null>(null);
  entries = $state<Entry[]>([]);
  queued = $state<string[]>([]);
  status = $state<Status>('loading');
  busy = $state(false);
  /** The thread itself answers, not only the sessions it handed work to. */
  own = $state(false);
  title = $state('');
  error = $state('');
  sending = $state(false);

  constructor(link: Link, delays: number[] = RECONNECT) {
    this.#link = link;
    this.#delays = delays;
  }

  /** Opens the thread and listens to what it tells from now on. */
  async start(): Promise<void> {
    const { transport } = this.#link;
    transport.onEvents((events) => this.receive(events));
    transport.onEnded(() => this.ended());
    transport.onRestarting(() => {
      this.status = 'restarting';
      this.busy = false;
    });
    try {
      this.state = await this.#link.bridge.InitialState();
    } catch (error) {
      this.fail(error);
      return;
    }
    await this.open();
  }

  /** Opens the thread again: on start, and once the stream ended and the node
   * came back. What is shown is replaced by what the thread tells anew. */
  async open(): Promise<void> {
    const ends = this.#ends;
    try {
      const snapshot = await this.#link.bridge.Open();
      this.replace(snapshot.events);
      // The stream may have ended while the snapshot was on its way, before
      // the page could show it as ready: that scroll would never move again.
      if (this.#ends !== ends) {
        this.ended();
        return;
      }
      this.status = 'ready';
      this.error = '';
    } catch (error) {
      this.fail(error);
    }
  }

  /** Sends text to the thread: a message, or the answer to its question, or,
   * while an answer is on its way, what it is to take next. */
  async send(text: string): Promise<boolean> {
    const body = text.trim();
    if (!body || this.sending) return false;
    this.sending = true;
    this.error = '';
    try {
      await this.#link.bridge.Send(body);
      return true;
    } catch (error) {
      this.error = reason(error);
      return false;
    } finally {
      this.sending = false;
    }
  }

  /** Stops the answer on its way, as the terminal UI does. */
  async stop(): Promise<void> {
    this.error = '';
    try {
      await this.#link.bridge.Stop();
    } catch (error) {
      this.error = reason(error);
    }
  }

  /** Lets go of the page: no reconnect runs after it. */
  dispose(): void {
    this.#disposed = true;
  }

  private receive(events: WireEvent[]): void {
    for (const event of events) this.apply(event);
  }

  private replace(events: WireEvent[]): void {
    this.entries = [];
    this.queued = [];
    this.busy = false;
    this.own = false;
    this.title = '';
    for (const event of events) this.apply(event);
  }

  private fail(error: unknown): void {
    this.status = 'offline';
    this.error = reason(error);
  }

  private ended(): void {
    this.#ends++;
    if (this.status === 'restarting') return;
    this.status = 'offline';
    this.busy = false;
    void this.reconnect();
  }

  private async reconnect(): Promise<void> {
    if (this.#retrying) return;
    this.#retrying = true;
    try {
      for (const delay of this.#delays) {
        await wait(delay);
        if (this.#disposed || this.status === 'restarting') return;
        try {
          const snapshot = await this.#link.bridge.Open();
          this.replace(snapshot.events);
          this.status = 'ready';
          this.error = '';
          return;
        } catch {
          // The node comes back after a rebuild; the next wait tries again.
        }
      }
    } finally {
      this.#retrying = false;
    }
  }

  private push(entry: Omit<Entry, 'id'>): Entry {
    const line: Entry = { ...entry, id: this.#next++ };
    this.entries.push(line);
    return line;
  }

  private last(): Entry | undefined {
    return this.entries[this.entries.length - 1];
  }

  /** Folds one event into the transcript, as the terminal UI folds it into
   * its conversation. Kinds the window has no place for are left out. */
  private apply(event: WireEvent): void {
    const text = event.text ?? '';
    switch (event.kind) {
      case 'message':
        this.push({ role: 'owner', text, at: event.at });
        this.queued = this.queued.slice(1);
        this.busy = true;
        break;
      case 'queued':
        this.queued.push(text);
        break;
      case 'unqueued':
        this.queued = this.queued.slice(0, -1);
        break;
      case 'title':
        this.title = text;
        break;
      case 'say': {
        const last = this.last();
        if (last?.streaming) last.streaming = false;
        this.push({ role: 'agent', text, at: event.at });
        break;
      }
      case 'answering': {
        const last = this.last();
        if (last?.role === 'agent' && last.streaming) last.text += text;
        else this.push({ role: 'agent', text, streaming: true, at: event.at });
        this.busy = true;
        break;
      }
      case 'answer': {
        const last = this.last();
        if (last?.role === 'agent' && last.streaming) {
          last.text = text;
          last.streaming = false;
        } else {
          this.push({ role: 'agent', text, at: event.at });
        }
        this.busy = false;
        this.own = false;
        break;
      }
      case 'failed':
        this.push({ role: 'error', text, at: event.at });
        this.busy = false;
        this.own = false;
        break;
      case 'canceled':
        this.push({ role: 'note', text: text || 'stopped', at: event.at });
        this.busy = false;
        this.own = false;
        break;
      case 'step': {
        if (event.done && event.call_id) {
          const open = this.entries.findLast((entry) => entry.role === 'tool' && entry.callId === event.call_id);
          if (open) {
            open.text = text || open.text;
            open.done = true;
            open.failed = !!event.failed;
            break;
          }
        }
        this.push({ role: 'tool', text: text || event.tool || '', tool: event.tool, callId: event.call_id, done: !!event.done, failed: !!event.failed, at: event.at });
        break;
      }
      case 'asked': {
        if (event.done) {
          const open = this.entries.findLast((entry) => entry.role === 'question' && entry.open);
          if (open) {
            open.open = false;
            open.answered = text;
          }
          break;
        }
        this.push({
          role: 'question',
          text,
          options: event.options ?? [],
          recommended: event.recommended,
          open: true,
          at: event.at,
        });
        break;
      }
      case 'working':
        this.busy = (event.n ?? 0) > 0;
        this.own = this.busy && !!event.own;
        break;
      default:
        break;
    }
  }
}

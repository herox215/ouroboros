// What the window receives from the Go side, and what it sends back. The
// shapes mirror internal/plugins/gui/window.go; the kinds are those of
// plugin.SessionEvent, which the window passes on as the terminal UI sees them.

export type Kind =
  | 'message'
  | 'queued'
  | 'unqueued'
  | 'say'
  | 'answering'
  | 'step'
  | 'asked'
  | 'answer'
  | 'failed'
  | 'canceled'
  | 'title'
  | 'working';

/** One thing the thread told, as the window shows it. */
export interface WireEvent {
  kind: Kind;
  text?: string;
  tool?: string;
  call_id?: string;
  done?: boolean;
  failed?: boolean;
  at?: string;
  n?: number;
  own?: boolean;
  options?: string[];
  recommended?: string;
}

/** The profile the window belongs to, and the conversation it shows. */
export interface State {
  profile: string;
  thread: string;
}

/** What the thread told so far, when the window opens it. */
export interface Snapshot {
  events: WireEvent[];
  before: number;
  has_more: boolean;
}

/** The bound methods of the Go window, window.go.gui.Window. */
export interface Bridge {
  InitialState(): Promise<State>;
  Open(): Promise<Snapshot>;
  History(before: number): Promise<Snapshot>;
  Send(text: string): Promise<void>;
  Stop(): Promise<void>;
}

/** What the window is told while it watches: what the thread tells, and that
 * the stream ended or the process starts anew. */
export interface Transport {
  onEvents(handler: (events: WireEvent[]) => void): void;
  onEnded(handler: () => void): void;
  onRestarting(handler: () => void): void;
}

export interface Link {
  bridge: Bridge;
  transport: Transport;
}

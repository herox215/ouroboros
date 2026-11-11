import type { Bridge, Link, Snapshot, State, Transport, WireEvent } from './wire';

/** The part of the Wails runtime the window uses. */
interface Runtime {
  EventsOn(name: string, handler: (...data: unknown[]) => void): () => void;
}

/** The bindings Wails generates from the exported methods of Window. */
interface WindowBindings {
  InitialState(): Promise<State>;
  Open(): Promise<Snapshot>;
  History(before: number): Promise<Snapshot>;
  Send(text: string): Promise<void>;
  Stop(): Promise<void>;
}

interface Host {
  go?: { gui?: { Window?: WindowBindings } };
  runtime?: Runtime;
}

/** The window Wails hosts. */
function wailsLink(): Link | null {
  const host = globalThis as unknown as Host;
  const bindings = host.go?.gui?.Window;
  const runtime = host.runtime;
  if (!bindings || !runtime) return null;
  const bridge: Bridge = {
    InitialState: () => bindings.InitialState(),
    Open: () => bindings.Open(),
    History: (before) => bindings.History(before),
    Send: (text) => bindings.Send(text),
    Stop: () => bindings.Stop(),
  };
  const transport: Transport = {
    onEvents(handler) {
      runtime.EventsOn('ouro:events', (batch) => {
        if (Array.isArray(batch)) handler(batch as WireEvent[]);
      });
    },
    onEnded(handler) {
      runtime.EventsOn('ouro:ended', () => handler());
    },
    onRestarting(handler) {
      runtime.EventsOn('ouro:restarting', () => handler());
    },
  };
  return { bridge, transport };
}

/** The link the page talks through: the native window, or none when the page
 * is looked at outside of it (a scratch page mounts the scripted thread). */
export function connect(): Link | null {
  return wailsLink();
}

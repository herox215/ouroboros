import type {
  AccessModels,
  AgentBridge,
  AgentChange,
  AgentInfo,
  AgentJob,
  BoardBridge,
  BoardView,
  Bridge,
  Host,
  SessionBridge,
  SessionInfo,
  SessionTrack,
  Snapshot,
  State,
  Ticket,
  Transport,
  WireEvent,
} from './wire';

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
  /** The board: a host from before it had one has neither. */
  Board?(): Promise<BoardView>;
  Ticket?(id: number): Promise<Ticket>;
  /** The sessions: a host from before it had them has neither. */
  Sessions?(): Promise<SessionInfo[]>;
  Session?(id: string): Promise<SessionTrack>;
  /** The agents: a host from before it had them has neither. */
  Agents?(): Promise<AgentInfo[]>;
  UpdateAgent?(id: string, change: AgentChange): Promise<AgentInfo>;
  CreateAgent?(change: AgentChange): Promise<AgentInfo>;
  DeleteAgent?(id: string): Promise<void>;
  AgentModels?(): Promise<AccessModels[]>;
  /** The scheduled jobs: a host from before them has none. */
  AgentJobs?(): Promise<AgentJob[]>;
  /** The build of the page, see window.go: a host from before it named one
   * has none. */
  Assets?(): string;
}

/** What a host that has no board answers when it is asked for one. */
const noBoard = () => Promise.reject(new Error('this ouro has no board'));

/** What a host that has no sessions answers when it is asked for one. */
const noSessions = () => Promise.reject(new Error('this ouro has no sessions'));

/** What a host that has no agents answers when it is asked for one. */
const noAgents = () => Promise.reject(new Error('this ouro lists no agents'));

/** What a host that lists no jobs of its agents answers when it is asked. */
const noJobs = () => Promise.reject(new Error('this ouro lists no jobs'));

interface Globals {
  go?: { gui?: { Window?: WindowBindings } };
  runtime?: Runtime;
}

/** The window Wails hosts. */
function wailsHost(): Host | null {
  const host = globalThis as unknown as Globals;
  const bindings = host.go?.gui?.Window;
  const runtime = host.runtime;
  if (!bindings || !runtime) return null;
  const bridge: Bridge = {
    InitialState: () => bindings.InitialState(),
    Open: () => bindings.Open(),
    History: (before) => bindings.History(before),
    Send: (text) => bindings.Send(text),
    Stop: () => bindings.Stop(),
    Assets: () => Promise.resolve(bindings.Assets?.() ?? ''),
  };
  const board: BoardBridge = {
    Board: () => bindings.Board?.() ?? noBoard(),
    Ticket: (id) => bindings.Ticket?.(id) ?? noBoard(),
  };
  const sessions: SessionBridge = {
    Sessions: () => bindings.Sessions?.() ?? noSessions(),
    Session: (id) => bindings.Session?.(id) ?? noSessions(),
  };
  const agents: AgentBridge = {
    Agents: () => bindings.Agents?.() ?? noAgents(),
    UpdateAgent: (id, change) => bindings.UpdateAgent?.(id, change) ?? noAgents(),
    CreateAgent: (change) => bindings.CreateAgent?.(change) ?? noAgents(),
    DeleteAgent: (id) => bindings.DeleteAgent?.(id) ?? noAgents(),
    AgentModels: () => bindings.AgentModels?.() ?? noAgents(),
    AgentJobs: () => bindings.AgentJobs?.() ?? noJobs(),
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
  return { bridge, transport, board, sessions, agents };
}

/** What the page talks through: the native window, or none when the page is
 * looked at outside of it (a scratch page mounts the scripted thread). */
export function connect(): Host | null {
  return wailsHost();
}

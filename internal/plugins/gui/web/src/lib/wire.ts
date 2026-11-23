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
  | 'picture'
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

/** The columns of the board, in the order its lanes come. */
export type Column = 'new' | 'ready' | 'progress' | 'blocked' | 'done';

/** A ticket as its lane shows it, see Card in board.go. */
export interface Card {
  id: number;
  title: string;
  column: Column;
  /** Why it is blocked, its start, and the column it came from. */
  reason?: string;
  from?: string;
  /** The session that works on it now: its role, its ID and since when,
   * and the agent it runs as, with what that was called when it began. */
  role?: string;
  session?: string;
  since?: string;
  agent?: string;
  agent_name?: string;
  /** The thread was told of the column it is in. */
  told?: boolean;
  changed: string;
  notes: number;
}

export interface Lane {
  column: Column;
  cards: Card[];
}

/** When the orchestrator looked at the board, and what it said it did. */
export interface Look {
  at: string;
  session: string;
  result?: string;
}

/** The board of the profile, as the window shows it. */
export interface BoardView {
  lanes: Lane[];
  looked?: Look;
  /** The board changed since the orchestrator looked. */
  pending: boolean;
}

/** A session that works on a ticket, or did: without ended, it still does. */
export interface TicketRun {
  session: string;
  role: string;
  /** The agent it runs as, with what that was called when it began: none for
   * a session from before the board knew it. */
  agent?: string;
  agent_name?: string;
  began: string;
  ended?: string;
  outcome?: string;
}

/** Something said at a ticket, or something that happened to it. */
export interface TicketNote {
  at: string;
  by: string;
  text: string;
}

/** A ticket in full, see Ticket in internal/board. */
export interface Ticket {
  id: number;
  title: string;
  text: string;
  directory?: string;
  column: Column;
  reason?: string;
  from?: string;
  result?: string;
  told?: boolean;
  created: string;
  changed: string;
  notes?: TicketNote[];
  runs?: TicketRun[];
}

/** The bound methods of the Go window, window.go.gui.Window. */
export interface Bridge {
  InitialState(): Promise<State>;
  Open(): Promise<Snapshot>;
  History(before: number): Promise<Snapshot>;
  Send(text: string): Promise<void>;
  Stop(): Promise<void>;
  /** The build of the page the node serves: a host from before it named one
   * has none, and then the page compares nothing. */
  Assets?(): Promise<string>;
}

/** The bound methods of the Go window that show the board, see board.go. */
export interface BoardBridge {
  Board(): Promise<BoardView>;
  Ticket(id: number): Promise<Ticket>;
}

/** A command that runs in the background for a session, see Job in sessions.go. */
export interface SessionJob {
  n: number;
  command: string;
  since: string;
}

/** A session as the window lists it, see Session in sessions.go. */
export interface SessionInfo {
  id: string;
  title: string;
  /** An answer is on its way in it. */
  running: boolean;
  last: string;
  /** The ticket of the board it works on, and its role in it. */
  ticket?: number;
  work?: string;
  role?: string;
  since?: string;
  jobs?: SessionJob[];
  /** The agent of the profile it runs as, see AgentInfo: a host from before
   * agents names none. */
  agent?: string;
}

/** One session in full: its line of the list and what it told lately. */
export interface SessionTrack extends SessionInfo {
  events: WireEvent[];
}

/** The bound methods of the Go window that show the sessions, see sessions.go. */
export interface SessionBridge {
  Sessions(): Promise<SessionInfo[]>;
  Session(id: string): Promise<SessionTrack>;
}

/** An agent of the profile, see Agent in agents.go. The agent of the thread is
 * called as the profile is. */
export interface AgentInfo {
  id: string;
  /** What the agent is: 'thread' for the agent of the thread, 'archivist' and
   * 'orchestrator' for the other built-in ones, 'custom' for one the owner
   * made. */
  kind: string;
  name: string;
  /** What the agent is for and who it is, as the owner wrote them: none for
   * nothing. */
  description?: string;
  soul?: string;
  /** The model of its own the agent answers with, through that access of the
   * node: none for the model of the settings, which the agent of the thread
   * always answers with. */
  access?: string;
  model?: string;
  /** The effort of its own the agent answers with: none for the effort of the
   * settings, which the agent of the thread always answers with. */
  effort?: string;
  /** The agent is kept for the work of the system: the archivist and the
   * orchestrator. What it is for is fixed, and no agent hands it a task. */
  reserved?: boolean;
}

/** What the owner says of an agent, see AgentChange in agents.go. */
export interface AgentChange {
  name: string;
  description: string;
  soul: string;
  /** Both '' for the model of the settings. */
  access: string;
  model: string;
  /** '' for the effort of the settings; the agent of the thread takes none. */
  effort: string;
}

/** The models of an access of the node an agent may answer with, see
 * AccessModels in agents.go: error says why it did not list them now. */
export interface AccessModels {
  access: string;
  label: string;
  provider: string;
  models: string[];
  error?: string;
}

/** A scheduled job, see AgentJob in jobs.go: what is planned, not what came
 * of it. */
export interface AgentJob {
  id: string;
  name: string;
  /** The ID of the agent it runs as. */
  agent: string;
  text?: string;
  cron?: string;
  at?: string;
  zone?: string;
  next?: string;
  paused?: boolean;
}

/** The bound methods of the Go window that show and change the agents and
 * list the scheduled jobs, see agents.go and jobs.go. */
export interface AgentBridge {
  Agents(): Promise<AgentInfo[]>;
  UpdateAgent(id: string, change: AgentChange): Promise<AgentInfo>;
  CreateAgent(change: AgentChange): Promise<AgentInfo>;
  DeleteAgent(id: string): Promise<void>;
  AgentModels(): Promise<AccessModels[]>;
  AgentJobs(): Promise<AgentJob[]>;
}

/** A memory the agent of the owner collected, see Memory in memories.go. */
export interface Memory {
  id: string;
  topic: string;
  key: string;
  content: string;
  /** When it was changed last, in RFC 3339. */
  updated: string;
  /** When a memory of a state ends; none for never. */
  expires?: string;
}

/** The memories that have not expired, the latest updated first, and the
 * counts beyond them, see MemoryList in memories.go. */
export interface MemoryList {
  memories: Memory[];
  /** How many memories there are, those expired included. */
  total: number;
  /** How many expired memories are left out. */
  hidden: number;
}

/** The bound method of the Go window that lists the memories, see
 * memories.go. */
export interface MemoryBridge {
  Memories(): Promise<MemoryList>;
}

/** A provider of this ouro to sign in with, see ProviderInfo in providers.go. */
export interface ProviderInfo {
  name: string;
  title: string;
}

/** An access of the node, see AccessInfo in providers.go. What it signs in
 * with is never part of it. */
export interface AccessInfo {
  id: string;
  provider: string;
  /** What the owner calls it, none for nothing; label is what it is shown as,
   * about what tells it apart, like the account. */
  name?: string;
  label: string;
  about?: string;
  /** Its provider is not part of this ouro: its plugin is turned off. */
  missing?: boolean;
  /** The scopes that answer through it, see Scope, and 'fallback'. */
  uses?: string[];
}

/** The providers and the accesses of the node, see ProviderList. */
export interface ProviderList {
  providers: ProviderInfo[];
  accesses: AccessInfo[];
}

/** The scopes of the node: the owner, which the others follow until they have
 * a model of their own, the thread, the sessions and peers. */
export type Scope = 'owner' | 'thread' | 'sessions' | 'peers';

/** What one scope answers with, see ScopeLLM: own says that the owner chose it
 * for the scope; else it is what the owner is answered with. */
export interface ScopeLLM {
  scope: Scope;
  access: string;
  model: string;
  own?: boolean;
}

/** What the scopes of the node answer with, see LLMList in providers.go. */
export interface LLMList {
  scopes: ScopeLLM[];
  fallbacks: { access: string; model: string }[];
  /** How much the model thinks before it answers the owner: '' for what the
   * model does by itself. */
  effort: string;
  efforts: string[];
  /** models.dev lists the model, and efforts are those it takes. */
  efforts_known?: boolean;
}

/** What a provider asks while it signs in, see LoginQuestion. A secret it has
 * is never sent: known says that an empty answer keeps it. */
export interface LoginQuestion {
  title: string;
  text?: string[];
  default?: string;
  hint?: string;
  secret?: boolean;
  known?: boolean;
}

/** What a sign-in wants of the page, see LoginStep in providers.go. */
export interface LoginStep {
  kind: 'ask' | 'tell' | 'reject' | 'open' | 'done' | 'failed' | 'wait';
  question?: LoginQuestion;
  text?: string;
  access?: AccessInfo;
  models?: string[];
  default?: string;
}

/** The bound methods of the Go window behind the options of the node, see
 * providers.go; AgentModels lists the models of every access. */
export interface OptionsBridge {
  Providers(): Promise<ProviderList>;
  LLMs(): Promise<LLMList>;
  SetLLM(scope: Scope, access: string, model: string): Promise<LLMList>;
  ToggleFallback(access: string, model: string): Promise<LLMList>;
  SetEffort(effort: string): Promise<LLMList>;
  RenameAccess(id: string, name: string): Promise<ProviderList>;
  RemoveAccess(id: string): Promise<string>;
  StartLogin(provider: string, again: string): Promise<string>;
  LoginStep(id: string): Promise<LoginStep>;
  AnswerLogin(id: string, answer: string): Promise<void>;
  CancelLogin(id: string): Promise<void>;
  AgentModels(): Promise<AccessModels[]>;
}

/** A field of the form a gateway asks for, see GatewayFieldInfo in gateways.go.
 * The value of a secret is always empty. */
export interface GatewayField {
  key: string;
  label?: string;
  kind: 'text' | 'secret' | 'list' | 'bool' | string;
  value?: string;
  list?: string[];
  on?: boolean;
  /** A public text under the field, never a value. */
  hint?: string;
}

/** A gateway of the profile, see GatewayInfo: never a password or a token. */
export interface GatewayInfo {
  name: string;
  title: string;
  enabled: boolean;
  homeserver?: string;
  user_id?: string;
  allowed_users: string[];
  logged_in: boolean;
  status?: string;
  fields?: GatewayField[];
}

/** What the owner changes of a gateway, see GatewayChangeBody. An empty
 * password keeps the login the gateway has. */
export interface GatewayChange {
  enabled: boolean;
  homeserver?: string;
  user_id?: string;
  password?: string;
  allowed_users?: string[];
  logout?: boolean;
  values?: Record<string, string>;
  lists?: Record<string, string[]>;
}

/** The bound methods of the Go window behind the gateways, see gateways.go. */
export interface GatewayBridge {
  Gateways(): Promise<GatewayInfo[]>;
  SaveGateway(name: string, change: GatewayChange): Promise<GatewayInfo[]>;
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
  /** Loads the page anew, as the window does when the node serves another
   * build: a test hands in what happens instead of a navigation. */
  reload?(): void;
}

/** All the page talks through: the thread, and the board, the sessions and the
 * agents beside it. */
export interface Host extends Link {
  board: BoardBridge;
  sessions: SessionBridge;
  agents: AgentBridge;
  memories: MemoryBridge;
  /** The options of the node: a host from before them has none. */
  options?: OptionsBridge;
  /** The gateways of the profile: a host from before them has none. */
  gateways?: GatewayBridge;
}

// A scripted thread for looking at the page in a browser while it is built
// (pnpm dev). The window itself never uses it: connect picks it only when no
// Wails host is there and the bundle is a dev build.

import type {
  AccessInfo,
  AccessModels,
  AgentBridge,
  AgentChange,
  AgentInfo,
  AgentJob,
  BoardBridge,
  Card,
  Link,
  LLMList,
  LoginStep,
  OptionsBridge,
  ProviderInfo,
  ProviderList,
  SessionBridge,
  SessionInfo,
  SessionTrack,
  Ticket,
  WireEvent,
} from './wire';

const wait = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

export function demoLink(): Link {
  const handlers: ((batch: WireEvent[]) => void)[] = [];
  const timers = new Set<ReturnType<typeof setTimeout>>();
  let busy = false;
  let scripted = false;
  let question: string | null = null;

  const send = (batch: WireEvent[]) => {
    for (const handler of handlers) handler(batch);
  };
  const later = (ms: number, run: () => void) => {
    const timer = setTimeout(() => {
      timers.delete(timer);
      run();
    }, ms);
    timers.add(timer);
  };
  const stream = (list: WireEvent[], gap = 140) => {
    list.forEach((event, index) => later(index * gap, () => send([event])));
  };
  const stopTimers = () => {
    for (const timer of timers) clearTimeout(timer);
    timers.clear();
  };

  const reply = async (body: string) => {
    send([{ kind: 'say', text: 'Ich sehe mir das an.' }]);
    await wait(220);
    send([{ kind: 'step', tool: 'bash', call_id: 'demo-1', text: 'bash · go test ./…' }]);
    await wait(480);
    send([{ kind: 'step', tool: 'bash', call_id: 'demo-1', text: 'bash · go test ./… · ok', done: true }]);
    for (const word of body.split(/(?= )/)) {
      if (!busy) return;
      send([{ kind: 'answering', text: word }]);
      await wait(12);
    }
    send([{ kind: 'answer', text: body }, { kind: 'working', n: 0 }]);
    busy = false;
  };

  return {
    bridge: {
      async InitialState() {
        return { profile: 'demo', thread: 'thread' };
      },
      async Open() {
        if (!scripted) {
          scripted = true;
          later(500, () => {
            stream([
              { kind: 'working', n: 1 },
              { kind: 'step', tool: 'session_status', call_id: 'demo-0', text: 'session_status · status' },
              { kind: 'step', tool: 'session_status', call_id: 'demo-0', text: 'session_status · status · 2 laufen', done: true },
              { kind: 'say', text: 'Zwei Sessions arbeiten noch: eine prüft die Scroll-Logik, die andere schreibt Tests.' },
              { kind: 'asked', text: 'Soll ich eine davon stoppen?', options: ['Ja, beide stoppen', 'Nein, laufen lassen'], recommended: 'Nein, laufen lassen' },
              { kind: 'working', n: 0 },
            ]);
            question = 'Soll ich eine davon stoppen?';
          });
        }
        return {
          before: 0,
          has_more: false,
          events: [
            { kind: 'message', text: 'Was hängt gerade?', at: new Date(Date.now() - 90000).toISOString() },
            {
              kind: 'answer',
              at: new Date(Date.now() - 85000).toISOString(),
              text: 'Nichts. Ich bin bereit.\n\n## Zuletzt\n\n- `go test ./...` lief durch\n- der Build ist aktuell\n\n```go\nfunc main() {\n\tprintln("bereit")\n}\n```',
            },
          ],
        };
      },
      async History() {
        return { events: [], before: 0, has_more: false };
      },
      async Send(text: string) {
        send([{ kind: 'message', text, at: new Date().toISOString() }, { kind: 'working', n: 1, own: true }]);
        busy = true;
        if (question) {
          question = null;
          send([{ kind: 'asked', text: 'Soll ich eine davon stoppen?', done: true }]);
          await wait(400);
          send([{ kind: 'answer', text: text === 'Ja, beide stoppen' ? 'Beide gestoppt. Der Thread ist wieder frei.' : 'Gut, sie laufen weiter.' }, { kind: 'working', n: 0 }]);
          busy = false;
          return;
        }
        await reply(
          `## Klar\n\nDas ist eine Demo-Antwort auf **${text}**.\n\n- der Verlauf rendert Markdown\n- Werkzeuge erscheinen als schmale Zeilen\n- Antworten wachsen beim Streamen\n\n\`\`\`go\nfunc main() {\n\tprintln("hallo")\n}\n\`\`\`\n\nEin [Link](https://example.com) bleibt Text.`,
        );
      },
      async Stop() {
        busy = false;
        stopTimers();
        send([{ kind: 'canceled', text: 'gestoppt' }, { kind: 'working', n: 0 }]);
      },
    },
    transport: {
      onEvents(handler) {
        handlers.push(handler);
      },
      onEnded() {},
      onRestarting() {},
    },
  };
}

/** A scripted board to look at beside the scripted thread. */
export function demoBoard(): BoardBridge {
  const before = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();
  const tickets: Ticket[] = [
    {
      id: 7,
      title: 'Suche über alle Notizen',
      text: 'Die Suche soll auch archivierte Notizen finden.',
      column: 'new',
      created: before(3),
      changed: before(2),
      runs: [{ session: '5f1c2a9e4b7d', role: 'refinement', agent: 'agent-3f9a1c07', agent_name: 'Researcher', began: before(2) }],
      notes: [{ at: before(2), by: 'orchestrator', text: 'started session 5f1c2a9e4b7d for its refinement' }],
    },
    {
      id: 6,
      title: 'Export als CSV',
      text: 'Die Tabelle der Auswertung soll sich als **CSV** herunterladen lassen.\n\n- alle Spalten\n- UTF-8',
      directory: '/home/demo/Projects/auswertung',
      column: 'ready',
      created: before(50),
      changed: before(12),
      runs: [{ session: 'a81d03c77f02', role: 'refinement', began: before(40), ended: before(12), outcome: 'ready' }],
      notes: [
        { at: before(40), by: 'orchestrator', text: 'started session a81d03c77f02 for its refinement' },
        { at: before(12), by: 'session a81d03c77f02', text: 'Der Export gehört in export.go; Trennzeichen ist das Semikolon.' },
        { at: before(12), by: 'session a81d03c77f02', text: 'moved from new to ready' },
      ],
    },
    {
      id: 5,
      title: 'Dark Mode für die Einstellungen',
      text: 'Die Einstellungen brauchen einen Dark Mode, der dem System folgt.',
      directory: '/home/demo/Projects/app',
      column: 'progress',
      created: before(180),
      changed: before(25),
      runs: [
        { session: '0c44e1b2d9aa', role: 'refinement', agent: 'thread', began: before(170), ended: before(150), outcome: 'ready' },
        { session: '7be90f3316c4', role: 'implementation', agent: 'thread', began: before(25) },
      ],
      notes: [
        { at: before(150), by: 'session 0c44e1b2d9aa', text: 'Die Farben stehen in theme.css.' },
        { at: before(25), by: 'orchestrator', text: 'started session 7be90f3316c4 for its implementation' },
      ],
    },
    {
      id: 4,
      title: 'Login über den Firmen-Account',
      text: 'Mitarbeiter sollen sich mit ihrem Firmen-Account anmelden können.',
      column: 'blocked',
      from: 'new',
      reason: '1. Welcher Anbieter: Entra ID oder Google Workspace?\n2. Sollen bestehende Konten zusammengeführt werden?',
      told: true,
      created: before(300),
      changed: before(200),
      runs: [{ session: 'e02a7c5519bd', role: 'refinement', began: before(220), ended: before(200), outcome: 'blocked' }],
      notes: [
        { at: before(200), by: 'session e02a7c5519bd', text: 'moved from new to blocked' },
        { at: before(199), by: 'orchestrator', text: 'told the thread' },
      ],
    },
    {
      id: 3,
      title: 'Tippfehler im Footer',
      text: 'Im Footer steht „Impresum“.',
      column: 'done',
      result: 'Korrigiert in `footer.html`; die Seite baut.',
      told: true,
      created: before(1500),
      changed: before(1400),
      runs: [{ session: '91aa0e44c0d1', role: 'implementation', began: before(1410), ended: before(1400), outcome: 'done' }],
      notes: [{ at: before(1400), by: 'session 91aa0e44c0d1', text: 'Korrigiert in footer.html; die Seite baut.' }],
    },
  ];
  const card = (ticket: Ticket): Card => {
    const run = ticket.runs?.find((r) => !r.ended);
    return {
      id: ticket.id,
      title: ticket.title,
      column: ticket.column,
      reason: ticket.reason?.replace(/\s+/g, ' '),
      from: ticket.from,
      role: run?.role,
      agent: run?.agent,
      agent_name: run?.agent_name,
      session: run?.session,
      since: run?.began,
      told: ticket.told,
      changed: ticket.changed,
      notes: ticket.notes?.length ?? 0,
    };
  };
  return {
    async Board() {
      return {
        lanes: (['new', 'ready', 'progress', 'blocked', 'done'] as const).map((column) => ({
          column,
          cards: tickets.filter((ticket) => ticket.column === column).map(card),
        })),
        looked: { at: before(2), session: 'd3b1f0a2', result: 'Started the refinement of ticket 7.' },
        pending: false,
      };
    },
    async Ticket(id: number) {
      const ticket = tickets.find((candidate) => candidate.id === id);
      if (!ticket) throw new Error(`the board has no ticket ${id} any more`);
      return ticket;
    },
  };
}

/** A scripted list of sessions to look at beside the scripted board. */
export function demoSessions(): SessionBridge {
  const before = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();
  const list: SessionInfo[] = [
    {
      id: '7be90f3316c4aa81',
      title: 'Dark Mode für die Einstellungen',
      running: true,
      last: before(1),
      ticket: 5,
      work: 'Dark Mode für die Einstellungen',
      role: 'implementation',
      since: before(25),
      jobs: [{ n: 1, command: 'go test ./internal/theme/', since: before(3) }],
      agent: 'thread',
    },
    { id: 'd3b1f0a2c9e44b10', title: 'Board orchestrator', running: false, last: before(2), agent: 'orchestrator' },
    {
      id: '5f1c2a9e4b7d0c12',
      title: 'Suche über alle Notizen',
      running: true,
      last: before(2),
      ticket: 7,
      work: 'Suche über alle Notizen',
      role: 'refinement',
      since: before(2),
      agent: 'thread',
    },
    { id: '4c0ffee5a7b8d9e1', title: 'Archive memories', running: false, last: before(380), agent: 'archivist' },
    { id: '91aa0e44c0d1e777', title: 'Tippfehler im Footer', running: false, last: before(1400), agent: 'thread' },
  ];
  const track = (session: SessionInfo): SessionTrack => ({
    ...session,
    events: [
      { kind: 'message', text: 'Bitte den Dark Mode an das System koppeln.', at: before(25) },
      { kind: 'say', text: 'Ich sehe mir `theme.css` an und prüfe, ob es schon `prefers-color-scheme` nutzt.', at: before(24) },
      { kind: 'step', tool: 'read', call_id: 'demo-1', text: 'read · internal/theme/theme.css', done: true, at: before(23) },
      { kind: 'step', tool: 'bash', call_id: 'demo-2', text: 'bash · go test ./internal/theme/', at: before(3) },
    ],
  });
  return {
    async Sessions() {
      return list;
    },
    async Session(id: string) {
      const session = list.find((candidate) => candidate.id === id);
      if (!session) throw new Error(`there is no session ${id} any more`);
      return track(session);
    },
  };
}

/** The scripted agents and their jobs: the agents are renamed in the page
 * alone. */
export function demoAgents(): AgentBridge {
  const after = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString();
  let list: AgentInfo[] = [
    { id: 'thread', kind: 'thread', name: 'pythia', soul: 'Ruhig, knapp, mit trockenem Humor.', access: 'claude-1', model: 'opus' },
    { id: 'archivist', kind: 'archivist', name: 'Archivist', reserved: true },
    { id: 'orchestrator', kind: 'orchestrator', name: 'Orchestrator', reserved: true },
    {
      id: 'agent-3f9a1c07',
      kind: 'custom',
      name: 'Researcher',
      description: 'Digs into sources and compares them before it answers.',
      soul: 'Thorough and sceptical. Names its sources.',
      access: 'openai-1',
      model: 'gpt-5',
    },
  ];
  let made = 0;
  const jobs: AgentJob[] = [
    {
      id: 'a1b2c3d4',
      name: 'Archive memories',
      agent: 'archivist',
      text: 'You are the archivist of the memories this agent keeps of its owner and their environment. You tidy them now, in a scheduled session, while nobody waits for you.',
      cron: '0 4 * * *',
      zone: 'Europe/Berlin',
      next: after(14 * 60),
    },
    {
      id: 'e5f6a7b8',
      name: 'Morgendliche Übersicht',
      agent: 'thread',
      text: 'Sag mir, was heute im Kalender steht und welche Tickets blockiert sind.',
      cron: '30 7 * * 1-5',
      zone: 'Europe/Berlin',
      paused: true,
    },
    {
      id: 'c9d0e1f2',
      name: 'Paper-Recherche',
      agent: 'agent-3f9a1c07',
      text: 'Fasse die neuen Paper zu Agenten-Gedächtnis zusammen.',
      at: after(26 * 60).slice(0, 16),
      zone: 'Europe/Berlin',
      next: after(26 * 60),
    },
  ];
  const free = (id: string, name: string) => {
    if (!name) throw new Error('the name of an agent cannot be empty');
    if (list.some((other) => other.id !== id && other.name.toLowerCase() === name.toLowerCase())) {
      throw new Error(`another agent is called "${name}" already`);
    }
  };
  const of = (change: AgentChange) => ({
    name: change.name.trim(),
    description: change.description.trim() || undefined,
    soul: change.soul.trim() || undefined,
    access: change.access || undefined,
    model: change.model || undefined,
  });
  return {
    async Agents() {
      return list;
    },
    async UpdateAgent(id: string, change: AgentChange) {
      const agent = list.find((candidate) => candidate.id === id);
      if (!agent) throw new Error(`the profile has no agent "${id}"`);
      const next = of(change);
      if (agent.reserved && next.description) throw new Error('what a system agent is for is fixed');
      free(id, next.name);
      const changed = { ...agent, ...next };
      list = list.map((candidate) => (candidate.id === id ? changed : candidate));
      return changed;
    },
    async CreateAgent(change: AgentChange) {
      const next = of(change);
      free('', next.name);
      const agent: AgentInfo = { id: `agent-demo${String(++made).padStart(4, '0')}`, kind: 'custom', ...next };
      list = [...list, agent];
      return agent;
    },
    async DeleteAgent(id: string) {
      const agent = list.find((candidate) => candidate.id === id);
      if (!agent) throw new Error(`the profile has no agent "${id}"`);
      if (agent.kind !== 'custom') throw new Error('a built-in agent cannot be removed');
      list = list.filter((candidate) => candidate.id !== id);
    },
    async AgentModels() {
      await new Promise((resolve) => setTimeout(resolve, 600));
      return [
        { access: 'openai-1', label: 'OpenAI', provider: 'openai', models: ['gpt-5', 'gpt-5-mini', 'o3'] },
        { access: 'claude-1', label: 'Claude Code', provider: 'claude-code', models: ['sonnet', 'opus', 'haiku'] },
        { access: 'deepseek-1', label: 'DeepSeek', provider: 'deepseek', models: [], error: 'it did not answer in time' },
      ];
    },
    async AgentJobs() {
      return jobs;
    },
  };
}

/** The options of a scripted node: two accesses, a third provider to sign in
 * with, and a sign-in that asks for a key and is saved. */
export function demoOptions(): OptionsBridge {
  const providers: ProviderInfo[] = [
    { name: 'openai', title: 'OpenAI' },
    { name: 'claude-code', title: 'Claude Code' },
    { name: 'deepseek', title: 'DeepSeek' },
    { name: 'compatible', title: 'OpenAI-compatible API' },
  ];
  let accesses: AccessInfo[] = [
    { id: 'openai-1', provider: 'openai', label: 'OpenAI', about: 'api.openai.com' },
    { id: 'claude-1', provider: 'claude-code', name: 'Work', label: 'Work', about: 'lucas@example.test' },
    { id: 'deepseek-1', provider: 'deepseek', label: 'DeepSeek', about: 'api.deepseek.com' },
  ];
  let llms: LLMList = {
    scopes: [
      { scope: 'owner', access: 'claude-1', model: 'sonnet' },
      { scope: 'sessions', access: 'claude-1', model: 'sonnet' },
      { scope: 'thread', access: 'openai-1', model: 'gpt-5', own: true },
      { scope: 'peers', access: 'claude-1', model: 'sonnet' },
    ],
    fallbacks: [{ access: 'openai-1', model: 'gpt-5-mini' }],
    effort: 'medium',
    efforts: ['', 'low', 'medium', 'high', 'max'],
    efforts_known: true,
  };
  const models: AccessModels[] = [
    { access: 'openai-1', label: 'OpenAI', provider: 'openai', models: ['gpt-5', 'gpt-5-mini', 'o3'] },
    { access: 'claude-1', label: 'Work', provider: 'claude-code', models: ['sonnet', 'opus', 'haiku'] },
    { access: 'deepseek-1', label: 'DeepSeek', provider: 'deepseek', models: [], error: 'it did not answer in time' },
  ];
  const uses = (): ProviderList => {
    const by: Record<string, string[]> = {};
    for (const s of llms.scopes) if (s.scope === 'owner' || s.own) (by[s.access] ??= []).push(s.scope);
    for (const f of llms.fallbacks) if (!(by[f.access] ??= []).includes('fallback')) by[f.access].push('fallback');
    return { providers, accesses: accesses.map((a) => ({ ...a, uses: by[a.id] })) };
  };
  const owner = () => llms.scopes[0];
  let steps: LoginStep[] = [];
  let login = '';
  return {
    async Providers() {
      return uses();
    },
    async LLMs() {
      return llms;
    },
    async SetLLM(scope, access, model) {
      await wait(250);
      llms = {
        ...llms,
        scopes: llms.scopes.map((s) =>
          s.scope === scope
            ? access
              ? { scope, access, model, own: scope !== 'owner' }
              : { scope, access: owner().access, model: owner().model }
            : scope === 'owner' && !s.own
              ? { ...s, access, model }
              : s,
        ),
      };
      return llms;
    },
    async ToggleFallback(access, model) {
      const at = llms.fallbacks.findIndex((f) => f.access === access && f.model === model);
      llms = { ...llms, fallbacks: at >= 0 ? llms.fallbacks.filter((_, i) => i !== at) : [...llms.fallbacks, { access, model }] };
      return llms;
    },
    async SetEffort(effort) {
      llms = { ...llms, effort };
      return llms;
    },
    async RenameAccess(id, name) {
      accesses = accesses.map((a) => (a.id === id ? { ...a, name, label: name || providers.find((p) => p.name === a.provider)!.title } : a));
      return uses();
    },
    async RemoveAccess(id) {
      await wait(400);
      accesses = accesses.filter((a) => a.id !== id);
      return '';
    },
    async StartLogin(provider) {
      login = `login-${Date.now()}`;
      const title = providers.find((p) => p.name === provider)?.title ?? provider;
      steps = [{ kind: 'ask', question: { title: 'API key', text: [`The key of your ${title} account.`], secret: true, hint: 'sk-…' } }];
      return login;
    },
    async LoginStep(id) {
      if (id !== login) throw new Error('the sign-in is over: start it again');
      await wait(400);
      return steps.shift() ?? { kind: 'wait' };
    },
    async AnswerLogin(_id, answer) {
      if (!answer.startsWith('sk-')) {
        steps.push({ kind: 'reject', text: 'API key rejected' }, { kind: 'ask', question: { title: 'API key', secret: true, hint: 'sk-…' } });
        return;
      }
      const access: AccessInfo = { id: `access-${accesses.length + 1}`, provider: 'deepseek', label: 'DeepSeek', about: 'second account' };
      accesses = [...accesses, access];
      steps.push({ kind: 'tell', text: 'Checking the key …' }, { kind: 'done', access, models: ['deepseek-chat', 'deepseek-reasoner'], default: 'deepseek-chat' });
    },
    async CancelLogin() {
      login = '';
    },
    async AgentModels() {
      await wait(500);
      return models;
    },
  };
}

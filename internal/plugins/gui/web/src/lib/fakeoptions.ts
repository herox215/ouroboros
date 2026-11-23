// The options of a node for tests: what is changed is kept, and what a sign-in
// wants of the page is handed in by the test, one step after the other.

import type { AccessInfo, AccessModels, LLMList, LoginStep, OptionsBridge, ProviderList, Scope } from './wire';

export interface FakeOptions extends OptionsBridge {
  /** Hands steps to the sign-in that asks for them. */
  push(...steps: LoginStep[]): void;
  answers: string[];
  canceled: string[];
  started: { provider: string; again: string }[];
  /** What a call of the name fails with, while it is set. */
  fail: Partial<Record<keyof OptionsBridge, string>>;
}

export function fakeOptions(): FakeOptions {
  let accesses: AccessInfo[] = [
    { id: 'a1', provider: 'openai', label: 'OpenAI', about: 'api.openai.com' },
    { id: 'a2', provider: 'claude-code', name: 'Work', label: 'Work', about: 'me@example.test' },
  ];
  let llms: LLMList = {
    scopes: [
      { scope: 'owner', access: 'a2', model: 'sonnet' },
      { scope: 'sessions', access: 'a2', model: 'sonnet' },
      { scope: 'thread', access: 'a1', model: 'gpt-5', own: true },
      { scope: 'peers', access: 'a2', model: 'sonnet' },
    ],
    fallbacks: [],
    effort: '',
    efforts: ['', 'none', 'low', 'medium', 'high', 'max'],
  };
  const models: AccessModels[] = [
    { access: 'a1', label: 'OpenAI', provider: 'openai', models: ['gpt-5', 'gpt-5-mini'] },
    { access: 'a2', label: 'Work', provider: 'claude-code', models: ['sonnet', 'opus'] },
  ];
  const queue: LoginStep[] = [];
  let waiting: (() => void) | null = null;
  let login = '';
  const fake: FakeOptions = {
    answers: [],
    canceled: [],
    started: [],
    fail: {},
    push(...steps) {
      queue.push(...steps);
      waiting?.();
      waiting = null;
    },
    async Providers(): Promise<ProviderList> {
      if (fake.fail.Providers) throw new Error(fake.fail.Providers);
      return {
        providers: [
          { name: 'openai', title: 'OpenAI' },
          { name: 'claude-code', title: 'Claude Code' },
          { name: 'deepseek', title: 'DeepSeek' },
        ],
        accesses: accesses.map((access) => ({
          ...access,
          uses: llms.scopes.filter((s) => s.access === access.id && (s.scope === 'owner' || s.own)).map((s) => s.scope),
        })),
      };
    },
    async LLMs() {
      return llms;
    },
    async SetLLM(scope: Scope, access: string, model: string) {
      if (fake.fail.SetLLM) throw new Error(fake.fail.SetLLM);
      const owner = llms.scopes[0];
      llms = {
        ...llms,
        scopes: llms.scopes.map((s) =>
          s.scope === scope ? (access ? { scope, access, model, own: scope !== 'owner' } : { scope, access: owner.access, model: owner.model }) : s,
        ),
      };
      return llms;
    },
    async ToggleFallback(access: string, model: string) {
      const at = llms.fallbacks.findIndex((f) => f.access === access && f.model === model);
      llms = { ...llms, fallbacks: at >= 0 ? llms.fallbacks.filter((_, i) => i !== at) : [...llms.fallbacks, { access, model }] };
      return llms;
    },
    async SetEffort(effort: string) {
      llms = { ...llms, effort };
      return llms;
    },
    async RenameAccess(id: string, name: string) {
      if (fake.fail.RenameAccess) throw new Error(fake.fail.RenameAccess);
      accesses = accesses.map((a) => (a.id === id ? { ...a, name, label: name || a.provider } : a));
      return fake.Providers();
    },
    async RemoveAccess(id: string) {
      if (fake.fail.RemoveAccess) throw new Error(fake.fail.RemoveAccess);
      accesses = accesses.filter((a) => a.id !== id);
      return 'the agents answer with gpt-5 of OpenAI now.';
    },
    async StartLogin(provider: string, again: string) {
      if (fake.fail.StartLogin) throw new Error(fake.fail.StartLogin);
      fake.started.push({ provider, again });
      login = `login-${fake.started.length}`;
      return login;
    },
    async LoginStep(id: string) {
      if (id !== login) throw new Error('the sign-in is over: start it again');
      while (queue.length === 0) await new Promise<void>((resolve) => (waiting = resolve));
      const step = queue.shift()!;
      if (step.kind === 'done' && step.access) accesses = [...accesses, step.access];
      return step;
    },
    async AnswerLogin(_id: string, answer: string) {
      fake.answers.push(answer);
    },
    async CancelLogin(id: string) {
      fake.canceled.push(id);
    },
    async AgentModels() {
      if (fake.fail.AgentModels) throw new Error(fake.fail.AgentModels);
      return models;
    },
  };
  return fake;
}

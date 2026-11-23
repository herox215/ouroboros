import type { AccessInfo, AccessModels, LLMList, LoginQuestion, OptionsBridge, ProviderList, Scope } from './wire';

function reason(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

/** One line a provider showed while it signs in: what it told, why it did not
 * take an answer, or a page to open. */
export interface LoginLine {
  kind: 'tell' | 'reject' | 'open';
  text: string;
}

/** A sign-in to a provider, as the options show it. */
export interface Login {
  id: string;
  /** What is signed in to: the title of the provider, or the label of the
   * access signed in to anew. */
  title: string;
  /** The provider signed in with, and the access signed in to anew, '' for
   * a new one: what it is started with again. */
  provider: string;
  again: string;
  /** 'working' while the provider works, 'asking' while it waits for an
   * answer, and how it ended. */
  status: 'working' | 'asking' | 'done' | 'failed';
  question: LoginQuestion | null;
  lines: LoginLine[];
  /** Why it failed. */
  error: string;
  /** The access it saved, and the models it offers. */
  access?: AccessInfo;
  models?: string[];
  default?: string;
}

/**
 * The options of the node: its accesses to the APIs of the providers, and what
 * each of its scopes answers with. They change only when the owner changes
 * them, so the store reads them when the options are shown. Each change holds
 * from the next message on: the node needs no restart for it.
 */
export class NodeOptions {
  readonly #bridge: OptionsBridge;
  #asked = 0;

  /** The providers and the accesses, null until they were read. */
  list = $state<ProviderList | null>(null);
  /** What the scopes answer with, null until it was read. */
  llms = $state<LLMList | null>(null);
  /** Why they could not be read, '' when they could. */
  error = $state('');

  /** The models of every access, null while they were not listed. */
  models = $state<AccessModels[] | null>(null);
  modelsError = $state('');
  listing = $state(false);

  /** What is being changed now, like 'owner' or 'rename:ID', null while
   * nothing is; changeError says why the last change failed. */
  busy = $state<string | null>(null);
  changeError = $state('');
  /** What the owner should know of the last change, like how removing an
   * access went. */
  note = $state('');

  /** The sign-in on its way, or how it ended, until it is closed. */
  login = $state<Login | null>(null);

  /** Called when the accesses changed: the agents list their models anew. */
  onaccesses: (() => void) | null = null;

  constructor(bridge: OptionsBridge) {
    this.#bridge = bridge;
  }

  /** Reads the accesses and what the scopes answer with. */
  async refresh(): Promise<void> {
    const asked = ++this.#asked;
    try {
      const [list, llms] = await Promise.all([this.#bridge.Providers(), this.#bridge.LLMs()]);
      if (asked !== this.#asked) return;
      this.list = list;
      this.llms = llms;
      this.error = '';
    } catch (error) {
      if (asked !== this.#asked) return;
      this.error = reason(error);
    }
  }

  /** Lists the models of every access; again only when force says so, as the
   * accesses take a while to answer. */
  async listModels(force = false): Promise<void> {
    if (this.listing || (this.models && !force)) return;
    this.listing = true;
    try {
      this.models = await this.#bridge.AgentModels();
      this.modelsError = '';
    } catch (error) {
      this.modelsError = reason(error);
    } finally {
      this.listing = false;
    }
  }

  /** What the access id is called, its ID while it is not known. */
  label(id: string): string {
    return this.list?.accesses.find((access) => access.id === id)?.label || id;
  }

  /** Makes model of access what scope answers with; access '' has a scope
   * other than the owner follow the owner again. */
  setLLM(scope: Scope, access: string, model: string): Promise<boolean> {
    return this.#change(scope, async () => {
      this.llms = await this.#bridge.SetLLM(scope, access, model);
      await this.#reread();
    });
  }

  /** Makes model of access the last fallback, or none any more. */
  toggleFallback(access: string, model: string): Promise<boolean> {
    return this.#change('fallback', async () => {
      this.llms = await this.#bridge.ToggleFallback(access, model);
      await this.#reread();
    });
  }

  /** Makes effort how much the model thinks before it answers the owner. */
  setEffort(effort: string): Promise<boolean> {
    return this.#change('effort', async () => {
      this.llms = await this.#bridge.SetEffort(effort);
    });
  }

  /** Calls the access id name, '' for what its service is called. */
  rename(id: string, name: string): Promise<boolean> {
    return this.#change(`rename:${id}`, async () => {
      this.list = await this.#bridge.RenameAccess(id, name.trim());
      this.#accessesChanged();
    });
  }

  /** Removes the access id; note says what the owner should know of it. */
  remove(id: string): Promise<boolean> {
    return this.#change(`remove:${id}`, async () => {
      this.note = await this.#bridge.RemoveAccess(id);
      this.#accessesChanged();
      await this.refresh();
    });
  }

  /** Forgets the note and the error of the last change. */
  clear(): void {
    this.note = '';
    this.changeError = '';
  }

  /** Signs in to a new access with the provider, or anew to the access again.
   * The sign-in that was on its way is given up. */
  async startLogin(provider: string, again = ''): Promise<void> {
    await this.closeLogin();
    this.clear();
    const title = again
      ? this.label(again)
      : (this.list?.providers.find((candidate) => candidate.name === provider)?.title ?? provider);
    this.login = { id: '', title, provider, again, status: 'working', question: null, lines: [], error: '' };
    // The store holds a proxy of what it was given: that is what is compared
    // and changed from here on.
    const login = this.login;
    try {
      login.id = await this.#bridge.StartLogin(provider, again);
    } catch (error) {
      if (this.login === login) this.#fail(reason(error));
      return;
    }
    if (this.login === login) void this.#follow(login);
  }

  /** Answers what the sign-in asks. */
  async answer(text: string): Promise<void> {
    const login = this.login;
    if (!login || login.status !== 'asking') return;
    login.status = 'working';
    login.question = null;
    try {
      await this.#bridge.AnswerLogin(login.id, text);
    } catch (error) {
      if (this.login === login) this.#fail(reason(error));
    }
  }

  /** Gives up the sign-in on its way, or closes the one that ended. */
  async closeLogin(): Promise<void> {
    const login = this.login;
    this.login = null;
    if (login && login.id && (login.status === 'working' || login.status === 'asking')) {
      try {
        await this.#bridge.CancelLogin(login.id);
      } catch {
        // Given up anyway: the node forgets it once nobody asks for it.
      }
    }
  }

  /** Asks the sign-in for its steps until it ended or was closed. */
  async #follow(login: Login): Promise<void> {
    while (this.login === login && (login.status === 'working' || login.status === 'asking')) {
      let step;
      try {
        step = await this.#bridge.LoginStep(login.id);
      } catch (error) {
        if (this.login === login) this.#fail(reason(error));
        return;
      }
      if (this.login !== login) return;
      switch (step.kind) {
        case 'ask':
          login.question = step.question ?? { title: '' };
          login.status = 'asking';
          break;
        case 'tell':
        case 'reject':
        case 'open':
          if (step.text) login.lines = [...login.lines, { kind: step.kind, text: step.text }];
          break;
        case 'done':
          login.status = 'done';
          login.question = null;
          login.access = step.access;
          login.models = step.models ?? [];
          login.default = step.default ?? '';
          this.#accessesChanged();
          await this.refresh();
          return;
        case 'failed':
          this.#fail(step.text ?? '');
          return;
      }
    }
  }

  #fail(why: string): void {
    if (!this.login) return;
    this.login.status = 'failed';
    this.login.question = null;
    this.login.error = why;
  }

  /** The accesses changed: what they offer is listed anew. */
  #accessesChanged(): void {
    this.models = null;
    this.onaccesses?.();
  }

  /** Reads the accesses again, which tell what answers through each. */
  async #reread(): Promise<void> {
    try {
      this.list = await this.#bridge.Providers();
    } catch {
      // What was shown stays.
    }
  }

  /** Runs one change: one at a time. */
  async #change(what: string, change: () => Promise<void>): Promise<boolean> {
    if (this.busy !== null) return false;
    this.busy = what;
    this.clear();
    try {
      await change();
      this.#asked++;
      return true;
    } catch (error) {
      this.changeError = reason(error);
      return false;
    } finally {
      this.busy = null;
    }
  }
}

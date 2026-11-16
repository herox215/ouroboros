import type { AccessModels, AgentBridge, AgentChange, AgentInfo } from './wire';

function reason(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

/** What the store asks of the host; AgentModels is optional, as a host from
 * before agents had models has it not. */
type Bridge = Pick<AgentBridge, 'Agents' | 'UpdateAgent' | 'CreateAgent' | 'DeleteAgent'> & Partial<Pick<AgentBridge, 'AgentModels'>>;

/** The ID under which the store says that a new agent is being saved. */
export const NEW_AGENT = '';

/**
 * The agents of the profile, as the window shows them: the agent of the
 * thread, which is called as the profile is, the archivist, the orchestrator
 * and those the owner made. They change only when the owner changes them, so
 * the store reads them when the view is shown, not on a timer.
 */
export class Agents {
  readonly #bridge: Bridge;
  /** Counts the reads: an answer that a newer read overtook is dropped. */
  #asked = 0;

  /** The agents as they were read last, null until the first answer. */
  list = $state<AgentInfo[] | null>(null);
  /** Why the agents could not be read, '' when they could. */
  error = $state('');
  /** The agent being saved or removed now, NEW_AGENT for one being made, null
   * while none is. */
  saving = $state<string | null>(null);
  /** Why the last change failed, '' when it did not. */
  changeError = $state('');

  /** The models an agent may answer with, as they were listed, null while
   * they were not; modelsError says why they could not be listed. */
  models = $state<AccessModels[] | null>(null);
  modelsError = $state('');
  #listing: Promise<void> | null = null;

  constructor(bridge: Bridge) {
    this.#bridge = bridge;
  }

  /** Reads the agents anew. What was shown stays while they cannot be read. */
  async refresh(): Promise<void> {
    const asked = ++this.#asked;
    try {
      const list = await this.#bridge.Agents();
      if (asked !== this.#asked) return;
      this.list = list;
      this.error = '';
    } catch (error) {
      if (asked !== this.#asked) return;
      this.error = reason(error);
    }
  }

  /** Changes the agent id as change says, and returns the agent as it is
   * then, or null when the node refused it: changeError then says why. */
  async update(id: string, change: AgentChange): Promise<AgentInfo | null> {
    return this.#change(id, async () => {
      const agent = await this.#bridge.UpdateAgent(id, change);
      this.list = (this.list ?? [agent]).map((candidate) => (candidate.id === agent.id ? agent : candidate));
      return agent;
    });
  }

  /** Makes an agent of the owner's as change says, and returns it, or null
   * when the node refused it: changeError then says why. */
  async create(change: AgentChange): Promise<AgentInfo | null> {
    return this.#change(NEW_AGENT, async () => {
      const agent = await this.#bridge.CreateAgent(change);
      this.list = [...(this.list ?? []), agent];
      return agent;
    });
  }

  /** Removes the agent id of the owner's, and reports whether it is gone:
   * changeError says why not. */
  async remove(id: string): Promise<boolean> {
    const done = await this.#change(id, async () => {
      await this.#bridge.DeleteAgent(id);
      this.list = (this.list ?? []).filter((candidate) => candidate.id !== id);
      return true;
    });
    return !!done;
  }

  /** Lists the models an agent may answer with, once: the accesses take a
   * while to answer, and what they offer changes seldom. A list that failed
   * is asked for again. A host from before agents had models lists none. */
  listModels(): Promise<void> {
    if (this.models || !this.#bridge.AgentModels) return Promise.resolve();
    if (this.#listing) return this.#listing;
    const listing = (async () => {
      try {
        this.models = await this.#bridge.AgentModels!();
        this.modelsError = '';
      } catch (error) {
        this.modelsError = reason(error);
      }
    })();
    this.#listing = listing;
    void listing.then(() => {
      if (this.#listing === listing) this.#listing = null;
    });
    return listing;
  }

  /** Forgets why the last change failed, as when the owner gives it up. */
  clearChangeError(): void {
    this.changeError = '';
  }

  /** Runs one change of the agent id: one at a time, and a read that was on
   * its way when it was made knew the agents as they were before. */
  async #change<T>(id: string, change: () => Promise<T>): Promise<T | null> {
    if (this.saving !== null) return null;
    this.saving = id;
    this.changeError = '';
    try {
      const done = await change();
      this.#asked++;
      return done;
    } catch (error) {
      this.changeError = reason(error);
      return null;
    } finally {
      this.saving = null;
    }
  }
}

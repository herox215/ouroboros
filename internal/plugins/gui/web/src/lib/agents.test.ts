import { describe, expect, it } from 'vitest';
import { Agents, NEW_AGENT } from './agents.svelte';
import type { AgentBridge, AgentChange, AgentInfo } from './wire';

type Bridge = Pick<AgentBridge, 'Agents' | 'UpdateAgent' | 'CreateAgent' | 'DeleteAgent'>;

const refused = async (): Promise<never> => {
  throw new Error('unused');
};

function bridgeOf(name = 'pythia'): Bridge & { changes: AgentChange[] } {
  let list: AgentInfo[] = [{ id: 'thread', kind: 'thread', name }];
  const changes: AgentChange[] = [];
  return {
    changes,
    async Agents() {
      return list;
    },
    async UpdateAgent(id: string, change: AgentChange) {
      changes.push(change);
      if (!list.some((agent) => agent.id === id)) throw new Error(`the profile has no agent "${id}"`);
      if (change.name === 'hermes') throw new Error('there is a profile called "hermes" already');
      const agent = { ...list.find((candidate) => candidate.id === id)!, ...change };
      list = list.map((candidate) => (candidate.id === id ? agent : candidate));
      return agent;
    },
    async CreateAgent(change: AgentChange) {
      changes.push(change);
      if (!change.name) throw new Error('the name of an agent cannot be empty');
      const agent = { id: `agent-${list.length}`, kind: 'custom', ...change };
      list = [...list, agent];
      return agent;
    },
    async DeleteAgent(id: string) {
      if (id === 'thread') throw new Error('a built-in agent cannot be removed');
      list = list.filter((agent) => agent.id !== id);
    },
  };
}

describe('Agents', () => {
  it('reads the agent of the thread, and changes it', async () => {
    const bridge = bridgeOf();
    const agents = new Agents(bridge);
    expect(agents.list).toBeNull();

    await agents.refresh();
    expect(agents.list).toEqual([{ id: 'thread', kind: 'thread', name: 'pythia' }]);
    expect(agents.error).toBe('');

    const changed = agents.update('thread', { name: 'Delphi', description: '', soul: 'Calm.', access: '', model: '', effort: '' });
    expect(agents.saving).toBe('thread');
    expect(await changed).toEqual({ id: 'thread', kind: 'thread', name: 'Delphi', description: '', soul: 'Calm.', access: '', model: '', effort: '' });
    expect(agents.saving).toBeNull();
    expect(agents.list?.[0].soul).toBe('Calm.');
    expect(agents.changeError).toBe('');
  });

  it('makes and removes agents of the owner', async () => {
    const agents = new Agents(bridgeOf());
    await agents.refresh();
    const making = agents.create({ name: 'Researcher', description: 'Digs.', soul: 'Thorough.', access: '', model: '', effort: '' });
    expect(agents.saving).toBe(NEW_AGENT);
    const made = await making;
    expect(made?.kind).toBe('custom');
    expect(agents.list?.map((agent) => agent.name)).toEqual(['pythia', 'Researcher']);

    expect(await agents.remove('thread')).toBe(false);
    expect(agents.changeError).toContain('built-in');
    expect(await agents.remove(made!.id)).toBe(true);
    expect(agents.changeError).toBe('');
    expect(agents.list?.map((agent) => agent.name)).toEqual(['pythia']);

    expect(await agents.create({ name: '', description: '', soul: '', access: '', model: '', effort: '' })).toBeNull();
    expect(agents.changeError).toContain('cannot be empty');
  });

  it('says why a change was refused and keeps the agent', async () => {
    const agents = new Agents(bridgeOf());
    await agents.refresh();
    expect(await agents.update('thread', { name: 'hermes', description: '', soul: '', access: '', model: '', effort: '' })).toBeNull();
    expect(agents.changeError).toContain('already');
    expect(agents.saving).toBeNull();
    expect(agents.list?.[0].name).toBe('pythia');
    agents.clearChangeError();
    expect(agents.changeError).toBe('');
  });

  it('makes one change at a time', async () => {
    let release: (agent: AgentInfo) => void = () => {};
    const agents = new Agents({
      Agents: async () => [],
      UpdateAgent: () => new Promise((resolve) => (release = resolve)),
      CreateAgent: refused,
      DeleteAgent: refused,
    });
    const first = agents.update('thread', { name: 'Delphi', description: '', soul: '', access: '', model: '', effort: '' });
    expect(await agents.create({ name: 'Researcher', description: '', soul: '', access: '', model: '', effort: '' })).toBeNull();
    expect(agents.changeError).toBe('');
    release({ id: 'thread', kind: 'thread', name: 'Delphi' });
    expect((await first)?.name).toBe('Delphi');
  });

  it('reports what cannot be read and keeps what was shown', async () => {
    let fail = false;
    const agents = new Agents({
      async Agents() {
        if (fail) throw new Error('the node does not answer');
        return [{ id: 'thread', kind: 'thread', name: 'pythia' }];
      },
      async UpdateAgent() {
        throw 'not now';
      },
      CreateAgent: refused,
      DeleteAgent: refused,
    });
    await agents.refresh();
    fail = true;
    await agents.refresh();
    expect(agents.error).toBe('the node does not answer');
    expect(agents.list?.[0].name).toBe('pythia');
    expect(await agents.update('thread', { name: 'Delphi', description: '', soul: '', access: '', model: '', effort: '' })).toBeNull();
    expect(agents.changeError).toBe('not now');
  });

  it('keeps a change over a read that was on its way', async () => {
    let release: (list: AgentInfo[]) => void = () => {};
    const agents = new Agents({
      Agents: () => new Promise((resolve) => (release = resolve)),
      UpdateAgent: async (id, change) => ({ id, kind: 'thread', ...change }),
      CreateAgent: async (change) => ({ id: 'agent-1', kind: 'custom', ...change }),
      DeleteAgent: refused,
    });
    const reading = agents.refresh();
    await agents.update('thread', { name: 'Delphi', description: '', soul: '', access: '', model: '', effort: '' });
    await agents.create({ name: 'Researcher', description: '', soul: '', access: '', model: '', effort: '' });
    release([{ id: 'thread', kind: 'thread', name: 'pythia' }]);
    await reading;
    expect(agents.list?.map((agent) => agent.name)).toEqual(['Delphi', 'Researcher']);
  });

  it('drops an answer that a newer read overtook', async () => {
    const answers: Array<(list: AgentInfo[]) => void> = [];
    const agents = new Agents({
      Agents: () => new Promise((resolve) => answers.push(resolve)),
      UpdateAgent: refused,
      CreateAgent: refused,
      DeleteAgent: refused,
    });
    const first = agents.refresh();
    const second = agents.refresh();
    answers[1]([{ id: 'thread', kind: 'thread', name: 'new' }]);
    await second;
    answers[0]([{ id: 'thread', kind: 'thread', name: 'old' }]);
    await first;
    expect(agents.list?.[0].name).toBe('new');
  });

  it('lists the models once, and again after it failed', async () => {
    let asked = 0;
    let fail = true;
    const agents = new Agents({
      Agents: async () => [],
      UpdateAgent: refused,
      CreateAgent: refused,
      DeleteAgent: refused,
      async AgentModels() {
        asked++;
        if (fail) throw new Error('not set up');
        return [{ access: 'openai-1', label: 'OpenAI', provider: 'openai', models: ['gpt-5'] }];
      },
    });
    await agents.listModels();
    expect(agents.modelsError).toBe('not set up');
    expect(agents.models).toBeNull();
    fail = false;
    await Promise.all([agents.listModels(), agents.listModels()]);
    await agents.listModels();
    expect(asked).toBe(2);
    expect(agents.modelsError).toBe('');
    expect(agents.models?.[0].models).toEqual(['gpt-5']);
    // A host from before agents had models lists none.
    const old = new Agents({ Agents: async () => [], UpdateAgent: refused, CreateAgent: refused, DeleteAgent: refused });
    await old.listModels();
    expect(old.models).toBeNull();
  });
});

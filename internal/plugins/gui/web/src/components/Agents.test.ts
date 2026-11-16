import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import AgentsView from './Agents.svelte';
import { Agents } from '../lib/agents.svelte';
import { setLocale } from '../lib/i18n';
import type { AgentBridge, AgentChange, AgentInfo } from '../lib/wire';

type Bridge = Pick<AgentBridge, 'Agents' | 'UpdateAgent' | 'CreateAgent' | 'DeleteAgent'>;

function bridgeOf(
  start: AgentInfo[] = [{ id: 'thread', kind: 'thread', name: 'pythia' }],
): Bridge & { changes: [string, AgentChange][]; removed: string[] } {
  let list = start;
  const changes: [string, AgentChange][] = [];
  const removed: string[] = [];
  return {
    changes,
    removed,
    async Agents() {
      return list;
    },
    async UpdateAgent(id: string, change: AgentChange) {
      changes.push([id, change]);
      if (change.name === 'hermes') throw new Error('there is a profile called "hermes" already');
      const agent: AgentInfo = { ...list.find((candidate) => candidate.id === id)!, ...change };
      list = list.map((candidate) => (candidate.id === id ? agent : candidate));
      return agent;
    },
    async CreateAgent(change: AgentChange) {
      changes.push(['', change]);
      const agent: AgentInfo = { id: 'agent-0a1b2c3d', kind: 'custom', ...change };
      list = [...list, agent];
      return agent;
    },
    async DeleteAgent(id: string) {
      removed.push(id);
      if (id === 'agent-busy') throw new Error('the agents cannot be written');
      list = list.filter((agent) => agent.id !== id);
    },
  };
}

async function shown(bridge = bridgeOf()) {
  const agents = new Agents(bridge);
  await agents.refresh();
  const renamed: AgentInfo[] = [];
  const view = render(AgentsView, { props: { agents, onrenamed: (agent: AgentInfo) => renamed.push(agent) } });
  return { ...view, agents, bridge, renamed };
}

function button(container: HTMLElement, text: string, within: ParentNode = container): HTMLButtonElement {
  const found = [...within.querySelectorAll('button')].find((candidate) => candidate.textContent?.trim() === text);
  if (!found) throw new Error(`no button ${text}`);
  return found as HTMLButtonElement;
}

function inputs(container: HTMLElement) {
  const fields = [...container.querySelectorAll('.agent-input')] as (HTMLInputElement | HTMLTextAreaElement)[];
  return { name: fields[0] as HTMLInputElement, description: fields[1] as HTMLInputElement, soul: fields[2] as HTMLTextAreaElement };
}

describe('Agents view', () => {
  beforeEach(() => {
    setLocale('en');
    localStorage.removeItem('ouro.agents.system');
  });

  it('lists the agent of the thread under the name of the profile', async () => {
    const { container } = await shown();
    expect(container.querySelector('.agent-name')?.textContent).toBe('pythia');
    expect(container.querySelector('.agent-kind')?.textContent).toBe('Thread agent');
    expect(container.querySelector('.agent-badge')?.textContent).toBe('P');
    expect(container.querySelector('.agent-about')?.textContent).toContain('thread');
    expect(container.querySelector('.agent-soul')).toBeNull();
    // The built-in agents are not removed.
    expect(() => button(container, 'Remove')).toThrow();
  });

  it('changes the name, what the agent is for and its soul, and tells who shows the name', async () => {
    const { container, bridge, renamed } = await shown();
    await fireEvent.click(button(container, 'Edit'));
    const { name, description, soul } = inputs(container);
    expect(name.value).toBe('pythia');
    expect(name.maxLength).toBe(64);
    expect(description.maxLength).toBe(300);
    expect(description.placeholder).toContain('thread');
    expect(soul.maxLength).toBe(8000);
    const save = container.querySelector('button[type="submit"]') as HTMLButtonElement;
    // The same agent, or one without a name, is nothing to save.
    expect(save.disabled).toBe(true);
    await fireEvent.input(name, { target: { value: '   ' } });
    expect(save.disabled).toBe(true);
    await fireEvent.input(name, { target: { value: 'pythia' } });
    await fireEvent.input(soul, { target: { value: '  Calm.\nDry humour. ' } });
    expect(save.disabled).toBe(false);
    await fireEvent.submit(container.querySelector('form') as HTMLFormElement);
    await waitFor(() => expect(container.querySelector('.agent-soul')?.textContent).toContain('Calm.\nDry humour.'));
    expect(bridge.changes).toEqual([['thread', { name: 'pythia', description: '', soul: 'Calm.\nDry humour.', access: '', model: '' }]]);
    // The name stayed: nobody is told of a new one.
    expect(renamed).toEqual([]);

    await fireEvent.click(button(container, 'Edit'));
    expect(inputs(container).soul.value).toBe('Calm.\nDry humour.');
    await fireEvent.input(inputs(container).name, { target: { value: '  Delphi ' } });
    await fireEvent.submit(container.querySelector('form') as HTMLFormElement);
    await waitFor(() => expect(container.querySelector('.agent-name')?.textContent).toBe('Delphi'));
    expect(renamed.map((agent) => agent.name)).toEqual(['Delphi']);
    expect(container.querySelector('form')).toBeNull();
  });

  it('shows why a change was refused and keeps the form open', async () => {
    const { container, renamed } = await shown();
    await fireEvent.click(button(container, 'Edit'));
    const { name } = inputs(container);
    await fireEvent.input(name, { target: { value: 'hermes' } });
    await fireEvent.submit(container.querySelector('form') as HTMLFormElement);
    await waitFor(() => expect(container.querySelector('.agent-error')?.textContent).toContain('already'));
    expect(renamed).toEqual([]);
    // Escape gives the change up: the name stays, and so does nothing of the error.
    await fireEvent.keyDown(name, { key: 'Escape' });
    expect(container.querySelector('form')).toBeNull();
    expect(container.querySelector('.agent-name')?.textContent).toBe('pythia');
    await fireEvent.click(button(container, 'Edit'));
    expect(container.querySelector('.agent-error')).toBeNull();
  });

  it('cancels with its button, and saves nothing unchanged', async () => {
    const { container, bridge } = await shown();
    await fireEvent.click(button(container, 'Edit'));
    await fireEvent.submit(container.querySelector('form') as HTMLFormElement);
    expect(container.querySelector('form')).toBeNull();
    await fireEvent.click(button(container, 'Edit'));
    await fireEvent.click(button(container, 'Cancel', container.querySelector('form')!));
    expect(container.querySelector('form')).toBeNull();
    expect(bridge.changes).toEqual([]);
  });

  it('makes an agent of the owner, and removes it after asking', async () => {
    const { container, bridge } = await shown();
    await fireEvent.click(button(container, 'New agent'));
    expect(button(container, 'New agent').disabled).toBe(true);
    const { name, description, soul } = inputs(container);
    expect(description.placeholder).toBe('');
    expect((container.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.input(name, { target: { value: ' Researcher ' } });
    await fireEvent.input(description, { target: { value: 'Digs into sources.' } });
    await fireEvent.input(soul, { target: { value: 'Thorough.' } });
    await fireEvent.click(button(container, 'Create'));
    await waitFor(() => expect(container.querySelectorAll('.agent')).toHaveLength(2));
    expect(bridge.changes).toEqual([['', { name: 'Researcher', description: 'Digs into sources.', soul: 'Thorough.', access: '', model: '' }]]);
    const made = container.querySelectorAll('.agent')[1] as HTMLElement;
    expect(made.querySelector('.agent-kind')?.textContent).toBe('Own agent');
    expect(made.querySelector('.agent-about')?.textContent).toBe('Digs into sources.');

    await fireEvent.click(button(container, 'Remove', made));
    expect(made.querySelector('.agent-confirm')?.textContent).toContain('run as the thread agent');
    await fireEvent.click(button(container, 'Cancel', made));
    expect(made.querySelector('.agent-confirm')).toBeNull();
    expect(bridge.removed).toEqual([]);
    await fireEvent.click(button(container, 'Remove', made));
    await fireEvent.click(button(container, 'Remove', made.querySelector('.agent-confirm')!));
    await waitFor(() => expect(container.querySelectorAll('.agent')).toHaveLength(1));
    expect(bridge.removed).toEqual(['agent-0a1b2c3d']);
  });

  it('says why an agent was not removed', async () => {
    const { container } = await shown(bridgeOf([{ id: 'agent-busy', kind: 'custom', name: 'Busy' }]));
    await fireEvent.click(button(container, 'Remove'));
    await fireEvent.click(button(container, 'Remove', container.querySelector('.agent-confirm')!));
    await waitFor(() => expect(container.querySelector('.agent-error')?.textContent).toBe('the agents cannot be written'));
    expect(container.querySelectorAll('.agent')).toHaveLength(1);
  });

  it('says that the agents are read, and what kept them from being read', async () => {
    const agents = new Agents({
      Agents: async () => {
        throw new Error('the node does not answer');
      },
      UpdateAgent: async () => {
        throw new Error('unused');
      },
      CreateAgent: async () => {
        throw new Error('unused');
      },
      DeleteAgent: async () => {
        throw new Error('unused');
      },
    });
    const { container } = render(AgentsView, { props: { agents } });
    expect(container.querySelector('.board-wait')?.textContent).toBe('Reading the agents …');
    await agents.refresh();
    await waitFor(() => expect(container.querySelector('.banner')?.textContent).toContain('the node does not answer'));
    expect(container.querySelector('.board-wait')).toBeNull();
  });

  it('shows the system agents with what they are for, fixed, and changes only their name and soul', async () => {
    const bridge = bridgeOf([
      { id: 'thread', kind: 'thread', name: 'pythia', description: 'The thread alone.' },
      { id: 'archivist', kind: 'archivist', name: 'Archivist', description: 'Does tasks.', reserved: true },
      { id: 'orchestrator', kind: 'orchestrator', name: 'Orchestrator', reserved: true },
    ]);
    const { container } = await shown(bridge);
    // The system agents are hidden until asked for.
    expect([...container.querySelectorAll('.agent-kind')].map((kind) => kind.textContent)).toEqual(['Thread agent']);
    await fireEvent.click(container.querySelector('.agents-system input') as HTMLInputElement);
    const items = [...container.querySelectorAll('.agent')] as HTMLElement[];
    expect(items.map((item) => item.querySelector('.agent-kind')?.textContent)).toEqual(['Thread agent', 'Archivist', 'Orchestrator']);
    expect(items.map((item) => !!item.querySelector('.agent-system'))).toEqual([false, true, true]);
    // The agent of the thread is for what the owner wrote, a system agent for what it is.
    expect(items[0].querySelector('.agent-about')?.textContent).toBe('The thread alone.');
    expect(items[1].querySelector('.agent-about')?.textContent).toBe('Tidies the memories once a day, in its job.');
    expect(items[2].querySelector('.agent-about')?.textContent).toContain('Looks at the board when it changed');

    await fireEvent.click(button(container, 'Edit', items[1]));
    const form = container.querySelector('.agent-form') as HTMLFormElement;
    // Only the agent of the thread is told that its name is that of the profile.
    expect(form.textContent).not.toContain('profile');
    expect(form.querySelectorAll('input.agent-input')).toHaveLength(1);
    expect(form.querySelector('.agent-fixed')?.textContent).toBe('Tidies the memories once a day, in its job.');
    expect(form.textContent).toContain('no other agent hands it a task');
    await fireEvent.input(form.querySelector('textarea') as HTMLTextAreaElement, { target: { value: 'Neat.' } });
    await fireEvent.submit(form);
    await waitFor(() => expect(container.querySelector('.agent-form')).toBeNull());
    expect(bridge.changes).toEqual([['archivist', { name: 'Archivist', description: '', soul: 'Neat.', access: '', model: '' }]]);
  });

  it('gives an agent a model of an access, or that of the settings, and keeps one no access lists now', async () => {
    const bridge = Object.assign(
      bridgeOf([
        { id: 'thread', kind: 'thread', name: 'pythia' },
        { id: 'archivist', kind: 'archivist', name: 'Archivist', reserved: true, access: 'old-1', model: 'legacy' },
      ]),
      {
        async AgentModels() {
          return [
            { access: 'openai-1', label: 'OpenAI', provider: 'openai', models: ['gpt-5', 'gpt-5-mini'] },
            { access: 'claude-1', label: 'Claude Code', provider: 'claude-code', models: [], error: 'signed out' },
          ];
        },
      },
    );
    const { container } = await shown(bridge);
    await fireEvent.click(container.querySelector('.agents-system input') as HTMLInputElement);
    const [thread, archivist] = [...container.querySelectorAll('.agent')] as HTMLElement[];
    expect(archivist.querySelector('.agent-model')?.textContent).toContain('legacy · old-1');
    expect(thread.querySelector('.agent-model')).toBeNull();

    // The agent of the thread answers with the models of the settings.
    await fireEvent.click(button(container, 'Edit', thread));
    expect(container.querySelector('select')).toBeNull();
    expect(container.querySelector('.agent-form')?.textContent).toContain('models of the settings');
    await fireEvent.click(button(container, 'Cancel', container.querySelector('form')!));

    await fireEvent.click(button(container, 'Edit', archivist));
    const select = container.querySelector('select') as HTMLSelectElement;
    await waitFor(() => expect(select.querySelectorAll('optgroup')).toHaveLength(1));
    expect([...select.options].map((option) => option.textContent?.trim())).toEqual([
      'As in the settings',
      'legacy · old-1 (not offered now)',
      'gpt-5',
      'gpt-5-mini',
    ]);
    expect(select.querySelector('optgroup')?.getAttribute('label')).toBe('OpenAI');
    expect(select.value).toBe('old-1\nlegacy');
    expect(container.querySelector('.agent-form')?.textContent).toContain('Claude Code: signed out');
    await fireEvent.change(select, { target: { value: 'openai-1\ngpt-5-mini' } });
    await fireEvent.submit(container.querySelector('form') as HTMLFormElement);
    await waitFor(() => expect(container.querySelector('.agent-form')).toBeNull());
    expect(bridge.changes).toEqual([['archivist', { name: 'Archivist', description: '', soul: '', access: 'openai-1', model: 'gpt-5-mini' }]]);
    expect(container.querySelectorAll('.agent')[1].querySelector('.agent-model')?.textContent).toContain('gpt-5-mini · OpenAI');

    await fireEvent.click(button(container, 'Edit', container.querySelectorAll('.agent')[1] as HTMLElement));
    await fireEvent.change(container.querySelector('select') as HTMLSelectElement, { target: { value: '' } });
    await fireEvent.submit(container.querySelector('form') as HTMLFormElement);
    await waitFor(() => expect(container.querySelector('.agent-form')).toBeNull());
    expect(bridge.changes[1]).toEqual(['archivist', { name: 'Archivist', description: '', soul: '', access: '', model: '' }]);
  });

  it('says why the models cannot be listed', async () => {
    const bridge = Object.assign(bridgeOf([{ id: 'agent-1', kind: 'custom', name: 'Researcher' }]), {
      AgentModels: async () => {
        throw new Error('the node is not set up');
      },
    });
    const { container } = await shown(bridge);
    await fireEvent.click(button(container, 'Edit'));
    await waitFor(() => expect(container.querySelector('.agent-form .agent-error')?.textContent).toBe('The models cannot be listed: the node is not set up'));
    expect([...(container.querySelector('select') as HTMLSelectElement).options].map((option) => option.value)).toEqual(['']);
  });

  it('hides the system agents until the owner shows them, and remembers it', async () => {
    const start: AgentInfo[] = [
      { id: 'thread', kind: 'thread', name: 'pythia' },
      { id: 'archivist', kind: 'archivist', name: 'Archivist', reserved: true },
      { id: 'agent-1', kind: 'custom', name: 'Researcher' },
    ];
    const first = await shown(bridgeOf(start));
    const names = (container: HTMLElement) => [...container.querySelectorAll('.agent-name')].map((name) => name.textContent);
    expect(names(first.container)).toEqual(['pythia', 'Researcher']);
    const toggle = first.container.querySelector('.agents-system input') as HTMLInputElement;
    expect(toggle.checked).toBe(false);
    expect(first.container.querySelector('.agents-system')?.textContent).toContain('Show system agents');
    await fireEvent.click(toggle);
    expect(names(first.container)).toEqual(['pythia', 'Archivist', 'Researcher']);
    // Editing a system agent ends when they are hidden again.
    await fireEvent.click(button(first.container, 'Edit', first.container.querySelectorAll('.agent')[1] as HTMLElement));
    expect(first.container.querySelector('.agent-form')).not.toBeNull();
    await fireEvent.click(toggle);
    expect(first.container.querySelector('.agent-form')).toBeNull();
    await fireEvent.click(toggle);
    first.unmount();

    const again = await shown(bridgeOf(start));
    expect(names(again.container)).toEqual(['pythia', 'Archivist', 'Researcher']);
    again.unmount();

    // Without system agents there is nothing to show.
    const none = await shown(bridgeOf([{ id: 'thread', kind: 'thread', name: 'pythia' }]));
    expect(none.container.querySelector('.agents-system')).toBeNull();
  });
});

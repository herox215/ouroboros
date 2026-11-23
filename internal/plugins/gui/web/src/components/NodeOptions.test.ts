import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import OptionsView from './Options.svelte';
import { Themes } from '../lib/themes.svelte';
import { NodeOptions } from '../lib/nodeoptions.svelte';
import { fakeOptions } from '../lib/fakeoptions';
import { setLocale } from '../lib/i18n';
import { Agents } from '../lib/agents.svelte';
import AgentsView from './Agents.svelte';
import type { AgentInfo } from '../lib/wire';

/** Agents of a profile: the agent of the thread with a model of its own, and
 * the archivist with an effort of its own. */
function agentsOf(list: AgentInfo[]): Agents {
  const refused = async (): Promise<never> => {
    throw new Error('unused');
  };
  return new Agents({ Agents: async () => list, UpdateAgent: refused, CreateAgent: refused, DeleteAgent: refused });
}

const OWN: AgentInfo[] = [
  { id: 'thread', kind: 'thread', name: 'pythia', access: 'a1', model: 'gpt-5-mini' },
  { id: 'archivist', kind: 'archivist', name: 'Archivist', reserved: true, effort: 'low' },
  { id: 'agent-1', kind: 'custom', name: 'Researcher', access: 'a2', model: 'opus' },
];

function shown(bridge = fakeOptions()) {
  const node = new NodeOptions(bridge);
  const themes = new Themes(null, document.documentElement);
  return { bridge, node, ...render(OptionsView, { props: { themes, node } }) };
}

const button = (container: Element, name: string) =>
  [...container.querySelectorAll('button')].find((candidate) => candidate.textContent?.trim() === name || candidate.getAttribute('aria-label') === name) as HTMLButtonElement;

const tab = (container: Element, name: string) =>
  [...container.querySelectorAll('[role="tab"]')].find((candidate) => candidate.textContent?.trim() === name) as HTMLButtonElement;

/** The field of what a sign-in asks, once it is there. */
const answerField = (container: Element) =>
  waitFor(() => {
    const field = container.querySelector('#login-answer');
    expect(field).not.toBeNull();
    return field as HTMLInputElement;
  });

describe('Options of the node', () => {
  beforeEach(() => {
    setLocale('en');
    localStorage.clear();
  });

  it('shows the models, the providers and the look in parts, and remembers the part', async () => {
    const { container } = shown();
    expect([...container.querySelectorAll('[role="tab"]')].map((candidate) => candidate.textContent?.trim())).toEqual([
      'Models',
      'Providers',
      'Appearance',
    ]);
    expect(tab(container, 'Models').getAttribute('aria-selected')).toBe('true');
    await waitFor(() => expect(container.querySelector('#scope-owner')).not.toBeNull());

    await fireEvent.click(tab(container, 'Appearance'));
    expect(container.querySelector('.theme-grid')).not.toBeNull();
    expect(localStorage.getItem('ouro.options.tab')).toBe('appearance');

    await fireEvent.keyDown(container.querySelector('[role="tablist"]') as HTMLElement, { key: 'ArrowRight' });
    expect(tab(container, 'Models').getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(tab(container, 'Models'));
    await fireEvent.keyDown(container.querySelector('[role="tablist"]') as HTMLElement, { key: 'ArrowLeft' });
    expect(tab(container, 'Appearance').getAttribute('aria-selected')).toBe('true');
  });

  it('shows the look alone for a host without the options of the node', () => {
    const { container } = render(OptionsView, { props: { themes: new Themes(null, document.documentElement) } });
    expect(container.querySelector('[role="tablist"]')).toBeNull();
    expect(container.querySelector('.theme-grid')).not.toBeNull();
  });

  it('says why the options cannot be read, and reads them again', async () => {
    const bridge = fakeOptions();
    bridge.fail.Providers = 'the node does not answer';
    const { container } = shown(bridge);
    await waitFor(() => expect(container.querySelector('.prefs-error')?.textContent).toContain('the node does not answer'));
    delete bridge.fail.Providers;
    await fireEvent.click(button(container, 'Try again'));
    await waitFor(() => expect(container.querySelector('#scope-owner')).not.toBeNull());
  });

  it('picks what each scope answers with, its effort and its fallbacks', async () => {
    const { container, node } = shown();
    await waitFor(() => expect(container.querySelectorAll('#scope-owner optgroup')).toHaveLength(2));
    const owner = container.querySelector('#scope-owner') as HTMLSelectElement;
    const sessions = container.querySelector('#scope-sessions') as HTMLSelectElement;
    const thread = container.querySelector('#scope-thread') as HTMLSelectElement;
    expect(owner.value).toBe('a2\nsonnet');
    expect(sessions.value).toBe('');
    expect(sessions.options[0].textContent).toBe('Like the default (sonnet · Work)');
    expect(thread.value).toBe('a1\ngpt-5');
    // The default always has a model: it follows nothing.
    expect([...owner.options].some((option) => option.value === '')).toBe(false);

    sessions.value = 'a2\nopus';
    await fireEvent.change(sessions);
    await waitFor(() => expect(node.llms?.scopes[1]).toMatchObject({ model: 'opus', own: true }));

    thread.value = '';
    await fireEvent.change(thread);
    await waitFor(() => expect(node.llms?.scopes[2].own).toBeFalsy());

    const effort = container.querySelector('#scope-effort') as HTMLSelectElement;
    expect(effort.options[0].textContent).toBe('As the model decides');
    effort.value = 'high';
    await fireEvent.change(effort);
    await waitFor(() => expect(node.llms?.effort).toBe('high'));
    expect(container.textContent).toContain('models.dev does not list');

    expect(container.textContent).toContain('None.');
    const add = container.querySelector('.fallback-add select') as HTMLSelectElement;
    // The default is no fallback of itself.
    expect([...add.options].map((option) => option.value)).not.toContain('a2\nsonnet');
    add.value = 'a1\ngpt-5-mini';
    await fireEvent.change(add);
    await fireEvent.click(button(container, 'Add'));
    await waitFor(() => expect(container.querySelectorAll('.fallback')).toHaveLength(1));
    expect(container.querySelector('.fallback')?.textContent).toContain('gpt-5-mini');
    await fireEvent.click(button(container, 'Remove: gpt-5-mini · OpenAI'));
    await waitFor(() => expect(container.querySelectorAll('.fallback')).toHaveLength(0));
  });

  it('shows what a scope had again when the node refuses the choice', async () => {
    const bridge = fakeOptions();
    const { container } = shown(bridge);
    await waitFor(() => expect(container.querySelectorAll('#scope-peers optgroup')).toHaveLength(2));
    bridge.fail.SetLLM = 'the access is gone';
    const peers = container.querySelector('#scope-peers') as HTMLSelectElement;
    peers.value = 'a1\ngpt-5';
    await fireEvent.change(peers);
    await waitFor(() => expect(container.querySelector('[role="alert"]')?.textContent).toBe('the access is gone'));
    expect(peers.value).toBe('');
  });

  it('adds a provider through what it asks, and uses its model', async () => {
    const { container, bridge, node } = shown();
    await fireEvent.click(tab(container, 'Providers'));
    await waitFor(() => expect(container.querySelectorAll('.access')).toHaveLength(2));
    expect(container.querySelector('.access .access-meta')?.textContent).toBe('OpenAI · api.openai.com');
    expect(container.querySelectorAll('.access')[1].querySelector('.use')?.textContent).toBe('Default');

    await fireEvent.click(button(container, 'Add provider'));
    await fireEvent.click([...container.querySelectorAll('.provider-pick')].find((pick) => pick.textContent?.includes('DeepSeek')) as HTMLButtonElement);
    expect(bridge.started).toEqual([{ provider: 'deepseek', again: '' }]);
    expect(container.querySelector('.login')?.textContent).toContain('One moment');

    bridge.push({ kind: 'ask', question: { title: 'API key', text: ['The key of your account.'], secret: true, hint: 'sk-…' } });
    const field = await answerField(container);
    expect(field.type).toBe('password');
    expect(field.placeholder).toBe('sk-…');
    await waitFor(() => expect(document.activeElement).toBe(field));
    await fireEvent.input(field, { target: { value: 'sk-1' } });
    await fireEvent.submit(container.querySelector('.login-form') as HTMLFormElement);
    expect(bridge.answers).toEqual(['sk-1']);

    bridge.push(
      { kind: 'open', text: 'https://example.test/device' },
      { kind: 'done', access: { id: 'a3', provider: 'deepseek', label: 'DeepSeek', about: 'second' }, models: ['chat', 'reasoner'], default: 'reasoner' },
    );
    await waitFor(() => expect(container.querySelector('.login-done')?.textContent).toContain('DeepSeek'));
    expect((container.querySelector('.login-link') as HTMLAnchorElement).href).toBe('https://example.test/device');
    await waitFor(() => expect(container.querySelectorAll('.access')).toHaveLength(3));
    const use = container.querySelector('#login-use') as HTMLSelectElement;
    expect(use.value).toBe('reasoner');
    await fireEvent.click(button(container, 'Use'));
    await waitFor(() => expect(container.textContent).toContain('from the next message on'));
    expect(node.llms?.scopes[0]).toMatchObject({ access: 'a3', model: 'reasoner' });

    await fireEvent.click(button(container, 'Close'));
    expect(container.querySelector('.login')).toBeNull();
  });

  it('keeps a secret the provider has, signs in again, and tries again after a failure', async () => {
    const { container, bridge } = shown();
    await fireEvent.click(tab(container, 'Providers'));
    await waitFor(() => expect(container.querySelectorAll('.access')).toHaveLength(2));
    await fireEvent.click(button(container, 'Sign in again: Work'));
    expect(bridge.started).toEqual([{ provider: 'claude-code', again: 'a2' }]);
    expect(container.querySelector('.login .prefs-sub')?.textContent).toBe('Sign in again to Work');
    bridge.push({ kind: 'ask', question: { title: 'Key', secret: true, known: true } });
    const field = await answerField(container);
    expect(field.placeholder).toContain('empty to keep');
    await fireEvent.keyDown(field, { key: 'Escape' });
    expect(container.querySelector('.login')).toBeNull();
    expect(bridge.canceled).toEqual(['login-1']);

    await fireEvent.click(button(container, 'Add provider'));
    await fireEvent.click(container.querySelector('.provider-pick') as HTMLButtonElement);
    bridge.push({ kind: 'failed', text: 'API key rejected' });
    await waitFor(() => expect(container.querySelector('.login [role="alert"]')?.textContent).toBe('API key rejected'));
    await fireEvent.click(button(container, 'Try again'));
    expect(bridge.started.at(-1)).toEqual({ provider: 'openai', again: '' });
  });

  it('renames and removes an access, after asking', async () => {
    const { container, bridge } = shown();
    await fireEvent.click(tab(container, 'Providers'));
    await waitFor(() => expect(container.querySelectorAll('.access')).toHaveLength(2));

    await fireEvent.click(button(container, 'Rename: OpenAI'));
    const name = container.querySelector('#access-name-a1') as HTMLInputElement;
    expect(document.activeElement).toBe(name);
    await fireEvent.input(name, { target: { value: 'Private' } });
    await fireEvent.submit(name.form as HTMLFormElement);
    await waitFor(() => expect(container.querySelector('.access-label')?.textContent).toBe('Private'));
    expect(container.querySelector('#access-name-a1')).toBeNull();

    await fireEvent.click(button(container, 'Rename: Private'));
    bridge.fail.RenameAccess = 'the name is too long';
    await fireEvent.submit((container.querySelector('#access-name-a1') as HTMLInputElement).form as HTMLFormElement);
    await waitFor(() => expect(container.querySelector('.access-form [role="alert"]')?.textContent).toBe('the name is too long'));
    await fireEvent.keyDown(container.querySelector('#access-name-a1') as HTMLInputElement, { key: 'Escape' });
    expect(container.querySelector('#access-name-a1')).toBeNull();

    await fireEvent.click(button(container, 'Remove: Work'));
    expect(container.querySelector('[role="alertdialog"]')?.textContent).toContain('signed out at its provider');
    await fireEvent.click(button(container, 'Cancel'));
    expect(container.querySelector('[role="alertdialog"]')).toBeNull();
    await fireEvent.click(button(container, 'Remove: Work'));
    await fireEvent.click(container.querySelector('[role="alertdialog"] .danger') as HTMLButtonElement);
    await waitFor(() => expect(container.querySelectorAll('.access')).toHaveLength(1));
    expect(container.textContent).toContain('the agents answer with gpt-5 of OpenAI now.');
  });

  it('names the agents that go before the defaults, and opens them', async () => {
    const agents = agentsOf(OWN);
    await agents.refresh();
    const node = new NodeOptions(fakeOptions());
    const opened: string[] = [];
    const { container } = render(OptionsView, {
      props: { themes: new Themes(null, document.documentElement), node, agents, onagents: () => opened.push('agents') },
    });
    await waitFor(() => expect(container.querySelector('#scope-owner')).not.toBeNull());
    const row = (id: string) => container.querySelector(`#scope-${id}`)!.closest('.scope') as HTMLElement;
    const names = (id: string) => [...row(id).querySelectorAll('.override')].map((chip) => chip.textContent);
    expect(container.querySelector('.prefs-title')?.textContent).toBe('Defaults');
    // The agent of the thread goes before the default of the thread, and of
    // the sessions, where it works too.
    expect(names('thread')).toEqual(['pythia → gpt-5-mini · OpenAI']);
    expect(row('thread').classList.contains('overridden')).toBe(true);
    expect(row('thread').textContent).toContain('does not hold now');
    expect(names('sessions')).toEqual(['pythia → gpt-5-mini · OpenAI', 'Researcher → opus · Work']);
    expect(names('owner')).toEqual([]);
    expect(names('peers')).toEqual([]);
    expect(names('effort')).toEqual(['Archivist → low']);
    await fireEvent.click(button(container, 'Open Agents'));
    expect(opened).toEqual(['agents']);
  });

  it('tells an agent what it answers with while it has nothing of its own', async () => {
    const agents = agentsOf([{ id: 'thread', kind: 'thread', name: 'pythia' }, { id: 'agent-1', kind: 'custom', name: 'Researcher' }]);
    await agents.refresh();
    const node = new NodeOptions(fakeOptions());
    await node.refresh();
    const { container } = render(AgentsView, { props: { agents, node } });
    await fireEvent.click(button(container, 'Edit: pythia'));
    const defaults = () => [...container.querySelectorAll('.agent-default')].map((line) => line.textContent);
    await waitFor(() => expect(defaults()).toEqual(['Default – Thread: gpt-5 · OpenAI, Sessions: sonnet · Work', 'Default: As the model decides']));
  });
});

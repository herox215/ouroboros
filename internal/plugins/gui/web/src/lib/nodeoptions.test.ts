import { describe, expect, it, vi } from 'vitest';
import { NodeOptions } from './nodeoptions.svelte';
import { fakeOptions } from './fakeoptions';

const settled = () => new Promise((resolve) => setTimeout(resolve, 0));

describe('NodeOptions', () => {
  it('reads the accesses and what the scopes answer with, and changes them', async () => {
    const node = new NodeOptions(fakeOptions());
    await node.refresh();
    expect(node.list?.accesses.map((a) => a.label)).toEqual(['OpenAI', 'Work']);
    expect(node.list?.accesses[1].uses).toEqual(['owner']);
    expect(node.label('a2')).toBe('Work');
    expect(node.label('gone')).toBe('gone');

    expect(await node.setLLM('sessions', 'a1', 'gpt-5-mini')).toBe(true);
    expect(node.llms?.scopes[1]).toEqual({ scope: 'sessions', access: 'a1', model: 'gpt-5-mini', own: true });
    // The accesses are read again: they tell what answers through them.
    expect(node.list?.accesses[0].uses).toEqual(['sessions', 'thread']);

    expect(await node.toggleFallback('a1', 'gpt-5')).toBe(true);
    expect(node.llms?.fallbacks).toEqual([{ access: 'a1', model: 'gpt-5' }]);
    expect(await node.setEffort('high')).toBe(true);
    expect(node.llms?.effort).toBe('high');
    expect(node.busy).toBeNull();
  });

  it('says why a change was refused, and runs one at a time', async () => {
    const bridge = fakeOptions();
    const node = new NodeOptions(bridge);
    await node.refresh();
    bridge.fail.SetLLM = 'the access offers no model "x"';
    expect(await node.setLLM('owner', 'a1', 'x')).toBe(false);
    expect(node.changeError).toBe('the access offers no model "x"');

    delete bridge.fail.SetLLM;
    const first = node.setEffort('low');
    expect(await node.setEffort('max')).toBe(false);
    expect(await first).toBe(true);
    expect(node.llms?.effort).toBe('low');
    expect(node.changeError).toBe('');
  });

  it('renames and removes an access, and has the models listed anew', async () => {
    const bridge = fakeOptions();
    const node = new NodeOptions(bridge);
    const changed = vi.fn();
    node.onaccesses = changed;
    await node.refresh();
    await node.listModels();
    expect(node.models).toHaveLength(2);

    expect(await node.rename('a1', '  Private ')).toBe(true);
    expect(node.list?.accesses[0].label).toBe('Private');
    expect(node.models).toBeNull();

    expect(await node.remove('a2')).toBe(true);
    expect(node.note).toContain('gpt-5');
    expect(node.list?.accesses.map((a) => a.id)).toEqual(['a1']);
    expect(changed).toHaveBeenCalledTimes(2);

    bridge.fail.RemoveAccess = 'the last access cannot be removed';
    expect(await node.remove('a1')).toBe(false);
    expect(node.changeError).toContain('last access');
    node.clear();
    expect(node.changeError).toBe('');
  });

  it('lists the models once, unless asked again, and says why it could not', async () => {
    const bridge = fakeOptions();
    const node = new NodeOptions(bridge);
    const asked = vi.spyOn(bridge, 'AgentModels');
    await Promise.all([node.listModels(), node.listModels()]);
    await node.listModels();
    expect(asked).toHaveBeenCalledTimes(1);
    bridge.fail.AgentModels = 'the node does not answer';
    await node.listModels(true);
    expect(node.modelsError).toBe('the node does not answer');
    expect(node.models).toHaveLength(2);
  });

  it('says why the options cannot be read', async () => {
    const bridge = fakeOptions();
    bridge.fail.Providers = 'this window cannot change the providers of the node';
    const node = new NodeOptions(bridge);
    await node.refresh();
    expect(node.error).toContain('cannot change');
    expect(node.list).toBeNull();
  });

  it('signs in step by step, and keeps the access that was saved', async () => {
    const bridge = fakeOptions();
    const node = new NodeOptions(bridge);
    const changed = vi.fn();
    node.onaccesses = changed;
    await node.refresh();

    await node.startLogin('deepseek');
    expect(bridge.started).toEqual([{ provider: 'deepseek', again: '' }]);
    expect(node.login).toMatchObject({ title: 'DeepSeek', provider: 'deepseek', again: '', status: 'working' });

    bridge.push({ kind: 'tell', text: 'Paste your key.' }, { kind: 'ask', question: { title: 'API key', secret: true } });
    await settled();
    expect(node.login?.status).toBe('asking');
    expect(node.login?.question?.title).toBe('API key');
    expect(node.login?.lines).toEqual([{ kind: 'tell', text: 'Paste your key.' }]);

    await node.answer('sk-bad');
    expect(node.login?.status).toBe('working');
    await node.answer('ignored'); // nothing is asked
    expect(bridge.answers).toEqual(['sk-bad']);

    bridge.push({ kind: 'reject', text: 'API key rejected' }, { kind: 'open', text: 'https://example.test' }, { kind: 'ask', question: { title: 'API key' } });
    await settled();
    expect(node.login?.lines.map((line) => line.kind)).toEqual(['tell', 'reject', 'open']);
    await node.answer('sk-good');

    bridge.push({
      kind: 'done',
      access: { id: 'a3', provider: 'deepseek', label: 'DeepSeek' },
      models: ['deepseek-chat'],
      default: 'deepseek-chat',
    });
    await settled();
    await settled();
    expect(node.login).toMatchObject({ status: 'done', access: { id: 'a3' }, models: ['deepseek-chat'], default: 'deepseek-chat' });
    expect(changed).toHaveBeenCalledTimes(1);
    expect(node.list?.accesses.map((a) => a.id)).toEqual(['a1', 'a2', 'a3']);

    await node.closeLogin();
    expect(node.login).toBeNull();
    expect(bridge.canceled).toEqual([]); // it was over already
  });

  it('gives up a sign-in, and tells why one failed', async () => {
    const bridge = fakeOptions();
    const node = new NodeOptions(bridge);
    await node.refresh();

    await node.startLogin('', 'a2');
    expect(node.login).toMatchObject({ title: 'Work', again: 'a2' });
    await node.closeLogin();
    expect(bridge.canceled).toEqual(['login-1']);

    // Another sign-in gives up the one on its way.
    await node.startLogin('openai');
    await node.startLogin('deepseek');
    expect(bridge.canceled).toEqual(['login-1', 'login-2']);
    bridge.push({ kind: 'failed', text: 'the sign-in was given up' });
    await settled();
    expect(node.login).toMatchObject({ status: 'failed', error: 'the sign-in was given up', provider: 'deepseek' });

    bridge.fail.StartLogin = 'too many sign-ins are on their way';
    await node.startLogin('openai');
    expect(node.login).toMatchObject({ status: 'failed', error: 'too many sign-ins are on their way' });
  });
});

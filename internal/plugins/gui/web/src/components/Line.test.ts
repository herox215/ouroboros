import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render } from '@testing-library/svelte';
import { tick } from 'svelte';
import Line from './Line.svelte';
import type { Entry } from '../lib/chat.svelte';
import { setLocale } from '../lib/i18n';

function mount(entry: Entry, over: { sending?: boolean } = {}) {
  const answers: string[] = [];
  const { container } = render(Line, {
    props: { entry, ...over, onanswer: (text: string) => answers.push(text) },
  });
  return { container, answers };
}

const line = (entry: Partial<Entry>): Entry => ({ id: 1, role: 'agent', text: '', ...entry });

describe('Line', () => {
  beforeEach(() => setLocale('en'));

  it('keeps what the owner wrote literal', () => {
    const { container } = mount(line({ role: 'owner', text: '**not bold**' }));
    const bubble = container.querySelector('.bubble') as HTMLElement;
    expect(bubble.textContent).toBe('**not bold**');
    expect(bubble.querySelector('strong')).toBeNull();
  });

  it('renders what the thread says as markdown', async () => {
    const { container } = mount(line({ text: '**bold** and `code`' }));
    await tick();
    expect(container.querySelector('.agent strong')?.textContent).toBe('bold');
    expect(container.querySelector('.agent code')?.textContent).toBe('code');
  });

  it('marks a call of a tool as done or failed', () => {
    const { container } = mount(line({ role: 'tool', text: 'bash · false', done: true, failed: true }));
    const tool = container.querySelector('.tool') as HTMLElement;
    expect(tool.classList.contains('failed')).toBe(true);
    expect(tool.classList.contains('done')).toBe(true);
    expect(tool.textContent).toContain('✗');
  });

  it('answers a question by choosing one of its options', async () => {
    const { container, answers } = mount(
      line({ role: 'question', text: 'Which one?', options: ['a', 'b'], recommended: 'b', open: true }),
    );
    const options = container.querySelectorAll('.options button');
    expect(options).toHaveLength(2);
    expect(options[1].classList.contains('recommended')).toBe(true);
    expect(options[1].textContent).toContain('recommended');
    await fireEvent.click(options[0]);
    expect(answers).toEqual(['a']);
  });

  it('shows what a question was answered with', () => {
    const { container } = mount(line({ role: 'question', text: 'Which one?', open: false, answered: 'b' }));
    expect(container.querySelector('.answered')?.textContent).toContain('b');
    expect(container.querySelector('.options')).toBeNull();
  });

  it('shows a failure as an alert, and a note quietly', () => {
    const failed = mount(line({ role: 'error', text: 'provider gone' }));
    expect(failed.container.querySelector('[role="alert"]')?.textContent).toBe('provider gone');
    const note = mount(line({ role: 'note', text: 'stopped' }));
    expect(note.container.querySelector('.note')?.textContent).toBe('stopped');
  });
});

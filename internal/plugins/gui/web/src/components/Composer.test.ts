import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import Composer from './Composer.svelte';
import { setLocale } from '../lib/i18n';

interface Props {
  disabled?: boolean;
  busy?: boolean;
  sending?: boolean;
  question?: boolean;
  queued?: string[];
  onsend?: (text: string) => Promise<boolean>;
  onstop?: () => void;
}

function mount(over: Props = {}) {
  const sent: string[] = [];
  let stopped = 0;
  const props: Required<Props> = {
    disabled: false,
    busy: false,
    sending: false,
    question: false,
    queued: [],
    onsend: async (text) => {
      sent.push(text);
      return true;
    },
    onstop: () => {
      stopped++;
    },
    ...over,
  };
  const { container } = render(Composer, { props });
  const area = container.querySelector('textarea') as HTMLTextAreaElement;
  return { container, area, sent, stopped: () => stopped };
}

describe('Composer', () => {
  beforeEach(() => setLocale('en'));

  it('sends with Enter, clears the field and keeps it in focus', async () => {
    const { area, sent } = mount();
    await fireEvent.input(area, { target: { value: 'hello' } });
    await fireEvent.keyDown(area, { key: 'Enter' });
    await waitFor(() => expect(sent).toEqual(['hello']));
    expect(area.value).toBe('');
    expect(document.activeElement).toBe(area);
  });

  it('keeps the text when Shift+Enter starts a new line', async () => {
    const { area, sent } = mount();
    await fireEvent.input(area, { target: { value: 'two' } });
    await fireEvent.keyDown(area, { key: 'Enter', shiftKey: true });
    expect(sent).toEqual([]);
  });

  it('keeps what was written when sending failed', async () => {
    const { area, sent } = mount({
      onsend: async (text) => {
        sent.push(text);
        return false;
      },
    });
    await fireEvent.input(area, { target: { value: 'keep me' } });
    await fireEvent.keyDown(area, { key: 'Enter' });
    await waitFor(() => expect(sent).toEqual(['keep me']));
    expect(area.value).toBe('keep me');
  });

  it('does not send an empty field', async () => {
    const { area, sent } = mount();
    await fireEvent.input(area, { target: { value: '   ' } });
    await fireEvent.keyDown(area, { key: 'Enter' });
    expect(sent).toEqual([]);
  });

  it('offers to stop while an answer is on its way', async () => {
    const { container, stopped } = mount({ busy: true });
    const stop = container.querySelector('button.stop') as HTMLButtonElement;
    expect(stop.getAttribute('aria-label')).toBe('Stop');
    await fireEvent.click(stop);
    expect(stopped()).toBe(1);
  });

  it('asks for an answer while a question waits', () => {
    const { area } = mount({ question: true });
    expect(area.placeholder).toBe('Answer the thread');
  });

  it('shows what waits to be taken, and is disabled when the window is not ready', () => {
    const { container, area } = mount({ queued: ['later'], disabled: true });
    expect(container.querySelector('.queued')?.textContent).toContain('later');
    expect(area.disabled).toBe(true);
  });
});

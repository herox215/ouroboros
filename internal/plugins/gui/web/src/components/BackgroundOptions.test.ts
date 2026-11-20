import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render } from '@testing-library/svelte';
import BackgroundOptions from './BackgroundOptions.svelte';
import OptionsView from './Options.svelte';
import { MAX_OPACITY, MIN_OPACITY, OPACITY_KEY, THEME_KEY, Themes } from '../lib/themes.svelte';
import { setLocale } from '../lib/i18n';

const page = () => document.documentElement;

function shown(store: Storage | null = null) {
  const themes = new Themes(store, page());
  return { themes, ...render(BackgroundOptions, { props: { themes } }) };
}

const slider = (container: Element) => container.querySelector('input[type="range"]') as HTMLInputElement;
const value = (container: Element) => container.querySelector('.opacity-value')?.textContent?.trim();
const button = (container: Element, name: string) =>
  [...container.querySelectorAll('button')].find((candidate) => candidate.textContent?.trim() === name) as HTMLButtonElement;

describe('Background options', () => {
  beforeEach(() => {
    setLocale('en');
    localStorage.clear();
  });

  it('show the ground of the window opaque, and let it be set see-through', async () => {
    const { container, themes } = shown();
    themes.select('nord');
    expect(slider(container).value).toBe(String(MAX_OPACITY));
    expect(slider(container).min).toBe(String(MIN_OPACITY));
    expect(slider(container).max).toBe(String(MAX_OPACITY));
    expect(value(container)).toBe('100 %');
    expect(page().style.getPropertyValue('--bg')).toBe('#2e3440');
    expect(button(container, 'Opaque').disabled).toBe(true);

    await fireEvent.input(slider(container), { target: { value: '50' } });
    expect(themes.opacity).toBe(50);
    expect(value(container)).toBe('50 %');
    // The window changes at once: no new window, no restart.
    expect(page().style.getPropertyValue('--bg')).toBe('rgb(46 52 64 / 50%)');
    expect(page().style.getPropertyValue('--surface')).toBe('#3b4252');
    expect(button(container, 'Opaque').disabled).toBe(false);
  });

  it('keep what is set, for when the window is opened again', async () => {
    const { container, themes } = shown(localStorage);
    await fireEvent.input(slider(container), { target: { value: '35' } });
    expect(localStorage.getItem(OPACITY_KEY)).toBe('35');
    expect(new Themes(localStorage, page()).opacity).toBe(35);
    expect(page().style.getPropertyValue('--bg')).toBe('rgb(234 231 225 / 35%)');
    themes.resetOpacity();
  });

  it('go back to an opaque window with one click', async () => {
    const { container, themes } = shown({ getItem: () => '40', setItem: () => {}, removeItem: () => {}, clear: () => {}, key: () => null, length: 1 });
    expect(value(container)).toBe('40 %');
    expect(page().style.getPropertyValue('--bg')).toBe('rgb(234 231 225 / 40%)');
    await fireEvent.click(button(container, 'Opaque'));
    expect(themes.opacity).toBe(MAX_OPACITY);
    expect(value(container)).toBe('100 %');
    expect(page().style.getPropertyValue('--bg')).toBe('');
    expect(button(container, 'Opaque').disabled).toBe(true);
  });

  it('hold whatever theme the window wears, system included', async () => {
    const { container, themes } = shown();
    await fireEvent.input(slider(container), { target: { value: '60' } });
    // The system: only the ground is painted, in the olive of the desktop.
    expect(page().style.getPropertyValue('--bg')).toBe('rgb(234 231 225 / 60%)');
    themes.select('dracula');
    expect(page().style.getPropertyValue('--bg')).toBe('rgb(33 34 44 / 60%)');
    expect(value(container)).toBe('60 %');
  });

  it('sit in the appearance of the options, under the theme', () => {
    const themes = new Themes(null, page());
    const { container } = render(OptionsView, { props: { themes } });
    expect(container.textContent).toContain('Window background');
    expect(slider(container)).not.toBeNull();
    expect(value(container)).toBe('100 %');
  });

  it('leave the ground of the app.css when nothing else is kept', () => {
    const themes = new Themes(localStorage, page());
    expect(themes.opacity).toBe(MAX_OPACITY);
    expect(localStorage.getItem(OPACITY_KEY)).toBe(null);
    expect(page().style.getPropertyValue('--bg')).toBe('');
    themes.select('nord');
    expect(localStorage.getItem(THEME_KEY)).toBe('nord');
  });
});

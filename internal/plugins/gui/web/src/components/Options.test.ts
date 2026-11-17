import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render } from '@testing-library/svelte';
import OptionsView from './Options.svelte';
import { SYSTEM, THEMES_KEY, Themes } from '../lib/themes.svelte';
import { setLocale } from '../lib/i18n';

const page = () => document.documentElement;

function shown(themes = new Themes(null, page())) {
  return { themes, ...render(OptionsView, { props: { themes } }) };
}

const button = (container: Element, name: string) =>
  [...container.querySelectorAll('button')].find((candidate) => candidate.textContent?.trim() === name || candidate.getAttribute('aria-label') === name) as HTMLButtonElement;

const pick = (container: Element, name: string) =>
  [...container.querySelectorAll('.theme-pick')].find((candidate) => candidate.querySelector('.theme-name')?.textContent === name) as HTMLButtonElement;

describe('Options view', () => {
  beforeEach(() => {
    setLocale('en');
    localStorage.clear();
  });

  it('lists the themes of ouro beside the system, and wears the one chosen', async () => {
    const { container, themes } = shown();
    expect(pick(container, 'System').getAttribute('aria-pressed')).toBe('true');
    expect(container.querySelectorAll('.theme-grid')).toHaveLength(1);
    expect(container.textContent).toContain('None yet.');

    await fireEvent.click(pick(container, 'Dracula'));
    expect(themes.selected).toBe('dracula');
    expect(pick(container, 'Dracula').getAttribute('aria-pressed')).toBe('true');
    expect(pick(container, 'System').getAttribute('aria-pressed')).toBe('false');
    expect(page().style.getPropertyValue('--bg')).toBe('#21222c');
    // A theme of ouro is copied, never changed or removed.
    expect(button(container, 'Copy: Dracula')).toBeDefined();
    expect(button(container, 'Edit: Dracula')).toBeUndefined();
    await fireEvent.click(pick(container, 'System'));
    expect(page().style.getPropertyValue('--bg')).toBe('');
  });

  it('makes a theme of a copy, shows it while it is edited, and keeps it', async () => {
    const { container, themes } = shown();
    await fireEvent.click(button(container, 'Copy: Nord'));
    const name = container.querySelector('#theme-name') as HTMLInputElement;
    expect(name.value).toBe('Nord (copy)');
    expect(document.activeElement).toBe(name);
    // Choosing is held while a theme is edited.
    expect(pick(container, 'Dracula').disabled).toBe(true);

    const accent = container.querySelector('#theme-color-accent') as HTMLInputElement;
    await fireEvent.input(accent, { target: { value: '#f0a' } });
    expect(page().style.getPropertyValue('--accent')).toBe('#ff00aa');
    expect(themes.selected).toBe(SYSTEM);

    await fireEvent.input(accent, { target: { value: 'pink' } });
    expect(container.querySelector('.theme-color-error')?.textContent).toContain('not a color');
    const save = button(container, 'Save and use');
    expect(save.disabled).toBe(true);
    await fireEvent.blur(accent);
    expect(accent.value).toBe('#ff00aa');
    expect(save.disabled).toBe(false);

    const dark = container.querySelector('.prefs-check input') as HTMLInputElement;
    await fireEvent.click(dark);
    expect(page().style.getPropertyValue('color-scheme')).toBe('light');

    await fireEvent.input(name, { target: { value: '  Pink Nord ' } });
    await fireEvent.submit(container.querySelector('form') as HTMLFormElement);
    expect(container.querySelector('form')).toBeNull();
    expect(themes.custom).toHaveLength(1);
    expect(themes.custom[0]).toMatchObject({ name: 'Pink Nord', dark: false });
    expect(themes.custom[0].colors.accent).toBe('#ff00aa');
    expect(themes.selected).toBe(themes.custom[0].id);
    expect(pick(container, 'Pink Nord').getAttribute('aria-pressed')).toBe('true');
    expect(container.querySelectorAll('.theme-grid')).toHaveLength(2);
  });

  it('takes the preview back when the editor is left, by Escape or by leaving the view', async () => {
    const { container, themes, unmount } = shown();
    themes.select('nord');
    await fireEvent.click(button(container, 'New theme'));
    expect((container.querySelector('#theme-name') as HTMLInputElement).value).toBe('');
    expect(button(container, 'Save and use').disabled).toBe(true);
    await fireEvent.input(container.querySelector('#theme-color-bg') as HTMLInputElement, { target: { value: '#000000' } });
    expect(page().style.getPropertyValue('--bg')).toBe('#000000');
    await fireEvent.keyDown(container.querySelector('#theme-name') as HTMLInputElement, { key: 'Escape' });
    expect(container.querySelector('form')).toBeNull();
    expect(page().style.getPropertyValue('--bg')).toBe('#2e3440');

    await fireEvent.click(button(container, 'New theme'));
    await fireEvent.input(container.querySelector('#theme-color-bg') as HTMLInputElement, { target: { value: '#000000' } });
    unmount();
    expect(page().style.getPropertyValue('--bg')).toBe('#2e3440');
    themes.select(SYSTEM);
  });

  it('edits and removes a theme of the owner, after asking', async () => {
    const themes = new Themes(localStorage, page());
    const kept = themes.save({ ...themes.find('nord')!, id: '', name: 'Mine' });
    const { container } = shown(themes);

    await fireEvent.click(button(container, 'Edit: Mine'));
    expect(container.querySelector('form')?.getAttribute('aria-label')).toBe('Edit theme');
    await fireEvent.input(container.querySelector('#theme-name') as HTMLInputElement, { target: { value: 'Ours' } });
    await fireEvent.input(container.querySelector('.theme-color-pick') as HTMLInputElement, { target: { value: '#101010' } });
    await fireEvent.click(button(container, 'Save and use'));
    expect(themes.custom).toEqual([expect.objectContaining({ id: kept.id, name: 'Ours' })]);
    expect(themes.custom[0].colors.bg).toBe('#101010');

    await fireEvent.click(button(container, 'Remove: Ours'));
    expect(container.querySelector('[role="alertdialog"]')?.textContent).toContain('gone for good');
    await fireEvent.click(button(container, 'Cancel'));
    expect(themes.custom).toHaveLength(1);
    await fireEvent.click(button(container, 'Remove: Ours'));
    await fireEvent.click(button(container, 'Remove'));
    expect(themes.custom).toHaveLength(0);
    expect(themes.selected).toBe(SYSTEM);
    expect(localStorage.getItem(THEMES_KEY)).toBe('[]');
  });

  it('says why a theme is not kept', async () => {
    const themes = new Themes(null, page());
    themes.save = () => {
      throw new Error('no more than 64 themes are kept');
    };
    const { container } = shown(themes);
    await fireEvent.click(button(container, 'New theme'));
    await fireEvent.input(container.querySelector('#theme-name') as HTMLInputElement, { target: { value: 'One more' } });
    await fireEvent.click(button(container, 'Save and use'));
    expect(container.querySelector('.prefs-error')?.textContent).toContain('64');
    expect(container.querySelector('form')).not.toBeNull();
    await fireEvent.click(button(container, 'Cancel'));
  });
});

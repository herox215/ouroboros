import { describe, expect, it } from 'vitest';
import {
  BUILTIN,
  COLOR_NAMES,
  MAX_THEMES,
  OUROBOROS_DARK,
  OUROBOROS_LIGHT,
  SYSTEM,
  THEME_KEY,
  THEMES_KEY,
  Themes,
  mix,
  normalizeColor,
  paint,
  variables,
  type Theme,
} from './themes.svelte';

/** A storage of its own for each test. */
function memory(seed: Record<string, string> = {}): Storage & { data: Record<string, string> } {
  const data: Record<string, string> = { ...seed };
  return {
    data,
    get length() {
      return Object.keys(data).length;
    },
    clear: () => Object.keys(data).forEach((key) => delete data[key]),
    getItem: (key) => (key in data ? data[key] : null),
    key: (i) => Object.keys(data)[i] ?? null,
    removeItem: (key) => void delete data[key],
    setItem: (key, value) => void (data[key] = String(value)),
  };
}

const root = () => document.createElement('html');

const own = (name: string, accent = '#ff0000'): Theme => ({
  id: '',
  name,
  dark: true,
  colors: { ...OUROBOROS_DARK.colors, accent },
});

describe('themes', () => {
  it('come with well known schemes, each with ten colors and an ID of its own', () => {
    const ids = BUILTIN.map((theme) => theme.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids).not.toContain(SYSTEM);
    for (const name of ['Dracula', 'Nord', 'Gruvbox Dark', 'Solarized Light', 'Catppuccin Mocha', 'Tokyo Night']) {
      expect(BUILTIN.map((theme) => theme.name)).toContain(name);
    }
    for (const theme of BUILTIN) {
      expect(theme.builtin).toBe(true);
      for (const name of COLOR_NAMES) expect(normalizeColor(theme.colors[name])).toBe(theme.colors[name]);
    }
  });

  it('read a color with or without its #, short or long, and nothing else', () => {
    expect(normalizeColor('#ABCDEF')).toBe('#abcdef');
    expect(normalizeColor(' abc ')).toBe('#aabbcc');
    expect(normalizeColor('#12345')).toBeNull();
    expect(normalizeColor('red')).toBeNull();
    expect(normalizeColor('')).toBeNull();
  });

  it('mix the soft colors from the ten', () => {
    expect(mix('#ffffff', '#000000', 0.5)).toBe('#808080');
    expect(mix('#ff0000', '#0000ff', 1)).toBe('#ff0000');
    const set = variables(OUROBOROS_LIGHT);
    expect(set['--accent']).toBe('#53583b');
    expect(set['--accent-soft']).toBe('rgb(83 88 59 / 9%)');
    expect(set['--line-strong']).toBe('#c2c3b6');
    expect(set['--err-bg']).toBe(mix('#873a29', '#ffffff', 0.08));
    expect(variables(OUROBOROS_DARK)['--shadow']).toContain('rgb(0 0 0 / 30%)');
  });

  it('paint the page, and leave it to app.css again for none', () => {
    const page = root();
    paint(BUILTIN.find((theme) => theme.id === 'nord')!, page);
    expect(page.style.getPropertyValue('--bg')).toBe('#2e3440');
    expect(page.style.getPropertyValue('color-scheme')).toBe('dark');
    expect(page.dataset.theme).toBe('nord');
    paint(null, page);
    expect(page.style.getPropertyValue('--bg')).toBe('');
    expect(page.style.getPropertyValue('color-scheme')).toBe('');
    expect(page.dataset.theme).toBeUndefined();
    paint(null, null); // no page: nothing to do
  });

  it('start with the system, and with what the window kept', () => {
    expect(new Themes(memory(), root()).selected).toBe(SYSTEM);
    const page = root();
    const themes = new Themes(memory({ [THEME_KEY]: 'dracula' }), page);
    expect(themes.selected).toBe('dracula');
    expect(page.style.getPropertyValue('--accent')).toBe('#bd93f9');
    expect(new Themes(memory({ [THEME_KEY]: 'gone' }), root()).selected).toBe(SYSTEM);
  });

  it('wear the theme chosen and remember it', () => {
    const store = memory();
    const page = root();
    const themes = new Themes(store, page);
    themes.select('gruvbox-light');
    expect(store.data[THEME_KEY]).toBe('gruvbox-light');
    expect(page.style.getPropertyValue('--bg')).toBe('#f2e5bc');
    themes.select('nothing-of-that-name');
    expect(themes.selected).toBe(SYSTEM);
    expect(page.style.getPropertyValue('--bg')).toBe('');
  });

  it('keep a theme of the owner, wear it, and change it in place', () => {
    const store = memory();
    const page = root();
    const themes = new Themes(store, page);
    const kept = themes.save(own('  Mine  '));
    expect(kept.id).toMatch(/^custom-/);
    expect(kept.name).toBe('Mine');
    expect(themes.selected).toBe(kept.id);
    expect(page.style.getPropertyValue('--accent')).toBe('#ff0000');
    expect(JSON.parse(store.data[THEMES_KEY])).toEqual([kept]);

    const changed = themes.save({ ...kept, name: 'Mine, bluer', colors: { ...kept.colors, accent: '#00f' } });
    expect(changed.id).toBe(kept.id);
    expect(themes.custom).toHaveLength(1);
    expect(page.style.getPropertyValue('--accent')).toBe('#0000ff');

    const again = new Themes(store, root());
    expect(again.custom.map((theme) => theme.name)).toEqual(['Mine, bluer']);
    expect(again.selected).toBe(kept.id);
  });

  it('keep a changed theme of ouro as a new one, and change nothing of it', () => {
    const themes = new Themes(memory(), root());
    const nord = BUILTIN.find((theme) => theme.id === 'nord')!;
    const kept = themes.save({ ...nord, colors: { ...nord.colors, accent: '#123456' } });
    expect(kept.id).not.toBe('nord');
    expect(kept.builtin).toBeUndefined();
    expect(themes.find('nord')?.colors.accent).toBe('#88c0d0');
  });

  it('refuse a theme with a color that is none, and more themes than are kept', () => {
    const themes = new Themes(memory(), root());
    expect(() => themes.save({ ...own('Bad'), colors: { ...own('Bad').colors, text: 'nope' } })).toThrow();
    for (let i = 0; i < MAX_THEMES; i++) themes.save(own(`T${i}`));
    expect(() => themes.save(own('one more'))).toThrow(String(MAX_THEMES));
  });

  it('show a theme for a while, and go back to the one worn', () => {
    const page = root();
    const themes = new Themes(memory({ [THEME_KEY]: 'nord' }), page);
    const draft = own('Draft', '#00ff00');
    themes.show(draft);
    draft.colors.accent = '#000000'; // the preview is a copy
    expect(themes.shown?.colors.accent).toBe('#00ff00');
    expect(page.style.getPropertyValue('--accent')).toBe('#00ff00');
    expect(themes.current().name).toBe('Draft');
    themes.show(null);
    expect(page.style.getPropertyValue('--accent')).toBe('#88c0d0');
  });

  it('start a new theme from what the desktop shows where none is worn', () => {
    const themes = new Themes(memory(), root());
    expect([OUROBOROS_LIGHT.id, OUROBOROS_DARK.id]).toContain(themes.current().id);
  });

  it('forget a theme of the owner, and wear the system for the one worn', () => {
    const store = memory();
    const page = root();
    const themes = new Themes(store, page);
    const first = themes.save(own('First'));
    const second = themes.save(own('Second', '#00ff00'));
    themes.remove(first.id);
    expect(themes.selected).toBe(second.id);
    expect(themes.custom.map((theme) => theme.id)).toEqual([second.id]);
    themes.show(second);
    themes.remove(second.id);
    expect(themes.selected).toBe(SYSTEM);
    expect(themes.preview).toBeNull();
    expect(page.style.getPropertyValue('--accent')).toBe('');
    expect(JSON.parse(store.data[THEMES_KEY])).toEqual([]);
    themes.remove('nord'); // a theme of ouro stays
    expect(themes.find('nord')).not.toBeNull();
  });

  it('pass over what was kept and is no theme', () => {
    const good = { id: 'custom-1', name: 'Good', dark: false, colors: { ...OUROBOROS_LIGHT.colors } };
    const kept = JSON.stringify([
      good,
      { ...good, id: 'custom-2', colors: { ...good.colors, bg: 'x' } },
      { ...good, id: 'nord' },
      { ...good, name: 'Twice' },
      { ...good, id: 'custom-3', name: '   ' },
      { ...good, id: 'custom-4', colors: null },
      null,
      'theme',
    ]);
    const themes = new Themes(memory({ [THEMES_KEY]: kept, [THEME_KEY]: 'custom-2' }), root());
    expect(themes.custom.map((theme) => `${theme.id}:${theme.name}`)).toEqual(['custom-1:Good', 'custom-3:custom-3']);
    expect(themes.selected).toBe(SYSTEM);
    expect(new Themes(memory({ [THEMES_KEY]: '{not json' }), root()).custom).toEqual([]);
    expect(new Themes(memory({ [THEMES_KEY]: '{}' }), root()).custom).toEqual([]);
  });

  it('go on without a storage, or with one that refuses', () => {
    const themes = new Themes(null, root());
    themes.select('dracula');
    expect(themes.selected).toBe('dracula');
    const refusing = memory();
    refusing.getItem = () => {
      throw new Error('denied');
    };
    refusing.setItem = () => {
      throw new Error('full');
    };
    const other = new Themes(refusing, root());
    expect(other.save(own('Kept here')).name).toBe('Kept here');
  });
});

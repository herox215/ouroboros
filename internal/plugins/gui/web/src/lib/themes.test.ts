import { describe, expect, it } from 'vitest';
import {
  BUILTIN,
  COLOR_NAMES,
  MAX_OPACITY,
  MAX_THEMES,
  MIN_OPACITY,
  OPACITY_KEY,
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

  it('paint the ground of the window as see-through as it is set, and back', () => {
    const store = memory({ [THEME_KEY]: 'nord' });
    const page = root();
    const themes = new Themes(store, page);
    expect(themes.opacity).toBe(MAX_OPACITY);
    expect(page.style.getPropertyValue('--bg')).toBe('#2e3440');

    themes.setOpacity(50);
    expect(store.data[OPACITY_KEY]).toBe('50');
    expect(page.style.getPropertyValue('--bg')).toBe('rgb(46 52 64 / 50%)');
    // Only the ground: cards, fields and text stay opaque and readable.
    expect(page.style.getPropertyValue('--surface')).toBe('#3b4252');
    expect(page.style.getPropertyValue('--text')).toBe('#eceff4');

    themes.setOpacity(20);
    expect(page.style.getPropertyValue('--bg')).toBe('rgb(46 52 64 / 20%)');
    themes.resetOpacity();
    expect(themes.opacity).toBe(MAX_OPACITY);
    expect(store.data[OPACITY_KEY]).toBe('100');
    expect(page.style.getPropertyValue('--bg')).toBe('#2e3440'); // no alpha left
  });

  it('ask the window to paint its whole face anew after another ground, once a frame', () => {
    const globals = globalThis as { requestAnimationFrame?: unknown; cancelAnimationFrame?: unknown };
    const oldFrame = globals.requestAnimationFrame;
    const oldCancel = globals.cancelAnimationFrame;
    const frames: (() => void)[] = [];
    const asked: number[] = [];
    try {
      globals.requestAnimationFrame = (run: () => void) => frames.push(run);
      globals.cancelAnimationFrame = (id: number) => void (frames[id - 1] = () => {});
      const themes = new Themes(memory(), root(), () => asked.push(themes.opacity));
      // The first ground a new window paints is painted anew as well.
      expect(frames).toHaveLength(1);
      frames.shift()!();
      expect(asked).toEqual([MAX_OPACITY]);

      // A drag of the slider asks once a frame, not once for every step.
      themes.setOpacity(60);
      themes.setOpacity(50);
      themes.setOpacity(40);
      expect(frames).toHaveLength(1);
      frames.shift()!();
      expect(asked).toEqual([MAX_OPACITY, 40]);

      // The end of the drag asks at once, and drops the frame still waiting.
      themes.setOpacity(30);
      themes.repaint(true);
      expect(asked).toEqual([MAX_OPACITY, 40, 30]);
      frames.shift()!();
      expect(asked).toEqual([MAX_OPACITY, 40, 30]);

      // A window that counts no frames is asked at once.
      globals.requestAnimationFrame = undefined;
      themes.setOpacity(70);
      expect(asked).toEqual([MAX_OPACITY, 40, 30, 70]);
    } finally {
      globals.requestAnimationFrame = oldFrame;
      globals.cancelAnimationFrame = oldCancel;
    }
  });

  it('ask no window where there is none, and go on without one', () => {
    const themes = new Themes(memory(), root());
    expect(() => themes.setOpacity(45)).not.toThrow();
    expect(() => themes.repaint(true)).not.toThrow();
  });

  it('keep the opacity within the bounds, and opaque for one that is none', () => {
    const themes = new Themes(memory(), root());
    themes.setOpacity(5);
    expect(themes.opacity).toBe(MIN_OPACITY);
    themes.setOpacity(180);
    expect(themes.opacity).toBe(MAX_OPACITY);
    themes.setOpacity(63.4);
    expect(themes.opacity).toBe(63);
    themes.setOpacity(Number.NaN);
    expect(themes.opacity).toBe(MAX_OPACITY);
  });

  it('start as see-through as was kept, and opaque for what is none', () => {
    const page = root();
    const kept = new Themes(memory({ [THEME_KEY]: 'dracula', [OPACITY_KEY]: '40' }), page);
    expect(kept.opacity).toBe(40);
    expect(page.style.getPropertyValue('--bg')).toBe('rgb(33 34 44 / 40%)');
    for (const value of ['x', '', '0', '-20']) {
      expect(new Themes(memory({ [OPACITY_KEY]: value }), root()).opacity).toBe(MAX_OPACITY);
    }
    expect(new Themes(memory({ [OPACITY_KEY]: '75' }), root()).opacity).toBe(75);
  });

  it('paint the olive of the desktop through the system, and follow it', () => {
    const oldMedia = globalThis.window?.matchMedia;
    const listeners: (() => void)[] = [];
    const query = (dark: boolean) =>
      ({
        matches: dark,
        addEventListener: (_: string, listener: () => void) => listeners.push(listener),
        removeEventListener: () => {},
      }) as unknown as MediaQueryList;
    try {
      globalThis.window.matchMedia = (() => query(true)) as typeof window.matchMedia;
      const page = root();
      const themes = new Themes(memory(), page);
      expect(page.style.getPropertyValue('--bg')).toBe(''); // app.css says it
      themes.setOpacity(50);
      expect(page.style.getPropertyValue('--bg')).toBe('rgb(32 34 29 / 50%)'); // the dark olive
      expect(page.style.getPropertyValue('--text')).toBe(''); // the rest is app.css'

      // The desktop turns light: the olive it shows through is the light one.
      globalThis.window.matchMedia = (() => query(false)) as typeof window.matchMedia;
      listeners.forEach((listener) => listener());
      expect(page.style.getPropertyValue('--bg')).toBe('rgb(234 231 225 / 50%)');

      themes.resetOpacity();
      expect(page.style.getPropertyValue('--bg')).toBe('');
      // A window that wears a theme of its own does not follow the desktop.
      themes.select('nord');
      globalThis.window.matchMedia = (() => query(true)) as typeof window.matchMedia;
      listeners.forEach((listener) => listener());
      expect(page.style.getPropertyValue('--bg')).toBe('#2e3440');
    } finally {
      if (oldMedia) globalThis.window.matchMedia = oldMedia;
      else delete (globalThis.window as Partial<Window>).matchMedia;
    }
  });

  it('paint no alpha for the system at an opacity of 100', () => {
    const page = root();
    paint(null, page, 100);
    expect(page.style.getPropertyValue('--bg')).toBe('');
    expect(page.dataset.theme).toBeUndefined();
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

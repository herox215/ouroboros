// The themes of the window. A theme is ten colors and whether it is dark; the
// rest of the palette of app.css (the soft accent, the grounds of an error and
// a warning, the shadow) is mixed from them. The theme "system" sets nothing:
// app.css then has the olive of the terminal UI, light or dark as the desktop
// is. Which theme the window wears, and the themes the owner made, are kept in
// the storage of the window, as the sidebar is.

/** The colors a theme names, as #rrggbb. */
export interface ThemeColors {
  /** The ground of the window. */
  bg: string;
  /** What lies on it: cards, bubbles, panels. */
  surface: string;
  /** The ground of the fields one writes in. */
  field: string;
  text: string;
  /** Text that says less: hints, times, names. */
  muted: string;
  line: string;
  lineStrong: string;
  accent: string;
  err: string;
  warn: string;
}

export type ColorName = keyof ThemeColors;

/** The colors in the order the editor lists them. */
export const COLOR_NAMES: readonly ColorName[] = [
  'bg',
  'surface',
  'field',
  'text',
  'muted',
  'line',
  'lineStrong',
  'accent',
  'err',
  'warn',
];

export interface Theme {
  id: string;
  name: string;
  dark: boolean;
  colors: ThemeColors;
  /** Comes with ouro: it is copied, never changed. */
  builtin?: boolean;
}

/** The theme that sets nothing and follows the desktop. */
export const SYSTEM = 'system';

export const THEME_KEY = 'ouro.theme';
export const THEMES_KEY = 'ouro.themes';
/** How much of the ground of the window is painted: 100 keeps it opaque, less
 * lets the desktop show through it. */
export const OPACITY_KEY = 'ouro.opacity';

/** The bounds of a theme the owner makes, and of how many are kept. */
export const MAX_NAME = 48;
export const MAX_THEMES = 64;

/** The bounds of how see-through the ground of the window may be: an opaque
 * window, and one the desktop still shows enough of to be read and taken hold
 * of. */
export const MAX_OPACITY = 100;
export const MIN_OPACITY = 20;

function theme(id: string, name: string, dark: boolean, c: [string, string, string, string, string, string, string, string, string, string]): Theme {
  const [bg, surface, field, text, muted, line, lineStrong, accent, err, warn] = c;
  return { id, name, dark, builtin: true, colors: { bg, surface, field, text, muted, line, lineStrong, accent, err, warn } };
}

/** The olive of app.css, light and dark: what "system" shows, as themes of
 * their own for a window that is to keep one of them whatever the desktop. */
export const OUROBOROS_LIGHT = theme('ouroboros-light', 'Ouroboros Light', false, [
  '#eae7e1', '#ffffff', '#fdfcf9', '#4d503b', '#828371', '#cccec0', '#c2c3b6', '#53583b', '#873a29', '#8a6d1f',
]);
export const OUROBOROS_DARK = theme('ouroboros-dark', 'Ouroboros Dark', true, [
  '#20221d', '#2b2e26', '#272a23', '#e1e4d4', '#a5a897', '#505645', '#777f65', '#c1cc99', '#f0ad96', '#d8c98f',
]);

/** The themes that come with ouro: well known schemes of editors and
 * terminals, in their own colors.
 *                 bg         surface    field      text       muted      line       strong     accent     err        warn */
export const BUILTIN: readonly Theme[] = [
  OUROBOROS_LIGHT,
  OUROBOROS_DARK,
  theme('catppuccin-latte', 'Catppuccin Latte', false, [
    '#e6e9ef', '#eff1f5', '#eff1f5', '#4c4f69', '#6c6f85', '#ccd0da', '#bcc0cc', '#8839ef', '#d20f39', '#df8e1d',
  ]),
  theme('catppuccin-mocha', 'Catppuccin Mocha', true, [
    '#181825', '#1e1e2e', '#1e1e2e', '#cdd6f4', '#a6adc8', '#313244', '#45475a', '#cba6f7', '#f38ba8', '#f9e2af',
  ]),
  theme('dracula', 'Dracula', true, [
    '#21222c', '#282a36', '#282a36', '#f8f8f2', '#8b95c4', '#44475a', '#6272a4', '#bd93f9', '#ff5555', '#ffb86c',
  ]),
  theme('everforest-dark', 'Everforest Dark', true, [
    '#232a2e', '#2d353b', '#2d353b', '#d3c6aa', '#9da9a0', '#3d484d', '#4f585e', '#a7c080', '#e67e80', '#dbbc7f',
  ]),
  theme('github-light', 'GitHub Light', false, [
    '#f6f8fa', '#ffffff', '#ffffff', '#1f2328', '#59636e', '#d1d9e0', '#b7bfc8', '#0969da', '#d1242f', '#9a6700',
  ]),
  theme('github-dark', 'GitHub Dark', true, [
    '#010409', '#0d1117', '#0d1117', '#e6edf3', '#9198a1', '#30363d', '#484f58', '#4493f8', '#f85149', '#d29922',
  ]),
  theme('gruvbox-light', 'Gruvbox Light', false, [
    '#f2e5bc', '#fbf1c7', '#f9f5d7', '#3c3836', '#7c6f64', '#d5c4a1', '#bdae93', '#af3a03', '#9d0006', '#b57614',
  ]),
  theme('gruvbox-dark', 'Gruvbox Dark', true, [
    '#282828', '#32302f', '#32302f', '#ebdbb2', '#a89984', '#504945', '#665c54', '#fabd2f', '#fb4934', '#fe8019',
  ]),
  theme('monokai', 'Monokai', true, [
    '#1e1f1c', '#272822', '#272822', '#f8f8f2', '#a59f85', '#3e3d32', '#75715e', '#a6e22e', '#f92672', '#e6db74',
  ]),
  theme('nord', 'Nord', true, [
    '#2e3440', '#3b4252', '#3b4252', '#eceff4', '#a0a8b7', '#434c5e', '#4c566a', '#88c0d0', '#bf616a', '#ebcb8b',
  ]),
  theme('one-dark', 'One Dark', true, [
    '#21252b', '#282c34', '#282c34', '#abb2bf', '#7f848e', '#3e4451', '#4b5263', '#61afef', '#e06c75', '#e5c07b',
  ]),
  theme('rose-pine', 'Rosé Pine', true, [
    '#191724', '#1f1d2e', '#1f1d2e', '#e0def4', '#908caa', '#403d52', '#524f67', '#ebbcba', '#eb6f92', '#f6c177',
  ]),
  theme('solarized-light', 'Solarized Light', false, [
    '#eee8d5', '#fdf6e3', '#fdf6e3', '#586e75', '#839496', '#e0d9c4', '#d3cbb3', '#268bd2', '#dc322f', '#b58900',
  ]),
  theme('solarized-dark', 'Solarized Dark', true, [
    '#002b36', '#073642', '#073642', '#93a1a1', '#839496', '#1d4f5c', '#586e75', '#268bd2', '#dc322f', '#b58900',
  ]),
  theme('tokyo-night', 'Tokyo Night', true, [
    '#16161e', '#1a1b26', '#1a1b26', '#c0caf5', '#9aa5ce', '#292e42', '#3b4261', '#7aa2f7', '#f7768e', '#e0af68',
  ]),
];

const HEX = /^#?([0-9a-f]{3}|[0-9a-f]{6})$/i;

/** A color as #rrggbb, from #rgb or #rrggbb with or without the #; null for
 * anything else. */
export function normalizeColor(value: string): string | null {
  const match = HEX.exec(value.trim());
  if (!match) return null;
  let hex = match[1].toLowerCase();
  if (hex.length === 3) hex = [...hex].map((digit) => digit + digit).join('');
  return '#' + hex;
}

function rgb(hex: string): [number, number, number] {
  const value = parseInt(hex.slice(1), 16);
  return [(value >> 16) & 255, (value >> 8) & 255, value & 255];
}

/** color over ground, weight its share. */
export function mix(color: string, ground: string, weight: number): string {
  const a = rgb(color);
  const b = rgb(ground);
  return '#' + a.map((channel, i) => Math.round(channel * weight + b[i] * (1 - weight)).toString(16).padStart(2, '0')).join('');
}

function alpha(color: string, share: number): string {
  return `rgb(${rgb(color).join(' ')} / ${Math.round(share * 100)}%)`;
}

/** An opacity as it is kept and shown: a whole number of percent within the
 * bounds, and opaque for anything that is none. */
export function clampOpacity(percent: number): number {
  const value = Math.round(Number.isFinite(percent) ? percent : MAX_OPACITY);
  return Math.min(MAX_OPACITY, Math.max(MIN_OPACITY, value));
}

/** The custom properties of app.css a theme sets, by their names. Below an
 * opacity of 100 the ground alone keeps alpha: cards, fields and text stay
 * opaque, so that the window stays readable. */
export function variables(theme: Theme, opacity: number = MAX_OPACITY): Record<string, string> {
  const c = theme.colors;
  const dark = theme.dark;
  const soft = dark ? 0.16 : 0.08;
  const edge = dark ? 0.45 : 0.32;
  return {
    '--bg': opacity >= MAX_OPACITY ? c.bg : alpha(c.bg, opacity / 100),
    '--surface': c.surface,
    '--field': c.field,
    '--text': c.text,
    '--muted': c.muted,
    '--line': c.line,
    '--line-strong': c.lineStrong,
    '--accent': c.accent,
    '--accent-soft': alpha(c.accent, dark ? 0.12 : 0.09),
    '--accent-line': alpha(c.accent, dark ? 0.32 : 0.28),
    '--err': c.err,
    '--err-bg': mix(c.err, c.surface, soft),
    '--err-line': mix(c.err, c.surface, edge),
    '--warn': c.warn,
    '--warn-bg': mix(c.warn, c.surface, soft),
    '--warn-line': mix(c.warn, c.surface, edge),
    '--shadow': dark
      ? '0 1px 2px rgb(0 0 0 / 30%), 0 14px 44px rgb(0 0 0 / 26%)'
      : `0 1px 2px ${alpha(c.text, 0.06)}, 0 10px 36px ${alpha(c.text, 0.06)}`,
  };
}

const VARIABLE_NAMES = Object.keys(variables(OUROBOROS_LIGHT));

/** Dresses the page in theme, or in what app.css says for null, and paints the
 * ground of the window as see-through as opacity says. */
export function paint(
  theme: Theme | null,
  root: HTMLElement | null = globalThis.document?.documentElement ?? null,
  opacity: number = MAX_OPACITY,
): void {
  if (!root) return;
  for (const name of VARIABLE_NAMES) root.style.removeProperty(name);
  root.style.removeProperty('color-scheme');
  if (!theme) {
    delete root.dataset.theme;
    // app.css says which olive the desktop shows, and only the ground of it
    // is painted here, as see-through as it is to be.
    if (opacity < MAX_OPACITY) {
      const olive = desktopDark() ? OUROBOROS_DARK : OUROBOROS_LIGHT;
      root.style.setProperty('--bg', alpha(olive.colors.bg, opacity / 100));
    }
    return;
  }
  for (const [name, value] of Object.entries(variables(theme, opacity))) root.style.setProperty(name, value);
  root.style.setProperty('color-scheme', theme.dark ? 'dark' : 'light');
  root.dataset.theme = theme.id;
}

/** Whether the desktop is dark, where the window can tell. */
export function desktopDark(): boolean {
  return !!globalThis.window?.matchMedia?.('(prefers-color-scheme: dark)').matches;
}

/** A theme the owner made, as it was kept; null for what is none. */
function parse(value: unknown): Theme | null {
  if (!value || typeof value !== 'object') return null;
  const raw = value as Record<string, unknown>;
  if (typeof raw.id !== 'string' || !raw.id || typeof raw.name !== 'string') return null;
  if (!raw.colors || typeof raw.colors !== 'object') return null;
  const given = raw.colors as Record<string, unknown>;
  const colors = {} as ThemeColors;
  for (const name of COLOR_NAMES) {
    const color = typeof given[name] === 'string' ? normalizeColor(given[name] as string) : null;
    if (!color) return null;
    colors[name] = color;
  }
  const name = raw.name.trim().slice(0, MAX_NAME);
  return { id: raw.id, name: name || raw.id, dark: raw.dark === true, colors };
}

function storage(): Storage | null {
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null; // a storage the page may not touch
  }
}

/** The themes of the window: those that come with ouro and those the owner
 * made, which one is worn, and the one shown for a while as it is edited. */
export class Themes {
  /** The themes the owner made, in the order they were made. */
  custom = $state<Theme[]>([]);
  /** The ID of the theme worn, SYSTEM for none. */
  selected = $state<string>(SYSTEM);
  /** A theme shown in place of the one worn, while it is being edited. */
  preview = $state<Theme | null>(null);
  /** How much of the ground of the window is painted, in percent. */
  opacity = $state<number>(MAX_OPACITY);

  readonly #store: Storage | null;
  readonly #root: HTMLElement | null;

  constructor(store: Storage | null = storage(), root: HTMLElement | null = globalThis.document?.documentElement ?? null) {
    this.#store = store;
    this.#root = root;
    this.#load();
    this.#paint();
    // Where the window follows the desktop, the ground it paints is the olive
    // of the desktop: it is painted anew when the desktop turns light or dark.
    globalThis.window?.matchMedia?.('(prefers-color-scheme: dark)')?.addEventListener?.('change', () => {
      if (this.shown === null && this.opacity < MAX_OPACITY) this.#paint();
    });
  }

  /** Every theme there is to choose, those of ouro first. */
  get all(): Theme[] {
    return [...BUILTIN, ...this.custom];
  }

  find(id: string): Theme | null {
    return this.all.find((candidate) => candidate.id === id) ?? null;
  }

  /** The theme the window shows now; null for "system". */
  get shown(): Theme | null {
    return this.preview ?? this.find(this.selected);
  }

  /** The colors the window shows now, also where it follows the desktop: what
   * a new theme starts from. */
  current(): Theme {
    return this.shown ?? (desktopDark() ? OUROBOROS_DARK : OUROBOROS_LIGHT);
  }

  select(id: string): void {
    this.selected = id === SYSTEM || this.find(id) ? id : SYSTEM;
    this.#write(THEME_KEY, this.selected);
    this.#paint();
  }

  /** Paints the ground of the window as see-through as percent says, at once
   * and in every theme, and remembers it: 100 is opaque with no alpha left. */
  setOpacity(percent: number): void {
    this.opacity = clampOpacity(percent);
    this.#write(OPACITY_KEY, String(this.opacity));
    this.#paint();
  }

  /** Paints the window opaque again, as it was before. */
  resetOpacity(): void {
    this.setOpacity(MAX_OPACITY);
  }

  /** Shows theme in place of the one worn, until null takes it back. */
  show(theme: Theme | null): void {
    this.preview = theme ? structuredCopy(theme) : null;
    this.#paint();
  }

  /** Keeps theme as one of the owner's and wears it: a theme of ouro, or one
   * without an ID, is kept as a new one. */
  save(theme: Theme): Theme {
    const parsed = parse({ ...theme, id: theme.id || 'new' });
    if (!parsed) throw new Error('a theme needs a name and ten colors');
    const known = this.custom.findIndex((candidate) => candidate.id === theme.id);
    let kept: Theme;
    if (known >= 0 && !theme.builtin) {
      kept = { ...parsed, id: theme.id };
      this.custom = this.custom.map((candidate, i) => (i === known ? kept : candidate));
    } else {
      if (this.custom.length >= MAX_THEMES) throw new Error(`no more than ${MAX_THEMES} themes are kept`);
      kept = { ...parsed, id: this.#newId() };
      this.custom = [...this.custom, kept];
    }
    this.#write(THEMES_KEY, JSON.stringify(this.custom));
    this.preview = null;
    this.select(kept.id);
    return kept;
  }

  /** Forgets a theme of the owner; one that was worn gives way to "system". */
  remove(id: string): void {
    if (!this.custom.some((candidate) => candidate.id === id)) return;
    this.custom = this.custom.filter((candidate) => candidate.id !== id);
    this.#write(THEMES_KEY, JSON.stringify(this.custom));
    if (this.preview?.id === id) this.preview = null;
    if (this.selected === id) this.select(SYSTEM);
    else this.#paint();
  }

  #newId(): string {
    for (let n = Date.now(); ; n++) {
      const id = 'custom-' + n.toString(36);
      if (!this.find(id)) return id;
    }
  }

  #load(): void {
    const kept = this.#read(THEMES_KEY);
    if (kept) {
      try {
        const list: unknown = JSON.parse(kept);
        if (Array.isArray(list)) {
          const seen = new Set(BUILTIN.map((builtin) => builtin.id));
          seen.add(SYSTEM);
          for (const value of list.slice(0, MAX_THEMES)) {
            const parsed = parse(value);
            if (parsed && !seen.has(parsed.id)) {
              seen.add(parsed.id);
              this.custom.push(parsed);
            }
          }
        }
      } catch {
        // What was kept is no list of themes: the window starts without them.
      }
    }
    const selected = this.#read(THEME_KEY);
    this.selected = selected && this.find(selected) ? selected : SYSTEM;
    // What is kept and is no opacity the window can paint leaves it opaque.
    const opacity = Number(this.#read(OPACITY_KEY));
    this.opacity = opacity > 0 ? clampOpacity(opacity) : MAX_OPACITY;
  }

  #paint(): void {
    paint(this.shown, this.#root, this.opacity);
  }

  #read(key: string): string | null {
    try {
      return this.#store?.getItem(key) ?? null;
    } catch {
      return null;
    }
  }

  #write(key: string, value: string): void {
    try {
      this.#store?.setItem(key, value);
    } catch {
      // Not remembered: the window still wears it until it is closed.
    }
  }
}

/** A copy of theme that shares nothing with it, also not a proxy of $state. */
export function structuredCopy(theme: Theme): Theme {
  const copy: Theme = { id: theme.id, name: theme.name, dark: theme.dark, colors: { ...theme.colors } };
  if (theme.builtin) copy.builtin = true;
  return copy;
}

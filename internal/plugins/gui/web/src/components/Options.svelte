<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import { t, type Key } from '../lib/i18n';
  import {
    COLOR_NAMES,
    MAX_NAME,
    OUROBOROS_DARK,
    OUROBOROS_LIGHT,
    SYSTEM,
    normalizeColor,
    structuredCopy,
    type ColorName,
    type Theme,
    type ThemeColors,
    type Themes,
  } from '../lib/themes.svelte';

  let { themes }: { themes: Themes } = $props();

  /** The theme being edited: a copy, '' as its ID while it is a new one. */
  let draft = $state<Theme | null>(null);
  /** What is typed into the field of a color that is no color (yet). */
  let typed = $state<Partial<Record<ColorName, string>>>({});
  let nameField = $state<HTMLInputElement | null>(null);
  let editorBox = $state<HTMLElement | null>(null);
  /** The theme the owner is asked whether to remove. */
  let removing = $state<string | null>(null);
  let saveError = $state('');

  const colorKey = (name: ColorName) => `themes.color.${name}` as Key;

  async function open(theme: Theme): Promise<void> {
    removing = null;
    saveError = '';
    typed = {};
    draft = theme;
    themes.show(theme);
    await tick();
    editorBox?.scrollIntoView?.({ block: 'nearest' });
    nameField?.focus();
  }

  /** A new theme of the owner's in the colors of from. */
  function fresh(from: Theme, name: string): Theme {
    const theme = structuredCopy(from);
    delete theme.builtin;
    return { ...theme, id: '', name: name.slice(0, MAX_NAME) };
  }

  function create(): void {
    void open(fresh(themes.current(), ''));
  }

  function copy(theme: Theme): void {
    void open(fresh(theme, `${theme.name} (${t('themes.copy.suffix')})`));
  }

  function edit(theme: Theme): void {
    void open(structuredCopy(theme));
  }

  function cancel(): void {
    draft = null;
    typed = {};
    saveError = '';
    themes.show(null);
  }

  /** The window shows each change at once. */
  function changed(): void {
    if (draft) themes.show(draft);
  }

  function setColor(name: ColorName, value: string): void {
    if (!draft) return;
    const color = normalizeColor(value);
    if (color) {
      draft.colors[name] = color;
      delete typed[name];
      changed();
    } else {
      typed[name] = value;
    }
  }

  function submit(event: SubmitEvent): void {
    event.preventDefault();
    if (!draft || !draft.name.trim()) return;
    try {
      themes.save({ ...structuredCopy(draft), name: draft.name.trim() });
      draft = null;
      typed = {};
      saveError = '';
    } catch (error) {
      saveError = error instanceof Error ? error.message : String(error);
    }
  }

  function keydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.preventDefault();
      cancel();
    }
  }

  function remove(theme: Theme): void {
    themes.remove(theme.id);
    removing = null;
  }

  // A theme that is being edited is shown only while its editor is.
  onDestroy(() => themes.show(null));

  const invalid = $derived(COLOR_NAMES.some((name) => typed[name] !== undefined));
  const worn = $derived(themes.selected);
</script>

{#snippet swatch(colors: ThemeColors)}
  <span class="swatch" style:background={colors.bg} style:border-color={colors.line} aria-hidden="true">
    <span class="swatch-card" style:background={colors.surface} style:border-color={colors.line}>
      <span class="swatch-text" style:background={colors.text}></span>
      <span class="swatch-text short" style:background={colors.muted}></span>
      <span class="swatch-dots">
        <span style:background={colors.accent}></span>
        <span style:background={colors.warn}></span>
        <span style:background={colors.err}></span>
      </span>
    </span>
  </span>
{/snippet}

{#snippet card(theme: Theme | null)}
  {@const id = theme?.id ?? SYSTEM}
  <li class="theme" class:worn={worn === id}>
    <button
      type="button"
      class="theme-pick"
      aria-pressed={worn === id}
      onclick={() => themes.select(id)}
      disabled={draft !== null}
    >
      {#if theme}
        {@render swatch(theme.colors)}
      {:else}
        <span class="swatch-split">
          {@render swatch(OUROBOROS_LIGHT.colors)}
          {@render swatch(OUROBOROS_DARK.colors)}
        </span>
      {/if}
      <span class="theme-name">{theme?.name ?? t('themes.system')}</span>
      <span class="theme-meta">
        {#if worn === id}
          <span class="theme-active">{t('themes.active')}</span>
        {/if}
        {theme ? (theme.dark ? t('themes.dark.tag') : t('themes.light.tag')) : t('themes.system.hint')}
      </span>
    </button>
    {#if theme && draft === null}
      {#if removing === theme.id}
        <div class="theme-confirm" role="alertdialog" aria-label="{t('themes.remove')}: {theme.name}">
          <p class="prefs-hint">{t('themes.remove.hint')}</p>
          <div class="prefs-row">
            <button type="button" class="prefs-button danger" onclick={() => remove(theme)}>{t('themes.remove')}</button>
            <button type="button" class="prefs-button" onclick={() => (removing = null)}>{t('themes.cancel')}</button>
          </div>
        </div>
      {:else}
        <div class="theme-actions">
          <button type="button" class="prefs-link" aria-label="{t('themes.copy')}: {theme.name}" onclick={() => copy(theme)}>
            {t('themes.copy')}
          </button>
          {#if !theme.builtin}
            <button type="button" class="prefs-link" aria-label="{t('themes.edit')}: {theme.name}" onclick={() => edit(theme)}>
              {t('themes.edit')}
            </button>
            <button type="button" class="prefs-link" aria-label="{t('themes.remove')}: {theme.name}" onclick={() => (removing = theme.id)}>
              {t('themes.remove')}
            </button>
          {/if}
        </div>
      {/if}
    {/if}
  </li>
{/snippet}

<section class="prefs" aria-label={t('options.label')}>
  <div class="prefs-page">
    <div class="prefs-head">
      <h2 class="prefs-title">{t('themes.title')}</h2>
      <button type="button" class="prefs-button primary" onclick={create} disabled={draft !== null}>{t('themes.new')}</button>
    </div>
    <p class="prefs-hint">{t('themes.hint')}</p>

    {#if draft}
      <form class="theme-editor" bind:this={editorBox} onsubmit={submit} aria-label={draft.id ? t('themes.edit.title') : t('themes.new.title')}>
        <h3 class="prefs-sub">{draft.id ? t('themes.edit.title') : t('themes.new.title')}</h3>
        <label class="prefs-label" for="theme-name">{t('themes.name')}</label>
        <input
          id="theme-name"
          class="prefs-input"
          type="text"
          autocomplete="off"
          spellcheck="false"
          maxlength={MAX_NAME}
          bind:value={draft.name}
          bind:this={nameField}
          onkeydown={keydown}
        />
        <label class="prefs-check">
          <input type="checkbox" bind:checked={draft.dark} onchange={changed} onkeydown={keydown} />
          {t('themes.dark')}
        </label>
        <p class="prefs-hint">{t('themes.dark.hint')}</p>

        <span class="prefs-label">{t('themes.colors')}</span>
        <div class="theme-colors">
          {#each COLOR_NAMES as name (name)}
            <div class="theme-color" class:bad={typed[name] !== undefined}>
              <input
                type="color"
                class="theme-color-pick"
                aria-label={t(colorKey(name))}
                value={draft.colors[name]}
                oninput={(event) => setColor(name, (event.currentTarget as HTMLInputElement).value)}
                onkeydown={keydown}
              />
              <label class="theme-color-name" for="theme-color-{name}">{t(colorKey(name))}</label>
              <input
                id="theme-color-{name}"
                class="prefs-input theme-color-hex"
                type="text"
                autocomplete="off"
                spellcheck="false"
                maxlength="7"
                aria-invalid={typed[name] !== undefined}
                value={typed[name] ?? draft.colors[name]}
                oninput={(event) => setColor(name, (event.currentTarget as HTMLInputElement).value)}
                onblur={() => delete typed[name]}
                onkeydown={keydown}
              />
              {#if typed[name] !== undefined}
                <span class="theme-color-error" role="alert">{t('themes.invalid')}</span>
              {/if}
            </div>
          {/each}
        </div>
        <p class="prefs-hint">{t('themes.preview')}</p>
        {#if saveError}
          <p class="prefs-error" role="alert">{saveError}</p>
        {/if}
        <div class="prefs-row">
          <button type="submit" class="prefs-button primary" disabled={!draft.name.trim() || invalid}>{t('themes.save')}</button>
          <button type="button" class="prefs-button" onclick={cancel}>{t('themes.cancel')}</button>
        </div>
      </form>
    {/if}

    <h3 class="prefs-sub">{t('themes.builtin')}</h3>
    <ul class="theme-grid">
      {@render card(null)}
      {#each themes.all.filter((theme) => theme.builtin) as theme (theme.id)}
        {@render card(theme)}
      {/each}
    </ul>

    <h3 class="prefs-sub">{t('themes.custom')}</h3>
    {#if themes.custom.length === 0}
      <p class="prefs-hint">{t('themes.custom.empty')}</p>
    {:else}
      <ul class="theme-grid">
        {#each themes.custom as theme (theme.id)}
          {@render card(theme)}
        {/each}
      </ul>
    {/if}
  </div>
</section>

<style>
  .prefs {
    flex: 1;
    min-height: 0;
    padding: 18px 0 12px;
    overflow-y: auto;
  }

  .prefs-page {
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-width: 960px;
  }

  .prefs-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .prefs-title {
    margin: 0;
    font-size: 1.1em;
    font-weight: 600;
  }

  .prefs-sub {
    margin: 14px 0 2px;
    font-family: var(--mono);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--muted);
  }

  .prefs-hint {
    margin: 0;
    font-size: 12.5px;
    color: var(--muted);
  }

  .prefs-error {
    margin: 0;
    font-size: 13px;
    color: var(--err);
  }

  .prefs-label {
    margin-top: 6px;
    font-family: var(--mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
  }

  .prefs-input {
    min-width: 0;
    padding: 6px 10px;
    font: inherit;
    color: var(--text);
    background-color: var(--field);
    border: 1px solid var(--line-strong);
    border-radius: 6px;
  }

  .prefs-input:focus {
    border-color: var(--accent-line);
    outline: none;
  }

  .prefs-check {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    margin-top: 6px;
    font-size: 13.5px;
    cursor: pointer;
  }

  .prefs-check input {
    margin: 0;
    accent-color: var(--accent);
  }

  .prefs-row {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 4px;
  }

  .prefs-button {
    flex: none;
    padding: 5px 12px;
    font: inherit;
    font-size: 13px;
    color: var(--text);
    background: transparent;
    border: 1px solid var(--line-strong);
    border-radius: 6px;
    cursor: pointer;
    transition: background 120ms ease, border-color 120ms ease, opacity 120ms ease;
  }

  .prefs-button:hover:not(:disabled) {
    background: var(--accent-soft);
  }

  .prefs-button.primary {
    color: var(--bg);
    background: var(--accent);
    border-color: var(--accent);
  }

  .prefs-button.primary:hover:not(:disabled) {
    background: var(--accent);
    filter: brightness(1.06);
  }

  .prefs-button.danger {
    color: var(--err);
    border-color: var(--err-line);
  }

  .prefs-button:disabled {
    opacity: 0.45;
    cursor: default;
  }

  .prefs-link {
    padding: 2px 4px;
    font: inherit;
    font-size: 12px;
    color: var(--muted);
    background: transparent;
    border: 0;
    border-radius: 4px;
    cursor: pointer;
  }

  .prefs-link:hover {
    color: var(--accent);
    background: var(--accent-soft);
  }

  /* The themes to choose, a card each with a small window in its colors. */
  .theme-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(176px, 1fr));
    gap: 10px;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .theme {
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 10px;
    overflow: hidden;
    transition: border-color 120ms ease, box-shadow 120ms ease;
  }

  .theme:hover {
    border-color: var(--line-strong);
  }

  .theme.worn {
    border-color: var(--accent);
    box-shadow: 0 0 0 1px var(--accent);
  }

  .theme-pick {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 10px 10px 8px;
    font: inherit;
    text-align: left;
    color: var(--text);
    background: transparent;
    border: 0;
    cursor: pointer;
  }

  .theme-pick:disabled {
    cursor: default;
  }

  .swatch-split {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 4px;
  }

  .swatch {
    display: flex;
    align-items: flex-end;
    height: 64px;
    padding: 10px 0 0 12px;
    border: 1px solid;
    border-radius: 7px;
    overflow: hidden;
  }

  .swatch-card {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 5px;
    height: 100%;
    padding: 8px 9px;
    border: 1px solid;
    border-right: 0;
    border-bottom: 0;
    border-radius: 6px 0 0 0;
  }

  .swatch-text {
    width: 72%;
    height: 4px;
    border-radius: 2px;
  }

  .swatch-text.short {
    width: 46%;
  }

  .swatch-dots {
    display: flex;
    gap: 4px;
    margin-top: auto;
  }

  .swatch-dots span {
    width: 9px;
    height: 9px;
    border-radius: 50%;
  }

  .theme-name {
    margin-top: 5px;
    font-size: 13.5px;
    font-weight: 600;
    overflow-wrap: anywhere;
  }

  .theme-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 2px 8px;
    font-size: 11.5px;
    color: var(--muted);
  }

  .theme-active {
    font-family: var(--mono);
    font-size: 10px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--accent);
  }

  .theme-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 2px;
    padding: 0 6px 6px;
  }

  .theme-confirm {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 0 10px 10px;
  }

  /* The editor: the name, light or dark, and the ten colors. */
  .theme-editor {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 8px;
    padding: 16px 18px;
    background: var(--surface);
    border: 1px solid var(--line-strong);
    border-radius: 10px;
    box-shadow: var(--shadow);
  }

  .theme-editor .prefs-sub {
    margin-top: 0;
  }

  .theme-colors {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
    gap: 8px 16px;
  }

  .theme-color {
    display: grid;
    grid-template-columns: auto 1fr 92px;
    align-items: center;
    gap: 2px 10px;
  }

  .theme-color-pick {
    width: 34px;
    height: 30px;
    padding: 2px;
    background: var(--field);
    border: 1px solid var(--line-strong);
    border-radius: 6px;
    cursor: pointer;
  }

  .theme-color-name {
    font-size: 13px;
  }

  .theme-color-hex {
    font-family: var(--mono);
    font-size: 12.5px;
  }

  .theme-color.bad .theme-color-hex {
    border-color: var(--err);
  }

  .theme-color-error {
    grid-column: 2 / -1;
    font-size: 12px;
    color: var(--err);
  }
</style>

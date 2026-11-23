<script lang="ts">
  import { t, type Key } from '../lib/i18n';
  import type { Themes } from '../lib/themes.svelte';
  import type { NodeOptions } from '../lib/nodeoptions.svelte';
  import type { Agents } from '../lib/agents.svelte';
  import type { Gateways } from '../lib/gateways.svelte';
  import ThemeOptions from './ThemeOptions.svelte';
  import BackgroundOptions from './BackgroundOptions.svelte';
  import AccessOptions from './AccessOptions.svelte';
  import ModelOptions from './ModelOptions.svelte';
  import GatewayOptions from './GatewayOptions.svelte';

  let {
    themes,
    node = null,
    gateways = null,
    agents = null,
    onagents,
  }: {
    themes: Themes;
    node?: NodeOptions | null;
    /** The gateways of the profile; a host without them shows no such part. */
    gateways?: Gateways | null;
    /** The agents, whose own models go before the defaults. */
    agents?: Agents | null;
    onagents?: () => void;
  } = $props();

  /** The options in parts: what the node answers with and the accesses it
   * answers through, which are those of the node and hold for every window,
   * and how this window looks. A host without the options of the node shows
   * the look alone. */
  type Tab = 'models' | 'accesses' | 'gateways' | 'appearance';
  const tabs = $derived<Tab[]>([...(node ? (['models', 'accesses'] as Tab[]) : []), ...(node && gateways ? (['gateways'] as Tab[]) : []), 'appearance']);

  /** The window remembers the part it showed, where it can. */
  const TAB_KEY = 'ouro.options.tab';

  function storedTab(): Tab | null {
    try {
      const stored = localStorage.getItem(TAB_KEY);
      return stored === 'models' || stored === 'accesses' || stored === 'gateways' || stored === 'appearance' ? stored : null;
    } catch {
      return null;
    }
  }

  let chosen = $state<Tab | null>(storedTab());
  const tab = $derived(chosen && tabs.includes(chosen) ? chosen : tabs[0]);

  function choose(next: Tab): void {
    chosen = next;
    try {
      localStorage.setItem(TAB_KEY, next);
    } catch {
      // Not remembered, still shown.
    }
  }

  const tabButtons: Partial<Record<Tab, HTMLButtonElement>> = {};

  /** The arrow keys move between the parts, as in any tab list. */
  function tabKey(event: KeyboardEvent): void {
    const at = tabs.indexOf(tab);
    let next = at;
    if (event.key === 'ArrowRight') next = (at + 1) % tabs.length;
    else if (event.key === 'ArrowLeft') next = (at - 1 + tabs.length) % tabs.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = tabs.length - 1;
    else return;
    event.preventDefault();
    choose(tabs[next]);
    tabButtons[tabs[next]]?.focus();
  }

  const tabKeyOf = (name: Tab) => `options.tab.${name}` as Key;

  // The options of the node are read whenever they are shown: another window,
  // the terminal or the agent may have changed them.
  $effect(() => {
    if (node) void node.refresh();
  });
  $effect(() => {
    if (node && gateways) void gateways.refresh();
  });
</script>

{#snippet icon(name: Tab)}
  <svg class="tab-icon" viewBox="0 0 24 24" aria-hidden="true">
    {#if name === 'models'}
      <rect x="6" y="6" width="12" height="12" rx="2" />
      <path d="M9 3v3M15 3v3M9 18v3M15 18v3M3 9h3M3 15h3M18 9h3M18 15h3" />
    {:else if name === 'accesses'}
      <circle cx="8" cy="12" r="3.5" />
      <path d="M11.5 12H21M17 12v3.5M20 12v2.5" />
    {:else if name === 'gateways'}
      <path d="M4 5.5h16v10H12l-4.5 3.5v-3.5H4z" />
    {:else}
      <path d="M12 3.5a8.5 8.5 0 1 0 0 17c1.2 0 1.8-.8 1.8-1.7 0-1.3-1-1.6-1-2.8 0-1 .8-1.7 1.8-1.7H17a3.5 3.5 0 0 0 3.5-3.5c0-4.2-3.8-7.3-8.5-7.3z" />
      <circle cx="7.8" cy="11" r="1.1" />
      <circle cx="10.5" cy="7.5" r="1.1" />
      <circle cx="15" cy="7.8" r="1.1" />
    {/if}
  </svg>
{/snippet}

<section class="prefs" aria-label={t('options.label')}>
  <div class="prefs-page">
    {#if tabs.length > 1}
      <!-- svelte-ignore a11y_interactive_supports_focus -->
      <div class="tabs" role="tablist" aria-label={t('options.title')} tabindex="-1" onkeydown={tabKey}>
        {#each tabs as name (name)}
          <button
            type="button"
            role="tab"
            id="prefs-tab-{name}"
            class="tab"
            aria-selected={tab === name}
            aria-controls="prefs-panel-{name}"
            tabindex={tab === name ? 0 : -1}
            bind:this={tabButtons[name]}
            onclick={() => choose(name)}
          >{@render icon(name)}<span>{t(tabKeyOf(name))}</span></button>
        {/each}
      </div>
    {/if}

    <div class="panel" role={tabs.length > 1 ? 'tabpanel' : undefined} id="prefs-panel-{tab}" aria-labelledby={tabs.length > 1 ? `prefs-tab-${tab}` : undefined}>
      {#if node && tab === 'gateways' && gateways}
        <GatewayOptions {gateways} />
      {:else if node && tab !== 'appearance' && !node.list && !node.error}
        <p class="prefs-hint" role="status">{t('options.loading')}</p>
      {:else if node && tab !== 'appearance' && node.error && !node.list}
        <p class="prefs-error" role="alert">{t('options.unreadable')}: {node.error}</p>
        <div class="prefs-row">
          <button type="button" class="prefs-button" onclick={() => node.refresh()}>{t('options.retry')}</button>
        </div>
      {:else if node && tab === 'models'}
        <ModelOptions {node} agents={agents?.list ?? null} {onagents} />
      {:else if node && tab === 'accesses'}
        <AccessOptions {node} />
      {:else}
        <ThemeOptions {themes} />
        <BackgroundOptions {themes} />
      {/if}
    </div>
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

  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-bottom: 10px;
    padding: 4px;
    width: fit-content;
    max-width: 100%;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 10px;
  }

  .tab {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding: 6px 14px;
    font: inherit;
    font-size: 13.5px;
    color: var(--muted);
    background: transparent;
    border: 0;
    border-radius: 7px;
    cursor: pointer;
    transition: background 120ms ease, color 120ms ease;
  }

  .tab:hover {
    color: var(--text);
  }

  .tab[aria-selected='true'] {
    color: var(--text);
    background: var(--accent-soft);
    box-shadow: inset 0 0 0 1px var(--accent-line);
  }

  .tab-icon {
    width: 16px;
    height: 16px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.6;
    stroke-linecap: round;
    stroke-linejoin: round;
  }

  .tab[aria-selected='true'] .tab-icon {
    color: var(--accent);
  }

  .panel {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
</style>

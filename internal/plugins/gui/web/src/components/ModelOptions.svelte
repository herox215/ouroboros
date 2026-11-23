<script lang="ts">
  import { t, type Key } from '../lib/i18n';
  import type { NodeOptions } from '../lib/nodeoptions.svelte';
  import type { AgentInfo, Scope, ScopeLLM } from '../lib/wire';

  let {
    node,
    agents = null,
    onagents,
  }: {
    node: NodeOptions;
    /** The agents of the profile, which go before the defaults where they have
     * a model or an effort of their own; null while they are not known. */
    agents?: AgentInfo[] | null;
    /** Shows the agents, where they are changed. */
    onagents?: () => void;
  } = $props();

  /** The scopes, the default first: the others follow it until they have a
   * model of their own. */
  const SCOPES: Scope[] = ['owner', 'thread', 'sessions', 'peers'];

  $effect(() => {
    void node.listModels();
  });

  /** The fallback chosen to add. */
  let fallback = $state('');

  const scopeKey = (scope: Scope) => `models.scope.${scope}` as Key;
  const hintKey = (scope: Scope) => `models.scope.${scope}.hint` as Key;

  /** A model of an access as one value of a choice. */
  function choiceOf(access: string, model: string): string {
    return access && model ? `${access}\n${model}` : '';
  }

  function split(value: string): [string, string] {
    const [access = '', model = ''] = value ? value.split('\n') : [];
    return [access, model];
  }

  function current(scope: Scope): ScopeLLM | undefined {
    return node.llms?.scopes.find((candidate) => candidate.scope === scope);
  }

  /** Whether an access of the node lists model now. */
  function listed(access: string, model: string): boolean {
    return (node.models ?? []).some((candidate) => candidate.access === access && candidate.models.includes(model));
  }

  function named(access: string, model: string): string {
    return `${model} · ${node.label(access)}`;
  }

  /** The agents that go before the default of scope: the agent of the thread
   * before that of the thread, every agent with a model of its own before
   * that of the sessions, as the agent of the thread works in sessions too.
   * Peers are answered by no agent of the profile. */
  function before(scope: Scope): AgentInfo[] {
    const own = (agent: AgentInfo) => !!agent.access && !!agent.model;
    if (scope === 'thread') return (agents ?? []).filter((agent) => agent.kind === 'thread' && own(agent));
    if (scope === 'sessions') return (agents ?? []).filter(own);
    return [];
  }

  const ownEffort = $derived((agents ?? []).filter((agent) => !!agent.effort));
  const owner = $derived(current('owner'));
  const offered = $derived((node.models ?? []).filter((candidate) => candidate.models.length > 0));
  const efforts = $derived(node.llms?.efforts ?? []);
  const effort = $derived(node.llms?.effort ?? '');
  const fallbacks = $derived(node.llms?.fallbacks ?? []);

  /** Makes what select shows what scope answers with; a choice the node
   * refused shows what was before again. */
  async function pick(scope: Scope, select: HTMLSelectElement, was: string): Promise<void> {
    const [access, model] = split(select.value);
    if (!(await node.setLLM(scope, access, model))) select.value = was;
  }

  async function pickEffort(select: HTMLSelectElement, was: string): Promise<void> {
    if (!(await node.setEffort(select.value))) select.value = was;
  }

  async function addFallback(): Promise<void> {
    const [access, model] = split(fallback);
    if (access && model && (await node.toggleFallback(access, model))) fallback = '';
  }
</script>

{#snippet models(exclude: (access: string, model: string) => boolean)}
  {#each offered as access (access.access)}
    <optgroup label={access.label || access.access}>
      {#each access.models.filter((model) => !exclude(access.access, model)) as model (model)}
        <option value={choiceOf(access.access, model)}>{model} · {access.label || access.access}</option>
      {/each}
    </optgroup>
  {/each}
{/snippet}

<div class="prefs-head">
  <h2 class="prefs-title">{t('models.title')}</h2>
  <button type="button" class="prefs-button" onclick={() => node.listModels(true)} disabled={node.listing}>{t('models.reload')}</button>
</div>
<p class="prefs-hint">{t('models.hint')} {t('options.applies')}</p>

{#if node.modelsError}
  <p class="prefs-error" role="alert">{t('models.unreadable')}: {node.modelsError}</p>
{:else if node.listing || !node.models}
  <p class="prefs-hint" role="status">{t('models.loading')}</p>
{:else}
  {#each node.models.filter((candidate) => candidate.error) as candidate (candidate.access)}
    <p class="prefs-hint">{candidate.label || candidate.access}: {candidate.error}</p>
  {/each}
  {#if offered.length === 0}
    <p class="prefs-hint">{t('models.none')}</p>
  {/if}
{/if}
{#if node.changeError}
  <p class="prefs-error" role="alert">{node.changeError}</p>
{/if}

<ul class="scopes">
  {#each SCOPES as scope (scope)}
    {@const llm = current(scope)}
    {@const own = scope === 'owner' || !!llm?.own}
    {@const value = own && llm ? choiceOf(llm.access, llm.model) : ''}
    {@const overrides = before(scope)}
    {@const overridden = scope === 'thread' && overrides.length > 0}
    <li class="scope" class:primary={scope === 'owner'} class:follows={!own} class:overridden>
      <div class="scope-text">
        <label class="scope-name" for="scope-{scope}">{t(scopeKey(scope))}</label>
        <span class="prefs-hint">{t(hintKey(scope))}</span>
        {#if overridden}
          <span class="prefs-hint override-note">{t('models.overridden')}</span>
        {/if}
        {#if overrides.length > 0}
          <span class="overrides">
            <span class="overrides-label">{t('models.before')}:</span>
            {#each overrides as agent (agent.id)}
              <span class="override">{agent.name} → {named(agent.access!, agent.model!)}</span>
            {/each}
          </span>
        {/if}
      </div>
      <div class="scope-choice">
        <select
          id="scope-{scope}"
          class="prefs-input"
          {value}
          onchange={(event) => pick(scope, event.currentTarget as HTMLSelectElement, value)}
          disabled={!llm || node.busy !== null}
        >
          {#if scope !== 'owner'}
            <option value="">{t('models.follow')}{owner ? ` (${named(owner.access, owner.model)})` : ''}</option>
          {/if}
          {#if llm && own && !listed(llm.access, llm.model)}
            <option {value}>{named(llm.access, llm.model)} ({t('models.unlisted')})</option>
          {/if}
          {@render models(() => false)}
        </select>
        {#if node.busy === scope}
          <span class="prefs-hint" role="status">{t('options.saving')}</span>
        {/if}
      </div>
    </li>
  {/each}

  <li class="scope">
    <div class="scope-text">
      <label class="scope-name" for="scope-effort">{t('models.effort')}</label>
      <span class="prefs-hint">{t('models.effort.hint')}</span>
      {#if node.llms && !node.llms.efforts_known}
        <span class="prefs-hint">{t('models.effort.unknown')}</span>
      {/if}
      {#if ownEffort.length > 0}
        <span class="overrides">
          <span class="overrides-label">{t('models.before')}:</span>
          {#each ownEffort as agent (agent.id)}
            <span class="override">{agent.name} → {agent.effort}</span>
          {/each}
        </span>
      {/if}
    </div>
    <div class="scope-choice">
      <select
        id="scope-effort"
        class="prefs-input"
        value={effort}
        onchange={(event) => pickEffort(event.currentTarget as HTMLSelectElement, effort)}
        disabled={!node.llms || node.busy !== null}
      >
        {#if !efforts.includes(effort)}
          <option value={effort}>{effort}</option>
        {/if}
        {#each efforts as choice (choice)}
          <option value={choice}>{choice === '' ? t('models.effort.auto') : choice}</option>
        {/each}
      </select>
      {#if node.busy === 'effort'}
        <span class="prefs-hint" role="status">{t('options.saving')}</span>
      {/if}
    </div>
  </li>
</ul>

<h3 class="prefs-sub">{t('models.fallbacks')}</h3>
<p class="prefs-hint">{t('models.fallbacks.hint')}</p>
{#if fallbacks.length === 0}
  <p class="prefs-hint">{t('models.fallbacks.empty')}</p>
{:else}
  <ol class="fallbacks">
    {#each fallbacks as entry, at (choiceOf(entry.access, entry.model))}
      <li class="fallback">
        <span class="fallback-at" aria-hidden="true">{at + 1}</span>
        <span class="fallback-name">
          {entry.model}
          <span class="fallback-access">· {node.label(entry.access)}</span>
          {#if node.models && !listed(entry.access, entry.model)}
            <span class="fallback-access">({t('models.unlisted')})</span>
          {/if}
        </span>
        <button
          type="button"
          class="prefs-link"
          aria-label="{t('models.fallbacks.remove')}: {named(entry.access, entry.model)}"
          onclick={() => node.toggleFallback(entry.access, entry.model)}
          disabled={node.busy !== null}
        >{t('models.fallbacks.remove')}</button>
      </li>
    {/each}
  </ol>
{/if}
<div class="fallback-add">
  <select class="prefs-input" aria-label={t('models.fallbacks.choose')} bind:value={fallback} disabled={offered.length === 0 || node.busy !== null}>
    <option value="">{t('models.fallbacks.choose')}</option>
    {@render models(
      (access, model) =>
        (owner?.access === access && owner?.model === model) ||
        fallbacks.some((entry) => entry.access === access && entry.model === model),
    )}
  </select>
  <button type="button" class="prefs-button" onclick={addFallback} disabled={!fallback || node.busy !== null}>
    {t('models.fallbacks.add')}
  </button>
</div>

<div class="agents-hint">
  <p class="prefs-hint">{t('models.agents')}</p>
  {#if onagents}
    <button type="button" class="prefs-button" onclick={onagents}>{t('models.agents.open')}</button>
  {/if}
</div>

<style>
  .scopes {
    display: flex;
    flex-direction: column;
    margin: 8px 0 0;
    padding: 0;
    list-style: none;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 10px;
    overflow: hidden;
  }

  .scope {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(220px, 1.2fr);
    align-items: center;
    gap: 8px 20px;
    padding: 12px 16px;
  }

  .scope + .scope {
    border-top: 1px solid var(--line);
  }

  .scope.primary {
    background: var(--accent-soft);
  }

  .scope-text {
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-width: 0;
  }

  .scope-name {
    font-size: 14px;
    font-weight: 600;
  }

  .scope-choice {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }

  .scope.follows select {
    color: var(--muted);
  }

  .fallbacks {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 4px 0 0;
    padding: 0;
    list-style: none;
  }

  .fallback {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 8px;
  }

  .fallback-at {
    flex: none;
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    font-family: var(--mono);
    font-size: 11px;
    color: var(--muted);
    border: 1px solid var(--line-strong);
    border-radius: 50%;
  }

  .fallback-name {
    flex: 1;
    min-width: 0;
    font-size: 13.5px;
    overflow-wrap: anywhere;
  }

  .fallback-access {
    color: var(--muted);
  }

  .fallback-add {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 6px;
  }

  .fallback-add select {
    flex: 1;
    min-width: 220px;
    max-width: 420px;
  }

  .agents-hint {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
    margin-top: 16px;
  }

  .overrides {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px;
    margin-top: 4px;
  }

  .overrides-label {
    font-size: 12px;
    color: var(--muted);
  }

  .override {
    padding: 1px 7px;
    font-size: 12px;
    color: var(--accent);
    background: var(--accent-soft);
    border: 1px solid var(--accent-line);
    border-radius: 999px;
    overflow-wrap: anywhere;
  }

  .override-note {
    color: var(--warn);
  }

  .scope.overridden select {
    opacity: 0.7;
  }

  @media (max-width: 640px) {
    .scope {
      grid-template-columns: 1fr;
    }
  }
</style>

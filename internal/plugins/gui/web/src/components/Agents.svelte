<script lang="ts">
  import { tick } from 'svelte';
  import { NEW_AGENT, type Agents } from '../lib/agents.svelte';
  import type { AgentChange, AgentInfo } from '../lib/wire';
  import { t, type Key } from '../lib/i18n';
  import { kindName } from '../lib/agentkind';
  import type { NodeOptions } from '../lib/nodeoptions.svelte';

  let {
    agents,
    node = null,
    onrenamed,
  }: {
    agents: Agents;
    /** The defaults of the options, which an agent without a model or an
     * effort of its own answers with: named where they are known. */
    node?: NodeOptions | null;
    onrenamed?: (agent: AgentInfo) => void;
  } = $props();

  /** The bounds of an agent, see agents.go: the name of a profile, what an
   * agent is for and its soul. */
  const MAX_NAME = 64;
  const MAX_DESCRIPTION = 300;
  const MAX_SOUL = 8000;

  /** The efforts an agent may answer with, see config.Efforts: '' is that of
   * the settings, and one the agent has that is none of these stays on. */
  const EFFORTS = ['none', 'low', 'medium', 'high', 'max'];

  const list = $derived(agents.list ?? []);

  /** The system agents, the archivist and the orchestrator, are hidden until
   * the owner asks for them; the window remembers that, where it can. */
  const SYSTEM_KEY = 'ouro.agents.system';

  function storedShowSystem(): boolean {
    try {
      return localStorage.getItem(SYSTEM_KEY) === 'shown';
    } catch {
      return false; // no storage here: they stay hidden
    }
  }

  let showSystem = $state(storedShowSystem());

  function toggleSystem(): void {
    showSystem = !showSystem;
    if (!showSystem && editedAgent?.reserved) cancel();
    if (!showSystem && list.some((agent) => agent.reserved && agent.id === removing)) removing = null;
    try {
      localStorage.setItem(SYSTEM_KEY, showSystem ? 'shown' : 'hidden');
    } catch {
      // Not remembered, still toggled.
    }
  }

  /** The agents the view shows: the system agents only when asked for. */
  const shown = $derived(list.filter((agent) => showSystem || !agent.reserved));
  const hiddenSystem = $derived(list.filter((agent) => agent.reserved).length);

  /** The agent being edited, NEW_AGENT for one being made, and what is typed
   * for it. */
  let editing = $state<string | null>(null);
  const EMPTY: AgentChange = { name: '', description: '', soul: '', access: '', model: '', effort: '' };
  let draft = $state<AgentChange>({ ...EMPTY });
  let field = $state<HTMLInputElement | null>(null);
  /** The agent the owner is asked whether to remove. */
  let removing = $state<string | null>(null);

  const editedAgent = $derived(list.find((candidate) => candidate.id === editing));

  const unchanged = $derived.by(() => {
    if (draft.name.trim() === '') return true;
    if (editing === NEW_AGENT) return false;
    const agent = editedAgent;
    return (
      draft.name.trim() === agent?.name &&
      draft.description.trim() === (agent?.description ?? '') &&
      draft.soul.trim() === (agent?.soul ?? '') &&
      draft.access === (agent?.access ?? '') &&
      draft.model === (agent?.model ?? '') &&
      draft.effort === (agent?.effort ?? '')
    );
  });

  /** A model of an access as one value of the choice of the model: '' for the
   * model of the settings. */
  function choiceOf(access: string, model: string): string {
    return access && model ? `${access}\n${model}` : '';
  }

  function choose(value: string): void {
    const [access = '', model = ''] = value ? value.split('\n') : [];
    draft.access = access;
    draft.model = model;
  }

  /** What an access is called, as it listed its models; its ID while they are
   * not listed. */
  function accessLabel(access: string): string {
    return agents.models?.find((listed) => listed.access === access)?.label || access;
  }

  /** What an agent of kind answers with while it has no model of its own,
   * as the options say: the default of the thread and of the sessions for the
   * agent of the thread, which works in both, that of the sessions for any
   * other; '' while the options are not known. */
  function modelDefault(kind: string): string {
    const scopes = node?.llms?.scopes;
    if (!scopes) return '';
    const named = (scope: string) => {
      const llm = scopes.find((candidate) => candidate.scope === scope);
      return llm ? `${llm.model} · ${node!.label(llm.access)}` : '';
    };
    if (kind === 'thread') {
      return `${t('agents.default')} – ${t('models.scope.thread')}: ${named('thread')}, ${t('models.scope.sessions')}: ${named('sessions')}`;
    }
    return `${t('agents.default')}: ${named('sessions')}`;
  }

  /** The model the agent answers with, as its card says: '' for that of the
   * settings. */
  function modelText(agent: AgentInfo): string {
    if (!agent.access || !agent.model) return '';
    return `${agent.model} · ${accessLabel(agent.access)}`;
  }

  /** Whether the model chosen is one no access of the node lists now: it stays
   * a choice, as the agent has it. */
  const unlisted = $derived(
    !!draft.access &&
      !!draft.model &&
      !(agents.models ?? []).some((listed) => listed.access === draft.access && listed.models.includes(draft.model)),
  );

  /** Whether the effort chosen is one the window does not offer: it stays a
   * choice, as the agent has it. */
  const effortUnlisted = $derived(draft.effort !== '' && !EFFORTS.includes(draft.effort));

  /** What the agent is for: what the owner wrote, or what a built-in agent
   * does, '' for neither; always the latter for a system agent. */
  function about(agent: AgentInfo): string {
    if (agent.reserved) return builtinAbout(agent.kind);
    return agent.description || builtinAbout(agent.kind);
  }

  function builtinAbout(kind: string): string {
    return t(`agents.about.${kind}` as Key) ?? '';
  }

  /** The first letter of the name, for the badge of the agent. */
  function initial(name: string): string {
    return Array.from(name.trim())[0]?.toUpperCase() ?? '?';
  }

  async function open(id: string, from: AgentChange): Promise<void> {
    agents.clearChangeError();
    removing = null;
    editing = id;
    draft = { ...from };
    void agents.listModels();
    await tick();
    field?.focus();
    field?.select();
  }

  function edit(agent: AgentInfo): Promise<void> {
    return open(agent.id, {
      name: agent.name,
      description: agent.description ?? '',
      soul: agent.soul ?? '',
      access: agent.access ?? '',
      model: agent.model ?? '',
      effort: agent.effort ?? '',
    });
  }

  function create(): Promise<void> {
    return open(NEW_AGENT, { ...EMPTY });
  }

  function cancel(): void {
    editing = null;
    draft = { ...EMPTY };
    agents.clearChangeError();
  }

  async function save(event?: SubmitEvent): Promise<void> {
    event?.preventDefault();
    if (editing === null || unchanged || agents.saving !== null) {
      if (unchanged && draft.name.trim() !== '') cancel();
      return;
    }
    // What a system agent is for is fixed: it takes no description.
    const description = editedAgent?.reserved ? '' : draft.description.trim();
    const change = { name: draft.name.trim(), description, soul: draft.soul.trim(), access: draft.access, model: draft.model, effort: draft.effort };
    const was = editedAgent;
    const saved = editing === NEW_AGENT ? await agents.create(change) : await agents.update(editing, change);
    if (!saved) {
      field?.focus();
      return;
    }
    cancel();
    if (was && was.name !== saved.name) onrenamed?.(saved);
  }

  function ask(agent: AgentInfo): void {
    agents.clearChangeError();
    cancel();
    removing = agent.id;
  }

  async function remove(agent: AgentInfo): Promise<void> {
    if (await agents.remove(agent.id)) removing = null;
  }

  function keydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.preventDefault();
      cancel();
    }
  }
</script>

{#snippet form(id: string, kind: string, reserved: boolean)}
  <form class="agent-form" onsubmit={save}>
    <label class="agent-label" for="agent-name-{id || 'new'}">{t('agents.name')}</label>
    <input
      id="agent-name-{id || 'new'}"
      class="agent-input"
      type="text"
      autocomplete="off"
      spellcheck="false"
      maxlength={MAX_NAME}
      bind:value={draft.name}
      bind:this={field}
      onkeydown={keydown}
      disabled={agents.saving === id}
    />
    {#if kind === 'thread'}
      <p class="agent-hint">{t('agents.rename.hint')}</p>
    {/if}

    <span class="agent-label">{t('agents.description')}</span>
    {#if reserved}
      <p class="agent-fixed">{builtinAbout(kind)}</p>
      <p class="agent-hint">{t('agents.reserved.hint')}</p>
    {:else}
      <input
        id="agent-description-{id || 'new'}"
        class="agent-input"
        type="text"
        autocomplete="off"
        maxlength={MAX_DESCRIPTION}
        placeholder={builtinAbout(kind)}
        aria-label={t('agents.description')}
        bind:value={draft.description}
        onkeydown={keydown}
        disabled={agents.saving === id}
      />
      <p class="agent-hint">{t('agents.description.hint')}</p>
    {/if}

    <label class="agent-label" for="agent-soul-{id || 'new'}">{t('agents.soul')}</label>
    <textarea
      id="agent-soul-{id || 'new'}"
      class="agent-input agent-soul-input"
      rows="6"
      maxlength={MAX_SOUL}
      placeholder={t('agents.soul.placeholder')}
      bind:value={draft.soul}
      onkeydown={keydown}
      disabled={agents.saving === id}
    ></textarea>
    <p class="agent-hint">{t('agents.soul.hint')}</p>

    <label class="agent-label" for="agent-model-{id || 'new'}">{t('agents.model')}</label>
    <select
      id="agent-model-{id || 'new'}"
      class="agent-input"
      value={choiceOf(draft.access, draft.model)}
      onchange={(event) => choose((event.currentTarget as HTMLSelectElement).value)}
      disabled={agents.saving === id}
    >
      <option value="">{t('agents.model.settings')}</option>
      {#if unlisted}
        <option value={choiceOf(draft.access, draft.model)}>{draft.model} · {accessLabel(draft.access)} ({t('agents.model.unlisted')})</option>
      {/if}
      {#each agents.models ?? [] as listed (listed.access)}
        {#if listed.models.length > 0}
          <optgroup label={listed.label || listed.access}>
            {#each listed.models as model (model)}
              <option value={choiceOf(listed.access, model)}>{model}</option>
            {/each}
          </optgroup>
        {/if}
      {/each}
    </select>
    {#if agents.modelsError}
      <p class="agent-error" role="alert">{t('agents.model.unreadable')}: {agents.modelsError}</p>
    {:else if !agents.models}
      <p class="agent-hint" role="status">{t('agents.model.loading')}</p>
    {:else}
      {#each agents.models.filter((listed) => listed.error) as listed (listed.access)}
        <p class="agent-hint">{listed.label || listed.access}: {listed.error}</p>
      {/each}
      <p class="agent-hint">{t('agents.model.hint')}</p>
    {/if}
    {#if modelDefault(kind)}
      <p class="agent-hint agent-default">{modelDefault(kind)}</p>
    {/if}

    <label class="agent-label" for="agent-effort-{id || 'new'}">{t('agents.effort')}</label>
    <select
      id="agent-effort-{id || 'new'}"
      class="agent-input"
      value={draft.effort}
      onchange={(event) => (draft.effort = (event.currentTarget as HTMLSelectElement).value)}
      onkeydown={keydown}
      disabled={agents.saving === id}
    >
      <option value="">{t('agents.effort.settings')}</option>
      {#if effortUnlisted}
        <option value={draft.effort}>{draft.effort} ({t('agents.effort.unlisted')})</option>
      {/if}
      {#each EFFORTS as effort (effort)}
        <option value={effort}>{effort}</option>
      {/each}
    </select>
    <p class="agent-hint">{t('agents.effort.hint')}</p>
    {#if node?.llms}
      <p class="agent-hint agent-default">{t('agents.default')}: {node.llms.effort || t('models.effort.auto')}</p>
    {/if}

    {#if agents.changeError}
      <p class="agent-error" role="alert">{agents.changeError}</p>
    {/if}
    <div class="agent-row">
      <button type="submit" class="agent-button primary" disabled={unchanged || agents.saving === id}>
        {agents.saving === id ? t('agents.saving') : id === NEW_AGENT ? t('agents.create') : t('agents.save')}
      </button>
      <button type="button" class="agent-button" onclick={cancel} disabled={agents.saving === id}>
        {t('agents.cancel')}
      </button>
    </div>
  </form>
{/snippet}

<section class="agents" aria-label={t('agents.label')}>
  {#if agents.error}
    <p class="banner" role="alert">{t('agents.unreadable')}: {agents.error}</p>
  {/if}

  {#if !agents.list}
    {#if !agents.error}
      <p class="board-wait" role="status">{t('agents.loading')}</p>
    {/if}
  {:else}
    <div class="agents-head">
      {#if hiddenSystem > 0}
        <label class="agents-system">
          <input type="checkbox" checked={showSystem} onchange={toggleSystem} />
          {t('agents.system.show')}
        </label>
      {/if}
      <button type="button" class="agent-button primary" onclick={create} disabled={editing === NEW_AGENT}>
        {t('agents.new')}
      </button>
    </div>

    <ul class="agent-list">
      {#if editing === NEW_AGENT}
        <li class="agent" data-kind="new">
          <span class="agent-badge" aria-hidden="true">{initial(draft.name || '+')}</span>
          <div class="agent-main">{@render form(NEW_AGENT, 'custom', false)}</div>
        </li>
      {/if}
      {#each shown as agent (agent.id)}
        <li class="agent" data-kind={agent.kind}>
          <span class="agent-badge" aria-hidden="true">{initial(agent.name)}</span>
          <div class="agent-main">
            {#if editing === agent.id}
              {@render form(agent.id, agent.kind, !!agent.reserved)}
            {:else}
              <div class="agent-head">
                <h2 class="agent-name">{agent.name}</h2>
                <span class="agent-kind">{kindName(agent.kind)}</span>
                {#if agent.reserved}
                  <span class="agent-system" title={t('agents.reserved.hint')}>{t('agents.reserved')}</span>
                {/if}
              </div>
              {#if about(agent)}
                <p class="agent-about">{about(agent)}</p>
              {/if}
              {#if modelText(agent)}
                <p class="agent-model"><span class="agent-label">{t('agents.model')}</span> {modelText(agent)}</p>
              {/if}
              {#if agent.effort}
                <p class="agent-effort"><span class="agent-label">{t('agents.effort')}</span> {agent.effort}</p>
              {/if}
              {#if agent.soul}
                <p class="agent-soul"><span class="agent-label">{t('agents.soul')}</span> {agent.soul}</p>
              {/if}
              {#if removing === agent.id}
                <div class="agent-confirm" role="alertdialog" aria-label="{t('agents.remove')}: {agent.name}">
                  <p class="agent-hint">{t('agents.remove.hint')}</p>
                  {#if agents.changeError}
                    <p class="agent-error" role="alert">{agents.changeError}</p>
                  {/if}
                  <div class="agent-row">
                    <button type="button" class="agent-button danger" onclick={() => remove(agent)} disabled={agents.saving === agent.id}>
                      {t('agents.remove')}
                    </button>
                    <button type="button" class="agent-button" onclick={() => (removing = null)} disabled={agents.saving === agent.id}>
                      {t('agents.cancel')}
                    </button>
                  </div>
                </div>
              {/if}
            {/if}
          </div>
          {#if editing !== agent.id && removing !== agent.id}
            <div class="agent-actions">
              <button type="button" class="agent-button" aria-label="{t('agents.edit')}: {agent.name}" onclick={() => edit(agent)}>
                {t('agents.edit')}
              </button>
              {#if agent.kind === 'custom'}
                <button type="button" class="agent-button" aria-label="{t('agents.remove')}: {agent.name}" onclick={() => ask(agent)}>
                  {t('agents.remove')}
                </button>
              {/if}
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .agents {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 18px 0 12px;
    overflow-y: auto;
  }

  .agents-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: flex-end;
    gap: 8px 16px;
    max-width: 760px;
  }

  .agents-system {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-right: auto;
    font-size: 13px;
    color: var(--muted);
    cursor: pointer;
  }

  .agents-system input {
    margin: 0;
    accent-color: var(--accent);
  }

  .agent-list {
    display: flex;
    flex-direction: column;
    gap: 10px;
    max-width: 760px;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .agent {
    display: flex;
    align-items: flex-start;
    gap: 14px;
    padding: 16px 18px;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: var(--shadow);
  }

  .agent-badge {
    flex: none;
    width: 38px;
    height: 38px;
    display: grid;
    place-items: center;
    border-radius: 50%;
    font-family: var(--mono);
    font-size: 15px;
    font-weight: 600;
    color: var(--accent);
    background: var(--accent-soft);
    border: 1px solid var(--accent-line);
  }

  .agent-main {
    flex: 1;
    min-width: 0;
  }

  .agent-head {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 4px 10px;
  }

  .agent-name {
    margin: 0;
    font-size: 1.1em;
    font-weight: 600;
    overflow-wrap: anywhere;
  }

  .agent-kind {
    font-family: var(--mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
  }

  .agent-about {
    margin: 4px 0 0;
    font-size: 13.5px;
    color: var(--muted);
  }

  .agent-system {
    padding: 0 6px;
    font-family: var(--mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--accent);
    border: 1px solid var(--accent-line);
    border-radius: 4px;
  }

  .agent-fixed {
    margin: 0;
    font-size: 13.5px;
  }

  .agent-soul {
    margin: 8px 0 0;
    font-size: 13px;
    white-space: pre-line;
    overflow-wrap: anywhere;
    display: -webkit-box;
    -webkit-line-clamp: 3;
    line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }

  .agent-soul .agent-label,
  .agent-model .agent-label,
  .agent-effort .agent-label {
    margin-right: 6px;
  }

  .agent-model,
  .agent-effort {
    margin: 8px 0 0;
    font-size: 13px;
    overflow-wrap: anywhere;
  }

  .agent-actions {
    flex: none;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .agent-form,
  .agent-confirm {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .agent-confirm {
    margin-top: 10px;
  }

  .agent-label {
    font-family: var(--mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
  }

  .agent-form .agent-label:not(:first-child) {
    margin-top: 6px;
  }

  .agent-row {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 4px;
  }

  .agent-input {
    min-width: 0;
    padding: 6px 10px;
    font: inherit;
    color: var(--text);
    background-color: var(--field);
    border: 1px solid var(--line-strong);
    border-radius: 6px;
  }

  /* Room for the arrow app.css draws into a choice. */
  select.agent-input {
    padding-right: 30px;
  }

  .agent-soul-input {
    resize: vertical;
    min-height: 96px;
    line-height: 1.45;
  }

  .agent-input:focus {
    border-color: var(--accent-line);
    outline: none;
  }

  .agent-button {
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

  .agent-button:hover:not(:disabled) {
    background: var(--accent-soft);
  }

  .agent-button.primary {
    color: var(--bg);
    background: var(--accent);
    border-color: var(--accent);
  }

  .agent-button.primary:hover:not(:disabled) {
    background: var(--accent);
    filter: brightness(1.06);
  }

  .agent-button.danger {
    color: var(--err);
    border-color: var(--err-line);
  }

  .agent-button:disabled {
    opacity: 0.45;
    cursor: default;
  }

  .agent-error {
    margin: 0;
    font-size: 13px;
    color: var(--err);
  }

  .agent-hint {
    margin: 0;
    font-size: 12.5px;
    color: var(--muted);
  }

  @media (max-width: 640px) {
    .agent {
      flex-wrap: wrap;
    }

    .agent-actions {
      flex-direction: row;
      width: 100%;
      padding-left: 52px;
    }
  }
</style>

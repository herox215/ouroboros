<script lang="ts">
  import { tick } from 'svelte';
  import { t, type Key } from '../lib/i18n';
  import type { NodeOptions } from '../lib/nodeoptions.svelte';
  import type { AccessInfo } from '../lib/wire';

  let { node }: { node: NodeOptions } = $props();

  /** How long the name of an access may be, see maxAccessName in providers.go. */
  const MAX_NAME = 80;

  /** The providers are shown to choose one to sign in with. */
  let adding = $state(false);
  /** The access a name is typed for, and the name. */
  let renaming = $state<string | null>(null);
  let name = $state('');
  let nameField = $state<HTMLInputElement | null>(null);
  /** The access the owner is asked whether to remove. */
  let removing = $state<string | null>(null);

  /** What is typed as the answer to what the provider asks. */
  let answer = $state('');
  let answerField = $state<HTMLInputElement | null>(null);
  let loginBox = $state<HTMLElement | null>(null);
  /** The model of an access that was just added, to use as the default. */
  let useModel = $state('');
  let used = $state(false);

  const accesses = $derived(node.list?.accesses ?? []);
  const providers = $derived(node.list?.providers ?? []);
  const login = $derived(node.login);
  const working = $derived(login?.status === 'working' || login?.status === 'asking');

  function providerTitle(name: string): string {
    return providers.find((provider) => provider.name === name)?.title ?? name;
  }

  function initial(text: string): string {
    return Array.from(text.trim())[0]?.toUpperCase() ?? '?';
  }

  const useKey = (use: string) => `access.use.${use}` as Key;

  // A question comes: its field is filled with what it offers and focused.
  $effect(() => {
    const question = node.login?.status === 'asking' ? node.login.question : null;
    if (!question) return;
    answer = question.secret ? '' : (question.default ?? '');
    void tick().then(() => answerField?.focus());
  });

  // A sign-in ended with an access: its default model is offered to use.
  $effect(() => {
    const done = node.login?.status === 'done' ? node.login : null;
    if (!done) return;
    used = false;
    useModel = done.default || done.models?.[0] || '';
  });

  async function start(provider: string, again = ''): Promise<void> {
    adding = false;
    renaming = null;
    removing = null;
    await node.startLogin(provider, again);
    await tick();
    loginBox?.scrollIntoView?.({ block: 'nearest' });
  }

  function submitAnswer(event: SubmitEvent): void {
    event.preventDefault();
    void node.answer(answer);
    answer = '';
  }

  function loginKey(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.preventDefault();
      void node.closeLogin();
    }
  }

  async function use(): Promise<void> {
    const access = node.login?.access;
    if (!access || !useModel) return;
    used = await node.setLLM('owner', access.id, useModel);
  }

  async function rename(access: AccessInfo): Promise<void> {
    removing = null;
    renaming = access.id;
    name = access.name ?? '';
    node.clear();
    await tick();
    nameField?.focus();
    nameField?.select();
  }

  async function saveName(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (renaming && (await node.rename(renaming, name))) renaming = null;
  }

  function nameKey(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.preventDefault();
      renaming = null;
    }
  }

  async function remove(id: string): Promise<void> {
    if (await node.remove(id)) removing = null;
  }
</script>

<div class="prefs-head">
  <h2 class="prefs-title">{t('access.title')}</h2>
  <button
    type="button"
    class="prefs-button primary"
    aria-expanded={adding}
    onclick={() => (adding = !adding)}
    disabled={working}
  >{t('access.add')}</button>
</div>
<p class="prefs-hint">{t('access.hint')}</p>

{#if adding}
  <section class="add" aria-label={t('access.choose')}>
    <h3 class="prefs-sub">{t('access.choose')}</h3>
    {#if providers.length === 0}
      <p class="prefs-hint">{t('access.noproviders')}</p>
    {:else}
      <ul class="provider-grid">
        {#each providers as provider (provider.name)}
          <li>
            <button type="button" class="provider-pick" onclick={() => start(provider.name)}>
              <span class="badge" aria-hidden="true">{initial(provider.title)}</span>
              <span class="provider-title">{provider.title}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
    <div class="prefs-row">
      <button type="button" class="prefs-button" onclick={() => (adding = false)}>{t('access.cancel')}</button>
    </div>
  </section>
{/if}

{#if login}
  <section class="login" bind:this={loginBox} aria-label="{login.again ? t('login.again') : t('login.new')} {login.title}" aria-busy={login.status === 'working'}>
    <h3 class="prefs-sub">{login.again ? t('login.again') : t('login.new')} {login.title}</h3>

    {#each login.lines as line, at (at)}
      {#if line.kind === 'open'}
        <p class="login-line">
          {t('login.open')}
          <a class="login-link" href={line.text} target="_blank" rel="noopener noreferrer">{line.text}</a>
        </p>
      {:else if line.kind === 'reject'}
        <p class="prefs-error" role="alert">{line.text}</p>
      {:else}
        <p class="login-line">{line.text}</p>
      {/if}
    {/each}

    {#if login.status === 'asking' && login.question}
      {@const question = login.question}
      <form class="login-form" onsubmit={submitAnswer}>
        <label class="prefs-label" for="login-answer">{question.title}</label>
        {#each question.text ?? [] as line, at (at)}
          <p class="login-line">{line}</p>
        {/each}
        <input
          id="login-answer"
          class="prefs-input"
          type={question.secret ? 'password' : 'text'}
          autocomplete="off"
          spellcheck="false"
          placeholder={question.known ? t('login.known') : (question.hint ?? '')}
          bind:value={answer}
          bind:this={answerField}
          onkeydown={loginKey}
        />
        {#if question.known && question.hint}
          <p class="prefs-hint">{question.hint}</p>
        {/if}
        <div class="prefs-row">
          <button type="submit" class="prefs-button primary">{t('login.next')}</button>
          <button type="button" class="prefs-button" onclick={() => node.closeLogin()}>{t('login.cancel')}</button>
        </div>
      </form>
    {:else if login.status === 'working'}
      <p class="prefs-hint" role="status"><span class="spinner" aria-hidden="true"></span>{t('login.working')}</p>
      <div class="prefs-row">
        <button type="button" class="prefs-button" onclick={() => node.closeLogin()}>{t('login.cancel')}</button>
      </div>
    {:else if login.status === 'done'}
      <p class="login-done" role="status">
        <span class="ok" aria-hidden="true">✓</span>
        {t('login.done')}: <strong>{login.access?.label}</strong>
        {#if login.access?.about}<span class="muted">· {login.access.about}</span>{/if}
      </p>
      {#if (login.models ?? []).length > 0}
        <label class="prefs-label" for="login-use">{t('login.use')}</label>
        <div class="login-use">
          <select id="login-use" class="prefs-input" bind:value={useModel} disabled={node.busy !== null || used}>
            {#each login.models ?? [] as model (model)}
              <option value={model}>{model}</option>
            {/each}
          </select>
          <button type="button" class="prefs-button" onclick={use} disabled={!useModel || node.busy !== null || used}>
            {t('login.use.button')}
          </button>
        </div>
        {#if used}
          <p class="prefs-hint" role="status">{t('login.used')}</p>
        {/if}
      {/if}
      <div class="prefs-row">
        <button type="button" class="prefs-button" onclick={() => node.closeLogin()}>{t('login.close')}</button>
      </div>
    {:else}
      <p class="prefs-error" role="alert">{login.error}</p>
      <div class="prefs-row">
        <button type="button" class="prefs-button primary" onclick={() => start(login.provider, login.again)}>{t('login.retry')}</button>
        <button type="button" class="prefs-button" onclick={() => node.closeLogin()}>{t('login.close')}</button>
      </div>
    {/if}
  </section>
{/if}

{#if node.note}
  <p class="prefs-hint" role="status">{node.note}</p>
{/if}
{#if node.changeError && !renaming && !removing}
  <p class="prefs-error" role="alert">{node.changeError}</p>
{/if}

{#if node.list && accesses.length === 0}
  <p class="prefs-hint">{t('access.empty')}</p>
{:else}
  <ul class="access-list">
    {#each accesses as access (access.id)}
      <li class="access" class:missing={access.missing}>
        <div class="access-main">
          <span class="badge" aria-hidden="true">{initial(providerTitle(access.provider))}</span>
          <div class="access-text">
            <span class="access-label">{access.label}</span>
            <span class="access-meta">{[providerTitle(access.provider), access.about].filter(Boolean).join(' · ')}</span>
            {#if access.uses?.length}
              <span class="access-uses">
                {#each access.uses as use (use)}
                  <span class="use" class:primary={use === 'owner'}>{t(useKey(use))}</span>
                {/each}
              </span>
            {/if}
            {#if access.missing}
              <span class="prefs-error">{t('access.missing')}</span>
            {/if}
          </div>
          {#if renaming !== access.id && removing !== access.id}
            <div class="access-actions">
              <button type="button" class="prefs-link" aria-label="{t('access.rename')}: {access.label}" onclick={() => rename(access)} disabled={working}>
                {t('access.rename')}
              </button>
              <button
                type="button"
                class="prefs-link"
                aria-label="{t('access.again')}: {access.label}"
                onclick={() => start(access.provider, access.id)}
                disabled={working || access.missing}
              >{t('access.again')}</button>
              <button
                type="button"
                class="prefs-link"
                aria-label="{t('access.remove')}: {access.label}"
                onclick={() => {
                  renaming = null;
                  node.clear();
                  removing = access.id;
                }}
                disabled={working}
              >{t('access.remove')}</button>
            </div>
          {/if}
        </div>

        {#if renaming === access.id}
          <form class="access-form" onsubmit={saveName} aria-label="{t('access.rename')}: {access.label}">
            <label class="prefs-label" for="access-name-{access.id}">{t('access.name')}</label>
            <input
              id="access-name-{access.id}"
              class="prefs-input"
              type="text"
              autocomplete="off"
              spellcheck="false"
              maxlength={MAX_NAME}
              placeholder={access.about || access.label}
              bind:value={name}
              bind:this={nameField}
              onkeydown={nameKey}
              disabled={node.busy !== null}
            />
            <p class="prefs-hint">{t('access.rename.hint')}</p>
            {#if node.changeError}
              <p class="prefs-error" role="alert">{node.changeError}</p>
            {/if}
            <div class="prefs-row">
              <button type="submit" class="prefs-button primary" disabled={node.busy !== null}>
                {node.busy === `rename:${access.id}` ? t('options.saving') : t('access.save')}
              </button>
              <button type="button" class="prefs-button" onclick={() => (renaming = null)}>{t('access.cancel')}</button>
            </div>
          </form>
        {:else if removing === access.id}
          <div class="access-form" role="alertdialog" aria-label="{t('access.remove')}: {access.label}">
            <p class="prefs-hint">{t('access.remove.hint')}</p>
            {#if node.changeError}
              <p class="prefs-error" role="alert">{node.changeError}</p>
            {/if}
            <div class="prefs-row">
              <button type="button" class="prefs-button danger" onclick={() => remove(access.id)} disabled={node.busy !== null}>
                {node.busy === `remove:${access.id}` ? t('options.saving') : t('access.remove')}
              </button>
              <button type="button" class="prefs-button" onclick={() => (removing = null)}>{t('access.cancel')}</button>
            </div>
          </div>
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  .add,
  .login {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 6px;
    padding: 14px 16px;
    background: var(--surface);
    border: 1px solid var(--line-strong);
    border-radius: 10px;
    box-shadow: var(--shadow);
  }

  .add .prefs-sub,
  .login .prefs-sub {
    margin-top: 0;
  }

  .provider-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 8px;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .provider-pick {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 9px 10px;
    font: inherit;
    text-align: left;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 8px;
    cursor: pointer;
    transition: border-color 120ms ease, background 120ms ease;
  }

  .provider-pick:hover {
    border-color: var(--accent-line);
    background: var(--accent-soft);
  }

  .provider-title {
    font-size: 13.5px;
    font-weight: 600;
    overflow-wrap: anywhere;
  }

  .badge {
    flex: none;
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    font-size: 13px;
    font-weight: 600;
    color: var(--accent);
    background: var(--accent-soft);
    border-radius: 8px;
  }

  .login-line {
    margin: 0;
    font-size: 13.5px;
    overflow-wrap: anywhere;
  }

  .login-link {
    color: var(--accent);
    word-break: break-all;
  }

  .login-form,
  .access-form {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .login-form .prefs-input {
    max-width: 520px;
  }

  .login-done {
    margin: 0;
    font-size: 13.5px;
  }

  .ok {
    color: var(--accent);
    font-weight: 700;
  }

  .muted {
    color: var(--muted);
  }

  .login-use {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }

  .login-use select {
    min-width: 220px;
  }

  .spinner {
    display: inline-block;
    width: 10px;
    height: 10px;
    margin-right: 8px;
    border: 2px solid var(--line-strong);
    border-top-color: var(--accent);
    border-radius: 50%;
    vertical-align: -1px;
    animation: spin 800ms linear infinite;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .spinner {
      animation: none;
    }
  }

  .access-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin: 10px 0 0;
    padding: 0;
    list-style: none;
  }

  .access {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px 14px;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 10px;
  }

  .access.missing {
    border-style: dashed;
  }

  .access-main {
    display: flex;
    align-items: flex-start;
    gap: 12px;
  }

  .access-text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .access-label {
    font-size: 14px;
    font-weight: 600;
    overflow-wrap: anywhere;
  }

  .access-meta {
    font-size: 12.5px;
    color: var(--muted);
    overflow-wrap: anywhere;
  }

  .access-uses {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-top: 4px;
  }

  .use {
    padding: 1px 7px;
    font-family: var(--mono);
    font-size: 10.5px;
    letter-spacing: 0.04em;
    color: var(--muted);
    border: 1px solid var(--line-strong);
    border-radius: 999px;
  }

  .use.primary {
    color: var(--accent);
    border-color: var(--accent-line);
    background: var(--accent-soft);
  }

  .access-actions {
    flex: none;
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 2px;
  }

  @media (max-width: 640px) {
    .access-main {
      flex-wrap: wrap;
    }

    .access-actions {
      width: 100%;
      justify-content: flex-start;
    }
  }
</style>

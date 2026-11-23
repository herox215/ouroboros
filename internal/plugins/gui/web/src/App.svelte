<script lang="ts">
  import { t } from './lib/i18n';
  import Transcript from './components/Transcript.svelte';
  import Composer from './components/Composer.svelte';
  import BoardView from './components/Board.svelte';
  import SessionsView from './components/Sessions.svelte';
  import AgentsView from './components/Agents.svelte';
  import JobsView from './components/Jobs.svelte';
  import MemoriesView from './components/Memories.svelte';
  import OptionsView from './components/Options.svelte';
  import type { Chat } from './lib/chat.svelte';
  import type { Board } from './lib/board.svelte';
  import type { Sessions } from './lib/sessions.svelte';
  import type { Agents } from './lib/agents.svelte';
  import type { Jobs } from './lib/jobs.svelte';
  import type { Memories } from './lib/memories.svelte';
  import type { Themes } from './lib/themes.svelte';
  import type { NodeOptions } from './lib/nodeoptions.svelte';
  import type { Gateways } from './lib/gateways.svelte';
  import type { AgentInfo } from './lib/wire';

  let {
    chat,
    board = null,
    sessions = null,
    agents = null,
    jobs = null,
    memories = null,
    themes = null,
    node = null,
    gateways = null,
  }: {
    chat: Chat | null;
    board?: Board | null;
    sessions?: Sessions | null;
    agents?: Agents | null;
    jobs?: Jobs | null;
    /** The memories of the agent, read only. */
    memories?: Memories | null;
    themes?: Themes | null;
    /** The options of the node: the accesses and the models. */
    node?: NodeOptions | null;
    /** The gateways of the profile: Matrix and the like. */
    gateways?: Gateways | null;
  } = $props();

  /** What the window shows: the thread, or the board, the sessions and the
   * agents beside it. The thread goes on either way; that what else is shown
   * is read only while it is shown. */
  type View = 'thread' | 'board' | 'sessions' | 'agents' | 'memories' | 'jobs' | 'options';
  let view = $state<View>('thread');

  /** The views sit in a sidebar on the left, open with their names or folded
   * to a rail of icons. The window remembers which, where it can; a narrow
   * window starts folded. */
  const SIDEBAR_KEY = 'ouro.sidebar';
  const narrow = () => typeof window !== 'undefined' && !!window.matchMedia?.('(max-width: 640px)').matches;

  function storedCollapsed(): boolean {
    try {
      const stored = localStorage.getItem(SIDEBAR_KEY);
      if (stored === 'collapsed') return true;
      if (stored === 'open') return false;
    } catch {
      // No storage here: the default holds.
    }
    return narrow();
  }

  let collapsed = $state(storedCollapsed());

  function toggleSidebar(): void {
    collapsed = !collapsed;
    try {
      localStorage.setItem(SIDEBAR_KEY, collapsed ? 'collapsed' : 'open');
    } catch {
      // Not remembered, still toggled.
    }
  }

  /** On a narrow window the open sidebar lies over the view: choosing one
   * folds it away again, without remembering that. */
  function choose(next: View): void {
    view = next;
    if (!collapsed && narrow()) collapsed = true;
  }

  const views = $derived(
    (
      [
        ['thread', true],
        ['board', !!board],
        ['sessions', !!sessions],
        ['jobs', !!jobs],
        ['agents', !!agents],
        ['memories', !!memories],
      ] as const
    )
      .filter(([, shown]) => shown)
      .map(([name]) => name),
  );
  /** The options sit apart, at the foot of the sidebar. */
  const options = $derived(!!themes);
  const sidebar = $derived(views.length + (options ? 1 : 0) > 1);

  $effect(() => {
    if (!chat) return;
    void chat.start();
    return () => chat.dispose();
  });

  $effect(() => {
    if (!board || view !== 'board') return;
    board.start();
    return () => board.stop();
  });

  $effect(() => {
    if (!sessions || view !== 'sessions') return;
    sessions.start();
    return () => sessions.stop();
  });

  $effect(() => {
    // The board, the sessions and the jobs name the agents they run as, and
    // the options those that go before the defaults.
    if (!agents || (view !== 'agents' && view !== 'sessions' && view !== 'jobs' && view !== 'board' && view !== 'options')) return;
    void agents.refresh();
  });

  $effect(() => {
    // The agents name the defaults of the options an agent without a model of
    // its own answers with.
    if (node && view === 'agents') void node.refresh();
  });

  $effect(() => {
    if (!jobs || view !== 'jobs') return;
    jobs.start();
    return () => jobs.stop();
  });

  $effect(() => {
    if (!memories || view !== 'memories') return;
    memories.start();
    return () => memories.stop();
  });

  /** The names of the agents by ID, as they were read last. */
  const names = $derived(Object.fromEntries((agents?.list ?? []).map((agent) => [agent.id, agent.name])));

  /** The agent of the thread is called as the profile: the head names the
   * profile by its new name at once. */
  function renamed(agent: AgentInfo): void {
    if (agent.kind === 'thread' && chat?.state) chat.state = { ...chat.state, profile: agent.name };
  }

  const question = $derived(!!chat?.entries.some((entry) => entry.role === 'question' && entry.open));
  const disabled = $derived(chat?.status !== 'ready');

  function tone(): string {
    if (!chat) return 'idle';
    if (chat.status !== 'ready') return chat.status;
    if (question) return 'waiting';
    return chat.busy ? 'working' : 'ready';
  }

  function statusText(): string {
    if (!chat) return '';
    switch (chat.status) {
      case 'loading':
        return t('status.loading');
      case 'offline':
        return t('status.offline');
      case 'restarting':
        return t('status.restarting');
      default:
        return question ? t('status.answering') : chat.busy ? t('status.working') : t('status.ready');
    }
  }
</script>

<svelte:head>
  <title>{chat?.title ? chat.title + ' · Ouroboros' : 'Ouroboros'}</title>
</svelte:head>

{#snippet icon(name: View)}
  <svg class="view-icon" viewBox="0 0 24 24" aria-hidden="true">
    {#if name === 'thread'}
      <path d="M4 5.5A1.5 1.5 0 0 1 5.5 4h13A1.5 1.5 0 0 1 20 5.5v9a1.5 1.5 0 0 1-1.5 1.5H10l-4.5 4v-4h0A1.5 1.5 0 0 1 4 14.5z" />
    {:else if name === 'board'}
      <rect x="3.5" y="4" width="5" height="16" rx="1.2" />
      <rect x="9.5" y="4" width="5" height="11" rx="1.2" />
      <rect x="15.5" y="4" width="5" height="7" rx="1.2" />
    {:else if name === 'sessions'}
      <path d="M12 3.5 20.5 8 12 12.5 3.5 8z" />
      <path d="M3.5 12 12 16.5 20.5 12" />
      <path d="M3.5 16 12 20.5 20.5 16" />
    {:else if name === 'jobs'}
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 7.5V12l3 2" />
    {:else if name === 'memories'}
      <path d="M6.5 3.5h11a1 1 0 0 1 1 1v16l-6.5-4-6.5 4v-16a1 1 0 0 1 1-1z" />
      <path d="M9.5 8h5M9.5 11h3" />
    {:else if name === 'options'}
      <path d="M4 7h9M17 7h3M4 17h3M11 17h9" />
      <circle cx="15" cy="7" r="2" />
      <circle cx="9" cy="17" r="2" />
    {:else}
      <circle cx="9" cy="8.5" r="3.2" />
      <path d="M3 19.5c0-3.3 2.7-5.5 6-5.5s6 2.2 6 5.5" />
      <path d="M15.5 5.6a3.2 3.2 0 0 1 0 5.8" />
      <path d="M17.5 14.4c2 .7 3.5 2.6 3.5 5.1" />
    {/if}
  </svg>
{/snippet}

{#if !chat}
  <main class="shell nowhere">
    <p>{t('nohost')}</p>
  </main>
{:else}
  <div class="frame" class:with-sidebar={sidebar} class:collapsed={sidebar && collapsed}>
  {#if sidebar}
    <aside class="sidebar" id="sidebar">
      <div class="side-head">
        <h1 class="brand">Ouroboros</h1>
        <button
          type="button"
          class="side-toggle"
          aria-controls="sidebar"
          aria-expanded={!collapsed}
          aria-label={collapsed ? t('sidebar.expand') : t('sidebar.collapse')}
          title={collapsed ? t('sidebar.expand') : t('sidebar.collapse')}
          onclick={toggleSidebar}
        >
          <svg class="view-icon" viewBox="0 0 24 24" aria-hidden="true">
            <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
            <path d="M9.5 4.5v15" />
            {#if collapsed}
              <path d="m13.5 10 2 2-2 2" />
            {:else}
              <path d="m16 10-2 2 2 2" />
            {/if}
          </svg>
        </button>
      </div>
      <nav class="views" aria-label={t('view.label')}>
        {#each views as name (name)}
          <button
            type="button"
            aria-pressed={view === name}
            title={collapsed ? t(`view.${name}`) : undefined}
            onclick={() => choose(name)}
          >{@render icon(name)}<span class="view-name">{t(`view.${name}`)}</span></button>
        {/each}
      </nav>
      {#if options}
        <nav class="views views-foot" aria-label={t('options.label')}>
          <button
            type="button"
            aria-pressed={view === 'options'}
            title={collapsed ? t('view.options') : undefined}
            onclick={() => choose('options')}
          >{@render icon('options')}<span class="view-name">{t('view.options')}</span></button>
        </nav>
      {/if}
    </aside>
  {/if}
  <main class="shell" class:wide={view !== 'thread'}>
    <header class="top">
      {#if !sidebar}
        <h1 class="brand">Ouroboros</h1>
      {/if}
      <p class="profile">{chat.state?.profile ?? ''}</p>
      <p class="state" data-tone={tone()} role="status">
        <span class="led" aria-hidden="true"></span>
        <span>{statusText()}</span>
      </p>
    </header>

    {#if board && view === 'board'}
      <BoardView {board} {names} />
    {:else if sessions && view === 'sessions'}
      <SessionsView {sessions} {names} />
    {:else if agents && view === 'agents'}
      <AgentsView {agents} {node} onrenamed={renamed} />
    {:else if jobs && view === 'jobs'}
      <JobsView {jobs} {names} />
    {:else if memories && view === 'memories'}
      <MemoriesView {memories} />
    {:else if themes && view === 'options'}
      <OptionsView {themes} {node} {gateways} {agents} onagents={agents ? () => choose('agents') : undefined} />
    {:else}
      <Transcript
        entries={chat.entries}
        sending={chat.sending}
        hasMore={chat.hasMore}
        loadingOlder={chat.loadingOlder}
        historyError={chat.historyError}
        onolder={() => chat.loadOlder()}
        onanswer={async (text) => {
          await chat.send(text);
        }}
        empty={chat.status === 'ready' && chat.entries.length === 0}
      />

      {#if chat.status === 'offline'}
        <div class="banner" role="alert">
          <p>{chat.error || t('offline.hint')}</p>
          <button type="button" onclick={() => chat.open()}>{t('retry')}</button>
        </div>
      {:else if chat.error}
        <p class="banner" role="alert">{chat.error}</p>
      {/if}

      {#if chat.status === 'restarting'}
        <p class="notice" role="status">{t('restarting.hint')}</p>
      {/if}

    {/if}

    <!-- The field stays while the board is shown, hidden: what was typed
         into it is still there when the owner comes back to the thread. -->
    <div class="compose" hidden={view !== 'thread'}>
      <Composer
        {disabled}
        busy={chat.busy}
        sending={chat.sending}
        {question}
        queued={chat.queued}
        onsend={(text) => chat.send(text)}
        onstop={() => chat.stop()}
      />
    </div>

  </main>
  </div>
{/if}

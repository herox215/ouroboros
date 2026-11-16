<script lang="ts">
  import type { Sessions } from '../lib/sessions.svelte';
  import type { WireEvent } from '../lib/wire';
  import { markdown } from '../lib/markdown';
  import { t } from '../lib/i18n';
  import { ago, exact } from '../lib/time';
  import { roleName, shortID } from '../lib/boardtext';
  import { agentName } from '../lib/agentkind';

  /** names are the names of the agents by ID, as they were read last: a
   * session says which of them it runs as. */
  let { sessions, names = {} }: { sessions: Sessions; names?: Record<string, string> } = $props();

  const list = $derived(sessions.list ?? []);
  const track = $derived(sessions.track);
  const events = $derived(track?.events ?? []);

  /** The time of day of an event: a session is read like a log, not dated
   * like the board. */
  function clock(at?: string): string {
    if (!at) return '';
    return new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  /** Whether the text of the event is what the agent wrote, which reads as
   * markdown, or a line of a tool. */
  function written(event: WireEvent): boolean {
    return event.kind === 'answer' || event.kind === 'say';
  }

  function mark(event: WireEvent): string {
    switch (event.kind) {
      case 'message':
        return '»';
      case 'step':
        return event.failed ? '✗' : event.done ? '✓' : '›';
      case 'failed':
        return '✗';
      case 'asked':
        return '?';
      case 'picture':
        return '▣';
      default:
        return '◆';
    }
  }
</script>

<section class="sessions" aria-label={t('sessions.label')}>
  {#if sessions.error}
    <p class="banner" role="alert">{t('sessions.unreadable')}: {sessions.error}</p>
  {/if}

  <div class="session-cols">
    <div class="session-list">
      {#if !sessions.list}
        {#if !sessions.error}
          <p class="board-wait" role="status">{t('sessions.loading')}</p>
        {/if}
      {:else if list.length === 0}
        <div class="empty">
          <p class="empty-title">{t('sessions.empty.title')}</p>
          <p class="empty-body">{t('sessions.empty.body')}</p>
        </div>
      {:else}
        <ol class="rows">
          {#each list as session (session.id)}
            <li>
              <button
                type="button"
                class="row"
                aria-pressed={sessions.opened === session.id}
                onclick={() => sessions.open(session.id)}
              >
                <span class="row-head">
                  <span class="led" aria-hidden="true" class:on={session.running}></span>
                  <span class="row-title">{session.title || t('sessions.untitled')}</span>
                  <span class="row-id" title={session.id}>{shortID(session.id)}</span>
                </span>
                {#if session.ticket}
                  <span class="row-work">
                    <span class="row-ticket">#{session.ticket}</span>
                    <span class="row-ticket-title">{session.work}</span>
                    {#if session.role}
                      <span class="row-role">{roleName(session.role)}</span>
                    {/if}
                  </span>
                {/if}
                {#if session.jobs?.length}
                  {#each session.jobs as job (job.n)}
                    <span class="row-job"><span aria-hidden="true">›</span> {job.command}</span>
                  {/each}
                {/if}
                <span class="row-meta" title={exact(session.last)}>
                  {session.running ? t('sessions.running') : t('sessions.idle')}
                  {#if session.agent}
                    · {t('sessions.as')} <span class="row-agent" data-agent={session.agent}>{agentName(session.agent, names)}</span>
                  {/if}
                  {#if session.last}
                    · {t('sessions.last')} {ago(session.last, sessions.now)}
                  {/if}
                </span>
              </button>
            </li>
          {/each}
        </ol>
      {/if}
    </div>

    {#if sessions.opened}
      <aside class="session-detail" aria-label={t('sessions.detail')}>
        <header class="detail-head">
          <p class="detail-id" title={sessions.opened}>{shortID(sessions.opened)}</p>
          <button type="button" class="detail-close" aria-label={t('sessions.close')} onclick={() => sessions.close()}>
            <span aria-hidden="true">×</span>
          </button>
          <h2>{track?.title || t('sessions.untitled')}</h2>
          {#if track}
            <p class="detail-meta">
              <span>{track.running ? t('sessions.running') : t('sessions.idle')}</span>
              {#if track.last}
                <span title={exact(track.last)}>{t('sessions.last')} {ago(track.last, sessions.now)}</span>
              {/if}
              {#if track.ticket}
                <span class="row-ticket">#{track.ticket}</span>
                <span>{track.work}</span>
                {#if track.role}
                  <span>{roleName(track.role)}</span>
                {/if}
              {/if}
            </p>
          {/if}
        </header>

        <div class="detail-body">
          {#if sessions.trackError}
            <p class="error" role="alert">{sessions.trackError}</p>
          {/if}
          {#if !track}
            {#if !sessions.trackError}
              <p class="board-wait" role="status">{t('sessions.loading')}</p>
            {/if}
          {:else}
            {#if track.jobs?.length}
              <section class="detail-part">
                <h3>{t('sessions.background')}</h3>
                <ul class="detail-jobs">
                  {#each track.jobs as job (job.n)}
                    <li><span aria-hidden="true">›</span> <code>{job.command}</code></li>
                  {/each}
                </ul>
              </section>
            {/if}

            <section class="detail-part">
              <h3>{t('sessions.lately')}</h3>
              {#if events.length === 0}
                <p class="detail-none">{t('sessions.nothing')}</p>
              {:else}
                <ol class="track">
                  {#each events as event, index (index)}
                    <li class="track-line" data-kind={event.kind} class:failed={event.failed}>
                      <span class="track-at" title={exact(event.at)}>{clock(event.at)}</span>
                      <span class="track-mark" aria-hidden="true">{mark(event)}</span>
                      {#if written(event)}
                        <div class="markdown" use:markdown={event.text ?? ''}></div>
                      {:else}
                        <span class="track-text">{event.text}</span>
                      {/if}
                    </li>
                  {/each}
                </ol>
              {/if}
            </section>
          {/if}
        </div>
      </aside>
    {/if}
  </div>
</section>

<style>
  .sessions {
    display: flex;
    flex-direction: column;
    min-height: 0;
    flex: 1;
    gap: 12px;
  }

  .session-cols {
    display: flex;
    gap: 16px;
    min-height: 0;
    flex: 1;
  }

  .session-list {
    flex: 1 1 42%;
    min-width: 0;
    overflow-y: auto;
    padding-right: 4px;
  }

  .rows {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .row {
    display: flex;
    flex-direction: column;
    gap: 4px;
    width: 100%;
    padding: 10px 12px;
    text-align: left;
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 8px;
    cursor: pointer;
  }

  .row:hover {
    border-color: var(--line-strong);
  }

  .row[aria-pressed='true'] {
    border-color: var(--accent-line);
    background: var(--accent-soft);
  }

  .row-head {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  .row-title {
    flex: 1;
    min-width: 0;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .row-id {
    color: var(--muted);
    font-family: var(--mono);
    font-size: 0.8em;
  }

  .row-work {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--muted);
    font-size: 0.85em;
    min-width: 0;
  }

  .row-ticket {
    font-family: var(--mono);
    color: var(--accent);
  }

  .row-ticket-title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .row-role {
    color: var(--muted);
  }

  .row-job {
    color: var(--muted);
    font-family: var(--mono);
    font-size: 0.8em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .row-meta {
    color: var(--muted);
    font-size: 0.8em;
  }

  .row-agent {
    color: var(--text);
  }

  .led {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--line-strong);
    flex: none;
  }

  .led.on {
    background: var(--accent);
    animation: session-pulse 1.6s ease-in-out infinite;
  }

  @keyframes session-pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.35;
    }
  }

  .session-detail {
    flex: 1 1 58%;
    min-width: 0;
    display: flex;
    flex-direction: column;
    min-height: 0;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: var(--shadow);
  }

  .detail-head {
    position: relative;
    padding: 14px 16px 10px;
    border-bottom: 1px solid var(--line);
  }

  .detail-id {
    margin: 0;
    color: var(--muted);
    font-family: var(--mono);
    font-size: 0.8em;
  }

  .detail-close {
    position: absolute;
    top: 10px;
    right: 10px;
    width: 28px;
    height: 28px;
    font: inherit;
    color: var(--muted);
    background: none;
    border: 1px solid transparent;
    border-radius: 6px;
    cursor: pointer;
  }

  .detail-close:hover {
    border-color: var(--line);
    color: var(--text);
  }

  .detail-head h2 {
    margin: 2px 0 6px;
    font-size: 1.05em;
  }

  .detail-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    margin: 0;
    color: var(--muted);
    font-size: 0.85em;
  }

  .detail-body {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 12px 16px 16px;
    overflow-y: auto;
    min-height: 0;
  }

  .detail-part h3 {
    margin: 0 0 6px;
    font-size: 0.8em;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--muted);
  }

  .detail-jobs {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 0.85em;
    color: var(--muted);
  }

  .detail-jobs code {
    font-family: var(--mono);
    color: var(--text);
  }

  .detail-none {
    margin: 0;
    color: var(--muted);
    font-size: 0.9em;
  }

  .track {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .track-line {
    display: grid;
    grid-template-columns: 44px 14px 1fr;
    gap: 6px;
    font-size: 0.9em;
  }

  .track-at {
    color: var(--muted);
    font-family: var(--mono);
    font-size: 0.8em;
    padding-top: 2px;
  }

  .track-mark {
    color: var(--muted);
    text-align: center;
  }

  .track-line[data-kind='message'] .track-mark {
    color: var(--accent);
  }

  .track-line.failed .track-mark {
    color: var(--err);
  }

  .track-text {
    min-width: 0;
    overflow-wrap: anywhere;
  }

  .track-line[data-kind='step'] .track-text {
    font-family: var(--mono);
    font-size: 0.85em;
    color: var(--muted);
  }

  @media (max-width: 720px) {
    .session-cols {
      flex-direction: column;
    }
  }
</style>

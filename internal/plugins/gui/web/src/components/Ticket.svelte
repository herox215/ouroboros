<script lang="ts">
  import { onMount } from 'svelte';
  import type { Ticket } from '../lib/wire';
  import { markdown } from '../lib/markdown';
  import { t } from '../lib/i18n';
  import { ago, exact } from '../lib/time';
  import { byName, laneName, roleName, shortID } from '../lib/boardtext';
  import { workerName } from '../lib/agentkind';

  let {
    id,
    ticket,
    error = '',
    now,
    names = {},
    onclose,
  }: { id: number; ticket: Ticket | null; error?: string; now: number; names?: Record<string, string>; onclose: () => void } = $props();

  let closer = $state<HTMLButtonElement | null>(null);

  // The latest note says how the ticket stands: it comes first.
  const notes = $derived(ticket?.notes ? [...ticket.notes].reverse() : []);
  const runs = $derived(ticket?.runs ? [...ticket.runs].reverse() : []);

  onMount(() => closer?.focus());

  function keydown(event: KeyboardEvent) {
    if (event.key !== 'Escape') return;
    event.preventDefault();
    onclose();
  }
</script>

<svelte:window onkeydown={keydown} />

<div class="ticket" role="dialog" aria-labelledby="ticket-title">
  <header class="ticket-head">
    <p class="ticket-id">
      <span>#{id}</span>
      {#if ticket}
        <span class="chip" data-column={ticket.column}>{laneName(ticket.column)}</span>
      {/if}
    </p>
    <button type="button" class="ticket-close" bind:this={closer} aria-label={t('ticket.close')} onclick={onclose}>
      <span aria-hidden="true">×</span>
    </button>
    <h2 id="ticket-title">{ticket?.title ?? ''}</h2>
    {#if ticket}
      <p class="ticket-meta">
        <span title={exact(ticket.created)}>{t('ticket.created')} {ago(ticket.created, now)}</span>
        <span title={exact(ticket.changed)}>{t('card.changed')} {ago(ticket.changed, now)}</span>
        {#if ticket.told}
          <span>✓ {t('card.told')}</span>
        {/if}
      </p>
    {/if}
  </header>

  <div class="ticket-body">
    {#if error}
      <p class="error" role="alert">{error}</p>
    {/if}
    {#if !ticket}
      {#if !error}
        <p class="board-wait" role="status">{t('ticket.loading')}</p>
      {/if}
    {:else}
      {#if ticket.column === 'blocked' && ticket.reason}
        <section class="ticket-part blocked">
          <h3>{t('ticket.blocked')}</h3>
          <p class="plain">{ticket.reason}</p>
          {#if ticket.from}
            <p class="ticket-from">{t('ticket.from')} {laneName(ticket.from)}</p>
          {/if}
        </section>
      {/if}

      {#if ticket.result}
        <section class="ticket-part result">
          <h3>{t('ticket.result')}</h3>
          <div class="markdown" use:markdown={ticket.result}></div>
        </section>
      {/if}

      <section class="ticket-part">
        <h3>{t('ticket.text')}</h3>
        <div class="markdown" use:markdown={ticket.text}></div>
      </section>

      {#if ticket.directory}
        <section class="ticket-part">
          <h3>{t('ticket.directory')}</h3>
          <p class="ticket-directory">{ticket.directory}</p>
        </section>
      {/if}

      <section class="ticket-part">
        <h3>{t('ticket.sessions')}</h3>
        {#if runs.length === 0}
          <p class="ticket-none">{t('ticket.nosessions')}</p>
        {:else}
          <ul class="runs">
            {#each runs as run (run.session)}
              <li class="run" class:working={!run.ended}>
                <span class="led" aria-hidden="true"></span>
                <span class="run-role">{roleName(run.role)}</span>
                {#if workerName(run.agent, run.agent_name, names)}
                  <span class="run-agent" data-agent={run.agent}>{workerName(run.agent, run.agent_name, names)}</span>
                {/if}
                <span class="run-session" title={run.session}>{shortID(run.session)}</span>
                {#if run.ended}
                  <span class="run-how" title={exact(run.ended)}>{t('run.ended')} {ago(run.ended, now)} · {run.outcome ? laneName(run.outcome) : ''}</span>
                {:else}
                  <span class="run-how" title={exact(run.began)}>{t('run.working')} · {ago(run.began, now)}</span>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      </section>

      <section class="ticket-part">
        <h3>{t('ticket.notes')}</h3>
        {#if notes.length === 0}
          <p class="ticket-none">{t('ticket.nonotes')}</p>
        {:else}
          <ol class="notes">
            {#each notes as note, index (index)}
              <li class="ticket-note" data-by={note.by.startsWith('session ') ? 'session' : note.by}>
                <p class="note-head">
                  <span class="note-by">{byName(note.by)}</span>
                  <span title={exact(note.at)}>{ago(note.at, now)}</span>
                </p>
                <p class="plain">{note.text}</p>
              </li>
            {/each}
          </ol>
        {/if}
      </section>
    {/if}
  </div>
</div>

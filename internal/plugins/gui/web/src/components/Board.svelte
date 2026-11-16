<script lang="ts">
  import type { Board } from '../lib/board.svelte';
  import type { Card } from '../lib/wire';
  import { t } from '../lib/i18n';
  import { ago, exact } from '../lib/time';
  import { laneName, roleName } from '../lib/boardtext';
  import { workerName } from '../lib/agentkind';
  import TicketPanel from './Ticket.svelte';

  /** names are the names of the agents by ID, as they were read last: a card
   * says which of them works on its ticket. */
  let { board, names = {} }: { board: Board; names?: Record<string, string> } = $props();

  const total = $derived(board.view?.lanes.reduce((sum, lane) => sum + lane.cards.length, 0) ?? 0);

  /** The card the open ticket was opened from: it gets the focus back. */
  let opener: HTMLElement | null = null;

  function open(card: Card, event: MouseEvent) {
    opener = event.currentTarget as HTMLElement;
    void board.open(card.id);
  }

  function close() {
    board.close();
    opener?.focus();
    opener = null;
  }
</script>

<section class="board" aria-label={t('board.label')}>
  {#if board.error}
    <p class="banner" role="alert">{t('board.unreadable')}: {board.error}</p>
  {/if}

  {#if !board.view}
    {#if !board.error}
      <p class="board-wait" role="status">{t('board.loading')}</p>
    {/if}
  {:else if total === 0}
    <div class="empty">
      <p class="empty-title">{t('board.empty.title')}</p>
      <p class="empty-body">{t('board.empty.body')}</p>
    </div>
  {:else}
    <div class="lanes">
      {#each board.view.lanes as lane (lane.column)}
        <section class="lane" data-column={lane.column} aria-labelledby={'lane-' + lane.column}>
          <header class="lane-head">
            <span class="lane-dot" aria-hidden="true"></span>
            <h2 id={'lane-' + lane.column}>{laneName(lane.column)}</h2>
            <span class="lane-count">{lane.cards.length}</span>
          </header>
          {#if lane.cards.length === 0}
            <p class="lane-empty">{t('lane.empty')}</p>
          {:else}
            <ul class="cards">
              {#each lane.cards as card (card.id)}
                <li>
                  <button
                    type="button"
                    class="card"
                    aria-haspopup="dialog"
                    aria-expanded={board.opened === card.id}
                    onclick={(event) => open(card, event)}
                  >
                    <span class="card-id">#{card.id}</span>
                    <span class="card-title">{card.title}</span>
                    {#if card.role}
                      <span class="card-work">
                        <span class="led" aria-hidden="true"></span>
                        <span>{roleName(card.role)} · {ago(card.since, board.now)}</span>
                      </span>
                      {#if workerName(card.agent, card.agent_name, names)}
                        <span class="card-agent" data-agent={card.agent}>{t('card.agent')} {workerName(card.agent, card.agent_name, names)}</span>
                      {/if}
                    {/if}
                    {#if card.reason}
                      <span class="card-reason">{card.reason}</span>
                    {/if}
                    <span class="card-meta" title={exact(card.changed)}>
                      <span>{t('card.changed')} {ago(card.changed, board.now)}</span>
                      {#if card.notes}
                        <span>{card.notes} {t(card.notes === 1 ? 'card.note' : 'card.notes')}</span>
                      {/if}
                      {#if card.told}
                        <span class="card-told">✓ {t('card.told')}</span>
                      {/if}
                    </span>
                  </button>
                </li>
              {/each}
            </ul>
          {/if}
        </section>
      {/each}
    </div>
  {/if}

  {#if board.view}
    <p class="board-foot">
      {#if board.view.looked}
        <span title={exact(board.view.looked.at)}>
          {t('board.looked')} {ago(board.view.looked.at, board.now)}{board.view.looked.result ? ': ' + board.view.looked.result : '.'}
        </span>
      {:else}
        <span>{t('board.never')}</span>
      {/if}
      {#if board.view.pending}
        <span class="board-pending">{t('board.pending')}</span>
      {/if}
    </p>
  {/if}

  {#if board.opened !== null}
    <TicketPanel id={board.opened} ticket={board.ticket} error={board.ticketError} now={board.now} {names} onclose={close} />
  {/if}
</section>

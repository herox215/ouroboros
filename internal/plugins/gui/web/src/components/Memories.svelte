<script lang="ts">
  import { filtered, topicsOf, type Memories } from '../lib/memories.svelte';
  import { t } from '../lib/i18n';
  import { exact } from '../lib/time';

  let { memories }: { memories: Memories } = $props();

  /** The topic the list is narrowed to, '' for all of them. */
  let topic = $state('');
  let query = $state('');

  const list = $derived(memories.list?.memories ?? []);
  const topics = $derived(memories.list ? topicsOf(memories.list) : []);
  /** A topic that went with the last read leaves the list whole. */
  const chosen = $derived(topics.find((candidate) => candidate.name.toLowerCase() === topic.toLowerCase())?.name ?? '');
  const shown = $derived(memories.list ? filtered(memories.list, chosen, query) : []);
  /** How many memories there are that are not expired: more than the list
   * holds when the window reads only the latest. */
  const alive = $derived((memories.list?.total ?? 0) - (memories.list?.hidden ?? 0));

  const isCore = (name: string) => name.toLowerCase() === 'core';

  /** The text of key with {n} and {total} filled in. */
  function said(key: 'memories.hidden' | 'memories.limited', values: Record<string, number>): string {
    return t(key).replace(/\{(\w+)\}/g, (_, name: string) => String(values[name] ?? ''));
  }
</script>

<section class="memories" aria-label={t('memories.label')}>
  {#if memories.error}
    <p class="banner" role="alert">{t('memories.unreadable')}: {memories.error}</p>
  {/if}

  {#if !memories.list}
    {#if !memories.error}
      <p class="board-wait" role="status">{t('memories.loading')}</p>
    {/if}
  {:else if list.length === 0}
    <div class="empty">
      <p class="empty-title">{t('memories.empty.title')}</p>
      <p>{t('memories.empty.body')}</p>
    </div>
  {:else}
    <div class="memory-filter">
      <div class="memory-topics" role="group" aria-label={t('memories.label')}>
        <button type="button" class="memory-topic" aria-pressed={chosen === ''} onclick={() => (topic = '')}>
          {t('memories.all')} ({list.length})
        </button>
        {#each topics as entry (entry.name.toLowerCase())}
          <button
            type="button"
            class="memory-topic"
            aria-pressed={chosen === entry.name}
            onclick={() => (topic = entry.name)}
          >{entry.name} ({entry.count})</button>
        {/each}
      </div>
      <input
        class="memory-search"
        type="search"
        placeholder={t('memories.search')}
        aria-label={t('memories.search')}
        bind:value={query}
      />
    </div>

    {#if shown.length === 0}
      <p class="memory-nomatch" role="status">{t('memories.nomatch')}</p>
    {:else}
      <ul class="card-grid">
        {#each shown as memory (memory.id)}
          <li class="memory" data-topic={memory.topic}>
            <div class="memory-head">
              <h2 class="memory-key">{memory.key}</h2>
              {#if chosen === ''}
                <span class="memory-tag" class:core={isCore(memory.topic)}>{memory.topic}</span>
              {/if}
            </div>
            <p class="memory-content">{memory.content}</p>
            <p class="memory-meta">{t('memories.updated')} <time datetime={memory.updated}>{exact(memory.updated)}</time></p>
          </li>
        {/each}
      </ul>
    {/if}

    {#if memories.list.hidden > 0 || list.length < alive}
      <p class="memory-foot">
        {#if list.length < alive}{said('memories.limited', { n: list.length, total: alive })}{/if}
        {#if memories.list.hidden > 0}{said('memories.hidden', { n: memories.list.hidden })}{/if}
      </p>
    {/if}
  {/if}
</section>

<style>
  .memories {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 18px 0 12px;
    overflow-y: auto;
  }

  .memory-filter {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 14px;
  }

  .memory-topics {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .memory-topic {
    padding: 3px 11px;
    font: inherit;
    font-size: 13px;
    color: var(--muted);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 999px;
    cursor: pointer;
  }

  .memory-topic:hover {
    border-color: var(--line-strong);
  }

  .memory-topic[aria-pressed='true'] {
    color: var(--accent);
    background: var(--accent-soft);
    border-color: var(--accent-line);
  }

  .memory-search {
    flex: 1 1 180px;
    min-width: 0;
    padding: 6px 10px;
    font: inherit;
    color: var(--text);
    background-color: var(--field);
    border: 1px solid var(--line-strong);
    border-radius: 6px;
  }

  .memory {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 14px 18px;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: var(--shadow);
  }

  .memory-head {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 4px 10px;
  }

  .memory-key {
    margin: 0;
    font-size: 1.05em;
    font-weight: 600;
    overflow-wrap: anywhere;
  }

  .memory-tag {
    font-family: var(--mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
  }

  .memory-tag.core {
    color: var(--accent);
  }

  .memory-content {
    margin: 0;
    font-size: 14px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  .memory-meta {
    margin: 0;
    font-size: 12px;
    color: var(--muted);
  }

  .memory-nomatch {
    margin: 12px 0;
    font-size: 13.5px;
    color: var(--muted);
  }

  .memory-foot {
    margin: 0;
    font-family: var(--mono);
    font-size: 12px;
    color: var(--muted);
  }
</style>

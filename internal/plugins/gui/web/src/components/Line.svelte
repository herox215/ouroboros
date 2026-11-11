<script lang="ts">
  import type { Entry } from '../lib/chat.svelte';
  import { markdown } from '../lib/markdown';
  import { t } from '../lib/i18n';

  let {
    entry,
    sending = false,
    onanswer,
  }: { entry: Entry; sending?: boolean; onanswer: (text: string) => void } = $props();
</script>

{#if entry.role === 'owner'}
  <div class="owner">
    <div class="bubble" title={entry.at}>{entry.text}</div>
  </div>
{:else if entry.role === 'agent'}
  <div class="agent markdown" class:streaming={entry.streaming} use:markdown={entry.text}></div>
{:else if entry.role === 'tool'}
  <div class="tool" class:done={entry.done} class:failed={entry.failed}>
    <span class="bullet" aria-hidden="true">{entry.failed ? '✗' : entry.done ? '✓' : '›'}</span>
    <span class="tool-text">{entry.text}</span>
  </div>
{:else if entry.role === 'question'}
  <section class="question" class:open={entry.open} aria-label={entry.text}>
    <p class="question-text">{entry.text}</p>
    {#if entry.open}
      {#if entry.options?.length}
        <div class="options">
          {#each entry.options as option (option)}
            <button
              type="button"
              class:recommended={option === entry.recommended}
              disabled={sending}
              onclick={() => onanswer(option)}
            >
              <span>{option}</span>
              {#if option === entry.recommended}
                <span class="tag">{t('question.recommended')}</span>
              {/if}
            </button>
          {/each}
        </div>
      {/if}
    {:else if entry.answered}
      <p class="answered">{t('question.answered')} · {entry.answered}</p>
    {/if}
  </section>
{:else if entry.role === 'error'}
  <div class="error" role="alert">{entry.text}</div>
{:else}
  <div class="note">{entry.text}</div>
{/if}

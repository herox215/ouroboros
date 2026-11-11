<script lang="ts">
  import type { Entry } from '../lib/chat.svelte';
  import Line from './Line.svelte';
  import { t } from '../lib/i18n';

  let {
    entries,
    sending,
    onanswer,
    empty,
  }: {
    entries: Entry[];
    sending: boolean;
    onanswer: (text: string) => void;
    empty: boolean;
  } = $props();

  let scroller = $state<HTMLElement | null>(null);
  let following = $state(true);

  // Whether the end of the history is in view: only then does new output move
  // it, as in the terminal UI. Within 56 px of the end still counts as there.
  const near = (): boolean => {
    if (!scroller) return true;
    return scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 56;
  };
  function onscroll(): void {
    following = near();
  }
  function jump(): void {
    following = true;
    if (scroller) scroller.scrollTop = scroller.scrollHeight;
  }

  $effect(() => {
    const last = entries[entries.length - 1];
    // Reading the length and the text makes this run for a new line and for
    // every piece of the answer on its way.
    void entries.length;
    void last?.text;
    if (!following || !scroller) return;
    scroller.scrollTop = scroller.scrollHeight;
  });
</script>

<div class="transcript-wrap">
  <!-- svelte-ignore a11y_no_noninteractive_tabindex (a log that scrolls is focusable, so that it can be scrolled by keyboard) -->
  <div
    class="transcript"
    bind:this={scroller}
    {onscroll}
    role="log"
    tabindex="0"
    aria-label={t('transcript.label')}
  >
    {#if empty}
      <div class="empty">
        <p class="empty-title">{t('empty.title')}</p>
        <p class="empty-body">{t('empty.body')}</p>
      </div>
    {:else}
      <ol class="lines">
        {#each entries as entry (entry.id)}
          <li class="line" data-role={entry.role}>
            <Line {entry} {sending} {onanswer} />
          </li>
        {/each}
      </ol>
    {/if}
  </div>
  {#if !following && !empty}
    <button class="jump" type="button" onclick={jump} aria-label={t('jump.latest')} title={t('jump.latest')}>
      <span aria-hidden="true">↓</span>
    </button>
  {/if}
</div>

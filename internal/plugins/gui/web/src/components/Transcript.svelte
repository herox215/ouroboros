<script lang="ts">
  import { tick, untrack } from 'svelte';
  import type { Entry } from '../lib/chat.svelte';
  import { VirtualRows } from '../lib/virtual';
  import Line from './Line.svelte';
  import { t } from '../lib/i18n';

  let {
    entries, sending, onanswer, empty,
    hasMore = false, loadingOlder = false, historyError = '', onolder = async () => {},
  }: {
    entries: Entry[];
    sending: boolean;
    onanswer: (text: string) => void;
    empty: boolean;
    hasMore?: boolean;
    loadingOlder?: boolean;
    historyError?: string;
    onolder?: () => Promise<void>;
  } = $props();

  let scroller = $state<HTMLElement | null>(null);
  let list = $state<HTMLOListElement | null>(null);
  let following = $state(true);
  let top = $state(0);
  let viewport = $state(600);
  let geometry = $state.raw(new VirtualRows([], new Map()));
  const heights = new Map<number, number>();
  let owed = 0;
  let revision = 0;
  let adjusting = false;
  let armed = false;
  let away = false;
  let dragging = false;
  let observer: ResizeObserver | undefined;

  const range = $derived(geometry.window(top, viewport));
  const visible = $derived(entries.slice(range.start, range.end));

  function inset(): number {
    if (!list || !scroller) return 0;
    return list.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
  }

  function position(): void {
    if (!scroller) return;
    top = scroller.scrollTop - inset();
    viewport = scroller.clientHeight;
  }

  // The view follows what changed above the row it shows, and nothing else:
  // new live output can arrive during paging, and measurements can change on
  // either side of the viewport. The scroll position is moved by that much
  // rather than set, and left alone when nothing above moved: writing it ends
  // a wheel scroll that is still on its way, as every piece of an answer
  // streaming in below would.
  async function layout(): Promise<void> {
    if (!scroller) return;
    const current = ++revision;
    adjusting = true;
    const next = new VirtualRows(entries.map((entry) => entry.id), heights);
    const shift = following ? 0 : geometry.moved(top, next);
    geometry = next;
    // A layout that a later one overtakes has not moved the scroller yet.
    owed = following ? 0 : owed + shift;
    if (following) top = Math.max(0, geometry.total - scroller.clientHeight);
    else top = Math.max(0, top + shift);
    await tick();
    if (current !== revision || !scroller) return;
    if (following) scroller.scrollTop = scroller.scrollHeight;
    else if (Math.abs(owed) >= 0.5) { scroller.scrollTop += owed; owed = 0; }
    position();
    adjusting = false;
  }

  function maybeOlder(): void {
    if (armed && !adjusting && scroller && scroller.scrollTop < 240 && hasMore && !loadingOlder) {
      armed = false;
      following = false;
      void onolder();
    }
  }

  function atEnd(): boolean {
    return !!scroller && scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 56;
  }

  function onscroll(): void {
    position();
    if (adjusting || !scroller) return;
    // Layout, clamping and ResizeObserver also generate scroll events. Only
    // the owner switches off tail following, by scrolling up or by dragging;
    // otherwise a growing last row can strand the viewport just above the
    // bottom before it is measured. And once they scrolled up, no scroll
    // event takes them back: the first one of a wheel scroll is still at the
    // end, and so is the late one of the last time the view followed.
    if (dragging) following = atEnd();
    else if (!away && atEnd()) following = true;
    maybeOlder();
  }

  function jump(): void {
    following = true;
    away = false;
    armed = false;
    void layout();
  }

  function onkeydown(event: KeyboardEvent): void {
    if (!scroller || event.target !== scroller) return;
    if (event.key === 'End') { event.preventDefault(); jump(); return; }
    const page = scroller.clientHeight * 0.9;
    const delta: Record<string, number> = {
      ArrowUp: -40, ArrowDown: 40, PageUp: -page, PageDown: page,
      Home: -scroller.scrollHeight, ' ': event.shiftKey ? -page : page,
    };
    const amount = delta[event.key];
    if (amount === undefined) return;
    // Native animated keyboard scrolling can be canceled by a height
    // correction from virtualization. Move deterministically instead.
    event.preventDefault();
    away = amount < 0;
    if (away) { following = false; armed = true; }
    scroller.scrollTop += amount;
    onscroll();
  }

  function measure(node: HTMLElement, id: number) {
    node.dataset.entryId = String(id);
    observer?.observe(node);
    return { destroy() { observer?.unobserve(node); } };
  }

  $effect(() => {
    if (!scroller) return;
    const node = scroller;
    let width = node.clientWidth;
    if (typeof ResizeObserver !== 'undefined') {
      observer = new ResizeObserver((records) => {
        let changed = records.some((record) => record.target === node);
        if (width !== node.clientWidth) {
          width = node.clientWidth;
          // Invalidate offscreen measurements, then measure every mounted row
          // at the new width, including those whose height did not change.
          // Observer record ordering must not discard fresh measurements.
          heights.clear();
          changed = true;
        }
        for (const row of node.querySelectorAll<HTMLElement>('[data-entry-id]')) {
          const id = Number(row.dataset.entryId);
          const height = row.getBoundingClientRect().height;
          if (height > 0 && Math.abs((heights.get(id) ?? 0) - height) > 0.5) {
            heights.set(id, height);
            changed = true;
          }
        }
        if (changed) void layout();
      });
      observer.observe(node);
      node.querySelectorAll<HTMLElement>('[data-entry-id]').forEach((row) => observer?.observe(row));
    }
    untrack(() => void layout());
    return () => { revision++; observer?.disconnect(); observer = undefined; };
  });

  $effect(() => {
    // Include text for streaming, even without ResizeObserver (test hosts).
    for (const entry of entries) { void entry.id; void entry.text; }
    untrack(() => void layout());
  });
</script>

<svelte:window onpointerup={() => { dragging = false; }} onpointercancel={() => { dragging = false; }} />

<div class="transcript-wrap">
  {#if hasMore || historyError}
    <div class="history-control">
      <button type="button" disabled={loadingOlder} onclick={() => { following = false; void onolder(); }}>
        {loadingOlder ? t('history.loading') : t('history.older')}
      </button>
      {#if historyError}<span role="alert">{historyError}</span>{/if}
    </div>
  {/if}
  <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions (a log supports native scrolling and keyboard navigation) -->
  <div
    class="transcript"
    bind:this={scroller}
    {onscroll}
    onwheel={(event) => {
      if (event.deltaY < 0) { away = true; following = false; armed = true; maybeOlder(); }
      // At the end already, the wheel moves nothing and no scroll event says so.
      else if (event.deltaY > 0) { away = false; if (atEnd()) following = true; }
    }}
    onpointerdown={(event) => {
      if (event.target !== scroller) return;
      dragging = true;
      away = false;
      armed = true;
    }}
    {onkeydown}
    role="log"
    tabindex="0"
    aria-label={t('transcript.label')}
    aria-busy={loadingOlder}
  >
    {#if empty}
      <div class="empty">
        <p class="empty-title">{t('empty.title')}</p>
        <p class="empty-body">{t('empty.body')}</p>
      </div>
    {:else}
      <ol class="lines" bind:this={list}>
        <li class="spacer" aria-hidden="true" style:height="{geometry.offsets[range.start] ?? 0}px"></li>
        {#each visible as entry, index (entry.id)}
          <li class="virtual-row" use:measure={entry.id} aria-posinset={range.start + index + 1} aria-setsize={entries.length}>
            <div class="line" data-role={entry.role}>
              <Line {entry} {sending} {onanswer} />
            </div>
          </li>
        {/each}
        <li class="spacer" aria-hidden="true" style:height="{geometry.total - (geometry.offsets[range.end] ?? 0)}px"></li>
      </ol>
    {/if}
  </div>
  {#if !following && !empty}
    <button class="jump" type="button" onclick={jump} aria-label={t('jump.latest')} title={t('jump.latest')}>
      <span aria-hidden="true">↓</span>
    </button>
  {/if}
</div>

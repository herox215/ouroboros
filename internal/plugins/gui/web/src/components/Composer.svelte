<script lang="ts">
  import { t } from '../lib/i18n';

  let {
    disabled,
    busy,
    sending,
    question,
    queued,
    onsend,
    onstop,
  }: {
    disabled: boolean;
    busy: boolean;
    sending: boolean;
    question: boolean;
    queued: string[];
    onsend: (text: string) => Promise<boolean>;
    onstop: () => void;
  } = $props();

  let draft = $state('');
  let area = $state<HTMLTextAreaElement | null>(null);

  $effect(() => {
    area?.focus();
  });

  // The field grows with what is written, up to the limit the stylesheet
  // gives it, and is one line again once it is sent.
  $effect(() => {
    void draft;
    if (!area) return;
    area.style.height = 'auto';
    area.style.height = Math.min(area.scrollHeight, 240) + 'px';
  });

  async function submit(): Promise<void> {
    if (disabled || sending || !draft.trim()) return;
    if (await onsend(draft)) draft = '';
    area?.focus();
  }

  function keydown(event: KeyboardEvent): void {
    if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return;
    event.preventDefault();
    void submit();
  }
</script>

<div class="composer">
  {#if queued.length}
    <p class="queued"><span>{t('queued.label')}</span> {queued.join(' · ')}</p>
  {/if}
  <form
    class="field"
    onsubmit={(event) => {
      event.preventDefault();
      void submit();
    }}
  >
    <textarea
      bind:this={area}
      bind:value={draft}
      rows="1"
      aria-label={t('composer.label')}
      placeholder={question ? t('composer.answer') : t('composer.placeholder')}
      {disabled}
      onkeydown={keydown}
    ></textarea>
    {#if busy}
      <button class="stop" type="button" onclick={onstop} title={t('composer.stop')} aria-label={t('composer.stop')}>
        <span aria-hidden="true">■</span>
      </button>
    {/if}
    <button
      class="send"
      type="submit"
      disabled={disabled || sending || !draft.trim()}
      title={t('composer.send')}
      aria-label={t('composer.send')}
    >
      <span aria-hidden="true">↑</span>
    </button>
  </form>
  <p class="hint">{t('composer.hint')}</p>
</div>

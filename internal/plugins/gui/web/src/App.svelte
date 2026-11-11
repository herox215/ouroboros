<script lang="ts">
  import { t } from './lib/i18n';
  import Transcript from './components/Transcript.svelte';
  import Composer from './components/Composer.svelte';
  import type { Chat } from './lib/chat.svelte';

  let { chat }: { chat: Chat | null } = $props();

  $effect(() => {
    if (!chat) return;
    void chat.start();
    return () => chat.dispose();
  });

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

{#if !chat}
  <main class="shell nowhere">
    <p>{t('nohost')}</p>
  </main>
{:else}
  <main class="shell">
    <header class="top">
      <h1 class="brand">Ouroboros</h1>
      <p class="profile">{chat.state?.profile ?? ''}</p>
      <p class="state" data-tone={tone()} role="status">
        <span class="led" aria-hidden="true"></span>
        <span>{statusText()}</span>
      </p>
    </header>

    <Transcript
      entries={chat.entries}
      sending={chat.sending}
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

    <Composer
      {disabled}
      busy={chat.busy}
      sending={chat.sending}
      {question}
      queued={chat.queued}
      onsend={(text) => chat.send(text)}
      onstop={() => chat.stop()}
    />

    {#if chat.status === 'restarting'}
      <div class="veil" role="status">
        <p>{t('restarting.hint')}</p>
      </div>
    {/if}
  </main>
{/if}

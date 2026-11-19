<script lang="ts">
  import { t } from '../lib/i18n';
  import type { Gateways } from '../lib/gateways.svelte';
  import GatewayForm from './GatewayForm.svelte';

  let { gateways }: { gateways: Gateways } = $props();
</script>

<div class="prefs-head">
  <h2 class="prefs-title">{t('gateway.title')}</h2>
</div>
<p class="prefs-hint">{t('gateway.hint')}</p>

{#if gateways.error && !gateways.list}
  <p class="prefs-error" role="alert">{t('gateway.unreadable')}: {gateways.error}</p>
  <div class="prefs-row">
    <button type="button" class="prefs-button" onclick={() => gateways.refresh()}>{t('options.retry')}</button>
  </div>
{:else if !gateways.list}
  <p class="prefs-hint" role="status">{t('gateway.loading')}</p>
{:else if gateways.list.length === 0}
  <p class="prefs-hint">{t('gateway.empty')}</p>
{:else}
  {#each gateways.list as info (info.name)}
    <GatewayForm {gateways} {info} />
  {/each}
{/if}

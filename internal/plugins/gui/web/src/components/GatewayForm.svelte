<script lang="ts">
  import { t } from '../lib/i18n';
  import type { Gateways } from '../lib/gateways.svelte';
  import type { GatewayChange, GatewayInfo } from '../lib/wire';

  let { gateways, info }: { gateways: Gateways; info: GatewayInfo } = $props();

  /** One entry per line, empty lines left out. */
  const lines = (text: string): string[] =>
    text
      .split(/[\n,]/)
      .map((line) => line.trim())
      .filter(Boolean);

  let enabled = $state(false);
  let homeserver = $state('');
  let userId = $state('');
  /** Typed here, sent once, and emptied as soon as it went out. */
  let password = $state('');
  let allowed = $state('');
  /** The fields of a gateway with a form of its own, by key: text and
   * secrets as typed, lists one entry per line, switches as 'true'/'false'. */
  let values = $state<Record<string, string>>({});

  const fields = $derived(info.fields ?? []);
  const busy = $derived(gateways.busy === info.name);
  const failed = $derived(gateways.changeFor === info.name ? gateways.changeError : '');
  const done = $derived(gateways.saved === info.name);

  // What the node answers with is what the form shows: again after a save,
  // when the password is gone from it.
  $effect(() => {
    enabled = info.enabled;
    homeserver = info.homeserver ?? '';
    userId = info.user_id ?? '';
    allowed = info.allowed_users.join('\n');
    password = '';
    const next: Record<string, string> = {};
    for (const field of info.fields ?? []) {
      if (field.kind === 'list') next[field.key] = (field.list ?? []).join('\n');
      else if (field.kind === 'bool') next[field.key] = String(!!field.on);
      else if (field.kind === 'secret') next[field.key] = '';
      else next[field.key] = field.value ?? '';
    }
    values = next;
  });

  function change(logout: boolean): GatewayChange {
    if (fields.length === 0) {
      return { enabled, homeserver, user_id: userId, password, allowed_users: lines(allowed), logout };
    }
    const sent: Record<string, string> = {};
    const lists: Record<string, string[]> = {};
    for (const field of fields) {
      if (field.kind === 'list') lists[field.key] = lines(values[field.key] ?? '');
      else sent[field.key] = logout && field.kind === 'secret' ? '' : (values[field.key] ?? '');
    }
    return { enabled, values: sent, lists, logout };
  }

  async function save(event: SubmitEvent | null, logout = false): Promise<void> {
    event?.preventDefault();
    const sent = change(logout);
    // The password leaves the form with the request, whatever comes of it: a
    // failed save asks for it again.
    password = '';
    for (const field of fields) if (field.kind === 'secret') values[field.key] = '';
    await gateways.save(info.name, sent);
    sent.password = '';
  }
</script>

<form class="gateway" onsubmit={(event) => save(event)} aria-label={info.title || info.name}>
  <div class="prefs-head">
    <h3 class="prefs-sub">{info.title || info.name}</h3>
    <span class="prefs-hint" role="status">
      {info.logged_in ? t('gateway.loggedin') : t('gateway.loggedout')}{info.status ? ` · ${info.status}` : ''}
    </span>
  </div>

  <label class="prefs-check">
    <input type="checkbox" bind:checked={enabled} disabled={busy} />
    <span>{t('gateway.enabled')}</span>
  </label>

  {#if fields.length === 0}
    <label class="prefs-label" for="gw-{info.name}-homeserver">{t('gateway.homeserver')}</label>
    <input id="gw-{info.name}-homeserver" class="prefs-input" type="text" autocomplete="off" spellcheck="false" placeholder="https://matrix.example.org" bind:value={homeserver} disabled={busy} />
    <p class="prefs-hint">{t('gateway.homeserver.hint')}</p>

    <label class="prefs-label" for="gw-{info.name}-user">{t('gateway.userid')}</label>
    <input id="gw-{info.name}-user" class="prefs-input" type="text" autocomplete="off" spellcheck="false" placeholder="@bot:example.org" bind:value={userId} disabled={busy} />

    <label class="prefs-label" for="gw-{info.name}-password">{t('gateway.password')}</label>
    <input id="gw-{info.name}-password" class="prefs-input" type="password" autocomplete="new-password" spellcheck="false" bind:value={password} disabled={busy} />
    <p class="prefs-hint">{t('gateway.password.hint')}</p>

    <label class="prefs-label" for="gw-{info.name}-allowed">{t('gateway.allowed')}</label>
    <textarea id="gw-{info.name}-allowed" class="prefs-input" rows="3" autocomplete="off" spellcheck="false" placeholder="@lucas:example.org" bind:value={allowed} disabled={busy}></textarea>
    <p class="prefs-hint">{t('gateway.allowed.hint')}</p>
  {:else}
    {#each fields as field (field.key)}
      {#if field.kind === 'bool'}
        <label class="prefs-check">
          <input
            type="checkbox"
            checked={values[field.key] === 'true'}
            onchange={(event) => (values[field.key] = String(event.currentTarget.checked))}
            disabled={busy}
          />
          <span>{field.label || field.key}</span>
        </label>
        {#if field.hint}<p class="prefs-hint">{field.hint}</p>{/if}
      {:else}
        <label class="prefs-label" for="gw-{info.name}-{field.key}">{field.label || field.key}</label>
        {#if field.kind === 'list'}
          <textarea id="gw-{info.name}-{field.key}" class="prefs-input" rows="3" autocomplete="off" spellcheck="false" bind:value={values[field.key]} disabled={busy}></textarea>
          <p class="prefs-hint">{t('gateway.list.hint')}</p>
        {:else if field.kind === 'secret'}
          <input id="gw-{info.name}-{field.key}" class="prefs-input" type="password" autocomplete="new-password" spellcheck="false" bind:value={values[field.key]} disabled={busy} />
          <p class="prefs-hint">{t('gateway.secret.hint')}</p>
        {:else}
          <input id="gw-{info.name}-{field.key}" class="prefs-input" type="text" autocomplete="off" spellcheck="false" bind:value={values[field.key]} disabled={busy} />
        {/if}
        {#if field.hint}<p class="prefs-hint">{field.hint}</p>{/if}
      {/if}
    {/each}
  {/if}

  <p class="prefs-hint">{t('gateway.access')}</p>
  {#if failed}
    <p class="prefs-error" role="alert">{failed}</p>
  {:else if done}
    <p class="prefs-hint" role="status">{t('gateway.saved')}</p>
  {/if}
  <div class="prefs-row">
    <button type="submit" class="prefs-button primary" disabled={gateways.busy !== null}>
      {busy ? t('options.saving') : t('gateway.save')}
    </button>
    <button type="button" class="prefs-button" onclick={() => save(null, true)} disabled={gateways.busy !== null || !info.logged_in}>
      {t('gateway.logout')}
    </button>
  </div>
</form>

<style>
  .gateway {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 14px;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 10px;
  }

  textarea.prefs-input {
    resize: vertical;
    font-family: inherit;
  }
</style>

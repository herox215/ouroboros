<script lang="ts">
  import { t } from '../lib/i18n';
  import { MAX_OPACITY, MIN_OPACITY, type Themes } from '../lib/themes.svelte';

  let { themes }: { themes: Themes } = $props();

  /** How much of the ground is painted, in percent: every step of the slider
   * shows at once and is kept, as a theme that is chosen is. */
  function moved(event: Event): void {
    themes.setOpacity(Number((event.currentTarget as HTMLInputElement).value));
  }
</script>

<div class="prefs-head">
  <h2 class="prefs-title">{t('background.title')}</h2>
  <button
    type="button"
    class="prefs-button"
    onclick={() => themes.resetOpacity()}
    disabled={themes.opacity >= MAX_OPACITY}
  >{t('background.opaque')}</button>
</div>
<p class="prefs-hint">{t('background.hint')}</p>

<div class="opacity-row">
  <label class="prefs-label" for="background-opacity">{t('background.opacity')}</label>
  <input
    id="background-opacity"
    class="opacity-range"
    type="range"
    min={MIN_OPACITY}
    max={MAX_OPACITY}
    step="5"
    value={themes.opacity}
    oninput={moved}
  />
  <output class="opacity-value" for="background-opacity">{themes.opacity} %</output>
</div>

<style>
  .opacity-row {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-top: 2px;
  }

  .opacity-row .prefs-label {
    margin-top: 0;
  }

  .opacity-range {
    width: 240px;
    max-width: 100%;
    accent-color: var(--accent);
    cursor: pointer;
  }

  .opacity-value {
    min-width: 4ch;
    font-family: var(--mono);
    font-size: 12px;
    color: var(--text);
    font-variant-numeric: tabular-nums;
  }
</style>

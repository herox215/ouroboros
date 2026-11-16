<script lang="ts">
  import type { Jobs } from '../lib/jobs.svelte';
  import { t } from '../lib/i18n';
  import { exact } from '../lib/time';
  import { agentName } from '../lib/agentkind';

  /** names are the names of the agents by ID, as they were read last: a job
   * says which of them it runs as. */
  let { jobs, names = {} }: { jobs: Jobs; names?: Record<string, string> } = $props();

  const list = $derived(jobs.list ?? []);

  /** The first letter of the name, for the badge of the agent. */
  function initial(name: string): string {
    return Array.from(name.trim())[0]?.toUpperCase() ?? '?';
  }
</script>

<section class="jobs" aria-label={t('jobs.label')}>
  {#if jobs.error}
    <p class="banner" role="alert">{t('jobs.unreadable')}: {jobs.error}</p>
  {/if}

  {#if !jobs.list}
    {#if !jobs.error}
      <p class="board-wait" role="status">{t('jobs.loading')}</p>
    {/if}
  {:else if list.length === 0}
    <div class="empty">
      <p class="empty-title">{t('jobs.empty.title')}</p>
      <p>{t('jobs.empty.body')}</p>
    </div>
  {:else}
    <ul class="job-list">
      {#each list as job (job.id)}
        <li class="job" data-agent={job.agent} class:paused={job.paused}>
          <div class="job-head">
            <h2 class="job-name">{job.name || job.id}</h2>
            {#if job.paused}
              <span class="job-tag">{t('jobs.paused')}</span>
            {/if}
          </div>

          <p class="job-agent">
            <span class="job-badge" aria-hidden="true">{initial(agentName(job.agent, names))}</span>
            {t('jobs.as')} <strong class="job-agent-name">{agentName(job.agent, names)}</strong>
          </p>

          <dl class="job-facts">
            {#if job.cron}
              <dt>{t('jobs.cron')}</dt>
              <dd><code>{job.cron}</code>{#if job.zone}<span class="job-zone">{' · '}{job.zone}</span>{/if}</dd>
            {:else if job.at}
              <dt>{t('jobs.once')}</dt>
              <dd title={job.at}>{exact(job.at)}</dd>
            {/if}
            {#if job.next && !job.paused}
              <dt>{t('jobs.next')}</dt>
              <dd>{exact(job.next)}</dd>
            {/if}
          </dl>

          {#if job.text}
            <p class="job-text">{job.text}</p>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .jobs {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    padding: 18px 0 12px;
    overflow-y: auto;
  }

  .job-list {
    display: flex;
    flex-direction: column;
    gap: 10px;
    max-width: 760px;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .job {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 16px 18px;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: var(--shadow);
  }

  .job.paused {
    opacity: 0.72;
  }

  .job-head {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 4px 10px;
  }

  .job-name {
    margin: 0;
    font-size: 1.05em;
    font-weight: 600;
    overflow-wrap: anywhere;
  }

  .job-tag {
    font-family: var(--mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--warn);
  }

  .job-agent {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 13.5px;
    color: var(--muted);
  }

  .job-agent-name {
    color: var(--text);
    font-weight: 600;
  }

  .job-badge {
    flex: none;
    width: 24px;
    height: 24px;
    display: grid;
    place-items: center;
    border-radius: 50%;
    font-family: var(--mono);
    font-size: 11.5px;
    font-weight: 600;
    color: var(--accent);
    background: var(--accent-soft);
    border: 1px solid var(--accent-line);
  }

  .job-facts {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 3px 14px;
    margin: 0;
    font-size: 13px;
  }

  .job-facts dt {
    font-family: var(--mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
    padding-top: 2px;
  }

  .job-facts dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }

  .job-facts code {
    font-family: var(--mono);
    font-size: 12.5px;
  }

  .job-zone {
    color: var(--muted);
  }

          .job-text {
    margin: 0;
    font-size: 13px;
    color: var(--muted);
    white-space: pre-line;
    display: -webkit-box;
    -webkit-line-clamp: 3;
    line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }

  @media (max-width: 640px) {
    .job-facts {
      grid-template-columns: 1fr;
      gap: 0;
    }

    .job-facts dd {
      margin-bottom: 6px;
    }
  }
</style>

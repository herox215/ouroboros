import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from '@testing-library/svelte';
import JobsView from './Jobs.svelte';
import { Jobs } from '../lib/jobs.svelte';
import { setLocale } from '../lib/i18n';
import type { AgentJob } from '../lib/wire';

const list: AgentJob[] = [
  {
    id: 'a1',
    name: 'Archive memories',
    agent: 'archivist',
    text: 'You are the archivist.',
    cron: '0 4 * * *',
    zone: 'Europe/Berlin',
    next: new Date(Date.now() + 3600_000).toISOString(),
  },
  { id: 'b2', name: 'Morning', agent: 'thread', at: '2026-12-01T07:30:00Z', paused: true, next: '2026-12-01T07:30:00Z' },
  { id: 'c3', name: 'Papers', agent: 'agent-3f9a1c07', cron: '0 9 * * 1' },
];

async function shown(names: Record<string, string> = {}, jobs: AgentJob[] = list) {
  const store = new Jobs({ AgentJobs: async () => jobs });
  await store.refresh();
  return render(JobsView, { props: { jobs: store, names } });
}

describe('Jobs view', () => {
  beforeEach(() => setLocale('en'));

  it('lists each scheduled job with the agent it runs as and when it runs, not what came of it', async () => {
    const { container } = await shown({ thread: 'pythia', archivist: 'Mnemosyne', 'agent-3f9a1c07': 'Researcher' });
    const jobs = [...container.querySelectorAll('.job')] as HTMLElement[];
    expect(jobs.map((job) => job.querySelector('.job-name')?.textContent)).toEqual(['Archive memories', 'Morning', 'Papers']);
    expect(jobs.map((job) => job.querySelector('.job-agent-name')?.textContent)).toEqual(['Mnemosyne', 'pythia', 'Researcher']);
    expect(jobs.map((job) => job.dataset.agent)).toEqual(['archivist', 'thread', 'agent-3f9a1c07']);

    const [archive, morning] = jobs;
    expect(archive.querySelector('.job-facts code')?.textContent).toBe('0 4 * * *');
    expect(archive.textContent).toContain('Europe/Berlin');
    expect(archive.textContent).toContain('Next');
    expect(archive.querySelector('.job-text')?.textContent).toBe('You are the archivist.');
    expect(container.querySelector('.job-last, .job-result, .job-status')).toBeNull();

    // A paused job says so, and not when it would run next.
    expect(morning.classList.contains('paused')).toBe(true);
    expect(morning.querySelector('.job-tag')?.textContent).toBe('Paused');
    expect(morning.textContent).toContain('Once at');
    expect(morning.textContent).not.toContain('Next');
  });

  it('names an agent by what it is while the names are not read', async () => {
    const { container } = await shown();
    expect([...container.querySelectorAll('.job-agent-name')].map((name) => name.textContent)).toEqual(['Archivist', 'Thread agent', 'agent-3f9a1c07']);
  });

  it('says why the jobs cannot be read, and waits for the first answer', async () => {
    const store = new Jobs({ AgentJobs: () => Promise.reject(new Error('no schedule')) });
    const { container } = render(JobsView, { props: { jobs: store } });
    expect(container.querySelector('[role="status"]')?.textContent).toBe('Reading the jobs …');
    await store.refresh();
    await vi.waitFor(() => expect(container.querySelector('[role="alert"]')?.textContent).toBe('The jobs cannot be read: no schedule'));
  });

  it('says that there is no scheduled job', async () => {
    const { container } = await shown({}, []);
    expect(container.querySelector('.empty-title')?.textContent).toBe('No scheduled job');
  });
});

describe('Jobs store', () => {
  afterEach(() => vi.useRealTimers());

  it('reads the jobs while it is started, and keeps what it read while they cannot be read', async () => {
    vi.useFakeTimers();
    let reads = 0;
    let fail = false;
    const store = new Jobs(
      {
        AgentJobs: async () => {
          reads++;
          if (fail) throw new Error('gone');
          return list;
        },
      },
      1000,
    );
    store.start();
    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(reads).toBe(1);
    expect(store.list).toHaveLength(3);
    await vi.advanceTimersByTimeAsync(2000);
    expect(reads).toBe(3);
    fail = true;
    await vi.advanceTimersByTimeAsync(1000);
    expect(store.error).toBe('gone');
    expect(store.list).toHaveLength(3);
    store.stop();
    await vi.advanceTimersByTimeAsync(5000);
    expect(reads).toBe(4);
    store.stop();
  });

  it('drops an answer a newer read overtook', async () => {
    const answers: ((jobs: AgentJob[]) => void)[] = [];
    const store = new Jobs({ AgentJobs: () => new Promise((resolve) => answers.push(resolve)) });
    const first = store.refresh();
    const second = store.refresh();
    answers[1]([list[2]]);
    answers[0](list);
    await Promise.all([first, second]);
    expect(store.list).toEqual([list[2]]);
  });
});

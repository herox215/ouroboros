import { afterEach, describe, expect, it } from 'vitest';
import { ago, exact } from './time';
import { setLocale } from './i18n';

const now = Date.parse('2026-11-16T12:00:00Z');
const before = (ms: number) => new Date(now - ms).toISOString();
const minute = 60_000;
const hour = 60 * minute;

describe('ago', () => {
  afterEach(() => setLocale(null));

  it('says how long ago a time was, in the unit that fits', () => {
    setLocale('en');
    expect(ago(before(0), now)).toBe('now');
    expect(ago(before(59_000), now)).toBe('now');
    expect(ago(before(minute), now)).toBe('1 min. ago');
    expect(ago(before(59 * minute + 59_000), now)).toBe('59 min. ago');
    expect(ago(before(hour), now)).toBe('1 hr. ago');
    expect(ago(before(47 * hour), now)).toBe('47 hr. ago');
    expect(ago(before(48 * hour), now)).toBe('2 days ago');
    expect(ago(before(100 * hour), now)).toBe('4 days ago');
  });

  it('speaks the language of the window', () => {
    setLocale('de');
    expect(ago(before(5 * minute), now)).toBe('vor 5 Min.');
  });

  it('takes a time ahead of the clock of the window as now', () => {
    setLocale('en');
    expect(ago(before(-5 * minute), now)).toBe('now');
  });

  it('says nothing of a time that is none', () => {
    expect(ago(undefined, now)).toBe('');
    expect(ago('', now)).toBe('');
    expect(ago('yesterday', now)).toBe('');
  });
});

describe('exact', () => {
  afterEach(() => setLocale(null));

  it('names the day and the time', () => {
    setLocale('en');
    const text = exact('2026-11-16T12:00:00Z');
    expect(text).toContain('2026');
    expect(text).toContain('Nov');
  });

  it('says nothing of a time that is none', () => {
    expect(exact(undefined)).toBe('');
    expect(exact('never')).toBe('');
  });
});

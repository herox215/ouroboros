import { afterEach, describe, expect, it } from 'vitest';
import { byName, laneName, roleName, shortID } from './boardtext';
import { setLocale } from './i18n';

describe('the words of the board', () => {
  afterEach(() => setLocale(null));

  it('names the lanes, and shows a column it does not know as it is', () => {
    setLocale('en');
    expect(['new', 'ready', 'progress', 'blocked', 'done'].map(laneName)).toEqual(['New', 'Ready', 'In progress', 'Blocked', 'Done']);
    expect(laneName('ended without a result')).toBe('ended without a result');
    setLocale('de');
    expect(laneName('progress')).toBe('In Arbeit');
  });

  it('names the roles of a session', () => {
    setLocale('de');
    expect(roleName('refinement')).toBe('Refinement');
    expect(roleName('implementation')).toBe('Umsetzung');
    expect(roleName('review')).toBe('review');
  });

  it('names who wrote a note, a session by the start of its ID', () => {
    setLocale('en');
    expect(byName('thread')).toBe('Thread');
    expect(byName('orchestrator')).toBe('Orchestrator');
    expect(byName('board')).toBe('Board');
    expect(byName('session 0123456789abcdef')).toBe('Session 01234567');
    expect(byName('someone else')).toBe('someone else');
  });

  it('shows the start of an ID', () => {
    expect(shortID('0123456789abcdef')).toBe('01234567');
    expect(shortID('0123')).toBe('0123');
  });
});

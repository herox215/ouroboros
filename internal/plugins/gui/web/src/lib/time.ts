import { locale } from './i18n';

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** How long before now the time at was, in the language of the window: "now",
 * "5 min. ago", "3 hr. ago", "2 days ago". Nothing for a time that is none. */
export function ago(at: string | undefined, now: number): string {
  const then = at ? Date.parse(at) : NaN;
  if (Number.isNaN(then)) return '';
  const passed = Math.max(0, now - then);
  const format = new Intl.RelativeTimeFormat(locale(), { numeric: 'auto', style: 'short' });
  if (passed < MINUTE) return format.format(0, 'second');
  if (passed < HOUR) return format.format(-Math.floor(passed / MINUTE), 'minute');
  if (passed < 2 * DAY) return format.format(-Math.floor(passed / HOUR), 'hour');
  return format.format(-Math.floor(passed / DAY), 'day');
}

/** The time at in full, for a title that says exactly when: "" for none. */
export function exact(at: string | undefined): string {
  const then = at ? Date.parse(at) : NaN;
  if (Number.isNaN(then)) return '';
  return new Intl.DateTimeFormat(locale(), { dateStyle: 'medium', timeStyle: 'short' }).format(then);
}

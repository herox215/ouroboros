// The window speaks the language of the browser, as the terminal UI speaks the
// language of the configuration. Only what the window itself says is
// translated: what the thread and the tools say is shown as it came.

const strings = {
  en: {
    'status.loading': 'Connecting …',
    'status.ready': 'Ready',
    'status.working': 'Working …',
    'status.answering': 'Needs your answer',
    'status.offline': 'Connection lost',
    'status.restarting': 'Restarting …',
    'empty.title': 'Nothing said yet',
    'empty.body': 'Tell the thread what is to be done.',
    'composer.label': 'Message',
    'composer.placeholder': 'Write to the thread',
    'composer.answer': 'Answer the thread',
    'composer.send': 'Send',
    'composer.stop': 'Stop',
    'composer.hint': 'Enter sends · Shift+Enter for a new line',
    'question.recommended': 'recommended',
    'question.answered': 'answered',
    'queued.label': 'Waiting',
    'note.stopped': 'stopped',
    'jump.latest': 'Jump to the newest',
    'history.older': 'Load older messages',
    'history.loading': 'Loading history …',
    'retry': 'Try again',
    'offline.hint': 'The node is not reachable. The transcript stays.',
    'restarting.hint': 'Ouroboros starts anew from a new build.',
    'transcript.label': 'Conversation',
    'nohost': 'This page runs only in the window of ouro.',
  },
  de: {
    'status.loading': 'Verbinde …',
    'status.ready': 'Bereit',
    'status.working': 'Arbeitet …',
    'status.answering': 'Wartet auf dich',
    'status.offline': 'Verbindung verloren',
    'status.restarting': 'Startet neu …',
    'empty.title': 'Noch nichts gesagt',
    'empty.body': 'Sag dem Thread, was zu tun ist.',
    'composer.label': 'Nachricht',
    'composer.placeholder': 'Schreib dem Thread',
    'composer.answer': 'Antworte dem Thread',
    'composer.send': 'Senden',
    'composer.stop': 'Stoppen',
    'composer.hint': 'Enter sendet · Shift+Enter für eine neue Zeile',
    'question.recommended': 'empfohlen',
    'question.answered': 'beantwortet',
    'queued.label': 'Wartet',
    'note.stopped': 'gestoppt',
    'jump.latest': 'Zum Neuesten',
    'history.older': 'Ältere Nachrichten laden',
    'history.loading': 'Verlauf wird geladen …',
    'retry': 'Erneut versuchen',
    'offline.hint': 'Der Node ist nicht erreichbar. Der Verlauf bleibt.',
    'restarting.hint': 'Ouroboros startet mit einem neuen Build neu.',
    'transcript.label': 'Verlauf',
    'nohost': 'Diese Seite läuft nur im Fenster von ouro.',
  },
} as const;

export type Key = keyof (typeof strings)['en'];
export type Language = keyof typeof strings;

let language: Language | null = null;

/** The language the window speaks: the first of the browser's languages that
 * ouro speaks as well, English for any other. */
export function locale(): Language {
  if (language) return language;
  const wanted = globalThis.navigator?.languages ?? [globalThis.navigator?.language ?? ''];
  for (const tag of wanted) {
    if (tag.toLowerCase().startsWith('de')) return 'de';
  }
  return 'en';
}

/** Fixes the language, for a test. */
export function setLocale(next: Language | null): void {
  language = next;
}

/** The text of key in the language of the window. */
export function t(key: Key): string {
  return strings[locale()][key];
}

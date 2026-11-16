import { t, type Key } from './i18n';

// What the board says in words of its own: the names of its lanes, of the
// roles of a session and of who wrote a note. What the board keeps under a
// name the window does not know is shown as it is.

const lanes: Record<string, Key> = {
  new: 'lane.new',
  ready: 'lane.ready',
  progress: 'lane.progress',
  blocked: 'lane.blocked',
  done: 'lane.done',
};

const roles: Record<string, Key> = {
  refinement: 'role.refinement',
  implementation: 'role.implementation',
};

const writers: Record<string, Key> = {
  thread: 'by.thread',
  orchestrator: 'by.orchestrator',
  board: 'by.board',
};

/** The name of the lane of column. */
export function laneName(column: string): string {
  const key = lanes[column];
  return key ? t(key) : column;
}

/** The name of the role a session works on a ticket in. */
export function roleName(role: string): string {
  const key = roles[role];
  return key ? t(key) : role;
}

/** The ID of a session as the board shows it: what tells the sessions of a
 * day apart. */
export function shortID(id: string): string {
  return id.slice(0, 8);
}

/** Who wrote a note, as the board names them: "session <id>" is a session. */
export function byName(by: string): string {
  const session = 'session ';
  if (by.startsWith(session)) return t('by.session') + ' ' + shortID(by.slice(session.length));
  const key = writers[by];
  return key ? t(key) : by;
}

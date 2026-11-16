import { t, type Key } from './i18n';

/** What an agent of the kind is, in the language of the window: the kind
 * itself for one the window does not know. */
export function kindName(kind: string): string {
  return t(`agents.kind.${kind}` as Key) ?? kind;
}

/** The name of the agent a session of the board runs as: as the agents read
 * last call it, or else as it was called when the session began, or what it
 * is; '' for a session from before the board knew its agent. */
export function workerName(id: string | undefined, named: string | undefined, names: Record<string, string>): string {
  if (!id) return '';
  return names[id] || named || kindName(id);
}

/** The name of the agent id, as the names of the agents read last have it,
 * or what it is, while they are not read. */
export function agentName(id: string, names: Record<string, string>): string {
  return names[id] || kindName(id);
}

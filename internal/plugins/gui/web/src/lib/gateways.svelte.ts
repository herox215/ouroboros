import type { GatewayBridge, GatewayChange, GatewayInfo } from './wire';

function reason(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

/**
 * The gateways of the profile, like Matrix: what the owner may see of each,
 * and the one call that changes one. The store holds no password: it passes
 * what the form sends on and keeps only the snapshot the node answers with,
 * which has none.
 */
export class Gateways {
  readonly #bridge: GatewayBridge;
  #asked = 0;

  /** The gateways, null until they were read. */
  list = $state<GatewayInfo[] | null>(null);
  /** Why they could not be read, '' when they could. */
  error = $state('');
  /** The gateway being saved now, null while none is. */
  busy = $state<string | null>(null);
  /** Why the last save failed, and the gateway it was for. */
  changeError = $state('');
  changeFor = $state('');
  /** The gateway that was saved last, until the next change. */
  saved = $state('');

  constructor(bridge: GatewayBridge) {
    this.#bridge = bridge;
  }

  /** Reads the gateways. */
  async refresh(): Promise<void> {
    const asked = ++this.#asked;
    try {
      const list = await this.#bridge.Gateways();
      if (asked !== this.#asked) return;
      this.list = list;
      this.error = '';
    } catch (error) {
      if (asked !== this.#asked) return;
      this.error = reason(error);
    }
  }

  /** Saves the change of the gateway name, one at a time. The change is
   * handed on as it is; whether it went through is what is returned. */
  async save(name: string, change: GatewayChange): Promise<boolean> {
    if (this.busy !== null) return false;
    this.busy = name;
    this.changeError = '';
    this.changeFor = name;
    this.saved = '';
    try {
      const list = await this.#bridge.SaveGateway(name, change);
      this.#asked++;
      this.list = list;
      this.error = '';
      this.saved = name;
      return true;
    } catch (error) {
      this.changeError = reason(error);
      return false;
    } finally {
      this.busy = null;
    }
  }
}

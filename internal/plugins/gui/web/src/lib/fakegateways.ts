// The gateways of a node for tests: what is saved is kept, as the node would,
// and a password is dropped as soon as it was used.

import type { GatewayBridge, GatewayChange, GatewayInfo } from './wire';

export interface FakeGateways extends GatewayBridge {
  /** What the gateway was saved with, passwords included, as it got them. */
  saves: { name: string; change: GatewayChange }[];
  /** What a call fails with, while it is set. */
  fail: { Gateways?: string; SaveGateway?: string };
}

export function fakeGateways(initial?: GatewayInfo[]): FakeGateways {
  let list: GatewayInfo[] = initial ?? [
    { name: 'matrix', title: 'Matrix', enabled: false, allowed_users: [], logged_in: false, status: 'Stopped' },
  ];
  const fake: FakeGateways = {
    saves: [],
    fail: {},
    async Gateways() {
      if (fake.fail.Gateways) throw new Error(fake.fail.Gateways);
      return structuredClone(list);
    },
    async SaveGateway(name, change) {
      if (fake.fail.SaveGateway) throw new Error(fake.fail.SaveGateway);
      fake.saves.push({ name, change: structuredClone(change) });
      list = list.map((g) =>
        g.name !== name
          ? g
          : {
              ...g,
              enabled: change.enabled,
              homeserver: change.homeserver ?? g.homeserver,
              user_id: change.user_id ?? g.user_id,
              allowed_users: change.allowed_users ?? g.allowed_users,
              logged_in: change.logout ? false : g.logged_in || !!change.password,
              status: change.logout ? 'Stopped' : 'Connected',
            },
      );
      return structuredClone(list);
    },
  };
  return fake;
}

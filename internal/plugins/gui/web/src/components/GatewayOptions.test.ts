import { describe, expect, it } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import OptionsView from './Options.svelte';
import { Themes } from '../lib/themes.svelte';
import { NodeOptions } from '../lib/nodeoptions.svelte';
import { Gateways } from '../lib/gateways.svelte';
import { fakeOptions } from '../lib/fakeoptions';
import { fakeGateways } from '../lib/fakegateways';
import { setLocale } from '../lib/i18n';

setLocale('en');

function shown(bridge = fakeGateways(), withGateways = true) {
  const node = new NodeOptions(fakeOptions());
  const gateways = withGateways ? new Gateways(bridge) : null;
  const themes = new Themes(null, document.documentElement);
  return { bridge, gateways, ...render(OptionsView, { props: { themes, node, gateways } }) };
}

const tab = (container: Element, name: string) =>
  [...container.querySelectorAll('[role="tab"]')].find((candidate) => candidate.textContent?.trim() === name) as HTMLButtonElement | undefined;

const button = (container: Element, name: string) =>
  [...container.querySelectorAll('button')].find((candidate) => candidate.textContent?.trim() === name) as HTMLButtonElement;

const input = (container: Element, id: string) => container.querySelector(`#${id}`) as HTMLInputElement;

describe('GatewayOptions', () => {
  it('shows the tab only for a host that has gateways', async () => {
    const without = shown(undefined, false);
    await waitFor(() => expect(tab(without.container, 'Providers')).toBeTruthy());
    expect(tab(without.container, 'Gateways')).toBeUndefined();
    without.unmount();

    const { container } = shown();
    expect(tab(container, 'Gateways')).toBeTruthy();
  });

  it('saves what was typed, sends the password once and empties the field', async () => {
    const { container, bridge, gateways } = shown();
    await fireEvent.click(tab(container, 'Gateways')!);
    await waitFor(() => expect(input(container, 'gw-matrix-homeserver')).toBeTruthy());
    expect(container.textContent).toContain('Not signed in · Stopped');
    expect(input(container, 'gw-matrix-password').type).toBe('password');

    await fireEvent.click(container.querySelector('input[type="checkbox"]') as HTMLInputElement);
    await fireEvent.input(input(container, 'gw-matrix-homeserver'), { target: { value: 'http://127.0.0.1:8008' } });
    await fireEvent.input(input(container, 'gw-matrix-user'), { target: { value: '@pythia:pythia' } });
    await fireEvent.input(input(container, 'gw-matrix-password'), { target: { value: 'hunter2' } });
    await fireEvent.input(input(container, 'gw-matrix-allowed'), { target: { value: '@lucas:pythia\n\n @other:pythia ' } });
    await fireEvent.click(button(container, 'Save'));

    await waitFor(() => expect(bridge.saves).toHaveLength(1));
    expect(bridge.saves[0]).toEqual({
      name: 'matrix',
      change: {
        enabled: true,
        homeserver: 'http://127.0.0.1:8008',
        user_id: '@pythia:pythia',
        password: 'hunter2',
        allowed_users: ['@lucas:pythia', '@other:pythia'],
        logout: false,
      },
    });
    await waitFor(() => expect(container.textContent).toContain('Saved.'));
    expect(input(container, 'gw-matrix-password').value).toBe('');
    expect(container.textContent).toContain('Signed in · Connected');
    // Nothing of the password is kept by the store.
    expect(JSON.stringify(gateways!.list)).not.toContain('hunter2');
    // Signing out is possible now.
    await fireEvent.click(button(container, 'Sign out'));
    await waitFor(() => expect(bridge.saves).toHaveLength(2));
    expect(bridge.saves[1].change.logout).toBe(true);
    expect(bridge.saves[1].change.password).toBe('');
  });

  it('shows why a save failed and asks for the password again', async () => {
    const bridge = fakeGateways();
    const { container } = shown(bridge);
    await fireEvent.click(tab(container, 'Gateways')!);
    await waitFor(() => expect(input(container, 'gw-matrix-password')).toBeTruthy());
    bridge.fail.SaveGateway = 'Gateway not saved: the homeserver refused the login';
    await fireEvent.input(input(container, 'gw-matrix-password'), { target: { value: 'wrong' } });
    await fireEvent.click(button(container, 'Save'));
    await waitFor(() => expect(container.querySelector('[role="alert"]')?.textContent).toBe('Gateway not saved: the homeserver refused the login'));
    expect(input(container, 'gw-matrix-password').value).toBe('');
    expect(container.textContent).not.toContain('wrong');
  });

  it('shows a form from the fields a gateway asks for', async () => {
    const bridge = fakeGateways([
      {
        name: 'other',
        title: 'Other',
        enabled: true,
        allowed_users: [],
        logged_in: true,
        fields: [
          { key: 'name', label: 'Name', kind: 'text', value: 'x' },
          { key: 'token', label: 'Token', kind: 'secret' },
          { key: 'rooms', label: 'Rooms', kind: 'list', list: ['a', 'b'] },
          { key: 'loud', label: 'Loud', kind: 'bool', on: true },
        ],
      },
    ]);
    const { container } = shown(bridge);
    await fireEvent.click(tab(container, 'Gateways')!);
    await waitFor(() => expect(input(container, 'gw-other-name')).toBeTruthy());
    expect(input(container, 'gw-other-name').value).toBe('x');
    expect(input(container, 'gw-other-token').type).toBe('password');
    expect(input(container, 'gw-other-rooms').value).toBe('a\nb');
    await fireEvent.input(input(container, 'gw-other-token'), { target: { value: 'tok' } });
    await fireEvent.click(button(container, 'Save'));
    await waitFor(() => expect(bridge.saves).toHaveLength(1));
    expect(bridge.saves[0].change).toEqual({
      enabled: true,
      values: { name: 'x', token: 'tok', loud: 'true' },
      lists: { rooms: ['a', 'b'] },
      logout: false,
    });
    await waitFor(() => expect(input(container, 'gw-other-token').value).toBe(''));
  });

  it('shows the hints of the fields and sends the plain HTTP confirmation', async () => {
    const bridge = fakeGateways([
      {
        name: 'matrix',
        title: 'Matrix',
        enabled: false,
        allowed_users: [],
        logged_in: false,
        fields: [
          { key: 'homeserver', label: 'Homeserver', kind: 'text', value: '', hint: 'HTTPS, or plain HTTP on this machine (127.0.0.1); HTTP elsewhere only when allowed below.' },
          { key: 'allow_http', label: 'Allow plain HTTP for this homeserver', kind: 'bool', on: false, hint: 'Sent unencrypted.' },
          { key: 'password', label: 'Password', kind: 'secret' },
        ],
      },
    ]);
    const { container } = shown(bridge);
    await fireEvent.click(tab(container, 'Gateways')!);
    await waitFor(() => expect(input(container, 'gw-matrix-homeserver')).toBeTruthy());
    expect(container.textContent).toContain('HTTPS, or plain HTTP on this machine (127.0.0.1); HTTP elsewhere only when allowed below.');
    expect(container.textContent).toContain('Sent unencrypted.');
    const box = [...container.querySelectorAll('label.prefs-check')].find((label) => label.textContent?.includes('Allow plain HTTP'))!.querySelector('input') as HTMLInputElement;
    expect(box.checked).toBe(false);
    await fireEvent.input(input(container, 'gw-matrix-homeserver'), { target: { value: 'http://100.125.108.63:8008' } });
    await fireEvent.click(box);
    await fireEvent.click(button(container, 'Save'));
    await waitFor(() => expect(bridge.saves).toHaveLength(1));
    expect(bridge.saves[0].change.values).toEqual({ homeserver: 'http://100.125.108.63:8008', allow_http: 'true', password: '' });
  });

  it('says when the gateways cannot be read, and tries again', async () => {
    const bridge = fakeGateways();
    bridge.fail.Gateways = 'down';
    const { container } = shown(bridge);
    await fireEvent.click(tab(container, 'Gateways')!);
    await waitFor(() => expect(container.querySelector('[role="alert"]')?.textContent).toContain('down'));
    delete bridge.fail.Gateways;
    await fireEvent.click(button(container, 'Try again'));
    await waitFor(() => expect(input(container, 'gw-matrix-homeserver')).toBeTruthy());
  });
});

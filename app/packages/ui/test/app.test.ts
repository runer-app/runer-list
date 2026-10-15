import { ClientCore, type ClientCoreOptions, type Platform, type Transport } from '@runer/client-core';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, describe, expect, test } from 'vitest';
import { MemoryServer } from '../../client-core/test/platform-node/memory-server.js';
import { createMemoryPlatform } from '../../client-core/test/platform-node/memory.js';
import { NodeVault } from '../../client-core/test/platform-node/vault.js';
import App from '../src/App.svelte';
import type { Shell } from '../src/shell.js';

// The thin slice through the real components and ClientCore, on the
// in-process server and the real vault.

const SERVER = 'https://runer.test';
const OPTS: ClientCoreOptions = { sync: { backoff: { firstMs: 5, maxMs: 20, jitterMs: 1 }, snapshotDelayMs: () => 0 } };
const cores: ClientCore[] = [];

afterEach(async () => {
  cleanup();
  for (const c of cores.splice(0)) await c.stop();
});

/** A shell on one device's platform, as apps/tauri builds it. */
function shellOn(platform: Platform): Shell {
  const keep = <T extends ClientCore | null>(c: T) => (c !== null && cores.push(c), c);
  return {
    defaultServer: SERVER,
    resume: async () => keep((await ClientCore.resume(platform, OPTS))[0] ?? null),
    signUp: async (server, name) => {
      const r = await ClientCore.signUp(platform, server, name, OPTS);
      keep(r.core);
      return r;
    },
    signIn: async (server, words) => keep(await ClientCore.signIn(platform, server, words, OPTS)),
  };
}

const offline: Transport = { request: () => Promise.reject(new Error('offline')), openSocket: () => Promise.reject(new Error('offline')) };

describe('the app', () => {
  test('Welcome → create account → 12 words → confirm 3 → Today → capture → restart → still there', async () => {
    const server = new MemoryServer();
    const p = createMemoryPlatform({ transport: server.transport() });
    render(App, { shell: shellOn(p) });

    // Welcome says what an account takes: a 12-word key, and no password.
    expect(await screen.findByText('No password. Just a 12-word key.')).toBeTruthy();

    // A name is offered, and nothing else is asked for: the account is created with the field left empty.
    await fireEvent.click(await screen.findByRole('button', { name: 'Create account' }));
    expect(await screen.findByText(/only shown to the people you share lists with/)).toBeTruthy();
    expect(((await screen.findByRole('textbox', { name: 'Your name (optional)' })) as HTMLInputElement).value).toBe('');
    expect(screen.getAllByRole('textbox')).toHaveLength(1);
    await fireEvent.click(screen.getByRole('button', { name: 'Create account' }));
    const list = await screen.findByRole('list', { name: 'Your account key' });
    const words = within(list)
      .getAllByRole('listitem')
      .map((li) => li.querySelector('.word')!.textContent!);
    expect(words).toHaveLength(12);
    // The words are copied or written down, not printed.
    expect(screen.queryByRole('button', { name: /Print/ })).toBeNull();

    // Copy puts the words on the clipboard in the form sign-in takes.
    let clip = '';
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: async (s: string) => void (clip = s) }, configurable: true });
    await fireEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeTruthy();
    expect(clip).toBe(words.join(' '));

    await fireEvent.click(screen.getByRole('button', { name: 'I’ve written them down' }));
    // A wrong answer is marked once the field is left, and the button stays off until all three are right.
    const inputs = () => screen.getAllByRole('textbox') as HTMLInputElement[];
    const open = () => screen.getByRole('button', { name: 'Open runer' }) as HTMLButtonElement;
    for (const input of inputs()) {
      await fireEvent.input(input, { target: { value: 'wrong' } });
      await fireEvent.blur(input);
    }
    expect(inputs()[0]!.getAttribute('aria-invalid')).toBe('true');
    expect(open().disabled).toBe(true);
    for (const [k, input] of inputs().entries()) {
      const n = Number(screen.getAllByText(/^Word \d+$/)[k]!.textContent!.replace('Word ', ''));
      await fireEvent.input(input, { target: { value: words[n - 1] } });
    }
    expect(screen.getAllByRole('img', { name: 'Correct' })).toHaveLength(3);
    await fireEvent.click(open());

    expect(await screen.findByRole('heading', { name: 'Today' })).toBeTruthy();
    const capture = screen.getByRole('textbox', { name: 'Add a task for today' });
    await fireEvent.input(capture, { target: { value: 'call the plumber' } });
    await fireEvent.submit(capture.closest('form')!);
    expect(await screen.findByText('call the plumber')).toBeTruthy();
    expect((capture as HTMLInputElement).value).toBe('');

    // The Inbox holds it too.
    await fireEvent.click(screen.getByRole('button', { name: /Inbox/ }));
    expect(await screen.findByRole('heading', { name: 'Inbox' })).toBeTruthy();
    expect(screen.getByDisplayValue('call the plumber')).toBeTruthy();

    // Quit, reopen with no network: still there.
    cleanup();
    for (const c of cores.splice(0)) await c.stop();
    const again = createMemoryPlatform({ vault: new NodeVault((p.vault as NodeVault).device), storage: p.storage, transport: offline });
    render(App, { shell: shellOn(again) });
    expect(await screen.findByText('call the plumber')).toBeTruthy();
    await waitFor(() => expect(screen.getByRole('status').textContent).toMatch(/Offline|Connecting/));
  });

  test('your own server: the field starts blank, and Cancel goes back to the hosted one', async () => {
    render(App, { shell: shellOn(createMemoryPlatform({ transport: offline })) });
    const create = () => screen.getByRole('button', { name: 'Create account' }) as HTMLButtonElement;

    await fireEvent.click(await screen.findByRole('button', { name: 'Use your own server' }));
    const field = screen.getByRole('textbox', { name: 'Server' }) as HTMLInputElement;
    expect(field.value).toBe('');
    expect(field.placeholder).not.toBe('');
    expect(screen.queryByText(SERVER)).toBeNull();
    expect(create().disabled).toBe(true);

    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('textbox', { name: 'Server' })).toBeNull();
    expect(create().disabled).toBe(false);
    expect(screen.getByRole('button', { name: 'Use your own server' })).toBeTruthy();
  });

  test('sign in refuses a typo before any request', async () => {
    const server = new MemoryServer();
    const requests: string[] = [];
    const t = server.transport();
    const p = createMemoryPlatform({ transport: { ...t, request: (m, path, b) => (requests.push(path), t.request(m, path, b)) } });
    render(App, { shell: shellOn(p) });
    await fireEvent.click(await screen.findByRole('button', { name: 'Sign in with my account key' }));
    await fireEvent.input(screen.getByRole('textbox', { name: 'Account key' }), { target: { value: Array(12).fill('abandon').join(' ') } });
    await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/valid key/);
    expect(requests).toEqual([]);
  });
});

import { createHash } from 'node:crypto';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { VaultError, type AccountHandle, type SignedEntry } from '../src/platform/vault.js';
import { NodeVault } from './platform-node/vault.js';

// The rules themselves are tested in `crates/runer-vault`; this checks the
// add-on and its mapping: verdicts, error codes, and the shapes that cross.

const DIR = join(import.meta.dirname, '../../../../protocol/log-fixtures');
const WORDS = 'legal winner thank year wave sausage worth useful legal winner thank yellow';

interface Fixture {
  valid: boolean;
  entries: SignedEntry[];
}

/** Fixtures store raw/sig as standard base64 (Go's `[]byte` JSON encoding). */
function load(file: string): Fixture {
  const f = JSON.parse(readFileSync(join(DIR, file), 'utf8')) as { valid: boolean; entries: { raw: string; sig: string }[] };
  const b64 = (s: string) => new Uint8Array(Buffer.from(s, 'base64'));
  return { valid: f.valid, entries: f.entries.map((e) => ({ raw: b64(e.raw), sig: b64(e.sig) })) };
}

const listIdOf = (genesis: SignedEntry) => createHash('sha256').update(genesis.raw).digest('hex');

async function signedIn(): Promise<[NodeVault, AccountHandle]> {
  const vault = new NodeVault();
  const r = await vault.signIn('https://runer.test', WORDS);
  return [vault, r!.account];
}

async function rejection(p: Promise<unknown>): Promise<VaultError> {
  const e = await p.then(
    () => undefined,
    (e: unknown) => e,
  );
  expect(e).toBeInstanceOf(VaultError);
  return e as VaultError;
}

describe('the vault through the Node add-on', () => {
  const files = readdirSync(DIR).filter((f) => f.endsWith('.json')).sort();

  it('gives every log fixture its verdict, rejecting at the last entry', async () => {
    expect(files).toHaveLength(32);
    for (const file of files) {
      const [vault, account] = await signedIn();
      const f = load(file);
      const applied = vault.applyLog(account, listIdOf(f.entries[0]!), f.entries);
      if (f.valid) {
        await expect(applied, file).resolves.toBeDefined();
      } else {
        const e = await rejection(applied);
        expect(e.message, file).toMatch(new RegExp(`^entry ${f.entries.length - 1}: `));
      }
    }
  });

  it('puts the rule in the error code', async () => {
    const [vault, account] = await signedIn();
    const f = load('join_after_epoch_change.json');
    const e = await rejection(vault.applyLog(account, listIdOf(f.entries[0]!), f.entries));
    expect(e.code).toBe('InviteNotOpen');
  });

  it('returns the verified state and what happened', async () => {
    const [vault, account] = await signedIn();
    const f = load('link_invite_lifecycle_valid.json');
    const list = listIdOf(f.entries[0]!);
    const { state, events, warnings } = await vault.applyLog(account, list, f.entries);
    expect(state.id).toBe(list);
    expect(state.head.seq).toBe(f.entries.length - 1);
    expect(state.head.hash).toEqual(new Uint8Array(createHash('sha256').update(f.entries.at(-1)!.raw).digest()));
    expect(state.owner.signPub).toBeInstanceOf(Uint8Array);
    expect(state.members[0]).toEqual(state.owner);
    expect(events.every((e) => e.list === list)).toBe(true);
    expect(events.map((e) => e.kind)).toContain('joined');
    expect(warnings).toEqual([]);
    expect(await vault.listState(account, list)).toEqual(state);
  });

  it('keeps nothing from a rejected batch', async () => {
    const [vault, account] = await signedIn();
    const f = load('join_bad_proof.json');
    const list = listIdOf(f.entries[0]!);
    await rejection(vault.applyLog(account, list, f.entries));
    expect((await rejection(vault.listState(account, list))).code).toBe('UnknownList');
    await vault.applyLog(account, list, f.entries.slice(0, -1));
    expect((await vault.listState(account, list)).head.seq).toBe(f.entries.length - 2);
  });

  it('checks the list id and the account handle', async () => {
    const [vault, account] = await signedIn();
    const f = load('list_lifecycle_valid.json');
    expect((await rejection(vault.applyLog(account, '00'.repeat(32), f.entries))).code).toBe('WrongList');
    expect((await rejection(vault.applyLog(account, 'nope', f.entries))).code).toBe('InvalidArgument');
    const other = 'acct_0' as AccountHandle;
    expect((await rejection(vault.listState(other, listIdOf(f.entries[0]!)))).code).toBe('UnknownAccount');
  });

  it('maps head checks to log warnings, and forgets lists', async () => {
    const [vault, account] = await signedIn();
    const f = load('list_lifecycle_valid.json');
    const list = listIdOf(f.entries[0]!);
    const { state } = await vault.applyLog(account, list, f.entries);
    expect(state.created).toBeGreaterThan(0);
    expect(await vault.checkHead(account, list, state.head.seq, state.head.hash)).toEqual([]);
    expect(await vault.checkHead(account, list, 0, new Uint8Array(32))).toEqual([
      { kind: 'log-rollback', list },
      { kind: 'log-fork', list },
    ]);
    await vault.forgetList(account, list);
    expect((await rejection(vault.listState(account, list))).code).toBe('UnknownList');
  });

  it('keeps a confirmed account and its logs for the next vault on the same device', async () => {
    const vault = new NodeVault();
    const s = await vault.startSignUp('https://runer.test');
    expect(await vault.storedAccounts()).toEqual([]);
    await vault.confirmSignUp(s.account);
    await vault.applyLog(s.account, s.inbox.list, [s.inbox.entry]);

    const after = new NodeVault(vault.device);
    expect(await after.storedAccounts()).toEqual([
      { account: s.account, server: 'https://runer.test', authPub: s.authPub, identity: s.identity, bundleGen: 1, inbox: s.inbox.list },
    ]);
    expect((await after.listState(s.account, s.inbox.list)).owner).toEqual(s.identity);
    expect((await rejection(after.setInbox(s.account, '00'.repeat(32)))).code).toBe('UnknownList');
    await after.signOut(s.account);
    expect(await new NodeVault(vault.device).storedAccounts()).toEqual([]);
  });

  it('signs in with the 12 words, and returns null for a typo', async () => {
    const vault = new NodeVault();
    expect(await vault.signIn('https://runer.test', 'legal winner thank')).toBeNull();
    const a = await vault.signIn('https://runer.test', WORDS);
    expect(a?.authPub).toHaveLength(32);
    expect((await vault.signIn('https://runer.test', WORDS))?.account).toBe(a?.account);
  });

  it('seals content and opens it in a batch, with rejections in place', async () => {
    const vault = new NodeVault();
    const s = await vault.startSignUp('https://runer.test');
    await vault.applyLog(s.account, s.inbox.list, [s.inbox.entry]);
    const update = await vault.sealContent(s.account, s.inbox.list, 'update', new Uint8Array([1, 2]));
    const snapshot = await vault.sealContent(s.account, s.inbox.list, { snapshot: { coversSeq: 3 } }, new Uint8Array([3]));
    const tampered = { ...update, ct: update.ct.map((b, i) => (i === 0 ? b ^ 1 : b)) };
    expect(await vault.openContents(s.account, s.inbox.list, [update, tampered, snapshot])).toEqual([
      { body: new Uint8Array([1, 2]), epoch: 0, author: s.identity.signPub, coversSeq: null, headKnown: true },
      { rejected: 'bad-content' },
      { body: new Uint8Array([3]), epoch: 0, author: s.identity.signPub, coversSeq: 3, headKnown: true },
    ]);
  });
});

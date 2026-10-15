import { describe, expect, it } from 'vitest';
import { createMemoryPlatform, ManualLifecycle, ManualLinkSource } from './platform-node/memory.js';

const bytes = (...n: number[]) => new Uint8Array(n);

describe('in-memory platform', () => {
  it('ranges over a prefix in key order, with after and limit', async () => {
    const { storage } = createMemoryPlatform();
    for (const k of ['a/3', 'a/1', 'b/1', 'a/2']) await storage.put('t', k, bytes(1));
    const keys = async (opts?: { after?: string; limit?: number }) => {
      const out: string[] = [];
      for await (const [k] of storage.range('t', 'a/', opts)) out.push(k);
      return out;
    };
    expect(await keys()).toEqual(['a/1', 'a/2', 'a/3']);
    expect(await keys({ after: 'a/1' })).toEqual(['a/2', 'a/3']);
    expect(await keys({ after: 'a/1', limit: 1 })).toEqual(['a/2']);
  });

  it('rolls a transaction back when it throws', async () => {
    const { storage } = createMemoryPlatform();
    await storage.put('t', 'k', bytes(1));
    await expect(
      storage.tx(async (tx) => {
        await tx.put('t', 'k', bytes(2));
        await tx.put('t', 'new', bytes(3));
        throw new Error('boom');
      }),
    ).rejects.toThrow('boom');
    expect(await storage.get('t', 'k')).toEqual(bytes(1));
    expect(await storage.get('t', 'new')).toBeNull();
    await storage.tx(async (tx) => tx.put('t', 'k', bytes(5)));
    expect(await storage.get('t', 'k')).toEqual(bytes(5));
  });

  it('delivers lifecycle and link events until unsubscribed', async () => {
    const life = new ManualLifecycle();
    const links = new ManualLinkSource('runer://join#first');
    const seen: string[] = [];
    const off = life.onOffline(() => seen.push('offline'));
    life.onOnline(() => seen.push('online'));
    life.goOffline();
    expect(life.isOnline()).toBe(false);
    off();
    life.goOffline();
    life.goOnline();
    expect(seen).toEqual(['offline', 'online']);
    expect(await links.initial()).toBe('runer://join#first');
    const urls: string[] = [];
    links.onLink((u) => urls.push(u));
    links.open('runer://join#second');
    expect(urls).toEqual(['runer://join#second']);
  });
});

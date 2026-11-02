import type { Platform, Transport } from '@runer/client-core';
import { createMemoryPlatform } from '../packages/client-core/test/platform-node/memory.js';
import { NodeVault } from '../packages/client-core/test/platform-node/vault.js';
import { httpTransport } from './http-transport.js';

/** The server under test (its `PUBLIC_URL`): `e2e/against-server.sh` starts one with PostgreSQL and sets this. */
const url = process.env.RUNER_E2E_SERVER;
if (url === undefined) throw new Error('RUNER_E2E_SERVER is not set: run the suite with e2e/against-server.sh');
export const server = { url };

/** A device's connection to the server, with its own session. */
export function transport(): Transport {
  return httpTransport(server.url);
}

/** A fresh device: its own vault, store and session, talking to the server. */
export function device(): Platform {
  return createMemoryPlatform({ transport: transport() });
}

/** The same device after a restart: its keychain, vault store and local store kept, memory and the session gone. */
export function restart(p: Platform): Platform {
  return createMemoryPlatform({
    vault: new NodeVault((p.vault as NodeVault).device),
    storage: p.storage,
    transport: transport(),
  });
}

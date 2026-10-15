import type { ListId } from '@runer/client-core';

/** Where the signed-in app is. Lists and shares are named by id, so a sync can't leave a stale object on screen. */
export type View =
  | { name: 'today' }
  | { name: 'lists' }
  | { name: 'list'; id: ListId }
  | { name: 'settings' }
  | { name: 'help' }
  | { name: 'search' }
  | { name: 'join'; link: string }
  | { name: 'warnings' };

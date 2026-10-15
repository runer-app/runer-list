<script lang="ts">
  import type { PublicIdentity } from '@runer/client-core';
  import { colorOf, idOf, initialOf, type Names } from '../people.js';

  /** Up to four round initials for a list's members, the owner first. */
  let { members, me, names = {}, size = 26 }: { members: readonly PublicIdentity[]; me: string; names?: Names; size?: number } = $props();
  const shown = $derived(members.slice(0, 4));
</script>

<span class="avatars" aria-hidden="true" style:--size="{size}px">
  {#each shown as m (idOf(m))}
    <span class="avatar" style:background={colorOf(idOf(m))}>{initialOf(idOf(m), me, members, names)}</span>
  {/each}
</span>

<style>
  .avatars {
    display: inline-flex;
  }
  .avatar {
    display: inline-grid;
    place-items: center;
    width: var(--size);
    height: var(--size);
    border-radius: 50%;
    border: 2px solid var(--bg);
    color: #fff;
    font-size: calc(var(--size) * 0.42);
    font-weight: 600;
  }
  .avatar + .avatar {
    margin-left: calc(var(--size) * -0.27);
  }
</style>

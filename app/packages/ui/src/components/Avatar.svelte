<script lang="ts">
  import { colorOf } from '../people.js';

  /** One person's round initial. */
  let { id, initial, size = 32 }: { id: string; initial: string; size?: number } = $props();
</script>

<span class="avatar" aria-hidden="true" style:background={colorOf(id)} style:width="{size}px" style:height="{size}px" style:font-size="{Math.round(size * 0.4)}px">{initial}</span>

<style>
  .avatar {
    display: inline-grid;
    flex: none;
    place-items: center;
    border-radius: 50%;
    color: #fff;
    font-weight: 600;
  }
</style>

<script lang="ts">
  import type { Item } from '@runer/client-core';
  import type { Snippet } from 'svelte';
  import { t } from '../messages.js';
  import Icon from './Icon.svelte';

  /**
   * A task in a view across lists (Today, Search): checkbox, title, and on
   * the right its date and the list it's in.
   */
  let {
    item,
    when,
    reminder = false,
    overdue = false,
    where,
    toggle,
    open,
    grip,
    dragged = false,
    drop = false,
    selected = false,
  }: {
    item: Item;
    /** The date, or date and time, beside the title. */
    when?: string | undefined;
    /** `when` has a time, which is the reminder. */
    reminder?: boolean;
    overdue?: boolean;
    /** The list's name. */
    where?: string | undefined;
    toggle: () => void;
    /** Opens the item's detail. */
    open?: () => void;
    /** A drag handle, shown in the gutter on hover or focus. */
    grip?: Snippet;
    /** Being dragged; a drop would land before this row. */
    dragged?: boolean;
    drop?: boolean;
    /** Open in the side panel. */
    selected?: boolean;
  } = $props();
</script>

<li class:done={item.done} class:dragged class:drop class:selected>
  {#if grip}<span class="gutter">{@render grip()}</span>{/if}
  <input type="checkbox" checked={item.done} onchange={toggle} aria-label={`${t('task.done')}: ${item.title}`} />
  {#if open !== undefined}
    <button class="title" onclick={open}>{item.title}</button>
  {:else}
    <span class="title">{item.title}</span>
  {/if}
  {#if overdue}<span class="when late">{t('task.overdue')}{when === undefined ? '' : ` · ${when}`}</span>
  {:else if when !== undefined}<span class="when" class:reminder>{#if reminder}<Icon name="bell" />{/if}{when}</span>{/if}
  {#if where !== undefined}<span class="where"><span class="ring" aria-hidden="true"></span>{where}</span>{/if}
</li>

<style>
  li {
    position: relative;
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 40px;
    border-bottom: 1px solid var(--divider);
  }
  li.selected {
    margin: 0 -10px;
    padding: 0 10px;
    border-bottom-color: transparent;
    border-radius: 8px;
    background: var(--surface);
    box-shadow: inset 0 0 0 1px var(--border);
  }
  .title {
    flex: 1;
    min-width: 0;
    padding: 0;
    border: 0;
    background: none;
    font-size: 15px;
    text-align: left;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  button.title {
    cursor: pointer;
  }
  button.title:hover {
    text-decoration: underline;
    text-decoration-color: var(--border-strong);
  }
  .done .title {
    color: var(--text-muted);
    text-decoration: line-through;
  }
  .when,
  .where {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    color: var(--text-muted);
    font-size: 12px;
    white-space: nowrap;
  }
  .when :global(svg) {
    width: 12px;
    height: 12px;
    vertical-align: 0;
  }
  .when.reminder {
    color: var(--accent-label);
    font-weight: 500;
  }
  .when.late {
    color: var(--danger);
  }
  .where {
    justify-content: flex-end;
    max-width: 140px;
    gap: 6px;
  }
  .ring {
    flex: none;
    width: 10px;
    height: 10px;
    border: 2px solid currentColor;
    border-radius: 50%;
    box-sizing: border-box;
  }
  .gutter {
    position: absolute;
    left: -28px;
    top: 50%;
    transform: translateY(-50%);
    opacity: 0;
  }
  li:hover .gutter,
  li:focus-within .gutter {
    opacity: 1;
  }
  /* Touch: nothing hovers, and a tap doesn't focus the row, so the handle is always there. */
  @media (hover: none) {
    .gutter {
      opacity: 1;
    }
  }
  @media (pointer: coarse) {
    li {
      min-height: 44px;
    }
    button.title {
      align-self: stretch;
    }
  }
  li.dragged {
    opacity: 0.35;
  }
  li.drop::before {
    content: '';
    position: absolute;
    left: 0;
    right: 0;
    top: -2px;
    height: 2px;
    border-radius: 1px;
    background: var(--accent);
  }
</style>

<script lang="ts" module>
  export type IconName = 'sun' | 'inbox' | 'list' | 'lists' | 'people' | 'lock' | 'settings' | 'back' | 'add-person' | 'chevron' | 'warning' | 'close' | 'plus' | 'copy' | 'check' | 'calendar' | 'clock' | 'person' | 'trash' | 'search' | 'bell' | 'laptop' | 'phone' | 'browser' | 'key' | 'server' | 'help';
</script>

<script lang="ts">
  /** Line icons, drawn inline so they look the same on every system (no emoji or icon font). */
  let { name }: { name: IconName } = $props();
</script>

<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
  {#if name === 'sun'}
    <circle cx="12" cy="12" r="4" />
    <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
  {:else if name === 'inbox'}
    <path d="M3 13l2.5-7.5A2 2 0 0 1 7.4 4h9.2a2 2 0 0 1 1.9 1.5L21 13v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
    <path d="M3 13h5l1.5 2.5h5L16 13h5" />
  {:else if name === 'list'}
    <circle cx="12" cy="12" r="8" />
  {:else if name === 'lists'}
    <path d="M9 6h11M9 12h11M9 18h11" />
    <path d="M4 6h.01M4 12h.01M4 18h.01" />
  {:else if name === 'people'}
    <circle cx="9" cy="8" r="3.5" />
    <path d="M2.5 20a6.5 6.5 0 0 1 13 0" />
    <path d="M16 4.5a3.5 3.5 0 0 1 0 7M18 14a6.5 6.5 0 0 1 3.5 6" />
  {:else if name === 'settings'}
    <circle cx="12" cy="12" r="3" />
    <path
      d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"
    />
  {:else if name === 'back'}
    <path d="M15 18l-6-6 6-6" />
  {:else if name === 'chevron'}
    <path d="M9 18l6-6-6-6" />
  {:else if name === 'add-person'}
    <circle cx="9" cy="8" r="3.5" />
    <path d="M2.5 20a6.5 6.5 0 0 1 13 0M19 8v6M16 11h6" />
  {:else if name === 'warning'}
    <path d="M10.3 3.9L1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" />
    <path d="M12 9v4M12 17h.01" />
  {:else if name === 'close'}
    <path d="M18 6L6 18M6 6l12 12" />
  {:else if name === 'search'}
    <circle cx="11" cy="11" r="6.5" />
    <path d="M16 16l4 4" />
  {:else if name === 'bell'}
    <path d="M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9" />
    <path d="M10.5 21a1.8 1.8 0 0 0 3 0" />
  {:else if name === 'laptop'}
    <rect x="4" y="5" width="16" height="11" rx="1.5" />
    <path d="M2 19h20" />
  {:else if name === 'phone'}
    <rect x="7" y="3" width="10" height="18" rx="2" />
    <path d="M11 18h2" />
  {:else if name === 'browser'}
    <rect x="3" y="4" width="18" height="16" rx="2" />
    <path d="M3 9h18" />
  {:else if name === 'key'}
    <circle cx="8" cy="15" r="4" />
    <path d="M11 12l9-9M16 7l3 3" />
  {:else if name === 'server'}
    <rect x="4" y="4" width="16" height="6" rx="1.5" />
    <rect x="4" y="14" width="16" height="6" rx="1.5" />
    <path d="M8 7h.01M8 17h.01" />
  {:else if name === 'plus'}
    <path d="M12 5v14M5 12h14" />
  {:else if name === 'copy'}
    <rect x="9" y="9" width="12" height="12" rx="2" />
    <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
  {:else if name === 'calendar'}
    <rect x="3.5" y="5" width="17" height="15.5" rx="2.5" />
    <path d="M3.5 10h17M8 3v4M16 3v4" />
  {:else if name === 'help'}
    <circle cx="12" cy="12" r="8.5" />
    <path d="M9.6 9.5a2.5 2.5 0 1 1 3.6 2.2c-.8.4-1.2 1-1.2 1.8M12 16.5h.01" />
  {:else if name === 'clock'}
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 7.5V12l3 2" />
  {:else if name === 'person'}
    <circle cx="12" cy="8" r="3.5" />
    <path d="M5 20a7 7 0 0 1 14 0" />
  {:else if name === 'trash'}
    <path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3" />
  {:else if name === 'check'}
    <path d="M20 6L9 17l-5-5" />
  {:else}
    <rect x="5" y="11" width="14" height="10" rx="2" />
    <path d="M8 11V8a4 4 0 0 1 8 0v3" />
    <circle cx="12" cy="16" r="1" />
  {/if}
</svg>

<style>
  svg {
    flex: none;
    vertical-align: -3px;
  }
</style>

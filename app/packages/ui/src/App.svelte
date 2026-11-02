<script lang="ts">
  import type { ClientCore } from '@runer/client-core';
  import { onMount } from 'svelte';
  import { t } from './messages.js';
  import type { Shell } from './shell.js';
  import Welcome from './screens/Welcome.svelte';
  import AccountKey from './screens/AccountKey.svelte';
  import SignIn from './screens/SignIn.svelte';
  import Main from './screens/Main.svelte';
  import Problem from './screens/Problem.svelte';

  let { shell }: { shell: Shell } = $props();

  type Screen =
    | { name: 'loading' }
    | { name: 'welcome' }
    | { name: 'sign-in'; server: string }
    | { name: 'account-key'; core: ClientCore; words: string[] }
    | { name: 'main'; core: ClientCore; link: string | null }
    | { name: 'problem'; error: unknown; retry: () => void };

  let screen = $state<Screen>({ name: 'loading' });
  /** An invite link that arrived while signed out: Join opens after sign-in, and refuses a link for another server. */
  let pendingLink: string | null = null;
  let updateReady = $state(false);

  async function start() {
    screen = { name: 'loading' };
    try {
      const core = await shell.resume();
      if (core === null) screen = { name: 'welcome' };
      else enter(core);
    } catch (error) {
      screen = { name: 'problem', error, retry: start };
    }
  }

  onMount(() => {
    shell.update?.onReady(() => (updateReady = true));
    void (async () => {
      const first = await shell.links?.initial().catch(() => null);
      if (first) pendingLink = first;
      await start();
    })();
    return shell.links?.onLink((link) => {
      if (screen.name !== 'main') pendingLink = link;
    });
  });

  function enter(core: ClientCore) {
    screen = { name: 'main', core, link: pendingLink };
    pendingLink = null;
  }
</script>

<!-- The window is dragged by its top edge, where the title bar would be (macOS overlay title bar; the shell enables it). -->
<div class="titlebar" data-tauri-drag-region aria-hidden="true"></div>

{#if screen.name === 'loading'}
  <p class="loading" aria-busy="true">{t('app.loading')}</p>
{:else if screen.name === 'welcome'}
  <Welcome
    defaultServer={shell.defaultServer}
    version={shell.version}
    signUp={async (server, name) => {
      const { core, accountKeyWords } = await shell.signUp(server, name);
      screen = { name: 'account-key', core, words: accountKeyWords };
    }}
    signIn={(server) => (screen = { name: 'sign-in', server })}
  />
{:else if screen.name === 'sign-in'}
  {@const server = screen.server}
  <SignIn
    signIn={async (words) => {
      const core = await shell.signIn(server, words);
      if (core !== null) enter(core);
      return core !== null;
    }}
    back={() => (screen = { name: 'welcome' })}
  />
{:else if screen.name === 'account-key'}
  {@const core = screen.core}
  <AccountKey words={screen.words} done={() => enter(core)} />
{:else if screen.name === 'main'}
  {#key screen.core}
    <Main
      core={screen.core}
      pendingLink={screen.link}
      links={shell.links ? (cb) => shell.links!.onLink(cb) : undefined}
      debugLog={shell.debugLog}
      update={updateReady ? shell.update : undefined}
      version={shell.version}
      signedOut={() => {
        pendingLink = null;
        screen = { name: 'welcome' };
      }}
    />
  {/key}
{:else}
  <Problem error={screen.error} retry={screen.retry} />
{/if}

<style>
  .titlebar {
    position: fixed;
    inset: 0 0 auto 0;
    height: var(--titlebar);
    z-index: 50;
  }
  .loading {
    display: grid;
    place-items: center;
    height: 100vh;
    margin: 0;
    color: var(--text-muted);
  }
</style>

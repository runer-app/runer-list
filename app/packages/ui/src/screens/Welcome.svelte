<script lang="ts">
  import { t } from '../messages.js';
  import Button from '../components/Button.svelte';
  import Centered from '../components/Centered.svelte';
  import Icon from '../components/Icon.svelte';
  import Logo from '../components/Logo.svelte';

  let {
    defaultServer,
    version,
    signUp,
    signIn,
  }: {
    defaultServer: string;
    version?: string | undefined;
    signUp: (server: string, name: string) => Promise<void>;
    signIn: (server: string) => void;
  } = $props();

  // The server picker (client-spec §7.1). The login message covers this URL, so it must equal the server's PUBLIC_URL.
  // The hosted server is the default; a self-hosted one is tucked away in the footer.
  // The field starts blank: the hosted address is never shown, and cancelling goes back to it.
  let server = $state('');
  let editing = $state(false);
  let busy = $state(false);
  let problem = $state('');
  // "Create account" offers a name before anything is made. It is optional: only the people the user shares lists with see it.
  let naming = $state(false);
  let name = $state('');

  const chosen = $derived((editing ? server : defaultServer).trim().replace(/\/+$/, ''));
  const valid = $derived(/^https?:\/\/[^\s/?#]+[^\s?#]*$/.test(chosen));

  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (!valid) return;
    problem = '';
    busy = true;
    try {
      await signUp(chosen, name);
    } catch (e) {
      problem = e instanceof TypeError || (e instanceof Error && /network|offline|connect/i.test(e.message)) ? t('error.network') : e instanceof Error ? e.message : String(e);
      busy = false;
    }
  }
</script>

{#if naming}
  <Centered>
    <h1>{t('welcome.nameTitle')}</h1>
    <p>{t('welcome.nameLead')}</p>
    <form onsubmit={create}>
      <label class="name">
        <span class="label">{t('welcome.name')}</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input class="field" bind:value={name} maxlength="60" autocomplete="name" disabled={busy} autofocus />
      </label>
      {#if problem !== ''}<p class="problem" role="alert">{problem}</p>{/if}
      <div class="actions">
        <Button large type="submit" disabled={busy}>{busy ? t('create.working') : t('welcome.create')}</Button>
        <button type="button" class="link" onclick={() => ((naming = false), (problem = ''))} disabled={busy}>{t('signIn.back')}</button>
      </div>
    </form>
  </Centered>
{:else}
<Centered header={false}>
  <Logo size={22} />
  <h1 class="big">{t('welcome.title')}</h1>
  <p>{t('welcome.lead')}</p>

  <div class="buttons">
    <Button large onclick={() => valid && (naming = true)} disabled={busy || !valid}>{t('welcome.create')}</Button>
    <Button large variant="quiet" onclick={() => valid && signIn(chosen)} disabled={busy || !valid}>{t('welcome.signIn')}</Button>
  </div>
  {#if problem !== ''}<p class="problem" role="alert">{problem}</p>{/if}

  {#if editing}
    <label class="server">
      {t('welcome.server')}
      <!-- svelte-ignore a11y_autofocus -->
      <input class="field mono" bind:value={server} placeholder="https://lists.example.com" inputmode="url" autocomplete="off" autocapitalize="none" spellcheck="false" autofocus />
      {#if chosen !== '' && !valid}<span class="problem">{t('welcome.serverInvalid')}</span>{/if}
    </label>
  {/if}

  <p class="key"><Icon name="key" />{t('welcome.noPassword')}</p>

  {#snippet footer()}
    {#if editing}
      <button class="own-link" onclick={() => ((editing = false), (server = ''))}>{t('common.cancel')}</button>
    {:else}
      <button class="own own-link" onclick={() => (editing = true)}><Icon name="server" />{t('welcome.ownServer')}</button>
    {/if}
    {#if version}<span>{t('welcome.version', { version })}</span>{/if}
  {/snippet}
</Centered>
{/if}

<style>
  .name {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-top: 28px;
  }
  .label {
    color: var(--text-muted);
    font-size: 12px;
  }
  .buttons {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-top: 32px;
  }
  .server {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-top: 20px;
    color: var(--text-muted);
    font-size: 12px;
  }
  .server .problem {
    margin: 0;
  }
  .mono {
    font-family: var(--font-mono);
  }
  .key {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 24px 0 0;
    color: var(--text-muted);
    font-size: 13px;
  }
  .key :global(svg) {
    width: 16px;
    height: 16px;
    color: var(--accent);
  }
  .own {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .own-link {
    padding: 0;
    border: 0;
    background: none;
    color: var(--text-muted);
    font-size: 13px;
    cursor: pointer;
  }
  .own-link:hover {
    color: var(--text);
  }
  .own :global(svg) {
    width: 14px;
    height: 14px;
  }
</style>

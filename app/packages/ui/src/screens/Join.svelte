<script lang="ts">
  import { InviteError, toBase64url, type ClientCore, type JoinableInvite, type ListId } from '@runer/client-core';
  import { onMount } from 'svelte';
  import { t, type MessageKey } from '../messages.js';
  import Avatar from '../components/Avatar.svelte';
  import Button from '../components/Button.svelte';
  import Centered from '../components/Centered.svelte';

  /**
   * Join (client-spec §7.1): read the link, show the list's name and who
   * invited (by the name in the invite; a link made without one names
   * nobody), then join. The others see us by our account name. The link's
   * secret never leaves the device (§6.3).
   */
  let { core, link, joined, cancel }: { core: ClientCore; link: string; joined: (id: ListId) => void; cancel: () => void } = $props();

  let invite = $state<JoinableInvite | null>(null);
  let error = $state<MessageKey | null>(null);
  let busy = $state(true);

  function explain(e: unknown): MessageKey {
    if (e instanceof InviteError) return `join.error.${e.code}`;
    return 'error.network';
  }

  onMount(() => {
    core.invites.open(link).then(
      (i) => ((invite = i), (busy = false)),
      (e: unknown) => ((error = explain(e)), (busy = false)),
    );
  });

  async function join(e: SubmitEvent) {
    e.preventDefault();
    if (invite === null) return;
    busy = true;
    error = null;
    try {
      joined(await invite.join());
    } catch (err) {
      error = explain(err);
      busy = false;
    }
  }

</script>

<Centered>
  {#if invite !== null}
    <p class="from">
      {#if invite.ownerName !== ''}
        <Avatar id={toBase64url(invite.owner.signPub)} initial={[...invite.ownerName][0]!.toUpperCase()} size={22} />
        {t('join.invited', { owner: invite.ownerName })}
      {:else}
        {t('join.invitedNameless')}
      {/if}
    </p>
    <h1>{invite.name || t('list.untitled')}</h1>
    <p>{t('join.lead')}</p>
    <form onsubmit={join}>
      {#if error !== null}<p class="problem" role="alert">{t(error)}</p>{/if}
      <div class="actions">
        <Button large type="submit" disabled={busy}>{busy ? t('join.joining') : t('join.join')}</Button>
        <button type="button" class="link" onclick={cancel}>{t('common.cancel')}</button>
      </div>
    </form>
  {:else}
    <h1>{t('join.title')}</h1>
    {#if busy}<p aria-busy="true">{t('join.opening')}</p>{/if}
    {#if error !== null}<p class="problem" role="alert">{t(error)}</p>{/if}
    <div class="actions"><Button large variant="quiet" onclick={cancel}>{t('join.back')}</Button></div>
  {/if}
</Centered>

<style>
  .from {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 14px;
    color: var(--text-muted);
    font-size: 13px;
    /* The owner's name comes from the invite, as their app wrote it: a long one without spaces wraps. */
    overflow-wrap: anywhere;
  }
</style>

<script lang="ts">
  import { toBase64url, type ClientCore, type ListId, type PublicIdentity } from '@runer/client-core';
  import { fromStore } from 'svelte/store';
  import { instantLabel } from '../dates.js';
  import { t } from '../messages.js';
  import { allNames, idOf, initialOf, nameOf } from '../people.js';
  import Avatar from '../components/Avatar.svelte';
  import Confirm from '../components/Confirm.svelte';
  import Dialog from '../components/Dialog.svelte';
  import Icon from '../components/Icon.svelte';

  /**
   * Share, a dialog over the list. Everyone sees
   * who is in it; the owner adds known contacts directly, makes a
   * single-use link for someone new, turns links off and removes people.
   * Safety codes are behind one line at the bottom: few people compare
   * them, and nobody is asked to.
   */
  let { core, id, close }: { core: ClientCore; id: ListId; close: () => void } = $props();

  const stores = $derived({ lists: fromStore(core.lists) });
  const summary = $derived(stores.lists.current.find((l) => l.id === id));
  // The handle's members, role and open invites are read when it's made: make it again when the lists change.
  const list = $derived.by(() => (stores.lists.current, core.list(id)));
  const members = $derived(summary?.members ?? list.members);
  const names = $derived(summary?.names ?? list.names);
  const everyone = $derived(allNames(stores.lists.current));
  const owner = $derived(list.role === 'owner');
  const listName = $derived(summary?.name || t('list.untitled'));

  let contacts = $state<PublicIdentity[]>([]);
  let showCodes = $state(false);
  let codes = $state<Record<string, string[]>>({});
  let link = $state<{ url: string; expires: number; invitePub: Uint8Array } | null>(null);
  let busy = $state(false);
  let error = $state('');
  let copied = $state(false);

  const candidates = $derived(contacts.filter((c) => !members.some((m) => idOf(m) === idOf(c))));
  // The link just made is shown on its own above; the rest are listed.
  const pending = $derived(list.openInvites.filter((i) => link === null || toBase64url(i.invitePub) !== toBase64url(link.invitePub)));

  $effect(() => {
    void stores.lists.current;
    if (owner) void core.knownContacts().then((c) => (contacts = c));
  });

  async function act(fn: () => Promise<unknown>) {
    busy = true;
    error = '';
    try {
      await fn();
    } catch {
      error = t('share.failed');
    } finally {
      busy = false;
    }
  }

  const createLink = () =>
    act(async () => {
      const made = await list.createInvite();
      link = { url: made.link, expires: made.expires, invitePub: made.invitePub };
      copied = false;
    });

  async function copy() {
    if (link === null) return;
    try {
      await navigator.clipboard.writeText(link.url);
      copied = true;
    } catch {
      copied = false;
    }
  }

  // Codes for whoever is in the list while they are shown, so someone who joins meanwhile gets one too.
  $effect(() => {
    if (!showCodes) return;
    void Promise.all(members.map(async (m) => [idOf(m), await core.safetyCode(m)] as const)).then((all) => (codes = Object.fromEntries(all)));
  });

  const display = (k: string) => nameOf(k, members, names);
</script>

<Dialog title={owner ? t('share.title', { name: listName }) : t('share.membersTitle', { name: listName })} lead={owner ? t('share.lead') : t('share.membersLead')} {close}>
  {#if error !== ''}<p class="error" role="alert">{error}</p>{/if}

  <section>
    <h3>{t('share.people')}</h3>
    <ul>
      {#each members as m, i (idOf(m))}
        {@const k = idOf(m)}
        <li>
          <div class="person">
            <Avatar id={k} initial={initialOf(k, core.me, members, names)} />
            <span class="name">
              {display(k)}
              {#if k === core.me}<span class="muted">{t('share.you')}</span>{/if}
            </span>
            <span class="actions">
              {#if i === 0}
                <span class="role">{t('share.owner')}</span>
              {:else if owner && k !== core.me}
                <Confirm
                  look="text"
                  label={t('share.remove')}
                  question={t('share.removeQuestion', { name: display(k) })}
                  warning={t('share.removeWarning', { name: display(k), list: listName })}
                  detail={t('share.removeDetail', { name: display(k) })}
                  person={{ id: k, initial: initialOf(k, core.me, members, names) }}
                  confirmLabel={t('share.removeConfirm', { name: display(k) })}
                  run={() => list.removeMember(m.signPub)}
                />
              {/if}
            </span>
          </div>
        </li>
      {/each}
    </ul>
  </section>

  {#if owner}
    {#if candidates.length > 0}
      <section>
        <h3>{t('share.with')}</h3>
        <ul>
          {#each candidates as c (idOf(c))}
            {@const k = idOf(c)}
            <li>
              <div class="person">
                <Avatar id={k} initial={initialOf(k, core.me, [c], everyone)} />
                <span class="name">{nameOf(k, [c], everyone)}</span>
                <span class="actions">
                  <button class="text" disabled={busy} onclick={() => void act(() => list.addMember(c.signPub))}>{t('share.add')}</button>
                </span>
              </div>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    <section>
      <h3>{link !== null ? t('share.link') : t('share.inviteTitle')}</h3>
      {#if link === null}
        <button class="primary" disabled={busy} onclick={() => void createLink()}>{t('share.createLink')}</button>
      {:else}
        {@const pub = link.invitePub}
        <div class="link">
          <input readonly value={link.url} aria-label={t('share.link')} onfocus={(e) => e.currentTarget.select()} />
          <button onclick={() => void copy()}>{copied ? t('share.copied') : t('share.copy')}</button>
        </div>
        <div class="below">
          <span>{t('share.linkHint', { when: instantLabel(link.expires) })}</span>
          <span class="below-actions">
            <button class="plain" onclick={() => (link = null)}>{t('share.anotherLink')}</button>
            <Confirm
              look="link"
              label={t('share.revoke')}
              question={t('share.revokeQuestion')}
              warning={t('share.revokeWarning')}
              confirmLabel={t('share.revokeConfirm')}
              run={async () => {
                await list.revokeInvite(pub);
                link = null;
              }}
            />
          </span>
        </div>
      {/if}
    </section>

    {#if pending.length > 0}
      <section>
        <h3>{t('share.pending')}</h3>
        <ul>
          {#each pending as inv (toBase64url(inv.invitePub))}
            <li>
              <div class="person">
                <span class="link-icon"><Icon name="add-person" /></span>
                <span class="name">
                  {t('share.link')}
                  <span class="sub">{inv.expires < Date.now() ? t('share.expired') : t('share.expires', { when: instantLabel(inv.expires) })}</span>
                </span>
                <span class="actions">
                  <Confirm
                    look="text"
                    label={t('share.revoke')}
                    question={t('share.revokeQuestion')}
                    warning={t('share.revokeWarning')}
                    confirmLabel={t('share.revokeConfirm')}
                    run={() => list.revokeInvite(inv.invitePub)}
                  />
                </span>
              </div>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  {/if}

  <section>
    <button class="plain" aria-expanded={showCodes} onclick={() => (showCodes = !showCodes)}>{showCodes ? t('share.hideCodes') : t('share.showCodes')}</button>
    {#if showCodes}
      <p class="hint">{t('share.safetyHint')}</p>
      <dl>
        {#each members as m (idOf(m))}
          {@const k = idOf(m)}
          {#if codes[k]}
            <dt>
              {display(k)}
              {#if k === core.me}<span class="muted">{t('share.you')}</span>{/if}
            </dt>
            <dd>{codes[k].join(' ')}</dd>
          {/if}
        {/each}
      </dl>
    {/if}
  </section>
</Dialog>

<style>
  section {
    margin-top: 20px;
  }
  h3 {
    margin: 0 0 4px;
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 500;
  }
  ul {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    border-bottom: 1px solid var(--divider);
  }
  .person {
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 52px;
  }
  .name {
    flex: 1;
    min-width: 0;
    font-size: 14px;
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .muted,
  .sub {
    color: var(--text-muted);
    font-weight: 400;
  }
  .sub {
    display: block;
    font-size: 12px;
  }
  .actions {
    display: flex;
    flex: none;
    align-items: center;
    gap: 2px;
  }
  .role {
    padding: 0 10px;
    color: var(--text-muted);
    font-size: 13px;
  }
  .text {
    height: 32px;
    padding: 0 10px;
    border: 0;
    border-radius: 8px;
    background: transparent;
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
  }
  .text:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  dl {
    margin: 10px 0 0;
  }
  dt {
    margin-top: 10px;
    font-size: 13px;
    font-weight: 500;
  }
  dd {
    margin: 2px 0 0;
    font-family: var(--font-mono);
    font-size: 13px;
    letter-spacing: 0.02em;
  }
  .hint {
    margin: 8px 0 0;
    color: var(--text-muted);
    font-size: 12px;
    line-height: 1.5;
  }
  .error {
    margin: 14px 0 0;
    color: var(--danger);
    font-size: 13px;
  }
  .link-icon {
    display: grid;
    flex: none;
    place-items: center;
    width: 32px;
    height: 32px;
    border-radius: 50%;
    background: var(--surface);
    color: var(--text-muted);
  }
  .link-icon :global(svg) {
    width: 16px;
    height: 16px;
  }

  .primary {
    height: 40px;
    margin-top: 6px;
    padding: 0 16px;
    border: 0;
    border-radius: 10px;
    background: var(--accent);
    color: var(--accent-text);
    font-size: 14px;
    font-weight: 600;
    white-space: nowrap;
    cursor: pointer;
  }
  .primary:disabled {
    opacity: 0.6;
    cursor: default;
  }

  .link {
    display: flex;
    align-items: center;
    gap: 4px;
    height: 40px;
    margin-top: 6px;
    padding: 0 4px 0 12px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--bg-sidebar);
  }
  .link input {
    flex: 1;
    min-width: 0;
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: 13px;
    outline: none;
    text-overflow: ellipsis;
  }
  .link button {
    display: inline-flex;
    align-items: center;
    height: 32px;
    padding: 0 12px;
    border: 0;
    border-radius: 7px;
    background: var(--surface-hover);
    color: var(--text);
    font-size: 13px;
    font-weight: 500;
    text-decoration: none;
    cursor: pointer;
  }
  .below {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    gap: 8px;
    margin-top: 8px;
    color: var(--text-muted);
    font-size: 12px;
  }
  .below-actions {
    display: flex;
    gap: 14px;
  }
  .plain {
    padding: 0;
    border: 0;
    background: none;
    color: var(--text-muted);
    font-size: 12px;
    cursor: pointer;
  }
  .plain:hover {
    color: var(--text);
  }
  .below :global(.trigger.link) {
    font-size: 12px;
  }
</style>

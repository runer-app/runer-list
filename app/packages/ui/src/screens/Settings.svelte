<script lang="ts">
  import type { ClientCore, Session } from '@runer/client-core';
  import { onMount } from 'svelte';
  import { fromStore } from 'svelte/store';
  import { deviceHour12 } from '../clock.svelte.js';
  import { agoLabel } from '../dates.js';
  import { describeDevice } from '../devices.js';
  import { t } from '../messages.js';
  import Confirm from '../components/Confirm.svelte';
  import KeyWords from '../components/KeyWords.svelte';
  import Icon from '../components/Icon.svelte';

  /**
   * Settings (client-spec §7.1), one section per row: what it is on the
   * left, the controls on the right. The account key, devices (sessions),
   * reminders and their privacy, the time format, and the server and
   * sign-out.
   */
  let { core, signedOut, help }: { core: ClientCore; signedOut: () => void; help: () => void } = $props();

  const WEEK = 7 * 24 * 60 * 60 * 1000;
  const ACTIVE = 5 * 60 * 1000;

  const settings = $derived(fromStore(core.settings));
  const clockChoice = $derived(settings.current.clock ?? (deviceHour12() ? '12h' : '24h'));
  let sessions = $state<Session[] | null>(null);
  let sessionsError = $state(false);
  let words = $state<{ list: string[]; fresh: boolean } | null>(null);
  let permission = $state<'granted' | 'denied' | 'prompt' | null>(null);
  let error = $state('');
  const accountName = $derived(fromStore(core.name));
  let name = $state('');
  $effect.pre(() => {
    name = accountName.current;
  });

  const seen = (s: Session) => Date.parse(s.lastSeenAt);
  /** This device first, then the most recently seen. Others not seen for a week fold into one line. */
  const recent = $derived((sessions ?? []).filter((s) => s.current || Date.now() - seen(s) < WEEK).sort((a, b) => Number(b.current) - Number(a.current) || seen(b) - seen(a)));
  const older = $derived((sessions ?? []).filter((s) => !s.current && Date.now() - seen(s) >= WEEK));
  const server = $derived.by(() => {
    try {
      return new URL(core.server).host;
    } catch {
      return core.server;
    }
  });

  async function loadSessions() {
    sessionsError = false;
    try {
      sessions = await core.sessions.list();
    } catch {
      sessionsError = true;
    }
  }

  onMount(() => {
    void loadSessions();
    void core.notificationPermission().then((p) => (permission = p), () => (permission = null));
  });

  async function guard(fn: () => Promise<void>, message = t('settings.failed')) {
    error = '';
    try {
      await fn();
    } catch {
      error = message;
      throw new Error(message);
    }
  }

  // Redraws the switches and choices after a failed save, so they show what is kept and not what was pressed.
  let unsaved = $state(0);

  /** A setting is kept on this device first and sent later, so a failure here is the device's, never the connection's. */
  function save(fn: () => Promise<void>) {
    void guard(fn, t('settings.notSaved')).catch(() => unsaved++);
  }

  function saveName(e: SubmitEvent) {
    e.preventDefault();
    save(() => core.setName(name));
  }

  const revoke = (ids: string[]) =>
    guard(async () => {
      for (const id of ids) await core.sessions.revoke(id);
      await loadSessions();
    });
</script>

<h1>{t('nav.settings')}</h1>
{#if error !== ''}<p class="error" role="alert">{error}</p>{/if}

{#snippet about(title: string, lead?: string)}
  <div class="about">
    <h2>{title}</h2>
    {#if lead}<p class="muted">{lead}</p>{/if}
  </div>
{/snippet}

<section>
  {@render about(t('settings.name'), t('settings.nameLead'))}
  <form class="key" onsubmit={saveName}>
    <input class="field" bind:value={name} maxlength="60" autocomplete="name" aria-label={t('settings.name')} />
    <!-- An empty name is saved too: it clears the name, here and in the lists shared with others. -->
    <button class="small" type="submit" disabled={name.trim() === accountName.current}>{t('settings.saveName')}</button>
  </form>
</section>

<section>
  {@render about(t('settings.account'), t('settings.accountLead'))}
  <div class="stack">
    {#if words === null}
      <div class="key">
        <span class="mono muted">{t('settings.keyHidden')}</span>
        <button class="small" onclick={() => void core.showAccountKey().then((list) => (words = { list, fresh: false }))}>{t('settings.showKey')}</button>
      </div>
    {:else}
      {#if words.fresh}<p class="notice">{t('settings.newKey')}</p>{/if}
      <KeyWords words={words.list} />
      <div><button class="small" onclick={() => (words = null)}>{words.fresh ? t('create.wordsDone') : t('settings.hideKey')}</button></div>
    {/if}
    <div class="muted note">
      {t('settings.keyNote')}
      <Confirm
        look="link"
        label={t('settings.changeKey')}
        warning={t('settings.changeKeyWarning')}
        confirmLabel={t('settings.changeKeyConfirm')}
        run={() =>
          guard(async () => {
            const { accountKeyWords } = await core.changeAccountKey();
            words = { list: accountKeyWords, fresh: true };
            void loadSessions();
          })}
      />
    </div>
  </div>
</section>

<section>
  {@render about(t('settings.sessions'), t('settings.sessionsLead'))}
  <div>
    {#if sessionsError}
      <p class="muted">{t('settings.sessionsOffline')}</p>
      <button class="small" onclick={() => void loadSessions()}>{t('error.retry')}</button>
    {:else if sessions === null}
      <p class="muted" aria-busy="true">{t('app.loading')}</p>
    {:else}
      <ul>
        {#each recent as s (s.id)}
          {@const device = describeDevice(s.userAgent)}
          <li class="device">
            <span class="badge" aria-hidden="true"><Icon name={device?.kind ?? 'browser'} /></span>
            <span class="what">
              <span class="name">
                {device?.name ?? t('settings.unknownDevice')}
                {#if s.current}<span class="here">{t('settings.thisDevice')}</span>{/if}
              </span>
              <span class="muted small-text">{s.current || Date.now() - seen(s) < ACTIVE ? t('settings.activeNow') : t('settings.lastSeen', { when: agoLabel(seen(s)) })}</span>
            </span>
            {#if !s.current}
              <Confirm look="text" label={t('settings.revoke')} warning={t('settings.revokeWarning')} confirmLabel={t('settings.revokeConfirm')} run={() => revoke([s.id])} />
            {/if}
          </li>
        {/each}
        {#if older.length > 0}
          <li class="older">
            <span class="muted small-text">{older.length === 1 ? t('settings.olderOne') : t('settings.older', { n: older.length })}</span>
            <Confirm
              look="outline"
              label={t('settings.revokeOlder', { n: older.length })}
              warning={t('settings.revokeOlderWarning')}
              confirmLabel={t('settings.revokeConfirm')}
              run={() => revoke(older.map((s) => s.id))}
            />
          </li>
        {/if}
      </ul>
    {/if}
  </div>
</section>

<section>
  {@render about(t('settings.reminders'), t('settings.remindersLead'))}
  <div class="stack roomy">
    <div class="setting">
      <span class="what">
        <span class="name">{t('settings.remindersHere')}</span>
        <span class="muted small-text">
          {#if permission === 'granted'}{t('settings.notificationsOn')}{:else if permission === 'denied'}{t('settings.notificationsDenied')}{:else if permission === 'prompt'}{t('settings.notificationsAsk')}{/if}
        </span>
      </span>
      {#if permission === 'prompt'}
        <button class="small" onclick={() => void core.requestNotificationPermission().then((p) => (permission = p))}>{t('settings.allowNotifications')}</button>
      {:else if permission !== null}
        <span class="state" class:on={permission === 'granted'}>{permission === 'granted' ? t('settings.on') : t('settings.off')}</span>
      {/if}
    </div>
    <label class="setting">
      <span class="what">
        <span class="name">{t('settings.reminderTitles')}</span>
        <span class="muted small-text">{t('settings.reminderTitlesHint')}</span>
      </span>
      {#key unsaved}
        <input class="switch" type="checkbox" role="switch" checked={settings.current.reminderTitles} onchange={(e) => save(() => core.setReminderTitles(e.currentTarget.checked))} />
      {/key}
    </label>
  </div>
</section>

<section>
  {@render about(t('settings.clock'), t('settings.clockLead'))}
  <div>
    <div class="choice" role="radiogroup" aria-label={t('settings.clock')}>
      {#each [['24h', t('settings.clock24')], ['12h', t('settings.clock12')]] as const as [value, label] (value)}
        <label>
          {#key unsaved}
            <input type="radio" name="clock" {value} checked={clockChoice === value} onchange={() => save(() => core.setClock(value))} />
          {/key}
          <span>{label}</span>
        </label>
      {/each}
    </div>
  </div>
</section>

<section>
  {@render about(t('settings.help'), t('settings.helpLead'))}
  <div>
    <button class="small" onclick={help}>{t('settings.openHelp')}</button>
  </div>
</section>

<section class="last">
  {@render about(t('settings.advanced'))}
  <div>
    <div class="line">
      <span>{t('settings.serverLabel')}</span>
      <span class="mono muted">{server}</span>
    </div>
    <div class="line end">
      <span>{t('settings.signOutHere')}</span>
      <Confirm
        look="outline"
        label={t('settings.signOut')}
        warning={t('settings.signOutWarning')}
        confirmLabel={t('settings.signOutConfirm')}
        run={async () => {
          await core.signOut();
          signedOut();
        }}
      />
    </div>
    <div class="line end">
      <span>{t('settings.deleteAccountLead')}</span>
      <Confirm
        look="outline"
        label={t('settings.deleteAccount')}
        warning={t('settings.deleteAccountWarning')}
        detail={t('settings.deleteAccountDetail')}
        confirmLabel={t('settings.deleteAccountConfirm')}
        run={async () => {
          await core.deleteAccount();
          signedOut();
        }}
      />
    </div>
  </div>
</section>

<style>
  h1 {
    margin: 8px 0 0;
    font-size: 32px;
    font-weight: 600;
    letter-spacing: -0.03em;
    line-height: 1.15;
  }
  /* What it is | its controls. */
  section {
    display: grid;
    grid-template-columns: 200px minmax(0, 1fr);
    column-gap: 40px;
    padding: 28px 0;
    border-bottom: 1px solid var(--divider);
    font-size: 14px;
  }
  section:first-of-type {
    margin-top: 8px;
  }
  section.last {
    border-bottom: 0;
  }
  .about {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
  }
  p {
    margin: 0;
  }
  .muted {
    color: var(--text-muted);
    font-size: 13px;
    line-height: 1.5;
  }
  .small-text {
    font-size: 12px;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .stack {
    display: flex;
    flex-direction: column;
    gap: 14px;
    min-width: 0;
  }
  .stack.roomy {
    gap: 18px;
  }
  .note {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 6px;
  }
  .notice {
    font-size: 14px;
  }
  .error {
    margin-top: 12px;
    color: var(--danger);
  }

  .small {
    flex: none;
    height: 32px;
    padding: 0 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
  }
  .small:hover {
    background: var(--surface-hover);
  }

  .key {
    position: relative;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 14px;
    border: 1px solid var(--divider);
    border-radius: 10px;
    background: var(--bg-sidebar);
  }
  .key > .mono {
    flex: 1;
    letter-spacing: 0.04em;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .device {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 14px;
    min-height: 56px;
    padding: 8px 0;
    border-bottom: 1px solid var(--divider);
  }
  .badge {
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    border-radius: 8px;
    background: var(--surface);
    color: var(--text-muted);
  }
  .what {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .name {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 500;
  }
  .here {
    display: inline-flex;
    align-items: center;
    height: 20px;
    padding: 0 7px;
    border-radius: 10px;
    background: color-mix(in srgb, var(--ok) 22%, transparent);
    color: var(--ok);
    font-size: 11px;
    font-weight: 500;
  }
  .older {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-height: 48px;
    padding: 8px 0;
  }

  .setting {
    display: flex;
    align-items: flex-start;
    gap: 16px;
  }
  .state {
    flex: none;
    color: var(--text-muted);
    font-size: 13px;
  }
  .state.on {
    color: var(--accent-label);
  }
  /* A checkbox drawn as a switch. */
  .switch {
    flex: none;
    position: relative;
    width: 38px;
    height: 22px;
    border: 0;
    border-radius: 11px;
    background: var(--border);
    transition: background 120ms;
  }
  .switch::after {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 18px;
    height: 18px;
    border-radius: 9px;
    background: var(--text);
    transition: transform 120ms;
  }
  .switch:checked {
    background: var(--accent);
    box-shadow: none;
  }
  .switch:checked::after {
    background: var(--bg);
    transform: translateX(16px);
  }
  @media (prefers-reduced-motion: reduce) {
    .switch,
    .switch::after {
      transition: none;
    }
  }

  .choice {
    display: inline-flex;
    padding: 3px;
    border: 1px solid var(--border);
    border-radius: 999px;
  }
  .choice label {
    position: relative;
  }
  .choice input {
    position: absolute;
    opacity: 0;
  }
  .choice span {
    display: block;
    padding: 5px 14px;
    border-radius: 999px;
    color: var(--text-muted);
    font-size: 13px;
    font-variant-numeric: tabular-nums;
    cursor: pointer;
  }
  .choice input:checked + span {
    background: var(--surface-hover);
    color: var(--text);
  }
  .choice input:focus-visible + span {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }

  .line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-height: 44px;
    border-bottom: 1px solid var(--divider);
  }
  .line.end {
    border-bottom: 0;
  }

  @media (max-width: 700px) {
    section {
      grid-template-columns: 1fr;
      row-gap: 14px;
    }
  }
</style>

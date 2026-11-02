<script lang="ts">
  import { en, localDateString, parseTask } from '@runer/client-core';
  import { readTyped } from '../dates.js';
  import { t, type MessageKey } from '../messages.js';
  import Icon from '../components/Icon.svelte';

  /**
   * What can be typed. The examples are the language's own sheet (`nl/en.ts`), read against
   * today's date by the same parser the capture field uses, so this page can't say something the app doesn't do.
   */
  let { back }: { back: () => void } = $props();

  const now = new Date();
  const at = { today: localDateString(now), now: `${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}`, lang: en };
  const examples = en.examples.flatMap(({ text }) => {
    const p = parseTask(text, at);
    const typed = readTyped(text, now);
    return p === null || typed === null ? [] : [{ before: text.slice(0, p.match[0]), words: text.slice(...p.match), after: text.slice(p.match[1]), title: typed.title, when: typed.label }];
  });
  const rules: MessageKey[] = ['help.ruleKeep', 'help.ruleNumber', 'help.ruleWeekday', 'help.ruleShort', 'help.rulePast', 'help.ruleOrder', 'help.ruleEdit'];
</script>

<p class="kicker"><button class="back" onclick={back}><Icon name="back" />{t('nav.settings')}</button></p>
<h1>{t('nav.help')}</h1>

<h2>{t('help.dates')}</h2>
<p class="lead">{t('help.datesLead')}</p>

<table>
  <thead>
    <tr><th>{t('help.typed')}</th><th>{t('help.title')}</th><th>{t('help.when')}</th></tr>
  </thead>
  <tbody>
    {#each examples as e (e.before + e.words + e.after)}
      <tr>
        <td>{e.before}<mark>{e.words}</mark>{e.after}</td>
        <td>{e.title}</td>
        <td class="when">{e.when}</td>
      </tr>
    {/each}
  </tbody>
</table>
<p class="note">{t('help.examplesNote')}</p>

<h3>{t('help.rules')}</h3>
<ul>
  {#each rules as rule (rule)}<li>{t(rule)}</li>{/each}
</ul>

<style>
  .kicker {
    margin: 0 0 12px;
  }
  .back {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 32px;
    margin-left: -8px;
    padding: 0 10px 0 4px;
    border: 0;
    border-radius: 8px;
    background: none;
    color: var(--text-muted);
    font-size: 13px;
    cursor: pointer;
  }
  .back:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .back :global(svg) {
    width: 16px;
    height: 16px;
  }
  h1 {
    margin: 0 0 28px;
    font-size: 32px;
    font-weight: 600;
    letter-spacing: -0.03em;
    line-height: 1.15;
  }
  h2 {
    margin: 0 0 6px;
    font-size: 18px;
    font-weight: 600;
  }
  h3 {
    margin: 28px 0 8px;
    font-size: 15px;
    font-weight: 600;
  }
  .lead,
  .note {
    margin: 0 0 20px;
    color: var(--text-muted);
    font-size: 14px;
    line-height: 1.5;
  }
  .note {
    margin: 10px 0 0;
    font-size: 12px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 14px;
  }
  th {
    padding: 8px 12px 8px 0;
    border-bottom: 1px solid var(--border);
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 500;
    text-align: left;
  }
  td {
    padding: 9px 12px 9px 0;
    border-bottom: 1px solid var(--divider);
    vertical-align: top;
  }
  mark {
    padding: 2px 5px;
    border-radius: 5px;
    background: var(--accent-soft);
    color: var(--accent-soft-text);
  }
  .when {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: 13px;
    white-space: nowrap;
  }
  ul {
    margin: 0;
    padding-left: 18px;
    color: var(--text-soft);
    font-size: 14px;
    line-height: 1.6;
  }

  /* Phones: what you type and what it becomes; the task's title is the rest of the text. */
  @media (max-width: 700px) {
    th:nth-child(2),
    td:nth-child(2) {
      display: none;
    }
  }
</style>

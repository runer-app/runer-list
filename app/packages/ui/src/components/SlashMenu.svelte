<script lang="ts" module>
  import type { LineType } from '../markdown.js';
  import type { MessageKey } from '../messages.js';

  /** What `/` offers: a task, or a line of text of some type (a note, stored as Markdown). */
  export interface SlashChoice {
    id: string;
    label: MessageKey;
    glyph: string;
    /** What to type at the start of a line instead. */
    hint: string;
    /** `null` for a task. */
    line: LineType | null;
    group: 'add' | 'format';
  }

  export const CHOICES: SlashChoice[] = [
    { id: 'task', label: 'slash.task', glyph: '✓', hint: '[ ]', line: null, group: 'add' },
    { id: 'text', label: 'slash.text', glyph: '¶', hint: 'text', line: 'p', group: 'add' },
    { id: 'h1', label: 'slash.h1', glyph: 'H', hint: '#', line: 'h1', group: 'add' },
    { id: 'h2', label: 'slash.h2', glyph: 'H2', hint: '##', line: 'h2', group: 'format' },
    { id: 'h3', label: 'slash.h3', glyph: 'H3', hint: '###', line: 'h3', group: 'format' },
    { id: 'bullets', label: 'slash.bullets', glyph: '•', hint: '-', line: 'ul', group: 'format' },
    { id: 'numbers', label: 'slash.numbers', glyph: '1.', hint: '1.', line: 'ol', group: 'format' },
    { id: 'quote', label: 'slash.quote', glyph: '”', hint: '>', line: 'quote', group: 'format' },
    { id: 'divider', label: 'slash.divider', glyph: '—', hint: '---', line: 'hr', group: 'format' },
  ];
</script>

<script lang="ts">
  import { t } from '../messages.js';

  /** The `/` menu, for the keyboard and the pointer. The text field keeps focus throughout. */
  let { choices, active, choose }: { choices: SlashChoice[]; active: number; choose: (c: SlashChoice) => void } = $props();
</script>

<div class="menu" role="listbox" aria-label={t('slash.title')}>
  {#each choices as c, i (c.id)}
    {#if i === 0 || choices[i - 1]!.group !== c.group}
      {#if i > 0}<div class="sep" role="presentation"></div>{/if}
      <div class="group" role="presentation">{t(c.group === 'add' ? 'slash.add' : 'slash.format')}</div>
    {/if}
    <div
      class="choice"
      class:active={i === active}
      role="option"
      tabindex="-1"
      aria-selected={i === active}
      onmousedown={(e) => e.preventDefault()}
      onclick={() => choose(c)}
      onkeydown={() => undefined}
    >
      <span class="glyph" aria-hidden="true">{c.glyph}</span><span class="label">{t(c.label)}</span><span class="hint" aria-hidden="true">{c.hint}</span>
    </div>
  {:else}
    <div class="none">{t('slash.none')}</div>
  {/each}
  <div class="keys" aria-hidden="true">
    <span><kbd>↑↓</kbd> {t('slash.move')}</span>
    <span><kbd>↵</kbd> {t('slash.insert')}</span>
    <span><kbd>esc</kbd> {t('slash.close')}</span>
  </div>
</div>

<style>
  .menu {
    position: absolute;
    z-index: 5;
    top: calc(100% + 4px);
    left: 30px;
    width: 296px;
    max-height: 420px;
    overflow-y: auto;
    padding: 6px 6px 0;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--bg-popover);
    box-shadow:
      0 16px 40px rgb(8 6 12 / 0.55),
      0 2px 6px rgb(8 6 12 / 0.4);
    font-size: 14px;
  }
  .group {
    padding: 6px 10px 4px;
    color: var(--text-faint);
    font-size: 11px;
    font-weight: 500;
  }
  .sep {
    height: 1px;
    margin: 6px 4px;
    background: var(--divider);
  }
  .choice {
    display: flex;
    align-items: center;
    gap: 12px;
    height: 36px;
    padding: 0 10px;
    border-radius: 8px;
    cursor: pointer;
  }
  @media (pointer: coarse) {
    .choice {
      height: 44px;
    }
  }
  .choice.active,
  .choice:hover {
    background: var(--accent-soft);
  }
  .glyph {
    width: 22px;
    text-align: center;
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 600;
  }
  .active .glyph {
    color: var(--accent-soft-text);
  }
  .label {
    flex: 1;
  }
  .hint {
    color: var(--text-faint);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .none {
    padding: 10px;
    color: var(--text-muted);
  }
  .keys {
    position: sticky;
    bottom: 0;
    display: flex;
    gap: 14px;
    margin: 6px -6px 0;
    padding: 9px 16px;
    border-top: 1px solid var(--divider);
    background: var(--bg-popover);
    color: var(--text-faint);
    font-size: 11px;
  }
  kbd {
    color: var(--text-muted);
    font-family: var(--font-mono);
  }
</style>

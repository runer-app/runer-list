<script lang="ts">
  import { t } from '../messages.js';
  import Button from '../components/Button.svelte';
  import Centered from '../components/Centered.svelte';
  import Icon from '../components/Icon.svelte';
  import KeyWords from '../components/KeyWords.svelte';

  /** Sign-up's last step (client-spec §7.1): show the 12 words once, then have the user type back three of them. */
  let { words, done }: { words: string[]; done: () => void } = $props();

  let step = $state<'show' | 'confirm'>('show');
  let asked = $state<number[]>([]);
  let answers = $state<string[]>(['', '', '']);
  /** A field marks a wrong word once the user has left it. */
  let left = $state<boolean[]>([false, false, false]);

  const right = (k: number) => answers[k]!.trim().toLowerCase() === words[asked[k]!];
  const allRight = $derived(asked.length > 0 && asked.every((_, k) => right(k)));

  function pickThree(n: number): number[] {
    const all = Array.from({ length: n }, (_, i) => i);
    const out: number[] = [];
    while (out.length < 3 && all.length > 0) {
      const r = crypto.getRandomValues(new Uint32Array(1))[0]! % all.length;
      out.push(all.splice(r, 1)[0]!);
    }
    return out.sort((a, b) => a - b);
  }

  function confirm() {
    asked = pickThree(words.length);
    answers = asked.map(() => '');
    left = asked.map(() => false);
    step = 'confirm';
  }

  function check(e: SubmitEvent) {
    e.preventDefault();
    if (allRight) done();
  }
</script>

{#if step === 'show'}
  <Centered step={1}>
    <h1>{t('create.wordsTitle')}</h1>
    <p>{t('create.wordsLead')}</p>
    <div class="words"><KeyWords {words} /></div>
    <div class="actions">
      <Button large onclick={confirm}>{t('create.wordsDone')}</Button>
      <span class="hint">{t('create.wordsNext')}</span>
    </div>
  </Centered>
{:else}
  <Centered step={2}>
    <h1>{t('create.confirmTitle')}</h1>
    <p>{t('create.confirmLead', { a: asked[0]! + 1, b: asked[1]! + 1, c: asked[2]! + 1 })}</p>
    <form onsubmit={check}>
      <div class="asked">
        {#each asked as i, k (i)}
          {@const ok = right(k)}
          {@const wrong = !ok && left[k] && answers[k]!.trim() !== ''}
          <label>
            <span class="label">{t('create.confirmWord', { n: i + 1 })}</span>
            <span class="field" class:ok class:wrong>
              <input
                bind:value={answers[k]}
                onblur={() => (left[k] = true)}
                oninput={() => (left[k] = false)}
                aria-invalid={wrong}
                autocomplete="off"
                autocapitalize="none"
                spellcheck="false"
              />
              {#if ok}<span class="tick" role="img" aria-label={t('create.confirmRight')}><Icon name="check" /></span>{/if}
            </span>
            {#if wrong}<span class="problem">{t('create.confirmWrong')}</span>{/if}
          </label>
        {/each}
      </div>
      <div class="actions">
        <Button large type="submit" disabled={!allRight}>{t('create.confirmDone')}</Button>
        <button type="button" class="link" onclick={() => (step = 'show')}>{t('create.confirmBack')}</button>
      </div>
    </form>
  </Centered>
{/if}

<style>
  .words {
    margin-top: 28px;
  }
  .asked {
    margin-top: 28px;
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .label {
    color: var(--text-muted);
    font-size: 12px;
  }
  .field {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .field:focus-within {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 18%, transparent);
  }
  .field.ok {
    border-color: color-mix(in srgb, var(--ok) 55%, var(--border));
    box-shadow: none;
  }
  .field.wrong {
    border-color: var(--danger);
  }
  input {
    flex: 1;
    min-width: 0;
    padding: 0;
    border: 0;
    background: transparent;
    font-family: var(--font-mono);
    font-size: 15px;
    outline: none;
  }
  .tick {
    display: flex;
    color: var(--ok);
  }
  .tick :global(svg) {
    width: 16px;
    height: 16px;
    stroke-width: 2.4;
  }
  label .problem {
    margin: 0;
    font-size: 12px;
  }
  @media (max-width: 480px) {
    .asked {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>

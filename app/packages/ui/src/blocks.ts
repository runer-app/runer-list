import type { Line } from './markdown.js';

/**
 * The page as the editor shows it: a run of blocks, each a list item (a
 * task, or a type from a newer version) or one line of a note. Rearranging
 * works on blocks; `group` turns them back into items, since consecutive
 * lines are one note and only other items separate notes.
 */
export type Block = { kind: 'item'; id: string } | { kind: 'line'; note: string; line: Line };

export type Planned = { kind: 'item'; id: string } | { kind: 'note'; id: string | null; lines: Line[] };

/**
 * Items in order. Each run of lines becomes a note that keeps the id of a
 * note it holds lines from (the first one not already used), so an edit
 * stays on the same note where it can; `null` for a note to create.
 */
export function group(blocks: readonly Block[]): Planned[] {
  const used = new Set<string>();
  const out: Planned[] = [];
  let run: { notes: string[]; lines: Line[] } | null = null;
  const flush = () => {
    if (run === null) return;
    const id = run.notes.find((n) => !used.has(n)) ?? null;
    if (id !== null) used.add(id);
    out.push({ kind: 'note', id, lines: run.lines });
    run = null;
  };
  for (const b of blocks) {
    if (b.kind === 'item') {
      flush();
      out.push(b);
    } else {
      run ??= { notes: [], lines: [] };
      if (!run.notes.includes(b.note)) run.notes.push(b.note);
      run.lines.push(b.line);
    }
  }
  flush();
  return out;
}

/** Moves block `from` into gap `gap` (0: before everything, `length`: after everything). */
export function moveBlock<T>(blocks: readonly T[], from: number, gap: number): T[] {
  const out = blocks.slice();
  const [b] = out.splice(from, 1);
  out.splice(gap > from ? gap - 1 : gap, 0, b!);
  return out;
}

/** Without blank plain lines at the end. */
export function trimBlankEnd(lines: readonly Line[]): Line[] {
  const out = lines.slice();
  while (out.length > 0 && out.at(-1)!.type === 'p' && out.at(-1)!.text.trim() === '') out.pop();
  return out;
}

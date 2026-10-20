<script lang="ts">
  import type { Item, ListHandle, ListSnapshot, PublicIdentity } from '@runer/client-core';
  import { tick } from 'svelte';
  import { dueLabel, readTyped, reminderLabel, today } from '../dates.js';
  import { group, moveBlock, trimBlankEnd, type Block } from '../blocks.js';
  import { startDrag, stepGap } from '../drag.js';
  import { hasMarks, lineShortcut, parseInline, parseLines, serializeLines, type Line, type LineType, type Span } from '../markdown.js';
  import { t, type MessageKey } from '../messages.js';
  import { personName, type Names } from '../people.js';
  import Grip from './Grip.svelte';
  import Icon from './Icon.svelte';
  import SlashMenu, { CHOICES, type SlashChoice } from './SlashMenu.svelte';

  /**
   * A list edited in place, like a page.
   * There are tasks and lines of text. Every line is typed into where it
   * stands: there is no edit mode and nothing to open. Enter starts the next
   * line, `/` changes what a line is, and "# ", "- ", "1. ", "> ", "---" at
   * the start of a line make it a heading, list, quote or divider, which
   * keeps its look while you type.
   *
   * Storage: a task is a task item. The lines between two tasks are one
   * note item, whose text is their Markdown (markdown.ts). Only a task
   * separates notes. One new task row waits at the end (or after the task
   * where Enter was pressed); Enter on it while empty starts a line of text,
   * and Enter on an empty last line of text goes back to a task.
   *
   * Edits are saved when a line is left or split, not on every key, so one
   * edit is one update. Nothing here waits on the network.
   */
  let {
    list,
    items,
    members,
    names = {},
    me,
    open,
    selected,
    showDone = false,
  }: {
    list: ListHandle;
    items: Item[];
    members: readonly PublicIdentity[];
    names?: Names;
    me: string;
    /** A task's detail: due, reminder, assignee, move, delete. Opened by clicking its title, or ⌘/Ctrl+Enter. */
    open: (id: string) => void;
    /** The task open in the side panel. */
    selected?: string | undefined;
    /** Done tasks are shown in place; otherwise they're hidden (the open one and the focused one stay). */
    showDone?: boolean;
  } = $props();

  const DRAFT = 'draft';
  const lineKey = (note: string, i: number) => `${note}#${i}`;

  /** The new task row: where it goes (`null`: after everything) and what's typed. Local until committed. */
  let draftAt = $state<number | null>(null);
  let draftText = $state('');
  /** The row with focus, and its text as typed (saved on leave). */
  let focused = $state<string | null>(null);
  let typed = $state('');
  let menuActive = $state(0);
  /** The line whose type badge opened the menu. */
  let menuFor = $state<string | null>(null);

  const fields = new Map<string, HTMLElement>();
  const now = today();
  const shared = $derived(members.length > 1);

  type Row =
    | { kind: 'draft'; key: string }
    | { kind: 'task'; key: string; item: Item }
    | { kind: 'line'; key: string; item: Item; line: number; type: LineType; text: string; num: number }
    | { kind: 'other'; key: string; item: Item };

  const draftIndex = $derived(draftAt === null ? items.length : Math.min(draftAt, items.length));
  const allRows = $derived.by(() => {
    const out: Row[] = [];
    items.forEach((item, index) => {
      if (index === draftIndex) out.push({ kind: 'draft', key: DRAFT });
      if (item.type === 'task') out.push({ kind: 'task', key: item.id, item });
      else if (item.type === 'note') {
        let num = 0;
        parseLines(item.title).forEach((l, i) => {
          num = l.type === 'ol' ? num + 1 : 0;
          out.push({ kind: 'line', key: lineKey(item.id, i), item, line: i, type: l.type, text: l.text, num });
        });
      } else out.push({ kind: 'other', key: item.id, item });
    });
    if (draftIndex >= items.length) out.push({ kind: 'draft', key: DRAFT });
    return out;
  });
  /** The rows on screen. Rearranging works on all of them (`blockRows`), so a folded task keeps its place. */
  const rows = $derived(showDone ? allRows : allRows.filter((r) => !(r.kind === 'task' && r.item.done && r.key !== focused && r.key !== selected)));

  const current = $derived(focused === DRAFT ? draftText : typed);
  const typedMenu = $derived(focused !== null && rows.some((r) => r.key === focused && (r.kind === 'draft' || r.kind === 'line')) && current.startsWith('/') && !current.includes(' '));
  const menuOpen = $derived(menuFor !== null || typedMenu);
  const menuChoices = $derived.by(() => {
    const q = menuFor !== null ? '' : current.slice(1).toLowerCase();
    return CHOICES.filter((c) => q === '' || t(c.label).toLowerCase().includes(q) || c.id.startsWith(q));
  });

  // --- reading the list as it is now (handlers run before the next render) ----

  function snapshot(): Item[] {
    let s: ListSnapshot | undefined;
    list.content.subscribe((v) => (s = v))();
    return s?.items ?? [];
  }
  const indexOf = (id: string) => snapshot().findIndex((i) => i.id === id);
  const itemAt = (index: number): Item | undefined => snapshot()[index];

  /** A note's lines, with what's being typed into its focused line. */
  function linesOf(item: Item): Line[] {
    const fresh = snapshot().find((i) => i.id === item.id) ?? item;
    const lines = parseLines(fresh.title);
    lines.forEach((l, i) => {
      if (focused === lineKey(item.id, i)) l.text = typed;
    });
    return lines;
  }

  const blank = (lines: Line[]) => lines.every((l) => l.type !== 'hr' && l.text.trim() === '');

  /** Writes a note's lines. A note with nothing left in it goes, unless it's being typed into. */
  function writeNote(id: string, lines: Line[], keepEmpty = true) {
    if (lines.length === 0 || (!keepEmpty && blank(lines))) {
      list.remove(id);
      return;
    }
    const md = serializeLines(lines);
    if (snapshot().find((i) => i.id === id)?.title !== md) list.setTitle(id, md);
  }

  // --- focus ----------------------------------------------------------------

  function grow(el: HTMLTextAreaElement) {
    el.style.height = 'auto';
    if (el.scrollHeight > 0) el.style.height = `${el.scrollHeight}px`;
  }

  /** Registers a row's field by key, and keeps a text field as tall as its text. */
  function field(el: HTMLElement, arg: { key: string; value: string }) {
    let key = arg.key;
    fields.set(key, el);
    const area = el instanceof HTMLTextAreaElement ? el : null;
    const onInput = () => area && grow(area);
    el.addEventListener('input', onInput);
    queueMicrotask(onInput);
    return {
      update(next: { key: string; value: string }) {
        if (next.key !== key) {
          if (fields.get(key) === el) fields.delete(key);
          key = next.key;
          fields.set(key, el);
        }
        queueMicrotask(onInput);
      },
      destroy() {
        el.removeEventListener('input', onInput);
        if (fields.get(key) === el) fields.delete(key);
      },
    };
  }

  /** Moves focus to a row (after the next render), with the caret at the start, the end, or an offset. */
  async function focusRow(key: string | undefined, at: 'start' | 'end' | number = 'end') {
    if (key === undefined) return;
    // A line showing its formatting becomes a text field once it has focus: save the row being left, then switch.
    const target = rows.find((r) => r.key === key);
    if (target?.kind === 'line' && focused !== key && hasMarks(target.text)) {
      release();
      enter(key, target.text);
    }
    await tick();
    const el = fields.get(key);
    if (el === undefined) return;
    el.focus();
    if (el instanceof HTMLTextAreaElement) {
      const pos = at === 'start' ? 0 : at === 'end' ? el.value.length : Math.min(at, el.value.length);
      el.setSelectionRange(pos, pos);
    }
  }

  /** Saves the row with focus as if it were left. */
  function release() {
    const row = rows.find((r) => r.key === focused);
    if (row === undefined) return;
    if (row.kind === 'draft') leaveDraft();
    else if (row.kind === 'task') leaveTask(row.item);
    else if (row.kind === 'line') leaveLine(row);
  }

  /** Hands focus to `key` right away, so the row being left doesn't save over what was just written. */
  function moveTo(key: string, text: string, at: 'start' | 'end' | number = 'end') {
    focused = key;
    if (key === DRAFT) draftText = text;
    else typed = text;
    void focusRow(key, at);
  }

  const rowIndex = (key: string) => rows.findIndex((r) => r.key === key);

  /** Up and down between rows when the caret is at the edge of one, like lines of a page. */
  function arrows(e: KeyboardEvent, key: string): boolean {
    if (e.altKey || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown')) return false;
    const el = e.currentTarget as HTMLElement;
    const area = el instanceof HTMLTextAreaElement ? el : null;
    const up = e.key === 'ArrowUp';
    if (area && (up ? area.selectionStart !== 0 || area.selectionEnd !== 0 : area.selectionStart !== area.value.length)) return false;
    const i = rowIndex(key);
    const next = (up ? rows.slice(0, i).reverse() : rows.slice(i + 1)).find((r) => r.kind !== 'other');
    if (next === undefined) return false;
    e.preventDefault();
    void focusRow(next.key, up ? 'end' : 'start');
    return true;
  }

  // --- making lines of text -------------------------------------------------

  /** Starts lines of text at item position `index`: joined to a note next to it, or a new note. Focuses the last. */
  function textAt(index: number, lines: Line[] = [{ type: 'p', text: '' }]) {
    const prev = itemAt(index - 1);
    const next = itemAt(index);
    if (prev?.type === 'note') {
      const all = [...parseLines(prev.title), ...lines];
      writeNote(prev.id, all);
      moveTo(lineKey(prev.id, all.length - 1), lines.at(-1)!.text);
    } else if (next?.type === 'note') {
      writeNote(next.id, [...lines, ...parseLines(next.title)]);
      moveTo(lineKey(next.id, lines.length - 1), lines.at(-1)!.text);
    } else {
      const id = list.addNote(serializeLines(lines), { index });
      moveTo(lineKey(id, lines.length - 1), lines.at(-1)!.text);
    }
    draftAt = null;
    draftText = '';
  }

  /** The lines a choice from `/` or a typed prefix makes; a divider is followed by a line to type in. */
  const linesFor = (type: LineType, text = ''): Line[] => (type === 'hr' ? [{ type: 'hr', text: '' }, { type: 'p', text }] : [{ type, text }]);

  /** Removes a task; the notes on either side become one, since only tasks separate notes. */
  function removeTask(id: string) {
    const index = indexOf(id);
    const prev = itemAt(index - 1);
    const next = itemAt(index + 1);
    list.remove(id);
    if (prev?.type === 'note' && next?.type === 'note') {
      writeNote(prev.id, [...parseLines(prev.title), ...parseLines(next.title)]);
      list.remove(next.id);
    }
  }

  // --- the new task row -----------------------------------------------------

  function typeDraft(e: Event) {
    const v = (e.currentTarget as HTMLTextAreaElement).value;
    const sc = lineShortcut(v);
    if (sc !== null) {
      textAt(draftIndex, linesFor(sc.type, sc.text));
      return;
    }
    draftText = v;
    if (v === '') draftAsText = false;
    menuActive = 0;
  }

  /** A date typed in the new task ("tomorrow 9:30"): shown as a chip, and off the title once the task is added. Escape keeps the words as text. */
  let draftAsText = $state(false);
  const draftWhen = $derived(draftAsText || draftText.startsWith('/') ? null : readTyped(draftText));

  function commitDraft(): boolean {
    const text = (draftWhen?.title ?? draftText).trim();
    if (text === '' || text.startsWith('/')) return false;
    const { due, remindAt } = draftWhen ?? {};
    list.addTask(text, { index: draftIndex, ...(due === undefined ? {} : { due }), ...(remindAt === undefined ? {} : { remindAt }) });
    draftAsText = false;
    return true;
  }

  function keepDraftText(e: Event) {
    e.preventDefault();
    draftAsText = true;
  }

  function keyOnDraft(e: KeyboardEvent) {
    if (e.isComposing || menuKeys(e)) return;
    if (arrows(e, DRAFT)) return;
    if (e.key === 'Escape' && draftWhen !== null) return keepDraftText(e);
    if (e.key === 'Escape') return (e.currentTarget as HTMLElement).blur();
    if (e.key === 'Backspace' && draftText === '') {
      e.preventDefault();
      const prev = rows[rowIndex(DRAFT) - 1];
      draftAt = null;
      if (prev !== undefined) void focusRow(prev.key, 'end');
      return;
    }
    if (e.key !== 'Enter') return;
    if (e.shiftKey) return e.preventDefault(); // a task is one line
    e.preventDefault();
    if (draftText.trim() === '') {
      textAt(draftIndex);
      return;
    }
    const at = draftIndex;
    // Only a date, and no title left: nothing to add, and the text stays.
    if (!commitDraft() && draftWhen !== null) return;
    draftAt = draftAt === null ? null : at + 1;
    moveTo(DRAFT, '');
  }

  function leaveDraft() {
    if (focused === DRAFT) {
      commitDraft();
      focused = null;
    }
    draftAt = null;
    draftText = '';
    draftAsText = false;
  }

  // --- tasks ----------------------------------------------------------------

  function enter(key: string, text: string) {
    focused = key;
    typed = text;
  }

  function saveTask(item: Item) {
    const text = typed.trim();
    if (text !== '' && text !== item.title) list.setTitle(item.id, text);
  }

  function leaveTask(item: Item) {
    if (focused !== item.id) return;
    saveTask(item);
    focused = null;
  }

  function keyOnTask(e: KeyboardEvent, item: Item) {
    if (e.isComposing) return;
    const index = indexOf(item.id);
    if (altMove(e, item.id)) return;
    if (arrows(e, item.id)) return;
    if (e.key === 'Escape') return (e.currentTarget as HTMLElement).blur();
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      return open(item.id);
    }
    if (e.key === 'Backspace' && typed === '') {
      e.preventDefault();
      const prev = rows[rowIndex(item.id) - 1];
      focused = null;
      removeTask(item.id);
      void focusRow(prev?.key ?? DRAFT, 'end');
      return;
    }
    if (e.key !== 'Enter') return;
    if (e.shiftKey) return e.preventDefault(); // a task is one line
    e.preventDefault();
    if (typed.trim() === '') {
      // An emptied task becomes a line of text, as on the new row.
      focused = null;
      removeTask(item.id);
      textAt(index);
      return;
    }
    saveTask(item);
    draftAt = index + 1;
    moveTo(DRAFT, '');
  }

  // --- lines of text --------------------------------------------------------

  function typeLine(e: Event, row: Extract<Row, { kind: 'line' }>) {
    const v = (e.currentTarget as HTMLTextAreaElement).value;
    const sc = row.type === 'p' ? lineShortcut(v) : null;
    if (sc !== null) {
      const lines = linesOf(row.item);
      lines.splice(row.line, 1, ...linesFor(sc.type, sc.text));
      writeNote(row.item.id, lines);
      moveTo(lineKey(row.item.id, row.line + (sc.type === 'hr' ? 1 : 0)), sc.text);
      return;
    }
    typed = v;
    menuActive = 0;
  }

  function leaveLine(row: Extract<Row, { kind: 'line' }>) {
    if (focused !== row.key) return;
    const lines = linesOf(row.item);
    focused = null;
    writeNote(row.item.id, lines, false);
    tidy(row.item.id);
  }

  /** Once focus has left a text altogether, blank lines at its end go (and the text, if nothing is left). */
  function tidy(id: string) {
    setTimeout(() => {
      if (focused?.startsWith(`${id}#`)) return;
      const item = snapshot().find((i) => i.id === id);
      if (item?.type !== 'note') return;
      writeNote(id, trimBlankEnd(parseLines(item.title)), false);
    });
  }

  function keyOnLine(e: KeyboardEvent, row: Extract<Row, { kind: 'line' }>) {
    if (e.isComposing || menuKeys(e)) return;
    if (altMove(e, row.key)) return;
    if (arrows(e, row.key)) return;
    if (e.key === 'Escape') return (e.currentTarget as HTMLElement).blur();
    const el = e.currentTarget as HTMLTextAreaElement;
    const { item, line: i } = row;
    const index = indexOf(item.id);

    if (e.key === 'Backspace' && el.selectionStart === 0 && el.selectionEnd === 0) {
      const lines = linesOf(item);
      if (row.type !== 'p') {
        // First Backspace takes the formatting off; the next joins the line above.
        e.preventDefault();
        lines[i] = { type: 'p', text: typed };
        writeNote(item.id, lines);
        moveTo(row.key, typed, 0);
        return;
      }
      if (i > 0) {
        e.preventDefault();
        const prev = lines[i - 1]!;
        if (prev.type === 'hr') {
          lines.splice(i - 1, 1);
          writeNote(item.id, lines);
          moveTo(lineKey(item.id, i - 1), typed, 0);
          return;
        }
        const join = prev.text.length;
        prev.text += typed;
        lines.splice(i, 1);
        writeNote(item.id, lines);
        moveTo(lineKey(item.id, i - 1), prev.text, join);
        return;
      }
      if (typed === '') {
        e.preventDefault();
        const prev = rows[rowIndex(row.key) - 1];
        lines.splice(0, 1);
        focused = null;
        writeNote(item.id, lines, false);
        void focusRow(prev?.key ?? DRAFT, 'end');
      }
      return;
    }

    if (e.key !== 'Enter' || e.shiftKey) return; // Shift+Enter: a newline in this block
    e.preventDefault();
    const lines = linesOf(item);
    // An empty list item or quote ends it.
    if (typed === '' && (row.type === 'ul' || row.type === 'ol' || row.type === 'quote')) {
      lines[i] = { type: 'p', text: '' };
      writeNote(item.id, lines);
      moveTo(row.key, '');
      return;
    }
    // An empty last line goes back to a task.
    if (typed === '' && row.type === 'p' && i === lines.length - 1 && i > 0) {
      lines.pop();
      focused = null;
      writeNote(item.id, lines, false);
      draftAt = index + 1;
      moveTo(DRAFT, '');
      return;
    }
    const caret = el.selectionStart;
    const before = typed.slice(0, caret);
    const after = typed.slice(caret);
    const nextType: LineType = row.type === 'ul' || row.type === 'ol' ? row.type : 'p';
    lines[i] = { type: row.type, text: before };
    lines.splice(i + 1, 0, { type: nextType, text: after });
    writeNote(item.id, lines);
    moveTo(lineKey(item.id, i + 1), after, 0);
  }

  function keyOnDivider(e: KeyboardEvent, row: Extract<Row, { kind: 'line' }>) {
    if (altMove(e, row.key)) return;
    if (arrows(e, row.key)) return;
    const lines = linesOf(row.item);
    if (e.key === 'Backspace' || e.key === 'Delete') {
      e.preventDefault();
      const prev = rows[rowIndex(row.key) - 1];
      lines.splice(row.line, 1);
      focused = null;
      writeNote(row.item.id, lines, false);
      void focusRow(prev?.key ?? DRAFT, 'end');
    } else if (e.key === 'Enter') {
      e.preventDefault();
      lines.splice(row.line + 1, 0, { type: 'p', text: '' });
      writeNote(row.item.id, lines);
      moveTo(lineKey(row.item.id, row.line + 1), '');
    }
  }

  // --- the / menu -----------------------------------------------------------

  function menuKeys(e: KeyboardEvent): boolean {
    if (!menuOpen) return false;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const n = menuChoices.length;
      if (n > 0) menuActive = (menuActive + (e.key === 'ArrowDown' ? 1 : n - 1)) % n;
      return true;
    }
    if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault();
      const c = menuChoices[menuActive];
      if (c !== undefined) choose(c);
      return true;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      if (menuFor !== null) menuFor = null;
      else if (focused === DRAFT) draftText = '';
      else typed = '';
      return true;
    }
    return false;
  }

  /**
   * A choice from the menu: typed `/` in an empty row (the row becomes that
   * type), or the type badge of a line (the line turns into that type and
   * keeps its text, a task too).
   */
  function choose(c: SlashChoice) {
    menuActive = 0;
    const key = menuFor ?? focused;
    const fromBadge = menuFor !== null;
    menuFor = null;
    if (key === DRAFT) {
      if (c.line === null) moveTo(DRAFT, '');
      else textAt(draftIndex, linesFor(c.line));
      return;
    }
    const row = rows.find((r) => r.key === key);
    if (row?.kind !== 'line') return;
    const lines = linesOf(row.item);
    const id = row.item.id;
    const text = fromBadge ? lines[row.line]!.text : '';
    if (c.line !== null) {
      lines.splice(row.line, 1, ...linesFor(c.line, text));
      writeNote(id, lines);
      moveTo(lineKey(id, row.line + (c.line === 'hr' ? 1 : 0)), text);
      return;
    }
    // A task in the middle of text splits the note around it.
    const index = indexOf(id);
    const before = trimBlankEnd(lines.slice(0, row.line));
    const after = trimBlankEnd(lines.slice(row.line + 1));
    focused = null;
    const kept = !blank(before);
    if (kept) writeNote(id, before);
    else list.remove(id);
    const at = kept ? index + 1 : index;
    if (!blank(after)) list.addNote(serializeLines(after), { index: at });
    if (text.trim() !== '') {
      const task = list.addTask(text.replace(/\n/g, ' ').trim(), { index: at });
      moveTo(task, text);
      return;
    }
    draftAt = at;
    moveTo(DRAFT, '');
  }

  // --- rearranging: drag a block by its handle, or Alt+↑/↓ -------------------

  /** Every row but the new one, as blocks (blocks.ts). */
  const blockRows = $derived(allRows.filter((r) => r.kind !== 'draft'));
  const blockIndex = $derived(new Map(blockRows.map((r, i) => [r.key, i])));

  function blocksNow(): Block[] {
    return blockRows.map((r) => (r.kind === 'line' ? { kind: 'line', note: r.item.id, line: { type: r.type, text: r.text } } : { kind: 'item', id: r.item!.id }));
  }

  /** Makes the list match `blocks`: notes regrouped, kept, made or removed, then everything put in order. One tick, so one update. */
  function arrange(blocks: Block[]) {
    const plan = group(blocks);
    const before = snapshot();
    const kept = new Set(plan.flatMap((p) => (p.id === null ? [] : [p.id])));
    for (const it of before) if (it.type === 'note' && !kept.has(it.id)) list.remove(it.id);
    const ids = plan.map((p) => {
      if (p.kind === 'item') return p.id;
      const md = serializeLines(p.lines);
      if (p.id === null) return list.addNote(md);
      if (before.find((i) => i.id === p.id)?.title !== md) list.setTitle(p.id, md);
      return p.id;
    });
    ids.forEach((id, k) => {
      if (indexOf(id) !== k) list.move(id, k);
    });
  }

  /** Moves block `from` into `gap`, and keeps focus on it when `focus` is set. */
  async function moveTo_(from: number, gap: number, focus: boolean) {
    if (gap === from || gap === from + 1) return;
    release();
    arrange(moveBlock(blocksNow(), from, gap));
    if (!focus) return;
    await tick();
    void focusRow(blockRows[gap > from ? gap - 1 : gap]?.key, 'end');
  }

  function altMove(e: KeyboardEvent, key: string): boolean {
    if (!e.altKey || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown')) return false;
    e.preventDefault();
    const from = blockIndex.get(key);
    if (from === undefined) return true;
    const gap = e.key === 'ArrowUp' ? from - 1 : from + 2;
    if (gap >= 0 && gap <= blockRows.length) void moveTo_(from, gap, true);
    return true;
  }

  /** The row being dragged, and the gap it would drop into. */
  let dragging = $state<string | null>(null);
  let dropGap = $state<number | null>(null);
  const rowEls = new Map<string, HTMLElement>();

  function rowEl(el: HTMLElement, key: string) {
    let k = key;
    rowEls.set(k, el);
    return {
      update(next: string) {
        if (rowEls.get(k) === el) rowEls.delete(k);
        k = next;
        rowEls.set(k, el);
      },
      destroy() {
        if (rowEls.get(k) === el) rowEls.delete(k);
      },
    };
  }

  function beginDrag(e: PointerEvent, key: string) {
    const from = blockIndex.get(key);
    if (from === undefined) return;
    release();
    (document.activeElement as HTMLElement | null)?.blur();
    dragging = key;
    startDrag(e, {
      rows: () => blockRows.map((r) => rowEls.get(r.key)),
      from,
      hover: (gap) => (dropGap = gap),
      drop: (gap) => void moveTo_(from, gap, false),
    });
    const done = () => {
      dragging = null;
      window.removeEventListener('pointerup', done);
      window.removeEventListener('pointercancel', done);
    };
    window.addEventListener('pointerup', done);
    window.addEventListener('pointercancel', done);
  }

  /** The handle also works from the keyboard: ↑/↓ move the block. */
  function keyOnGrip(e: KeyboardEvent, key: string) {
    const from = blockIndex.get(key);
    const gap = from === undefined ? null : stepGap(from, blockRows.length, e.key);
    if (from === undefined || gap === null) return;
    e.preventDefault();
    void moveTo_(from, gap, false).then(async () => {
      await tick();
      (rowEls.get(blockRows[gap > from ? gap - 1 : gap]!.key)?.querySelector('.grip') as HTMLElement | null)?.focus();
    });
  }

  // --- looks ----------------------------------------------------------------

  const draftPlaceholder = $derived(focused === DRAFT ? t('editor.taskFocused') : items.length === 0 ? t('editor.emptyList') : t('editor.newTask'));

  /** The type badge shown for a line in the gutter, as in the menu. */
  const GLYPH = Object.fromEntries(CHOICES.flatMap((c) => (c.line === null ? [] : [[c.line, c.glyph]]))) as Record<LineType, string>;

  const LINE_PLACEHOLDER: Record<LineType, MessageKey> = {
    p: 'editor.line',
    h1: 'slash.h1',
    h2: 'slash.h2',
    h3: 'slash.h3',
    ul: 'editor.listItem',
    ol: 'editor.listItem',
    quote: 'slash.quote',
    hr: 'slash.divider',
  };

  /** What sits at the right of a task: who it's for (shared lists), and its date, or date and time. */
  function meta(item: Item): { who: string | undefined; when: string | undefined; reminder: boolean; late: boolean } {
    return {
      who: shared && item.assignee !== undefined ? personName(item.assignee, me, members, names) : undefined,
      when: item.remindAt !== undefined ? reminderLabel(item.remindAt, now) : item.due !== undefined ? dueLabel(item.due, now) : undefined,
      reminder: item.remindAt !== undefined,
      late: !item.done && item.due !== undefined && item.due < now,
    };
  }
</script>

{#snippet spans(list: Span[])}
  {#each list as s, i (i)}
    {#if s.code}<code>{s.text}</code>{:else if s.bold}<strong>{s.text}</strong>{:else if s.italic}<em>{s.text}</em>{:else if s.strike}<s>{s.text}</s>{:else}{s.text}{/if}
  {/each}
{/snippet}


<div class="editor" class:is-dragging={dragging !== null}>
  {#each rows as row (row.key)}
    {#if row.kind === 'draft'}
      <div class="row task draft">
        <span class="lead plus" aria-hidden="true"><Icon name="plus" /></span>
        <div class="body">
          <textarea
            rows="1"
            use:field={{ key: DRAFT, value: draftText }}
            value={draftText}
            placeholder={draftPlaceholder}
            aria-label={t('editor.newTaskLabel')}
            oninput={typeDraft}
            onkeydown={keyOnDraft}
            onfocus={() => (focused = DRAFT)}
            onblur={leaveDraft}
          ></textarea>
        </div>
        <span class="typed" aria-live="polite">
          {#if draftWhen !== null}
            <!-- mousedown, so the press doesn't take the focus and add the task first -->
            <button type="button" class="when" title={t('typed.hint')} aria-label={t('typed.chip', { when: draftWhen.label })} onmousedown={keepDraftText}>
              <Icon name="calendar" />{draftWhen.label}
            </button>
          {/if}
        </span>
        {#if menuOpen && focused === DRAFT && menuFor === null}<SlashMenu choices={menuChoices} active={menuActive} {choose} />{/if}
      </div>
    {:else if row.kind === 'task'}
      {@const item = row.item}
      {@const sub = meta(item)}
      <div class="row task" class:done={item.done} class:selected={selected === item.id} class:dragged={dragging === row.key} class:drop={dropGap === blockIndex.get(row.key)} use:rowEl={row.key}>
        <span class="gutter"><Grip down={(e) => beginDrag(e, row.key)} key={(e) => keyOnGrip(e, row.key)} /></span>
        <span class="lead">
          <input type="checkbox" checked={item.done} onchange={() => list.toggleDone(item.id)} aria-label={`${t('task.done')}: ${item.title}`} />
        </span>
        <div class="body">
          <textarea
            rows="1"
            use:field={{ key: item.id, value: item.title }}
            value={focused === item.id ? typed : item.title}
            aria-label={t('editor.taskLabel')}
            onfocus={() => enter(item.id, item.title)}
            onclick={() => open(item.id)}
            oninput={(e) => (typed = e.currentTarget.value)}
            onkeydown={(e) => keyOnTask(e, item)}
            onblur={() => leaveTask(item)}
          ></textarea>
        </div>
        {#if sub.who !== undefined}<span class="meta">{sub.who}</span>{/if}
        {#if sub.late}<span class="meta late">{t('task.overdue')}{sub.when === undefined ? '' : ` · ${sub.when}`}</span>
        {:else if sub.when !== undefined}<span class="meta" class:reminder={sub.reminder}>{#if sub.reminder}<Icon name="bell" />{/if}{sub.when}</span>{/if}
      </div>
    {:else if row.kind === 'line'}
      <div
        class="row line {row.type}"
        class:first={row.line === 0}
        class:dragged={dragging === row.key}
        class:drop={dropGap === blockIndex.get(row.key)}
        class:menu-open={menuFor === row.key}
        use:rowEl={row.key}
      >
        <span class="gutter"><Grip down={(e) => beginDrag(e, row.key)} key={(e) => keyOnGrip(e, row.key)} /></span>
        <span class="lead">
          <button
            class="badge"
            aria-label={t('editor.turnInto')}
            aria-haspopup="listbox"
            aria-expanded={menuFor === row.key}
            onclick={() => ((menuActive = 0), (menuFor = menuFor === row.key ? null : row.key))}
            onkeydown={(e) => menuFor === row.key && menuKeys(e)}
            onblur={() => menuFor === row.key && (menuFor = null)}>{GLYPH[row.type]}</button
          >
        </span>
        <div class="body">
          {#if row.type === 'ul'}<span class="marker" aria-hidden="true">•</span>{:else if row.type === 'ol'}<span class="marker num" aria-hidden="true">{row.num}.</span>{/if}
          {#if row.type === 'hr'}
            <button class="divider" aria-label={t('slash.divider')} use:field={{ key: row.key, value: '' }} onkeydown={(e) => keyOnDivider(e, row)} onfocus={() => (focused = row.key)} onblur={() => focused === row.key && (focused = null)}>
              <hr />
            </button>
          {:else if focused !== row.key && hasMarks(row.text)}
            <!-- Formatting shows until the line is typed in; then its Markdown does. -->
            <div class="text rendered" role="textbox" tabindex="0" aria-label={t('editor.lineLabel')} onmousedown={(e) => (e.preventDefault(), void focusRow(row.key, 'end'))} onfocus={() => void focusRow(row.key, 'end')}>
              {@render spans(parseInline(row.text))}
            </div>
          {:else}
            <textarea
              class="text"
              rows="1"
              use:field={{ key: row.key, value: row.text }}
              value={focused === row.key ? typed : row.text}
              placeholder={focused === row.key ? t(LINE_PLACEHOLDER[row.type]) : ''}
              aria-label={t('editor.lineLabel')}
              onfocus={() => enter(row.key, row.text)}
              oninput={(e) => typeLine(e, row)}
              onkeydown={(e) => keyOnLine(e, row)}
              onblur={() => leaveLine(row)}
            ></textarea>
          {/if}
        </div>
        {#if menuOpen && (menuFor === row.key || (menuFor === null && focused === row.key))}<SlashMenu choices={menuChoices} active={menuActive} {choose} />{/if}
      </div>
    {:else}
      <!-- A type from a newer version: shown as text, kept as it is. -->
      <div class="row other" class:dragged={dragging === row.key} class:drop={dropGap === blockIndex.get(row.key)} use:rowEl={row.key}>
        <span class="gutter"><Grip down={(e) => beginDrag(e, row.key)} key={(e) => keyOnGrip(e, row.key)} /></span>
        <span class="lead"></span>
        <div class="body"><span class="plain">{row.item.title}</span></div>
      </div>
    {/if}
  {/each}
  <button class="tail" class:drop={dropGap === blockRows.length} tabindex="-1" aria-hidden="true" onclick={() => void focusRow(DRAFT, 'end')}></button>
</div>

<style>
  .editor {
    display: flex;
    flex-direction: column;
    margin-top: 20px;
  }
  /* The height of a row's first line, so the gutter and the badge sit centred on it whatever the text size. */
  .row {
    --line: 30px;
    position: relative;
    display: flex;
    align-items: flex-start;
    gap: 12px;
  }
  /* 40px rows with a hairline between them. */
  .row.task {
    padding: 5px 0;
    border-bottom: 1px solid var(--divider);
  }
  .row.task.draft {
    border-bottom-color: transparent;
  }
  /* The open task: widened by its own padding, so the text stays put and nothing overlaps the next row. */
  .row.task.selected {
    margin: 0 -10px;
    padding: 5px 10px;
    border-bottom-color: transparent;
    border-radius: 8px;
    background: var(--surface);
    box-shadow: inset 0 0 0 1px var(--border);
  }
  .row.task.selected .gutter {
    left: calc(-28px + 10px);
  }
  .plus {
    color: var(--text-muted);
  }
  /* The date understood in the new task: the look of a set date (DatePicker). */
  .when {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 30px;
    padding: 0 10px;
    border: 0;
    border-radius: 7px;
    background: var(--accent-soft);
    color: var(--accent-soft-text);
    font-size: 13px;
    white-space: nowrap;
    cursor: pointer;
  }
  .when :global(svg) {
    width: 13px;
    height: 13px;
  }
  .plus :global(svg) {
    width: 14px;
    height: 14px;
  }
  .meta {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: var(--line);
    color: var(--text-muted);
    font-size: 12px;
    white-space: nowrap;
  }
  .meta :global(svg) {
    width: 12px;
    height: 12px;
    vertical-align: 0;
  }
  .meta.reminder {
    color: var(--accent-label);
    font-weight: 500;
  }
  .meta.late {
    color: var(--danger);
  }
  .task .body textarea {
    font-size: 15px;
  }
  .lead {
    flex: none;
    display: grid;
    place-items: center;
    width: 18px;
    height: var(--line);
  }
  .body {
    position: relative;
    flex: 1;
    min-width: 0;
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
  }
  textarea,
  .text {
    flex: 1;
    width: 100%;
    min-width: 0;
    min-height: var(--line);
    margin: 0;
    padding: 3px 0;
    border: 0;
    outline: none;
    resize: none;
    overflow: hidden;
    background: transparent;
    font: inherit;
    font-size: 15px;
    line-height: 1.6;
    color: inherit;
    overflow-wrap: anywhere;
    white-space: pre-wrap;
  }
  /* A task's title takes its own line, with the sub-line under it. */
  .task .body textarea {
    flex-basis: 100%;
  }
  .rendered {
    cursor: text;
  }
  textarea::placeholder {
    color: var(--text-faint);
  }
  .done textarea {
    color: var(--text-muted);
    text-decoration: line-through;
  }
  /* Lines of text: a paragraph in a softer colour, with air before and after a run of tasks. */
  .line .text,
  .line textarea {
    color: var(--text-soft);
  }
  .line.first {
    margin-top: 10px;
  }
  .line + .task {
    margin-top: 10px;
  }
  /* A section ("#"): a small label over the tasks under it, as in the mockups. */
  .line.h1 .text,
  .line.h1 textarea {
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 500;
    line-height: 1.5;
  }
  .line.h1 {
    --line: 26px;
    margin-top: 22px;
  }
  .line.h2 .text,
  .line.h2 textarea {
    color: var(--text);
    font-size: 18px;
    font-weight: 600;
    letter-spacing: -0.01em;
    line-height: 1.4;
  }
  .line.h2 {
    --line: 32px;
    margin-top: 16px;
  }
  .line.h3 .text,
  .line.h3 textarea {
    color: var(--text);
    font-weight: 600;
  }
  .line.h3 {
    margin-top: 8px;
  }
  .marker {
    flex: none;
    width: 18px;
    padding: 3px 0;
    font-size: 15px;
    line-height: 1.6;
    color: var(--text-muted);
  }
  .marker.num {
    width: 24px;
    font-variant-numeric: tabular-nums;
  }
  .line.quote .body {
    padding-left: 14px;
    border-left: 2px solid var(--accent);
  }
  .line.quote .text,
  .line.quote textarea {
    color: var(--text-muted);
  }
  .divider {
    flex: 1;
    padding: 0;
    border: 0;
    background: transparent;
    cursor: default;
    display: flex;
    align-items: center;
    min-height: 30px;
    border-radius: 8px;
    outline-offset: 2px;
  }
  .divider hr {
    width: 100%;
    margin: 0;
    border: 0;
    border-top: 1px solid var(--divider);
  }
  code {
    padding: 1px 5px;
    border-radius: 4px;
    background: var(--surface-hover);
    font-family: var(--font-mono);
    font-size: 0.9em;
  }
  .plain {
    display: block;
    padding: 3px 0;
    color: var(--text-muted);
    white-space: pre-wrap;
  }
  /* The gutter: a drag handle, and a line's type badge in place of a checkbox; shown on hover or focus. */
  .gutter {
    position: absolute;
    left: -28px;
    top: 0;
    display: flex;
    align-items: center;
    height: var(--line);
  }
  .row.task .gutter {
    top: 5px;
    height: 30px;
  }
  /* A line's type, in the checkbox's place: a small rounded square, as in the mockups. */
  .badge {
    display: grid;
    place-items: center;
    width: 18px;
    height: 18px;
    padding: 0;
    border: 0;
    border-radius: 5px;
    background: var(--surface);
    color: var(--text-muted);
    font-size: 10px;
    font-weight: 600;
    cursor: pointer;
    opacity: 0;
  }
  .badge:hover,
  .menu-open .badge {
    background: var(--border);
    color: var(--text);
  }
  .gutter {
    opacity: 0;
  }
  .row:hover .gutter,
  .row:focus-within .gutter,
  .row:hover .badge,
  .row:focus-within .badge,
  .menu-open .badge,
  .gutter:focus-within,
  .badge:focus-visible {
    opacity: 1;
  }
  .is-dragging .row:hover .gutter,
  .is-dragging .row:hover .badge {
    opacity: 0;
  }
  .row.dragged {
    opacity: 0.35;
  }
  .row.drop::before,
  .tail.drop::before {
    content: '';
    position: absolute;
    left: 0;
    right: 0;
    top: -2px;
    height: 2px;
    border-radius: 1px;
    background: var(--accent);
  }
  .tail {
    position: relative;
    min-height: 30vh;
    border: 0;
    background: transparent;
    cursor: text;
  }
</style>

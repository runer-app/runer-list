import * as Y from 'yjs';
import type { Readable } from '../readable.js';
import { isDateString, ListError, type Item, type ListSnapshot } from './item.js';

export interface ListDocOptions {
  /** Item id generator; a test seam. Default: `crypto.randomUUID()`. */
  newId?: () => string;
}

export interface AddOptions {
  /** Position among the visible items. Default: the end. */
  index?: number;
}

export interface AddTaskOptions extends AddOptions {
  due?: string;
  remindAt?: number;
  assignee?: string;
  body?: string;
}

/**
 * The list document: typed operations over one Yjs doc.
 *
 * ```text
 * name:   Y.Text
 * order:  Y.Array<item_id>
 * items:  Y.Map<item_id, Y.Map>   // type, title, body?, done, due?, remind_at?, assignee?
 * people: Y.Map<key, string>      // names, by a member's identity_sign_pub (base64url)
 * ```
 *
 * The UI never touches Yjs; this class is the only code that does. `title`
 * and `body` are `Y.Text`, written as a diff, so concurrent edits to one
 * note's text merge; other fields are last-writer-wins. `order` has no native move, so a move is
 * delete + insert and two concurrent moves of one item can leave its id in
 * `order` twice; reads therefore skip repeats (first wins), skip ids with no
 * item, and append items missing from `order`, so replicas always agree.
 */
export class ListDoc {
  readonly doc: Y.Doc;
  private readonly nameText: Y.Text;
  private readonly order: Y.Array<string>;
  private readonly itemMap: Y.Map<Y.Map<unknown>>;
  /** Personal orders of computed views, by view name: item ids. Only the Inbox uses it (it's private). */
  private readonly views: Y.Map<string[]>;
  /** What the people in the list are called, by member id. Any member may set any name. */
  private readonly peopleMap: Y.Map<string>;
  private readonly newId: () => string;

  constructor(doc: Y.Doc = new Y.Doc(), opts: ListDocOptions = {}) {
    this.doc = doc;
    this.nameText = doc.getText('name');
    this.order = doc.getArray<string>('order');
    this.itemMap = doc.getMap<Y.Map<unknown>>('items');
    this.views = doc.getMap<string[]>('views');
    this.peopleMap = doc.getMap<string>('people');
    this.newId = opts.newId ?? (() => crypto.randomUUID());
  }

  // --- reading ---------------------------------------------------------

  get name(): string {
    return this.nameText.toString();
  }

  /** Visible items in order. */
  items(): Item[] {
    return this.visibleIds().map((id) => readItem(id, this.itemMap.get(id)!));
  }

  item(id: string): Item | undefined {
    const m = this.itemMap.get(id);
    return m === undefined ? undefined : readItem(id, m);
  }

  has(id: string): boolean {
    return this.itemMap.has(id);
  }

  /** The order the user dragged a computed view (Today, My tasks) into: item ids, first first. */
  viewOrder(view: string): string[] {
    const v = this.views.get(view);
    return Array.isArray(v) ? v.filter((x) => typeof x === 'string') : [];
  }

  setViewOrder(view: string, ids: readonly string[]): void {
    this.views.set(view, [...ids]);
  }

  /** Names by member id; a member without one isn't in it. Any member can write anything here, so each is cleaned (`cleanName`). */
  people(): Record<string, string> {
    const out: Record<string, string> = {};
    for (const [k, v] of this.peopleMap) {
      const n = typeof v === 'string' ? cleanName(v) : '';
      if (n !== '') out[k] = n;
    }
    return out;
  }

  /** What the list holds for one member: `undefined` when nothing was ever written, empty when a name was cleared. */
  person(key: string): string | undefined {
    const v = this.peopleMap.get(key);
    return typeof v === 'string' ? cleanName(v) : undefined;
  }

  snapshot(listId: string): ListSnapshot {
    return { id: listId, name: this.name, items: this.items(), people: this.people() };
  }

  /** Calls `fn` after every transaction that changed the list, local or remote. */
  subscribe(fn: () => void): () => void {
    const handler = (tr: Y.Transaction) => {
      if (tr.changed.size > 0) fn();
    };
    this.doc.on('afterTransaction', handler);
    return () => this.doc.off('afterTransaction', handler);
  }

  readable(listId: string): Readable<ListSnapshot> {
    return {
      subscribe: (run) => {
        run(this.snapshot(listId));
        return this.subscribe(() => run(this.snapshot(listId)));
      },
    };
  }

  // --- writing ---------------------------------------------------------

  setName(name: string): void {
    this.doc.transact(() => diffInto(this.nameText, name));
  }

  /**
   * Names a member (by id). An empty name clears it, and is kept as empty
   * rather than removed: a device that hasn't heard of the change fills in
   * a missing name, and would write the old one back.
   */
  setPerson(key: string, name: string): void {
    this.peopleMap.set(key, cleanName(name));
  }

  addTask(title: string, opts: AddTaskOptions = {}): string {
    if (opts.due !== undefined) assertDate(opts.due);
    const fields: Record<string, unknown> = { type: 'task', title, done: false };
    if (opts.body !== undefined) fields['body'] = opts.body;
    if (opts.due !== undefined) fields['due'] = opts.due;
    if (opts.remindAt !== undefined) fields['remind_at'] = assertReminder(opts.remindAt);
    if (opts.assignee !== undefined) fields['assignee'] = opts.assignee;
    return this.create(fields, opts.index);
  }

  addNote(text: string, opts: AddOptions = {}): string {
    return this.create({ type: 'note', title: text }, opts.index);
  }

  /** Every mutator below returns false when the item no longer exists (e.g. removed by a sync). */
  setTitle(id: string, title: string): boolean {
    return this.edit(id, (m) => setText(m, 'title', title));
  }

  setBody(id: string, body: string | null): boolean {
    return this.edit(id, (m) => (body === null ? m.delete('body') : setText(m, 'body', body)));
  }

  /** Tasks only. `done` omitted = toggle. */
  toggleDone(id: string, done?: boolean): boolean {
    return this.editTask(id, (m) => m.set('done', done ?? m.get('done') !== true));
  }

  /** Tasks only. `null` clears the date. */
  setDue(id: string, due: string | null): boolean {
    if (due !== null) assertDate(due);
    return this.editTask(id, (m) => (due === null ? m.delete('due') : m.set('due', due)));
  }

  /** Tasks only. `null` clears the reminder. */
  setReminder(id: string, at: number | null): boolean {
    if (at !== null) assertReminder(at);
    return this.editTask(id, (m) => (at === null ? m.delete('remind_at') : m.set('remind_at', at)));
  }

  /** Tasks only. `null` unassigns. */
  assign(id: string, assignee: string | null): boolean {
    return this.editTask(id, (m) => (assignee === null ? m.delete('assignee') : m.set('assignee', assignee)));
  }

  /** Moves the item so it sits at `toIndex` among the visible items. */
  move(id: string, toIndex: number): boolean {
    if (!this.itemMap.has(id)) return false;
    this.doc.transact(() => {
      this.deleteFromOrder(id);
      this.insertAt(id, toIndex);
    });
    return true;
  }

  remove(id: string): boolean {
    if (!this.itemMap.has(id)) return false;
    this.doc.transact(() => {
      this.deleteFromOrder(id);
      this.itemMap.delete(id);
    });
    return true;
  }

  // --- moving between lists (see move.ts) ------------------------------

  /** Every field of an item, including ones this client does not know. */
  exportItem(id: string): Record<string, unknown> | undefined {
    return this.itemMap.get(id)?.toJSON() as Record<string, unknown> | undefined;
  }

  /** Inserts an item under an existing id. No-op (false) if the id is already here. */
  importItem(id: string, fields: Record<string, unknown>, index?: number): boolean {
    if (this.itemMap.has(id)) return false;
    this.doc.transact(() => {
      this.itemMap.set(id, toYMap(fields));
      this.insertAt(id, index);
    });
    return true;
  }

  // --- internals --------------------------------------------------------

  private visibleIds(): string[] {
    const seen = new Set<string>();
    const ids: string[] = [];
    for (const id of this.order.toArray()) {
      if (!seen.has(id) && this.itemMap.has(id)) {
        seen.add(id);
        ids.push(id);
      }
    }
    const orphans = [...this.itemMap.keys()].filter((id) => !seen.has(id)).sort();
    return ids.concat(orphans);
  }

  private create(fields: Record<string, unknown>, index: number | undefined): string {
    const id = this.newId();
    this.doc.transact(() => {
      this.itemMap.set(id, toYMap(fields));
      this.insertAt(id, index);
    });
    return id;
  }

  /** Puts `id` into `order` so it is at visible position `index` (default: the end). */
  private insertAt(id: string, index: number | undefined): void {
    const others = this.visibleIds().filter((v) => v !== id);
    if (index === undefined || index >= others.length) {
      this.order.push([id]);
      return;
    }
    const at = this.order.toArray().indexOf(others[Math.max(0, index)]!);
    if (at < 0) this.order.push([id]);
    else this.order.insert(at, [id]);
  }

  private deleteFromOrder(id: string): void {
    const arr = this.order.toArray();
    for (let i = arr.length - 1; i >= 0; i--) if (arr[i] === id) this.order.delete(i, 1);
  }

  private edit(id: string, fn: (m: Y.Map<unknown>) => void): boolean {
    const m = this.itemMap.get(id);
    if (m === undefined) return false;
    this.doc.transact(() => fn(m));
    return true;
  }

  private editTask(id: string, fn: (m: Y.Map<unknown>) => void): boolean {
    if (this.itemMap.get(id)?.get('type') !== 'task') return false;
    return this.edit(id, fn);
  }
}

/**
 * A person's name as it is kept and shown: one line, without control or
 * text-direction characters, at most 60 characters. Nothing else checks a
 * name: the server never sees it, and another member's app may write
 * anything into a list or an invite.
 */
export function cleanName(name: string): string {
  return name
    .replace(/[\p{Cc}‪-‮⁦-⁩]/gu, ' ')
    .replace(/\s+/g, ' ')
    .trim()
    .slice(0, 60)
    .trimEnd();
}

const TEXT_FIELDS = new Set(['title', 'body']);

function toYMap(fields: Record<string, unknown>): Y.Map<unknown> {
  const m = new Y.Map<unknown>();
  for (const [k, v] of Object.entries(fields)) m.set(k, TEXT_FIELDS.has(k) && typeof v === 'string' ? new Y.Text(v) : v);
  return m;
}

/** A text field as a string; documents written before `Y.Text` hold a plain string. */
function textOf(v: unknown): string | undefined {
  return v instanceof Y.Text ? v.toString() : typeof v === 'string' ? v : undefined;
}

/** Sets a text field to `value`; see `diffInto`. */
function setText(m: Y.Map<unknown>, key: string, value: string): void {
  const t = m.get(key);
  if (t instanceof Y.Text) diffInto(t, value);
  else m.set(key, new Y.Text(value));
}

/** Changes `t` to `value` by replacing only the part between the common prefix and suffix. */
function diffInto(t: Y.Text, value: string): void {
  const old = t.toString();
  let start = 0;
  while (start < old.length && start < value.length && old[start] === value[start]) start++;
  let end = 0;
  while (end < old.length - start && end < value.length - start && old[old.length - 1 - end] === value[value.length - 1 - end]) end++;
  // Never split a surrogate pair.
  if (start > 0 && isHighSurrogate(old.charCodeAt(start - 1))) start--;
  if (end > 0 && isLowSurrogate(old.charCodeAt(old.length - end))) end--;
  if (old.length - start - end > 0) t.delete(start, old.length - start - end);
  if (value.length - start - end > 0) t.insert(start, value.slice(start, value.length - end));
}

const isHighSurrogate = (c: number) => c >= 0xd800 && c <= 0xdbff;
const isLowSurrogate = (c: number) => c >= 0xdc00 && c <= 0xdfff;

function readItem(id: string, m: Y.Map<unknown>): Item {
  const type = m.get('type');
  const title = textOf(m.get('title'));
  const body = textOf(m.get('body'));
  const due = m.get('due');
  const remindAt = m.get('remind_at');
  const assignee = m.get('assignee');
  return {
    id,
    type: typeof type === 'string' ? type : 'note',
    title: typeof title === 'string' ? title : '',
    ...(typeof body === 'string' ? { body } : {}),
    done: m.get('done') === true,
    ...(typeof due === 'string' ? { due } : {}),
    ...(typeof remindAt === 'number' ? { remindAt } : {}),
    ...(typeof assignee === 'string' ? { assignee } : {}),
  };
}

function assertDate(due: string): void {
  if (!isDateString(due)) throw new ListError('invalid-date', `not a YYYY-MM-DD date: ${due}`);
}

function assertReminder(at: number): number {
  if (!Number.isSafeInteger(at) || at < 0) throw new ListError('invalid-reminder', `not a Unix-ms instant: ${at}`);
  return at;
}

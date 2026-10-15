import * as Y from 'yjs';
import { describe, expect, it } from 'vitest';
import { ListDoc, ListError, moveItem } from '../src/list/index.js';

let lists = 0;
/** Each list gets its own id prefix, as random UUIDs would never collide across lists. */
const mk = () => {
  const prefix = `l${++lists}`;
  let n = 0;
  return new ListDoc(new Y.Doc(), { newId: () => `${prefix}-${++n}` });
};
const titles = (l: ListDoc) => l.items().map((i) => i.title);

/** Two replicas of one list, and a way to sync them both ways. */
function replicas() {
  const a = new ListDoc(new Y.Doc(), { newId: () => `a-${crypto.randomUUID()}` });
  const b = new ListDoc(new Y.Doc(), { newId: () => `b-${crypto.randomUUID()}` });
  const sync = () => {
    Y.applyUpdate(b.doc, Y.encodeStateAsUpdate(a.doc));
    Y.applyUpdate(a.doc, Y.encodeStateAsUpdate(b.doc));
    Y.applyUpdate(b.doc, Y.encodeStateAsUpdate(a.doc));
  };
  return { a, b, sync };
}

describe('ListDoc', () => {
  it('adds tasks and notes in order, with an optional position', () => {
    const l = mk();
    l.addNote('# Groceries');
    l.addTask('Milk');
    l.addNote('Ask about oat milk');
    l.addTask('Bread', { index: 1 });
    expect(l.items().map((i) => [i.type, i.title])).toEqual([
      ['note', '# Groceries'], ['task', 'Bread'], ['task', 'Milk'], ['note', 'Ask about oat milk'],
    ]);
  });

  it('keeps a personal view order (Today), last writer wins, as a copy', () => {
    const l = mk();
    expect(l.viewOrder('today')).toEqual([]);
    const ids = ['b', 'a'];
    l.setViewOrder('today', ids);
    ids.push('c');
    expect(l.viewOrder('today')).toEqual(['b', 'a']);
    l.setViewOrder('today', ['a']);
    expect(l.viewOrder('today')).toEqual(['a']);
  });

  it('keeps the name and lets it change', () => {
    const l = mk();
    l.setName('Trip');
    l.setName('Trip to Rome');
    expect(l.name).toBe('Trip to Rome');
  });

  it('edits task fields and clears them again', () => {
    const l = mk();
    const id = l.addTask('Call Anna');
    expect(l.toggleDone(id)).toBe(true);
    expect(l.item(id)?.done).toBe(true);
    l.toggleDone(id, false);
    l.setDue(id, '2026-10-02');
    l.setReminder(id, 1_790_000_000_000);
    l.assign(id, 'anna-key');
    l.setBody(id, 'about the trip');
    l.setTitle(id, 'Call Anna back');
    expect(l.item(id)).toEqual({
      id, type: 'task', title: 'Call Anna back', body: 'about the trip', done: false,
      due: '2026-10-02', remindAt: 1_790_000_000_000, assignee: 'anna-key',
    });
    l.setDue(id, null);
    l.setReminder(id, null);
    l.assign(id, null);
    l.setBody(id, null);
    expect(l.item(id)).toEqual({ id, type: 'task', title: 'Call Anna back', done: false });
  });

  it('rejects bad dates and reminders', () => {
    const l = mk();
    const id = l.addTask('x');
    for (const bad of ['2026-02-30', '2026-13-01', 'tomorrow', '26-10-02', '2026-1-2']) {
      expect(() => l.setDue(id, bad), bad).toThrow(ListError);
    }
    expect(() => l.addTask('y', { due: 'nope' })).toThrow(ListError);
    expect(() => l.setReminder(id, -1)).toThrow(ListError);
    expect(() => l.setReminder(id, 1.5)).toThrow(ListError);
  });

  it('task-only operations do nothing on notes', () => {
    const l = mk();
    const note = l.addNote('n');
    expect(l.toggleDone(note)).toBe(false);
    expect(l.setDue(note, '2026-10-02')).toBe(false);
    expect(l.assign(note, 'x')).toBe(false);
    expect(l.item(note)?.done).toBe(false);
  });

  it('moves items within the list and removes them', () => {
    const l = mk();
    const [a, b, c] = [l.addTask('a'), l.addTask('b'), l.addTask('c')] as [string, string, string];
    l.move(c, 0);
    expect(titles(l)).toEqual(['c', 'a', 'b']);
    l.move(c, 99);
    expect(titles(l)).toEqual(['a', 'b', 'c']);
    l.move(a, 1);
    expect(titles(l)).toEqual(['b', 'a', 'c']);
    l.remove(b);
    expect(titles(l)).toEqual(['a', 'c']);
    expect(l.has(b)).toBe(false);
  });

  it('operations on a missing item report false instead of throwing', () => {
    const l = mk();
    expect(l.setTitle('nope', 'x')).toBe(false);
    expect(l.move('nope', 0)).toBe(false);
    expect(l.remove('nope')).toBe(false);
  });

  it('shows unknown item types as they are and never drops their fields', () => {
    const l = mk();
    l.importItem('x1', { type: 'image', title: 'Sunset', url: 'https://example.test/a.png', size: 3 });
    l.addTask('mine');
    expect(l.item('x1')).toEqual({ id: 'x1', type: 'image', title: 'Sunset', done: false });
    l.setTitle('x1', 'Sunset at sea');
    expect(l.exportItem('x1')).toEqual({ type: 'image', title: 'Sunset at sea', url: 'https://example.test/a.png', size: 3 });
    expect(l.toggleDone('x1')).toBe(false);
  });

  it('notifies on local and remote changes, not on no-ops', () => {
    const { a, b, sync } = replicas();
    let n = 0;
    const off = a.subscribe(() => n++);
    a.addTask('one');
    expect(n).toBe(1);
    b.addTask('two');
    sync();
    expect(n).toBe(2);
    a.remove('missing');
    expect(n).toBe(2);
    off();
    a.addTask('three');
    expect(n).toBe(2);
  });

  it('is a Readable that emits the current value first', () => {
    const l = mk();
    l.setName('Home');
    const seen: string[][] = [];
    const off = l.readable('L1').subscribe((s) => seen.push([s.name, ...s.items.map((i) => i.title)]));
    l.addTask('paint');
    off();
    l.addTask('ignored');
    expect(seen).toEqual([['Home'], ['Home', 'paint']]);
  });

  it('cleans the names members write: one line, no control or direction characters, 60 at most; a cleared name is kept as empty', () => {
    const l = mk();
    l.setPerson('a', '  Anna \n\t B‮  ');
    // Another member's app may write anything straight into the document.
    l.doc.getMap('people').set('b', 'x'.repeat(5000));
    l.doc.getMap('people').set('c', 7);
    l.doc.getMap('people').set('d', ' \u0000\n ');
    expect(l.people()).toEqual({ a: 'Anna B', b: 'x'.repeat(60) });
    l.setPerson('a', ' ');
    expect(l.people()).toEqual({ b: 'x'.repeat(60) });
    expect([l.person('a'), l.person('b'), l.person('nobody')]).toEqual(['', 'x'.repeat(60), undefined]);
  });
});

describe('ListDoc across two devices', () => {
  it('merges concurrent edits to different fields of one task', () => {
    const { a, b, sync } = replicas();
    const id = a.addTask('Plan trip');
    sync();
    a.setDue(id, '2026-10-05');
    b.toggleDone(id, true);
    sync();
    for (const d of [a, b]) expect(d.item(id)).toMatchObject({ due: '2026-10-05', done: true });
  });

  it('merges concurrent edits to the text of one note, and to a task description', () => {
    const { a, b, sync } = replicas();
    const note = a.addNote('# Trip\n- passport\n- tickets');
    const task = a.addTask('Pack', { body: 'small bag' });
    sync();
    a.setTitle(note, '# Trip to Bornholm\n- passport\n- tickets');
    b.setTitle(note, '# Trip\n- passport\n- tickets\n- swimsuit 🩱');
    a.setBody(task, 'small bag, and snacks');
    b.setBody(task, 'one small bag');
    sync();
    for (const d of [a, b]) {
      expect(d.item(note)?.title).toBe('# Trip to Bornholm\n- passport\n- tickets\n- swimsuit 🩱');
      expect(d.item(task)?.body).toBe('one small bag, and snacks');
    }
  });

  it('reads and edits text stored as a plain string by an older client', () => {
    const doc = new Y.Doc();
    const m = new Y.Map<unknown>();
    doc.getMap('items').set('n1', m);
    m.set('type', 'note');
    m.set('title', 'old text');
    const l = new ListDoc(doc);
    expect(l.item('n1')?.title).toBe('old text');
    l.setTitle('n1', 'new text');
    expect(l.item('n1')?.title).toBe('new text');
  });

  it('merges concurrent adds without loss and with the same order on both', () => {
    const { a, b, sync } = replicas();
    a.addTask('a1');
    b.addTask('b1');
    a.addTask('a2');
    b.addTask('b2');
    sync();
    expect(titles(a)).toHaveLength(4);
    expect(titles(a)).toEqual(titles(b));
  });

  it('shows an item once even when two devices move it concurrently', () => {
    const { a, b, sync } = replicas();
    const ids = ['x', 'y', 'z'].map((t) => a.addTask(t));
    sync();
    a.move(ids[0]!, 2);
    b.move(ids[0]!, 1);
    sync();
    expect(a.items().filter((i) => i.title === 'x')).toHaveLength(1);
    expect(a.items()).toEqual(b.items());
    expect(titles(a).sort()).toEqual(['x', 'y', 'z']);
  });

  it('a concurrent move and remove leave the item removed', () => {
    const { a, b, sync } = replicas();
    const id = a.addTask('x');
    a.addTask('y');
    sync();
    a.move(id, 1);
    b.remove(id);
    sync();
    expect(titles(a)).toEqual(['y']);
    expect(titles(b)).toEqual(['y']);
  });

  it('lists an item that is in the map but missing from the order, so nothing is lost', () => {
    const l = mk();
    l.addTask('a');
    const doc = l.doc;
    doc.getMap<Y.Map<unknown>>('items').set('orphan', new Y.Map(Object.entries({ type: 'task', title: 'orphan' })));
    expect(titles(l)).toEqual(['a', 'orphan']);
  });
});

describe('moveItem between lists', () => {
  it('keeps the id and every field, and removes it from the source', () => {
    const inbox = mk();
    const groceries = mk();
    const id = inbox.addTask('Milk', { due: '2026-10-01', assignee: 'me' });
    groceries.addTask('Bread');
    expect(moveItem(inbox, groceries, id, 0)).toBe(true);
    expect(inbox.has(id)).toBe(false);
    expect(groceries.items().map((i) => i.title)).toEqual(['Milk', 'Bread']);
    expect(groceries.item(id)).toMatchObject({ title: 'Milk', due: '2026-10-01', assignee: 'me' });
  });

  it('finishes an interrupted move without creating a second copy', () => {
    const src = mk();
    const dst = mk();
    const id = src.addTask('Milk');
    // crash after the insert, before the delete:
    dst.importItem(id, src.exportItem(id)!);
    expect(src.has(id) && dst.has(id)).toBe(true);
    expect(moveItem(src, dst, id)).toBe(true);
    expect(src.has(id)).toBe(false);
    expect(dst.items()).toHaveLength(1);
  });

  it('is a no-op for an item that is nowhere', () => {
    expect(moveItem(mk(), mk(), 'ghost')).toBe(false);
  });
});

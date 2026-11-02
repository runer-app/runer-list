import { describe, expect, test } from 'vitest';
import { group, moveBlock, trimBlankEnd, type Block } from '../src/blocks.js';

const p = (text: string) => ({ type: 'p' as const, text });
const item = (id: string): Block => ({ kind: 'item', id });
const line = (note: string, text: string): Block => ({ kind: 'line', note, line: p(text) });

describe('group', () => {
  test('consecutive lines are one note, keeping the id of the note their first line came from', () => {
    expect(group([line('n1', 'a'), line('n1', 'b'), item('t1'), line('n2', 'c')])).toEqual([
      { kind: 'note', id: 'n1', lines: [p('a'), p('b')] },
      { kind: 'item', id: 't1' },
      { kind: 'note', id: 'n2', lines: [p('c')] },
    ]);
  });

  test('a note split in two keeps its id for the first part; the second part is new', () => {
    expect(group([line('n1', 'a'), item('t1'), line('n1', 'b')])).toEqual([
      { kind: 'note', id: 'n1', lines: [p('a')] },
      { kind: 'item', id: 't1' },
      { kind: 'note', id: null, lines: [p('b')] },
    ]);
  });

  test('two notes that meet become one, under the first one’s id', () => {
    expect(group([line('n1', 'a'), line('n2', 'b')])).toEqual([{ kind: 'note', id: 'n1', lines: [p('a'), p('b')] }]);
  });

  test('a run whose first note is taken uses another note it holds', () => {
    expect(group([line('n1', 'a'), item('t'), line('n1', 'b'), line('n2', 'c')])).toEqual([
      { kind: 'note', id: 'n1', lines: [p('a')] },
      { kind: 'item', id: 't' },
      { kind: 'note', id: 'n2', lines: [p('b'), p('c')] },
    ]);
  });
});

describe('moveBlock', () => {
  const blocks = [item('a'), item('b'), item('c'), item('d')];
  const ids = (bs: Block[]) => bs.map((b) => (b.kind === 'item' ? b.id : b.line.text));

  test('moves to a gap: before the block now at `gap`', () => {
    expect(ids(moveBlock(blocks, 0, 2))).toEqual(['b', 'a', 'c', 'd']);
    expect(ids(moveBlock(blocks, 0, 4))).toEqual(['b', 'c', 'd', 'a']);
    expect(ids(moveBlock(blocks, 3, 0))).toEqual(['d', 'a', 'b', 'c']);
    expect(ids(moveBlock(blocks, 1, 1))).toEqual(['a', 'b', 'c', 'd']);
    expect(ids(moveBlock(blocks, 1, 2))).toEqual(['a', 'b', 'c', 'd']);
  });
});

describe('trimBlankEnd', () => {
  test('drops blank plain lines at the end only', () => {
    expect(trimBlankEnd([p('a'), p(''), p('b'), p(''), p(' ')])).toEqual([p('a'), p(''), p('b')]);
    expect(trimBlankEnd([{ type: 'hr', text: '' }])).toEqual([{ type: 'hr', text: '' }]);
  });
});

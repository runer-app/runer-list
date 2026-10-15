import { describe, expect, test } from 'vitest';
import { lineShortcut, parseInline, parseLines, serializeLines } from '../src/markdown.js';

describe('note lines', () => {
  test('each line has a type and its text without the Markdown prefix', () => {
    expect(parseLines('# Trip\n## Before\n### Small\nplain\n> quoted\n---\n- a\n* b\n3. c\n\n')).toEqual([
      { type: 'h1', text: 'Trip' },
      { type: 'h2', text: 'Before' },
      { type: 'h3', text: 'Small' },
      { type: 'p', text: 'plain' },
      { type: 'quote', text: 'quoted' },
      { type: 'hr', text: '' },
      { type: 'ul', text: 'a' },
      { type: 'ul', text: 'b' },
      { type: 'ol', text: 'c' },
      { type: 'p', text: '' },
      { type: 'p', text: '' },
    ]);
    expect(parseLines('#hashtag')).toEqual([{ type: 'p', text: '#hashtag' }]);
    expect(parseLines('#### four')).toEqual([{ type: 'p', text: '#### four' }]);
  });

  test('serializing gives Markdown back, numbering each run of a numbered list from 1', () => {
    const md = '# Trip\nplain\n> q\n---\n- a\n- b\n1. x\n2. y\n\np\n1. again';
    expect(serializeLines(parseLines(md))).toBe(md);
    expect(serializeLines([
      { type: 'ol', text: 'a' },
      { type: 'ol', text: 'b' },
    ])).toBe('1. a\n2. b');
  });

  test('a newline inside a block (Shift+Enter) is a Markdown hard break, so blocks and lines stay apart', () => {
    const lines = [
      { type: 'p' as const, text: 'one block\nstill the same block' },
      { type: 'ul' as const, text: 'item\nmore of the item' },
      { type: 'p' as const, text: 'next block' },
    ];
    const md = serializeLines(lines);
    expect(md).toBe('one block\\\nstill the same block\n- item\\\nmore of the item\nnext block');
    expect(parseLines(md)).toEqual(lines);
  });

  test('typing a prefix on a plain line turns it into that type', () => {
    expect(lineShortcut('# ')).toEqual({ type: 'h1', text: '' });
    expect(lineShortcut('## Plan')).toEqual({ type: 'h2', text: 'Plan' });
    expect(lineShortcut('- ')).toEqual({ type: 'ul', text: '' });
    expect(lineShortcut('* x')).toEqual({ type: 'ul', text: 'x' });
    expect(lineShortcut('1. ')).toEqual({ type: 'ol', text: '' });
    expect(lineShortcut('> ')).toEqual({ type: 'quote', text: '' });
    expect(lineShortcut('---')).toEqual({ type: 'hr', text: '' });
    expect(lineShortcut('#nope')).toBeNull();
    expect(lineShortcut('plain')).toBeNull();
  });
});

describe('parseInline', () => {
  test('bold, italic, code and strike', () => {
    expect(parseInline('a **b** *c* _d_ `e` ~~f~~')).toEqual([
      { text: 'a ' },
      { text: 'b', bold: true },
      { text: ' ' },
      { text: 'c', italic: true },
      { text: ' ' },
      { text: 'd', italic: true },
      { text: ' ' },
      { text: 'e', code: true },
      { text: ' ' },
      { text: 'f', strike: true },
    ]);
  });

  test('unclosed markers are text, and code is literal; markup is never HTML', () => {
    expect(parseInline('2 * 3 = **6')).toEqual([{ text: '2 * 3 = **6' }]);
    expect(parseInline('`**x**`')).toEqual([{ text: '**x**', code: true }]);
    expect(parseInline('snake_case_name')).toEqual([{ text: 'snake_case_name' }]);
    expect(parseInline('<img src=x onerror=alert(1)>')).toEqual([{ text: '<img src=x onerror=alert(1)>' }]);
  });
});

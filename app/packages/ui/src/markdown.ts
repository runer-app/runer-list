/**
 * The Markdown a note understands, line by line: `#`–`###` headings, `-`/`*`
 * bullets, `1.` numbers, `>` quotes, `---` dividers, and **bold**, *italic*,
 * `code`, ~~strike~~ inside a line. A note is stored as Markdown and edited
 * as lines: each line keeps its look while you type, and its prefix is not
 * shown. Nothing here is ever HTML, because a note may come from another
 * member and the webview can reach the vault. No links: following one would
 * navigate the app away.
 */

export type LineType = 'p' | 'h1' | 'h2' | 'h3' | 'ul' | 'ol' | 'quote' | 'hr';

export interface Line {
  type: LineType;
  /** Without the Markdown prefix. */
  text: string;
}

export interface Span {
  text: string;
  bold?: true;
  italic?: true;
  code?: true;
  strike?: true;
}

const PREFIXES: [RegExp, LineType][] = [
  [/^### ([\s\S]*)$/, 'h3'],
  [/^## ([\s\S]*)$/, 'h2'],
  [/^# ([\s\S]*)$/, 'h1'],
  [/^[-*] ([\s\S]*)$/, 'ul'],
  [/^\d{1,9}\. ([\s\S]*)$/, 'ol'],
  [/^> ?([\s\S]*)$/, 'quote'],
];
const DIVIDER = /^(-{3,}|\*{3,}|_{3,})$/;

export function parseLine(raw: string): Line {
  if (DIVIDER.test(raw.trim())) return { type: 'hr', text: '' };
  for (const [re, type] of PREFIXES) {
    const m = re.exec(raw);
    if (m) return { type, text: m[1]! };
  }
  return { type: 'p', text: raw };
}

/** One line per block; a line ending in a backslash continues the same block (a newline typed with Shift+Enter). */
export function parseLines(source: string): Line[] {
  const blocks: string[] = [];
  for (const raw of source.split('\n')) {
    const prev = blocks.at(-1);
    if (prev?.endsWith('\\')) blocks[blocks.length - 1] = `${prev.slice(0, -1)}\n${raw}`;
    else blocks.push(raw);
  }
  return blocks.map(parseLine);
}

/** Markdown again; each run of numbered lines counts from 1. */
export function serializeLines(lines: readonly Line[]): string {
  let n = 0;
  return lines
    .map((l) => {
      n = l.type === 'ol' ? n + 1 : 0;
      const text = l.text.replace(/\n/g, '\\\n');
      switch (l.type) {
        case 'h1':
          return `# ${text}`;
        case 'h2':
          return `## ${text}`;
        case 'h3':
          return `### ${text}`;
        case 'ul':
          return `- ${text}`;
        case 'ol':
          return `${n}. ${text}`;
        case 'quote':
          return `> ${text}`;
        case 'hr':
          return '---';
        default:
          return text;
      }
    })
    .join('\n');
}

/** A plain line that now starts with a prefix ("# ", "- ", "1. ", "> ", "---") becomes that type. */
export function lineShortcut(typed: string): Line | null {
  const line = parseLine(typed);
  return line.type === 'p' ? null : line;
}

type Mark = 'bold' | 'italic' | 'code' | 'strike';
const MARKERS: [string, Mark][] = [
  ['**', 'bold'],
  ['~~', 'strike'],
  ['`', 'code'],
  ['*', 'italic'],
  ['_', 'italic'],
];

/** One level of marks: no nesting, and an unclosed marker is text. */
export function parseInline(s: string): Span[] {
  const out: Span[] = [];
  let plain = '';
  let i = 0;
  outer: while (i < s.length) {
    for (const [marker, mark] of MARKERS) {
      if (!s.startsWith(marker, i)) continue;
      const end = s.indexOf(marker, i + marker.length);
      const inner = end < 0 ? '' : s.slice(i + marker.length, end);
      // "2 * 3" and snake_case are not emphasis: marked text can't start or end with a space, and `_` can't sit inside a word.
      const wordy = marker === '_' && (/\w/.test(s[i - 1] ?? '') || /\w/.test(s[end + 1] ?? ''));
      if (end < 0 || inner === '' || (mark !== 'code' && (inner.trim() !== inner || wordy))) continue;
      if (plain !== '') out.push({ text: plain });
      plain = '';
      out.push({ text: inner, [mark]: true });
      i = end + marker.length;
      continue outer;
    }
    plain += s[i];
    i++;
  }
  if (plain !== '') out.push({ text: plain });
  return out;
}

/** Whether a line shows differently from its source (so it must be rendered when not being edited). */
export const hasMarks = (text: string): boolean => parseInline(text).some((s) => s.bold || s.italic || s.code || s.strike);

import { addDays, ClientCore, en, localDateString, type ClientCoreOptions, type Readable } from '@runer/client-core';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, describe, expect, test, vi } from 'vitest';
import { MemoryServer } from '../../client-core/test/platform-node/memory-server.js';
import { createMemoryPlatform, ManualLinkSource, RecordingNotifier } from '../../client-core/test/platform-node/memory.js';
import App from '../src/App.svelte';
import Main from '../src/screens/Main.svelte';
import { startAppearance } from '../src/appearance.svelte.js';
import type { Shell } from '../src/shell.js';

// The screens through the real components and ClientCore, on
// the in-process server and the real vault.

const SERVER = 'https://runer.test';
const OPTS: ClientCoreOptions = { sync: { backoff: { firstMs: 5, maxMs: 20, jitterMs: 1 }, snapshotDelayMs: () => 0 } };
const cores: ClientCore[] = [];
let server: MemoryServer;

afterEach(async () => {
  cleanup();
  for (const c of cores.splice(0)) await c.stop();
});

async function person(notifier = new RecordingNotifier()): Promise<ClientCore> {
  const { core } = await ClientCore.signUp(createMemoryPlatform({ transport: server.transport(), notifications: notifier }), SERVER, 'Someone', OPTS);
  cores.push(core);
  return core;
}

function value<T>(r: Readable<T>): T {
  let v!: T;
  r.subscribe((x) => (v = x))();
  return v;
}

async function signedIn(core?: ClientCore) {
  server ??= new MemoryServer();
  const c = core ?? (await person());
  const signedOut = vi.fn();
  render(Main, { core: c, signedOut });
  return { core: c, signedOut };
}

async function capture(label: string | RegExp, text: string) {
  const input = screen.getByRole('textbox', { name: label });
  await fireEvent.input(input, { target: { value: text } });
  await fireEvent.submit(input.closest('form')!);
}

const nav = () => within(screen.getByRole('navigation'));

/** The new task row: at the end of a list, or after the task where Enter was pressed. */
const newRow = () => screen.getByRole('textbox', { name: 'New task' }) as HTMLTextAreaElement;

/** The field with focus, once the editor has moved it there. */
async function here(): Promise<HTMLTextAreaElement> {
  await waitFor(() => expect(document.activeElement?.tagName).toBe('TEXTAREA'));
  return document.activeElement as HTMLTextAreaElement;
}

/** Types into the field with focus (replacing its text) and presses `key`. */
async function type(text: string, key = 'Enter') {
  const el = await here();
  await fireEvent.input(el, { target: { value: text } });
  if (key !== '') await fireEvent.keyDown(el, { key });
}

async function startTyping() {
  newRow().focus();
  await here();
}

const shape = (core: ClientCore, id: string) => value(core.list(id).content).items.map((i) => [i.type, i.title]);

async function openList(core: ClientCore, name: string): Promise<string> {
  const id = await core.createList(name);
  await signedIn(core);
  await fireEvent.click(await nav().findByRole('button', { name }));
  return id;
}

describe('the list editor', () => {
  test('Enter after a task starts the next task; Enter on an empty one starts text; Enter in text starts the next line of the same text', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await openList(core, 'Weekend trip');
    await startTyping();

    await type('Book a room');
    await type('Find a hike');
    expect((await here()).getAttribute('aria-label')).toBe('New task');
    await type(''); // empty task row: text
    expect((await here()).getAttribute('aria-label')).toBe('Text');
    await type('The ferry leaves at **09:30**.');
    await type('Be there in good time.');
    expect(shape(core, id)).toEqual([
      ['task', 'Book a room'],
      ['task', 'Find a hike'],
      ['note', 'The ferry leaves at **09:30**.\nBe there in good time.\n'],
    ]);

    // Enter on the empty last line goes back to a task, and the empty line goes.
    await type('');
    expect((await here()).getAttribute('aria-label')).toBe('New task');
    await type('Buy dinner');
    expect(shape(core, id)).toEqual([
      ['task', 'Book a room'],
      ['task', 'Find a hike'],
      ['note', 'The ferry leaves at **09:30**.\nBe there in good time.'],
      ['task', 'Buy dinner'],
    ]);

    // Formatting shows when the line isn't being typed in.
    (document.activeElement as HTMLElement).blur();
    expect(screen.getByText('09:30').tagName).toBe('STRONG');
  });

  test('"# ", "- " and "---" at the start of a line make a heading, a list, a divider; the list continues and an empty item ends it', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await openList(core, 'Packing');
    await startTyping();

    await type('# ', ''); // on the task row: becomes a heading line
    expect((await here()).value).toBe('');
    await type('Before we go');
    await type('- ', '');
    await type('passport');
    expect((await here()).value).toBe(''); // the next list item
    await type('tickets');
    await type(''); // an empty item ends the list
    await type('---', '');
    await type('after the line', '');
    (document.activeElement as HTMLElement).blur();
    expect(shape(core, id)).toEqual([['note', '# Before we go\n- passport\n- tickets\n---\nafter the line']]);
    expect(screen.getAllByRole('textbox', { name: 'Text' }).map((t) => (t as HTMLTextAreaElement).value)).toEqual(['Before we go', 'passport', 'tickets', 'after the line']);
    expect(screen.getByRole('button', { name: 'Divider' })).toBeTruthy();
  });

  test('"/" offers task, text, headings, lists, quote and divider; a task in the middle of text splits it', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await openList(core, 'Notes');
    await startTyping();

    await type('/', '');
    const menu = screen.getByRole('listbox', { name: 'Insert' });
    expect(within(menu).getAllByRole('option').map((o) => o.querySelector('.label')!.textContent)).toEqual([
      'Task',
      'Note',
      'Section',
      'Heading',
      'Small heading',
      'Bullet list',
      'Numbered list',
      'Quote',
      'Divider',
    ]);
    await type('/heading', '');
    await fireEvent.keyDown(await here(), { key: 'Enter' });
    await type('Plan');
    await type('first line');
    await type('second line');
    // Now "/task" on the empty third line, which sits after "second line"... use the line between instead:
    const lines = screen.getAllByRole('textbox', { name: 'Text' }) as HTMLTextAreaElement[];
    lines[1]!.focus();
    await fireEvent.keyDown(lines[1]!, { key: 'Enter' }); // a new line after "first line"
    await type('/task', 'Enter');
    expect((await here()).getAttribute('aria-label')).toBe('New task');
    await type('Call the ferry');
    (document.activeElement as HTMLElement).blur();
    await waitFor(() => expect(shape(core, id)[2]).toEqual(['note', 'second line']));
    expect(shape(core, id)).toEqual([
      ['note', '## Plan\nfirst line'],
      ['task', 'Call the ferry'],
      ['note', 'second line'],
    ]);
    expect(screen.queryByRole('listbox')).toBeNull();
  });

  test('deleting the task between two texts joins them; Backspace at the start of a line joins it to the one above', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = core.list((await core.createList('Home')) as string).id;
    core.list(id).addNote('above');
    core.list(id).addTask('between');
    core.list(id).addNote('below');
    await signedIn(core);
    await fireEvent.click(await nav().findByRole('button', { name: 'Home' }));

    const task = (await screen.findByDisplayValue('between')) as HTMLTextAreaElement;
    task.focus();
    await fireEvent.input(task, { target: { value: '' } });
    await fireEvent.keyDown(task, { key: 'Backspace' });
    expect(shape(core, id)).toEqual([['note', 'above\nbelow']]);

    const below = (await screen.findByDisplayValue('below')) as HTMLTextAreaElement;
    below.focus();
    below.setSelectionRange(0, 0);
    await fireEvent.keyDown(below, { key: 'Backspace' });
    (document.activeElement as HTMLElement).blur();
    expect(shape(core, id)).toEqual([['note', 'abovebelow']]);
  });

  test('a task edited in place is saved on leaving it, and its checkbox completes it; clicking its title opens its detail, text has none', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await openList(core, 'Errands');
    core.list(id).addTask('post the leter');
    core.list(id).addNote('just text');
    const field = (await screen.findByDisplayValue('post the leter')) as HTMLTextAreaElement;
    field.focus();
    await fireEvent.input(field, { target: { value: 'post the letter' } });
    expect(shape(core, id)[0]).toEqual(['task', 'post the leter']); // not on every key
    field.blur();
    expect(shape(core, id)[0]).toEqual(['task', 'post the letter']);
    await fireEvent.click(await screen.findByRole('checkbox', { name: 'Done: post the letter' }));
    expect(value(core.list(id).content).items[0]!.done).toBe(true);

    // Done, it's hidden until "Show completed" in the ⋯ menu.
    await waitFor(() => expect(screen.queryByDisplayValue('post the letter')).toBeNull());
    await fireEvent.click(screen.getByRole('button', { name: 'List options' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Show completed (1)' }));
    const done = (await screen.findByDisplayValue('post the letter')) as HTMLTextAreaElement;
    await fireEvent.click(screen.getByRole('button', { name: 'List options' }));
    expect(screen.getByRole('button', { name: 'Hide completed' })).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'List options' }));

    // Clicking a task's title opens its detail; clicking text opens nothing.
    const panel = () => screen.queryByRole('complementary', { name: 'Task' });
    await fireEvent.click(screen.getByDisplayValue('just text'));
    expect(panel()).toBeNull();
    await fireEvent.click(done);
    expect(within(panel()!).getByDisplayValue('post the letter')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    await fireEvent.keyDown(done, { key: 'Enter', metaKey: true });
    expect(panel()).not.toBeNull();
  });

  test('Shift+Enter is a newline inside a block; Enter starts the next block', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await openList(core, 'Notes');
    core.list(id).addNote('first');
    const first = (await screen.findByDisplayValue('first')) as HTMLTextAreaElement;
    first.focus();
    await fireEvent.keyDown(first, { key: 'Enter', shiftKey: true }); // the browser adds the newline itself
    await fireEvent.input(first, { target: { value: 'first\nstill first' } });
    await fireEvent.keyDown(first, { key: 'Enter' });
    await type('second', '');
    (document.activeElement as HTMLElement).blur();
    await waitFor(() => expect(shape(core, id)).toEqual([['note', 'first\\\nstill first\nsecond']]));
    expect(screen.getAllByRole('textbox', { name: 'Text' })).toHaveLength(2);
  });

  test('a line’s type badge turns it into a heading, or into a task with the same text', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await openList(core, 'Notes');
    core.list(id).addNote('Plan\ncall the ferry\nmore text');

    const [plan, call] = await screen.findAllByRole('button', { name: 'Turn into' });
    await fireEvent.click(plan!);
    await fireEvent.click(within(screen.getByRole('listbox', { name: 'Insert' })).getByRole('option', { name: /Section/ }));
    await waitFor(() => expect(shape(core, id)).toEqual([['note', '# Plan\ncall the ferry\nmore text']]));

    await fireEvent.click(call!);
    await fireEvent.click(within(screen.getByRole('listbox', { name: 'Insert' })).getByRole('option', { name: /Task/ }));
    await waitFor(() =>
      expect(shape(core, id)).toEqual([
        ['note', '# Plan'],
        ['task', 'call the ferry'],
        ['note', 'more text'],
      ]),
    );
  });

  test('Alt+↓ and the drag handle move blocks; lines regroup into texts around the tasks', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await openList(core, 'Order');
    core.list(id).addNote('a\nb');
    core.list(id).addTask('task');

    // "b" moves below the task: it becomes its own text.
    const b = (await screen.findByDisplayValue('b')) as HTMLTextAreaElement;
    b.focus();
    await fireEvent.keyDown(b, { key: 'ArrowDown', altKey: true });
    await waitFor(() =>
      expect(shape(core, id)).toEqual([
        ['note', 'a'],
        ['task', 'task'],
        ['note', 'b'],
      ]),
    );
    // The task moves up from its handle, by keyboard: "a" and "b" meet again as one text.
    const handles = screen.getAllByRole('button', { name: 'Move' });
    await fireEvent.keyDown(handles[1]!, { key: 'ArrowUp' });
    await waitFor(() =>
      expect(shape(core, id)).toEqual([
        ['task', 'task'],
        ['note', 'a\nb'],
      ]),
    );
  });

  test('the Inbox is the same page of tasks and text', async () => {
    server = new MemoryServer();
    const { core } = await signedIn();
    await fireEvent.click(nav().getByRole('button', { name: 'Inbox' }));
    await startTyping();
    await type('plant tulips');
    await type('');
    await type('# ', '');
    await type('Ideas', '');
    (document.activeElement as HTMLElement).blur();
    expect(shape(core, core.inbox)).toEqual([
      ['task', 'plant tulips'],
      ['note', '# Ideas'],
    ]);
    // Its ⋯ menu only shows completed tasks: the Inbox can't be renamed or deleted.
    await fireEvent.click(screen.getByRole('button', { name: 'List options' }));
    expect(screen.getByRole('button', { name: 'Show completed (0)' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Rename' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Delete list' })).toBeNull();
  });
});

describe('lists', () => {
  test('a new list from Lists opens it; Lists shows "Only me" and the open tasks', async () => {
    server = new MemoryServer();
    const { core } = await signedIn();
    await fireEvent.click(nav().getByRole('button', { name: 'Lists' }));
    expect(await screen.findByRole('heading', { name: 'Lists' })).toBeTruthy();
    expect(screen.getByText(/No lists yet/)).toBeTruthy();

    await capture('New list', 'Weekend trip');
    const main = within(screen.getByRole('main'));
    expect(await main.findByRole('button', { name: 'Weekend trip' })).toBeTruthy(); // the title, click to rename
    await startTyping();
    await type('Book a room');
    await type('Buy dinner');
    const id = value(core.lists).find((l) => l.name === 'Weekend trip')!.id;
    expect(shape(core, id)).toHaveLength(2);

    await fireEvent.click(nav().getByRole('button', { name: 'Lists' }));
    expect(await screen.findByRole('heading', { name: 'Only me' })).toBeTruthy();
    expect(screen.getByText('2 open tasks · private')).toBeTruthy();
  });

  test('a new list from the sidebar opens it and shows its open tasks', async () => {
    server = new MemoryServer();
    const { core } = await signedIn();
    await fireEvent.click(nav().getByRole('button', { name: 'Add a list' }));
    await capture('New list', 'Weekend trip');
    expect(await within(screen.getByRole('main')).findByRole('button', { name: 'Weekend trip' })).toBeTruthy();
    const id = value(core.lists).find((l) => l.name === 'Weekend trip')!.id;
    core.list(id).addTask('Book a room');
    expect(await nav().findByText('1')).toBeTruthy();
    expect(screen.queryByRole('textbox', { name: 'New list' })).toBeNull();
  });

  test('with no network a new list opens at once and takes a task; the status line says "Offline…", then "Synced · encrypted" again', async () => {
    server = new MemoryServer();
    const { core } = await signedIn();
    const status = () => screen.getByRole('status').textContent;
    await waitFor(() => expect(status()).toBe('Synced · encrypted'));

    server.offline = true;
    server.drop();
    await waitFor(() => expect(status()).toMatch(/^Offline/), { timeout: 3000 });
    await fireEvent.click(nav().getByRole('button', { name: 'Add a list' }));
    await capture('New list', 'Errands');
    expect(await screen.findByRole('heading', { name: 'Errands' })).toBeTruthy();
    expect(screen.queryByRole('alert')).toBeNull();
    await startTyping();
    await type('buy stamps');
    const id = value(core.lists).find((l) => l.name === 'Errands')!.id;
    expect(shape(core, id)).toEqual([['task', 'buy stamps']]);

    server.offline = false;
    await waitFor(() => expect(status()).toBe('Synced · encrypted'), { timeout: 3000 });
    expect(value(core.warnings)).toEqual([]);
  });

  test('search finds a task in any list and opens it in the panel; a list by its name', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await core.createList('Garden');
    core.list(id).addTask('plant tulips', { body: 'by the fence' });
    await signedIn(core);
    await fireEvent.click(nav().getAllByRole('button', { name: 'Search' })[0]!);
    await fireEvent.input(screen.getByRole('searchbox', { name: 'Search' }), { target: { value: 'fence' } });
    await fireEvent.click(await screen.findByRole('button', { name: 'plant tulips' }));
    expect(within(screen.getByRole('complementary', { name: 'Task' })).getByDisplayValue('plant tulips')).toBeTruthy();
    await fireEvent.input(screen.getByRole('searchbox', { name: 'Search' }), { target: { value: 'gard' } });
    await fireEvent.click(await within(screen.getByRole('main')).findByRole('button', { name: /Garden/ }));
    expect(await within(screen.getByRole('main')).findByRole('button', { name: 'Garden' })).toBeTruthy();
  });

  test('a list has a way back to the page it was opened from (phones have no sidebar); the Inbox has none', async () => {
    server = new MemoryServer();
    const core = await person();
    await core.createList('Garden');
    await signedIn(core);
    const main = () => within(screen.getByRole('main'));
    const listsTab = () => nav().getAllByRole('button', { name: 'Lists' })[0]!;

    await fireEvent.click(listsTab());
    await fireEvent.click(await main().findByRole('button', { name: /Garden/ }));
    expect(listsTab().classList.contains('active')).toBe(true); // a list is a page under Lists
    await fireEvent.click(await main().findByRole('button', { name: 'Lists' }));
    expect(await screen.findByRole('heading', { name: 'Lists' })).toBeTruthy();

    await fireEvent.click(nav().getAllByRole('button', { name: 'Search' })[0]!);
    await fireEvent.input(screen.getByRole('searchbox', { name: 'Search' }), { target: { value: 'gard' } });
    await fireEvent.click(await main().findByRole('button', { name: /Garden/ }));
    await fireEvent.click(await main().findByRole('button', { name: 'Search' }));
    expect(screen.getByRole('searchbox', { name: 'Search' })).toBeTruthy();

    await fireEvent.click(nav().getByRole('button', { name: /Inbox/ }));
    expect(await main().findByRole('heading', { name: 'Inbox' })).toBeTruthy();
    expect(main().queryByRole('button', { name: 'Lists' })).toBeNull();
  });

  test('rename a list by clicking its title', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await openList(core, 'Hjemme');
    await fireEvent.click(await within(screen.getByRole('main')).findByRole('button', { name: 'Hjemme' }));
    const input = await screen.findByRole('textbox', { name: 'List name' });
    await fireEvent.input(input, { target: { value: 'Home' } });
    await fireEvent.submit(input.closest('form')!);
    await waitFor(() => expect(value(core.list(id).content).name).toBe('Home'));
    expect(nav().getByRole('button', { name: 'Home' })).toBeTruthy();
  });

  test('the owner deletes a list from the ⋯ menu after confirming', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await openList(core, 'Old plans');
    await fireEvent.click(await screen.findByRole('button', { name: 'List options' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Delete list' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Delete for good' }));
    await waitFor(() => expect(value(core.lists).some((l) => l.id === id)).toBe(false));
    expect(await screen.findByRole('heading', { name: 'Lists' })).toBeTruthy();
  });
});

describe('Today', () => {
  test('tasks can be put in your own order from their handles', async () => {
    server = new MemoryServer();
    const core = await person();
    const today = localDateString(new Date());
    for (const title of ['first', 'second', 'third']) core.list(core.inbox).addTask(title, { due: today });
    await signedIn(core);
    const titles = () => within(screen.getByRole('main')).getAllByRole('checkbox').map((c) => c.getAttribute('aria-label'));
    expect(titles()).toEqual(['Done: first', 'Done: second', 'Done: third']);
    await fireEvent.keyDown(screen.getAllByRole('button', { name: 'Move' })[2]!, { key: 'ArrowUp' });
    await waitFor(() => expect(titles()).toEqual(['Done: first', 'Done: third', 'Done: second']));
    expect(value(core.today).map((r) => r.item.title)).toEqual(['first', 'third', 'second']);
  });

  test('overdue tasks get their own section; N jumps to "Add a task" unless typing in a field', async () => {
    server = new MemoryServer();
    const core = await person();
    const yesterday = new Date();
    yesterday.setDate(yesterday.getDate() - 1);
    core.list(core.inbox).addTask('call the landlord', { due: localDateString(yesterday) });
    core.list(core.inbox).addTask('pay the bill', { due: localDateString(new Date()) });
    await signedIn(core);
    const main = within(screen.getByRole('main'));
    expect(await main.findByRole('heading', { name: /^Overdue/ })).toBeTruthy();
    expect(main.getByRole('heading', { name: /^Today\d/ })).toBeTruthy();

    const capture = main.getByRole('textbox', { name: 'Add a task for today' });
    await fireEvent.keyDown(document.body, { key: 'n' });
    expect(document.activeElement).toBe(capture);
    // Typed into a field, it's just a letter.
    capture.blur();
    const typed = new KeyboardEvent('keydown', { key: 'n', bubbles: true, cancelable: true });
    document.body.appendChild(Object.assign(document.createElement('input'), { id: 'elsewhere' })).dispatchEvent(typed);
    expect(typed.defaultPrevented).toBe(false);
    document.getElementById('elsewhere')!.remove();
  });
});

describe('typed dates', () => {
  const inbox = (core: ClientCore) => value(core.list(core.inbox).content).items;
  const field = () => screen.getByRole('textbox', { name: 'Add a task for today' }) as HTMLInputElement;
  const tomorrow = addDays(localDateString(new Date()), 1);

  test('a date typed in the capture field shows as a chip, is set on the task and leaves its title', async () => {
    server = new MemoryServer();
    const core = await person();
    await signedIn(core);
    await fireEvent.input(field(), { target: { value: 'Call mom tomorrow 9:30' } });
    expect(screen.getByRole('button', { name: /^Date: Tomorrow / })).toBeTruthy();
    await fireEvent.submit(field().closest('form')!);
    const [h, m] = [9, 30];
    const at = new Date(`${tomorrow}T00:00`);
    at.setHours(h, m);
    expect(inbox(core)).toMatchObject([{ title: 'Call mom', due: tomorrow, remindAt: at.getTime() }]);
    expect(field().value).toBe('');
  });

  test('Escape keeps the words as text, for this task only; a date alone adds nothing', async () => {
    server = new MemoryServer();
    const core = await person();
    await signedIn(core);
    await fireEvent.input(field(), { target: { value: 'Report tomorrow' } });
    await fireEvent.keyDown(field(), { key: 'Escape' });
    expect(screen.queryByRole('button', { name: /^Date:/ })).toBeNull();
    await fireEvent.submit(field().closest('form')!);
    expect(inbox(core)).toMatchObject([{ title: 'Report tomorrow', due: localDateString(new Date()) }]);

    await fireEvent.input(field(), { target: { value: 'tomorrow' } });
    expect(screen.getByRole('button', { name: /^Date: Tomorrow/ })).toBeTruthy();
    await fireEvent.submit(field().closest('form')!);
    expect(inbox(core)).toHaveLength(1);
    expect(field().value).toBe('tomorrow');
  });

  test('a new task in a list takes a typed date too; a list’s name doesn’t', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await openList(core, 'Trip tomorrow');
    await startTyping();
    await fireEvent.input(await here(), { target: { value: 'Book a room in 3 days' } });
    expect(screen.getByRole('button', { name: /^Date:/ })).toBeTruthy();
    await fireEvent.keyDown(await here(), { key: 'Enter' });
    await type('Pack tomorrow', 'Escape');
    await fireEvent.keyDown(await here(), { key: 'Enter' });
    const items = value(core.list(id).content).items;
    expect(items).toMatchObject([{ title: 'Book a room', due: addDays(localDateString(new Date()), 3) }, { title: 'Pack tomorrow' }]);
    expect(items[1]!.due).toBeUndefined();

    await fireEvent.click(nav().getByRole('button', { name: 'Lists' }));
    await capture('New list', 'Weekend plans tomorrow');
    await waitFor(() => expect(value(core.lists).map((l) => l.name)).toContain('Weekend plans tomorrow'));
  });

  test('Help shows every example of the language, read against today', async () => {
    await signedIn();
    await fireEvent.click(nav().getByRole('button', { name: 'Help' }));
    const main = within(screen.getByRole('main'));
    expect(main.getByRole('heading', { name: 'Typing dates' })).toBeTruthy();
    expect(main.getAllByRole('row')).toHaveLength(en.examples.length + 1);
    expect(main.getByRole('row', { name: /^Call mom tomorrow Call mom Tomorrow$/ })).toBeTruthy();
    // On a phone it's opened from Settings, and goes back there.
    await fireEvent.click(main.getByRole('button', { name: 'Settings' }));
    await fireEvent.click(within(screen.getByRole('main')).getByRole('button', { name: 'Open Help' }));
    expect(screen.getByRole('heading', { name: 'Help' })).toBeTruthy();
  });
});

describe('item detail', () => {
  test('an Inbox item moves to a list and keeps its id', async () => {
    server = new MemoryServer();
    const core = await person();
    const id = await core.createList('Garden');
    const item = core.list(core.inbox).addTask('plant tulips');
    await signedIn(core);
    await fireEvent.click(nav().getByRole('button', { name: 'Inbox' }));
    await fireEvent.click(await screen.findByDisplayValue('plant tulips'));
    await fireEvent.change(screen.getByLabelText('List'), { target: { value: id } });
    expect(value(core.list(id).content).items.map((i) => i.id)).toEqual([item]);
    expect(value(core.list(core.inbox).content).items).toEqual([]);
  });

  test('description, a date with a time (its reminder), done, delete; edits are kept when the panel is closed straight away', async () => {
    server = new MemoryServer();
    const notifier = new RecordingNotifier();
    const core = await person(notifier);
    const id = await core.createList('Errands');
    const task = core.list(id).addTask('post the letter');
    await signedIn(core);
    await fireEvent.click(nav().getByRole('button', { name: 'Errands' }));
    await fireEvent.click(await screen.findByDisplayValue('post the letter'));
    const item = () => value(core.list(id).content).items[0]!;

    // Typed, never blurred, then closed: still saved.
    await fireEvent.input(screen.getByRole('textbox', { name: 'Description' }), { target: { value: 'the blue one' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(item().body).toBe('the blue one');
    await fireEvent.click(await screen.findByDisplayValue('post the letter'));

    // One date: a quick pick, then a time, which is the reminder.
    const today = localDateString(new Date());
    await fireEvent.click(screen.getByRole('button', { name: 'Date: none' }));
    await fireEvent.click(within(screen.getByRole('dialog', { name: 'Date' })).getByRole('button', { name: /^Today/ }));
    expect(item().due).toBe(today);
    expect(item().remindAt).toBeUndefined();
    await fireEvent.click(screen.getByRole('button', { name: /^Date: Today/ }));
    await fireEvent.click(screen.getByRole('button', { name: 'Add time' }));
    // Typed, then a press outside closes the picker: the time is kept.
    await fireEvent.input(screen.getByLabelText('Time'), { target: { value: '11.59pm' } });
    await fireEvent.pointerDown(document.body);
    expect(screen.queryByRole('dialog', { name: 'Date' })).toBeNull();
    expect(item().remindAt).toBe(new Date(`${today}T23:59`).getTime());
    expect(item().due).toBe(today);
    await waitFor(() => expect(notifier.scheduled.map((r) => r.title)).toEqual(['post the letter']));

    await fireEvent.click(nav().getByRole('button', { name: 'Today' }));
    expect(await screen.findByRole('button', { name: 'post the letter' })).toBeTruthy();
    expect(within(screen.getByRole('main')).getByText(/Errands/)).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'post the letter' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Remove date' }));
    expect(item().due).toBeUndefined();
    expect(item().remindAt).toBeUndefined();
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Done' }));
    expect(item().done).toBe(true);
    await fireEvent.click(screen.getByRole('button', { name: 'Delete task' }));
    expect(value(core.list(id).content).items.map((i) => i.id)).not.toContain(task);
    expect(await screen.findByRole('heading', { name: 'Today' })).toBeTruthy();
  });
});

describe('sharing', () => {
  test('the owner makes a link; the invitee opens it while signed out, signs up on the same server, and joins', async () => {
    server = new MemoryServer();
    const alice = await person();
    const list = await alice.createList('Groceries');
    alice.list(list).addTask('milk');

    // Alice's Share dialog makes a link for Bob.
    await signedIn(alice);
    await fireEvent.click(nav().getByRole('button', { name: 'Groceries' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Share' }));
    const dialog = within(await screen.findByRole('dialog', { name: 'Share “Groceries”' }));
    await fireEvent.click(dialog.getByRole('button', { name: 'Make an invite link' }));
    const link = ((await dialog.findByRole('textbox', { name: 'Invite link' })) as HTMLInputElement).value;
    expect(link.startsWith(`${SERVER}/join#`)).toBe(true);
    expect(dialog.queryByRole('textbox', { name: 'Who is it for?' })).toBeNull();
    cleanup();

    // Bob's app is opened with the link.
    const bobPlatform = createMemoryPlatform({ transport: server.transport(), links: new ManualLinkSource(link) });
    const shell: Shell = {
      defaultServer: SERVER,
      resume: async () => null,
      signUp: async (s, name) => {
        const r = await ClientCore.signUp(bobPlatform, s, name, OPTS);
        cores.push(r.core);
        return r;
      },
      signIn: async () => null,
      links: bobPlatform.links,
    };
    render(App, { shell });
    await fireEvent.click(await screen.findByRole('button', { name: 'Create account' }));
    await fireEvent.input(await screen.findByRole('textbox', { name: 'Your name (optional)' }), { target: { value: 'Robert' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Create account' }));
    const words = within(await screen.findByRole('list', { name: 'Your account key' }))
      .getAllByRole('listitem')
      .map((li) => li.querySelector('.word')!.textContent!);
    await fireEvent.click(screen.getByRole('button', { name: 'I’ve written them down' }));
    for (const [k, input] of (screen.getAllByRole('textbox') as HTMLInputElement[]).entries()) {
      const n = Number(screen.getAllByText(/^Word \d+$/)[k]!.textContent!.replace('Word ', ''));
      await fireEvent.input(input, { target: { value: words[n - 1] } });
    }
    await fireEvent.click(screen.getByRole('button', { name: 'Open runer' }));

    expect(await screen.findByRole('heading', { name: 'Groceries' })).toBeTruthy();
    expect(screen.getByText('Someone invited you to a list')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Join the list' }));
    expect(await screen.findByDisplayValue('milk', {}, { timeout: 3000 })).toBeTruthy();
    expect(screen.getByText(/Shared list · encrypted/)).toBeTruthy();
    // Nobody is asked for a name here: each goes by their account name, not by the link's.
    expect(await screen.findByText(/You and Someone/)).toBeTruthy();

    await waitFor(() => expect(value(alice.notices).some((n) => n.kind === 'joined')).toBe(true));
    await waitFor(() => expect(Object.values(value(alice.lists).find((l) => l.id === list)!.names).sort()).toEqual(['Robert', 'Someone']));
  });

  test('an invite link pasted where a list is added opens Join, in the sidebar and on the Lists page', async () => {
    server = new MemoryServer();
    const [alice, bob] = [await person(), await person()];
    const list = await alice.createList('Groceries');
    const { link } = await alice.list(list).createInvite();

    await signedIn(bob);
    await fireEvent.click(nav().getByRole('button', { name: 'Lists' }));
    await capture('New list', link);
    expect(await screen.findByRole('heading', { name: 'Groceries' })).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

    await screen.findByRole('navigation');
    await fireEvent.click(nav().getByRole('button', { name: 'Add a list' }));
    await capture('New list', ` ${link} `);
    await fireEvent.click(await screen.findByRole('button', { name: 'Join the list' }));
    await screen.findByRole('navigation');
    expect(await nav().findByRole('button', { name: 'Groceries' })).toBeTruthy();
    expect(value(bob.lists).map((l) => l.name)).toContain('Groceries');
  });

  test('half an invite link does nothing: no list is made and the text stays, in the sidebar and on the Lists page', async () => {
    server = new MemoryServer();
    const [alice, bob] = [await person(), await person()];
    const { link } = await alice.list(await alice.createList('Groceries')).createInvite();
    const half = link.slice(0, -10);

    await signedIn(bob);
    const before = value(bob.lists).length;
    await fireEvent.click(nav().getByRole('button', { name: 'Lists' }));
    await capture('New list', half);
    expect((screen.getByRole('textbox', { name: 'New list' }) as HTMLInputElement).value).toBe(half);

    await fireEvent.click(nav().getByRole('button', { name: 'Today' }));
    await fireEvent.click(nav().getByRole('button', { name: 'Add a list' }));
    await capture('New list', half);
    expect((screen.getByRole('textbox', { name: 'New list' }) as HTMLInputElement).value).toBe(half);

    expect(screen.queryByRole('button', { name: 'Join the list' })).toBeNull();
    expect(value(bob.lists).length).toBe(before);
  });

  test('a member without a name is shown by a tag from their key, never as a blank row', async () => {
    server = new MemoryServer();
    const { core: alice } = await ClientCore.signUp(createMemoryPlatform({ transport: server.transport() }), SERVER, '', OPTS);
    cores.push(alice);
    const bob = await person();
    const list = await alice.createList('Groceries');
    await (await bob.invites.open((await alice.list(list).createInvite()).link)).join();

    await signedIn(bob);
    await fireEvent.click(await nav().findByRole('button', { name: 'Groceries' }));
    expect(await screen.findByText(/You and Member [0-9A-F]{4}$/)).toBeTruthy();
    await fireEvent.click(await screen.findByRole('button', { name: 'Share' }));
    const dialog = within(await screen.findByRole('dialog', { name: 'People in “Groceries”' }));
    await waitFor(() => expect(dialog.getByText('Someone')).toBeTruthy());
    const rows = dialog.getAllByRole('listitem').map((li) => li.querySelector('.name')!.textContent!.trim());
    expect(rows[0]).toMatch(/^Member [0-9A-F]{4}$/);
    expect(rows.every((r) => r !== '')).toBe(true);
  });

  test('a member sees who is in the list and their safety codes, and can leave', async () => {
    server = new MemoryServer();
    const [alice, bob] = [await person(), await person()];
    const list = await alice.createList('Groceries');
    await (await bob.invites.open((await alice.list(list).createInvite()).link)).join();

    await signedIn(bob);
    await fireEvent.click(await nav().findByRole('button', { name: 'Groceries' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Share' }));
    const dialog = within(await screen.findByRole('dialog', { name: 'People in “Groceries”' }));
    expect(dialog.queryByRole('button', { name: 'Make an invite link' })).toBeNull();
    expect(dialog.queryByRole('button', { name: 'Remove' })).toBeNull();
    expect(dialog.getByText('Owner')).toBeTruthy();

    // Safety codes are one line at the bottom, for everyone at once; the rows carry none.
    expect(dialog.queryByRole('button', { name: 'Safety code' })).toBeNull();
    await fireEvent.click(dialog.getByRole('button', { name: 'Show safety codes' }));
    const codes = await dialog.findAllByRole('definition');
    expect(codes).toHaveLength(2);
    for (const code of codes) expect(code.textContent!.trim().split(' ')).toHaveLength(6);
    await fireEvent.click(dialog.getByRole('button', { name: 'Hide safety codes' }));
    expect(dialog.queryByRole('definition')).toBeNull();

    // Both go by their account names, and nobody is renamed here: a name is changed in Settings.
    await waitFor(() => expect(dialog.getAllByText('Someone')).toHaveLength(2));
    expect(dialog.getByText('(you)')).toBeTruthy();
    expect(dialog.queryByRole('textbox')).toBeNull();
    for (const name of dialog.getAllByText('Someone')) expect(name.closest('button, a')).toBeNull();

    await fireEvent.click(dialog.getByRole('button', { name: 'Close' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'List options' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Leave list' }));
    await fireEvent.click(within(screen.getByRole('alertdialog', { name: 'Leave “Groceries”?' })).getByRole('button', { name: 'Leave' }));
    await waitFor(() => expect(value(bob.lists).some((l) => l.id === list)).toBe(false));

    // Alice is told, can still edit, and neither app raised a security warning over the join or the leave.
    await waitFor(() => expect(value(alice.lists).find((l) => l.id === list)!.members).toHaveLength(1));
    alice.list(list).addTask('after');
    await waitFor(() => expect(value(alice.connection)).toBe('online'));
    expect(value(alice.warnings)).toEqual([]);
    expect(value(bob.warnings)).toEqual([]);
  });

  test('the owner adds a known contact directly, and removes them with the right wording', async () => {
    server = new MemoryServer();
    const [alice, bob] = [await person(), await person()];
    const first = await alice.createList('Groceries');
    await (await bob.invites.open((await alice.list(first).createInvite()).link)).join();
    const second = await alice.createList('Holiday');

    await signedIn(alice);
    await fireEvent.click(nav().getByRole('button', { name: 'Holiday' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Share' }));
    expect(await screen.findByRole('heading', { name: 'People you’ve shared with' })).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await waitFor(() => expect(value(alice.lists).find((l) => l.id === second)!.members).toHaveLength(2));

    // The owner can't rename anyone either: a name is plain text, and the dialog has no field for one.
    const dialog = within(screen.getByRole('dialog', { name: 'Share “Holiday”' }));
    await waitFor(() => expect(dialog.getAllByText('Someone')).toHaveLength(2));
    for (const name of dialog.getAllByText('Someone')) expect(name.closest('button, a')).toBeNull();
    expect(dialog.queryByRole('textbox')).toBeNull();

    await fireEvent.click(await screen.findByRole('button', { name: 'Remove' }));
    const ask = within(screen.getByRole('alertdialog', { name: /^Remove .+ from this list\?$/ }));
    expect(ask.getByText(/has already seen stays on their devices/)).toBeTruthy();
    await fireEvent.click(ask.getByRole('button', { name: /^Remove ./ }));
    await waitFor(() => expect(value(alice.lists).find((l) => l.id === second)!.members).toHaveLength(1));
  });

  test('a task assigned to the other person is theirs: it shows in their Today and leaves the assigner’s', async () => {
    server = new MemoryServer();
    const [alice, bob] = [await person(), await person()];
    const list = await alice.createList('Groceries');
    await (await bob.invites.open((await alice.list(list).createInvite()).link)).join();
    await waitFor(() => expect(value(alice.lists).find((l) => l.id === list)!.members).toHaveLength(2));
    alice.list(list).addTask('buy milk', { due: localDateString(new Date()) });
    const today = (core: ClientCore) => value(core.today).map((r) => r.item.title);
    // Nobody's yet, so it is everyone's.
    await waitFor(() => expect(today(bob)).toEqual(['buy milk']));
    expect(today(alice)).toEqual(['buy milk']);

    await signedIn(alice);
    await fireEvent.click(nav().getByRole('button', { name: 'Groceries' }));
    await fireEvent.click(await screen.findByDisplayValue('buy milk'));
    await fireEvent.change(await screen.findByRole('combobox', { name: 'Assigned to' }), { target: { value: bob.me } });
    await waitFor(() => expect(today(alice)).toEqual([]));
    await waitFor(() => expect(today(bob)).toEqual(['buy milk']));
    cleanup();

    await signedIn(bob);
    expect(await within(screen.getByRole('main')).findByRole('button', { name: 'buy milk' })).toBeTruthy();
  });

  test('two apps side by side: what one adds or ticks shows in the other; once removed, the list is gone there and the spent link says so', async () => {
    server = new MemoryServer();
    const [alice, bob] = [await person(), await person()];
    const list = await alice.createList('Groceries');
    const { link } = await alice.list(list).createInvite();
    await (await bob.invites.open(link)).join();

    const a = within(render(Main, { core: alice, signedOut: vi.fn() }).container);
    const b = within(render(Main, { core: bob, signedOut: vi.fn() }).container);
    await fireEvent.click(await a.findByRole('button', { name: 'Groceries' }));
    await fireEvent.click(await b.findByRole('button', { name: 'Groceries' }));
    const add = async (app: typeof a, text: string) => {
      const row = await app.findByRole('textbox', { name: 'New task' });
      row.focus();
      await fireEvent.input(row, { target: { value: text } });
      await fireEvent.keyDown(row, { key: 'Enter' });
    };

    await add(a, 'milk');
    expect(await b.findByDisplayValue('milk', {}, { timeout: 3000 })).toBeTruthy();
    await add(b, 'bread');
    expect(await a.findByDisplayValue('bread', {}, { timeout: 3000 })).toBeTruthy();

    // Done tasks are hidden, so a task Bob ticks leaves Alice's page.
    await fireEvent.click(b.getByRole('checkbox', { name: 'Done: milk' }));
    await waitFor(() => expect(a.queryByDisplayValue('milk')).toBeNull(), { timeout: 3000 });

    await fireEvent.click(a.getByRole('button', { name: 'Share' }));
    await fireEvent.click(await a.findByRole('button', { name: 'Remove' }));
    await fireEvent.click(within(a.getByRole('alertdialog')).getByRole('button', { name: /^Remove ./ }));
    await waitFor(() => expect(b.queryByRole('button', { name: 'Groceries' })).toBeNull(), { timeout: 3000 });
    expect(b.getByRole('heading', { name: 'Lists' })).toBeTruthy();
    await waitFor(() => expect(value(alice.connection)).toBe('online'));
    expect(value(alice.warnings)).toEqual([]);
    expect(value(bob.warnings)).toEqual([]);
    expect(a.queryByText(/Two versions/)).toBeNull();
    expect(b.queryByText(/Two versions/)).toBeNull();

    const field = b.getByRole('textbox', { name: 'New list' });
    await fireEvent.input(field, { target: { value: link } });
    await fireEvent.submit(field.closest('form')!);
    expect(await b.findByText(/This invite can’t be used any more/)).toBeTruthy();
  });
});

describe('settings', () => {
  test('the app version is shown', async () => {
    server = new MemoryServer();
    render(Main, { core: await person(), signedOut: vi.fn(), version: '1.2.3' });
    await fireEvent.click(nav().getByRole('button', { name: 'Settings' }));
    expect(await screen.findByText('1.2.3')).toBeTruthy();
  });

  test('the time format: chosen in Settings, and every time follows it', async () => {
    server = new MemoryServer();
    const core = await person();
    const at = new Date();
    at.setHours(21, 30, 0, 0);
    core.list(core.inbox).addTask('call Anna', { due: localDateString(at), remindAt: at.getTime() });
    await signedIn(core);
    await fireEvent.click(nav().getByRole('button', { name: 'Settings' }));
    await fireEvent.click(screen.getByRole('radio', { name: '24-hour (21:30)' }));
    await waitFor(() => expect(value(core.settings).clock).toBe('24h'));
    await fireEvent.click(nav().getByRole('button', { name: 'Today' }));
    expect(await screen.findByText(/21:30/)).toBeTruthy();

    await fireEvent.click(nav().getByRole('button', { name: 'Settings' }));
    await fireEvent.click(screen.getByRole('radio', { name: '12-hour (9:30 PM)' }));
    await waitFor(() => expect(value(core.settings).clock).toBe('12h'));
    await fireEvent.click(nav().getByRole('button', { name: 'Today' }));
    expect(await screen.findByText(/9:30\sPM/)).toBeTruthy();
  });

  test('the look: a theme and light or dark, kept on this device and drawn at once', async () => {
    server = new MemoryServer();
    localStorage.clear();
    startAppearance();
    const root = document.documentElement;
    expect(root.dataset['theme']).toBe('default');
    const core = await person();
    await signedIn(core);
    await fireEvent.click(nav().getByRole('button', { name: 'Settings' }));
    expect((screen.getByRole('radio', { name: 'System' }) as HTMLInputElement).checked).toBe(true);

    await fireEvent.click(screen.getByRole('radio', { name: 'Light' }));
    expect(root.dataset['mode']).toBe('light');
    await fireEvent.click(screen.getByRole('radio', { name: 'Moss' }));
    expect(root.dataset['theme']).toBe('moss');
    // The theme previews are drawn in the mode the app is in.
    const preview = screen.getByRole('radio', { name: 'Harbor' }).parentElement!.querySelector('[data-theme]')!;
    expect(preview.getAttribute('data-mode')).toBe('light');

    // The next start reads it back before anything is drawn.
    root.removeAttribute('data-theme');
    startAppearance();
    expect([root.dataset['theme'], root.dataset['mode']]).toEqual(['moss', 'light']);
    await fireEvent.click(screen.getByRole('radio', { name: 'System' }));
    // jsdom has no system setting: System is dark there.
    expect(root.dataset['mode']).toBe('dark');
    localStorage.clear();
  });

  test('a setting the device could not keep says so, and the choice goes back', async () => {
    server = new MemoryServer();
    const core = await person();
    await signedIn(core);
    await fireEvent.click(nav().getByRole('button', { name: 'Settings' }));
    const clocks = () => screen.getAllByRole('radio', { name: /-hour/ }) as HTMLInputElement[];
    const chosen = clocks().findIndex((r) => r.checked);
    vi.spyOn(core, 'setClock').mockRejectedValue(new Error('disk full'));
    await fireEvent.click(clocks()[1 - chosen]!);
    expect((await screen.findByRole('alert')).textContent).toBe('Couldn’t save that on this device. Try again.');
    await waitFor(() => expect(clocks().findIndex((r) => r.checked)).toBe(chosen));
  });

  test('reminder privacy, account key, no device list, sign-out', async () => {
    server = new MemoryServer();
    const core = await person();
    const { signedOut } = await signedIn(core);
    await fireEvent.click(nav().getByRole('button', { name: 'Settings' }));
    expect(await screen.findByRole('heading', { name: 'Settings' })).toBeTruthy();

    // The name, from sign-up: it can be changed, and cleared. Nothing else about the user is asked for.
    const save = () => screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement;
    const name = screen.getByRole('textbox', { name: 'Name' }) as HTMLInputElement;
    expect(name.value).toBe('Someone');
    expect(save().disabled).toBe(true);
    expect(screen.queryByRole('textbox', { name: 'Email' })).toBeNull();
    await fireEvent.input(name, { target: { value: 'Anna' } });
    await fireEvent.click(save());
    await waitFor(() => expect(value(core.name)).toBe('Anna'));
    await fireEvent.input(name, { target: { value: ' ' } });
    expect(save().disabled).toBe(false);
    await fireEvent.click(save());
    await waitFor(() => expect(value(core.name)).toBe(''));
    await waitFor(() => expect(save().disabled).toBe(true));

    const toggle = screen.getByRole('switch', { name: /Show task titles/ });
    await fireEvent.click(toggle);
    await waitFor(() => expect(value(core.settings).reminderTitles).toBe(false));

    await fireEvent.click(screen.getByRole('button', { name: 'Reveal' }));
    const shown = within(await screen.findByRole('list', { name: 'Your account key' })).getAllByRole('listitem');
    expect(shown).toHaveLength(12);
    await fireEvent.click(screen.getByRole('button', { name: 'Hide' }));

    expect(screen.queryByRole('heading', { name: 'Devices' })).toBeNull();

    await fireEvent.click(screen.getByRole('button', { name: 'Sign out' }));
    await fireEvent.click(within(screen.getByRole('alertdialog', { name: 'Sign out' })).getByRole('button', { name: 'Sign out' }));
    await waitFor(() => expect(signedOut).toHaveBeenCalled());
    cores.splice(cores.indexOf(core), 1);
  });

  test('delete account: asked first, with what happens to the lists, then back to Welcome', async () => {
    server = new MemoryServer();
    const core = await person();
    await core.createList('Groceries');
    const { signedOut } = await signedIn(core);
    await fireEvent.click(nav().getByRole('button', { name: 'Settings' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Delete account' }));
    const dialog = within(screen.getByRole('alertdialog', { name: 'Delete account' }));
    expect(dialog.getByText(/Lists you own are deleted for everyone/)).toBeTruthy();
    expect(signedOut).not.toHaveBeenCalled();
    await fireEvent.click(dialog.getByRole('button', { name: 'Delete for good' }));
    await waitFor(() => expect(signedOut).toHaveBeenCalled());
    cores.splice(cores.indexOf(core), 1);
    expect(await ClientCore.resume(createMemoryPlatform({ transport: server.transport() }), OPTS)).toEqual([]);
  });

  test('a device signed out from another one says so, and stays that way', async () => {
    server = new MemoryServer();
    const core = await person();
    const other = (await ClientCore.signIn(createMemoryPlatform({ transport: server.transport() }), SERVER, await core.showAccountKey(), OPTS))!;
    cores.push(other);
    const { signedOut } = await signedIn(other);
    await waitFor(() => expect(value(other.connection)).toBe('online'));

    await core.changeAccountKey();
    server.drop(); // the real server closes the socket
    expect(await screen.findByRole('heading', { name: 'You’re signed out' }, { timeout: 3000 })).toBeTruthy();
    expect(screen.queryByRole('navigation')).toBeNull();
    expect(signedOut).not.toHaveBeenCalled();

    await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(signedOut).toHaveBeenCalled());
    cores.splice(cores.indexOf(other), 1);
  });
});

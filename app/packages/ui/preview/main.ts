/**
 * The design preview: the real UI in a browser, on sample data, with no
 * server, vault or Tauri. A stand-in for `ClientCore` built on the real list
 * document (`ListDoc`), so editing behaves as in the app; sharing and
 * account calls are inert. For look-and-feel work only; never shipped.
 */

import {
  ListDoc,
  localDateString,
  addDays,
  inOrder,
  moveItem,
  myTasksView,
  openTaskCount,
  todayView,
  Writable,
  type ClientCore,
  type ListHandle,
  type ListSummary,
  type PublicIdentity,
} from '@runer/client-core';
import { mount } from 'svelte';
import { App, startAppearance, type Shell } from '../src/index.js';
import '../src/theme.css';

const key = (n: number) => new Uint8Array(32).fill(n);
const me: PublicIdentity = { signPub: key(0xa7), agreePub: key(1) };
const anna: PublicIdentity = { signPub: key(0x3f), agreePub: key(2) };
const b64 = (u: Uint8Array) => btoa(String.fromCharCode(...u)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
const ME = b64(me.signPub);
const ANNA = b64(anna.signPub);

const docs = new Map<string, ListDoc>();
const members = new Map<string, PublicIdentity[]>();
const today = localDateString(new Date());

function seed(id: string, name: string, people: PublicIdentity[], fill: (d: ListDoc) => void) {
  const d = new ListDoc();
  d.setName(name);
  fill(d);
  docs.set(id, d);
  members.set(id, people);
}

seed('inbox', '', [me], (d) => {
  d.addTask('Call the plumber', { due: today });
  d.addTask('Gift idea for Mum');
  d.addNote('Book recommendation from Jonas: *The Overstory*');
});
seed('trip', 'Weekend trip', [me, anna], (d) => {
  d.addNote('A small plan, so we can look forward to the trip without losing the details in the chat.');
  d.addNote('# Before we go');
  d.addTask('Book a room', { due: today, assignee: ME, body: 'Somewhere close to the water' });
  d.addTask('Find a hiking route', { due: addDays(today, 2), assignee: ANNA });
  d.setPerson(ANNA, 'Anna');
  d.addNote('> The ferry leaves at **09:30**. Be there in good time.');
  d.addNote('## When we’re there');
  d.addTask('Buy dinner');
  d.addNote('- swimsuit\n- walking shoes\n- `power bank`');
});
seed('ideas', 'My ideas', [me], (d) => {
  d.addTask('Draft the article', { due: today });
  d.addNote('Write about **private-by-default** tools.');
});

const lists = new Writable<ListSummary[]>([]);
const todayRefs = new Writable<ReturnType<typeof todayView>>([]);
const mine = new Writable<ReturnType<typeof myTasksView>>({ today: [], upcoming: [], planned: [] });
const snapshots = () => [...docs].map(([id, d]) => d.snapshot(id));

function recompute() {
  const ctx = { me: ME, today: localDateString(new Date()) };
  const order = docs.get('inbox')!.viewOrder('tasks');
  todayRefs.set(inOrder(todayView(snapshots(), ctx), order));
  const m = myTasksView(snapshots(), ctx);
  mine.set({ today: inOrder(m.today, order), upcoming: inOrder(m.upcoming, order), planned: inOrder(m.planned, order) });
  lists.set(
    snapshots().map((l) => ({
      id: l.id,
      name: l.name,
      inbox: l.id === 'inbox',
      openTasks: openTaskCount(l),
      doneTasks: l.items.filter((i) => i.type === 'task' && i.done).length,
      role: 'owner',
      members: members.get(l.id)!,
      shared: members.get(l.id)!.length > 1,
      names: l.people ?? {},
    })),
  );
}
const watch = (id: string) => docs.get(id)!.readable(id).subscribe(recompute);
for (const id of docs.keys()) watch(id);

const inert = async () => undefined;

function handle(id: string): ListHandle {
  const d = docs.get(id);
  if (d === undefined) throw new Error(`no list ${id}`);
  return {
    id,
    inbox: id === 'inbox',
    role: 'owner',
    members: members.get(id)!,
    names: d.people(),
    content: d.readable(id),
    setName: (n) => d.setName(n),
    addTask: (title, o) => d.addTask(title, o),
    addNote: (text, o) => d.addNote(text, o),
    setTitle: (i, v) => d.setTitle(i, v),
    setBody: (i, v) => d.setBody(i, v),
    toggleDone: (i, v) => d.toggleDone(i, v),
    setDue: (i, v) => d.setDue(i, v),
    setReminder: (i, v) => d.setReminder(i, v),
    assign: (i, v) => d.assign(i, v),
    move: (i, to) => d.move(i, to),
    remove: (i) => d.remove(i),
    moveToList: (i, target, index) => moveItem(d, docs.get(target)!, i, index),
    delete: async () => {
      docs.delete(id);
      recompute();
    },
    openInvites: [],
    addMember: inert,
    removeMember: inert,
    createInvite: async () => ({ link: 'https://example.test/join#preview', invitePub: key(9), expires: Date.now() + 48 * 3600_000 }),
    revokeInvite: inert,
    leave: inert,
  };
}

let next = 0;
const core = {
  server: 'https://preview.local',
  me: ME,
  inbox: 'inbox',
  lists,
  today: todayRefs,
  myTasks: mine,
  connection: new Writable('online'),
  warnings: new Writable([]),
  notices: new Writable([]),
  name: new Writable('You'),
  setName: async (n: string) => core.name.set(n.trim()),
  settings: new Writable<{ reminderTitles: boolean; clock?: '12h' | '24h' }>({ reminderTitles: true }),
  list: handle,
  async createList(name: string) {
    const id = `list-${++next}`;
    seed(id, name, [me], () => undefined);
    watch(id);
    return id;
  },
  knownContacts: async () => [anna],
  safetyCode: async () => ['12345', '67890', '13579', '24680', '11223', '44556'],
  sessions: { list: async () => [{ id: 's1', userAgent: 'Preview', createdAt: new Date().toISOString(), lastSeenAt: new Date().toISOString() }], revoke: inert },
  invites: { open: async () => Promise.reject(new Error('preview')) },
  showAccountKey: async () => 'abandon ability able about above absent absorb abstract absurd abuse access accident'.split(' '),
  changeAccountKey: async () => ({ accountKeyWords: [] }),
  signOut: inert,
  stop: inert,
  notificationPermission: async () => 'prompt' as const,
  requestNotificationPermission: async () => 'granted' as const,
  setReminderTitles: async (v: boolean) => core.settings.set({ ...core.settings.value, reminderTitles: v }),
  setClock: async (v: '12h' | '24h') => core.settings.set({ ...core.settings.value, clock: v }),
  search: async () => [],
  setTaskOrder: (ids: string[]) => docs.get('inbox')!.setViewOrder('tasks', ids),
};

const shell: Shell = {
  defaultServer: 'https://preview.local',
  resume: async () => core as unknown as ClientCore,
  signUp: () => Promise.reject(new Error('preview')),
  signIn: async () => null,
};

startAppearance();
mount(App, { target: document.getElementById('app')!, props: { shell } });

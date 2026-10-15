import { createHash, randomBytes } from 'node:crypto';
import type { HttpResponse, Socket, Transport } from '../../src/platform/index.js';

/**
 * An in-memory server for the unit tests: the HTTP calls `Account` and
 * `sync/logs.ts` make, and the Sync protocol's frames (Sync §3–§5), for any
 * number of accounts, each transport its own session. It checks little of what
 * the real server checks: the session, membership (read from the log,
 * unverified) for listing and subscribing, the head on appends, the epoch of
 * writes and `covers_seq` against the head. The vault on each device does the
 * verifying. The e2e suite (`app/e2e`) runs against the real server; when the
 * API changes, change this with it.
 */

interface LogRow {
  seq: number;
  entry: string;
  sig: string;
  hash: string;
}

interface Group {
  log: LogRow[];
  epoch: number;
  /** identity_sign_pub (base64url) of every current member, from the log. */
  members: Set<string>;
  deleted: boolean;
  updates: { seq: number; msg: unknown }[];
  snapshot?: { epoch: number; covers_seq: number; msg: unknown };
}

type Frame = Record<string, unknown>;

interface MemoryAccount {
  authPub: string;
  signPub: string;
  bundle: { identity_bundle: string; bundle_gen: number };
  inbox: string;
  settingsSealed: string | null;
  settingsGen: number;
}

/** A link invite: its package, until its `join` consumes it. */
interface Invite {
  group: string;
  package: string;
  invitePub: string;
  /** Joined, uninvited, or voided by a new epoch: the package is no longer served. */
  used: boolean;
}

/** Who a transport is signed in as. */
interface Session {
  account?: MemoryAccount | undefined;
  /** Set at login; `GET /v1/sessions` lists it. */
  id?: string;
  created?: string;
  /** Ended from another device: answers `session_revoked` until the next login. */
  revoked?: boolean;
}

interface EntryFields {
  seq: number;
  epoch: number;
  type: string;
  author: string;
  subject?: string;
  invite?: string;
}

const parseEntry = (entry: string) => JSON.parse(Buffer.from(entry, 'base64url').toString()) as EntryFields;

/** Membership as the log says, without checking anything: the vault does that. */
function applyMembership(members: Set<string>, e: EntryFields): void {
  if (e.type === 'create' || e.type === 'join') members.add(e.author);
  if (e.type === 'add') members.add(e.subject!);
  if (e.type === 'remove') members.delete(e.subject!);
  if (e.type === 'leave') members.delete(e.author);
}

const b64 = (b: Uint8Array) => Buffer.from(b).toString('base64url');
const unb64 = (s: string) => new Uint8Array(Buffer.from(s, 'base64url'));
const sha256 = (b: Uint8Array) => createHash('sha256').update(b).digest();

class MemorySocket implements Socket {
  private readonly msgs = new Set<(f: unknown) => void>();
  private readonly closes = new Set<(i: { code: number; reason: string }) => void>();
  readonly subs = new Set<string>();
  private out: Promise<void> = Promise.resolve();
  open = true;

  constructor(
    private readonly server: MemoryServer,
    /** The session's identity_sign_pub. */
    readonly member: string,
  ) {}

  send(frame: unknown): void {
    if (!this.open) return;
    // Like the real server: one frame at a time, in order.
    const copy = JSON.parse(JSON.stringify(frame)) as Frame;
    this.out = this.out.then(() => this.server.handle(this, copy));
  }
  onMessage(cb: (f: unknown) => void) {
    this.msgs.add(cb);
    return () => void this.msgs.delete(cb);
  }
  onClose(cb: (i: { code: number; reason: string }) => void) {
    this.closes.add(cb);
    return () => void this.closes.delete(cb);
  }
  close(): void {
    if (!this.open) return;
    this.open = false;
    this.server.sockets.delete(this);
    setTimeout(() => {
      for (const cb of [...this.closes]) cb({ code: 1000, reason: '' });
    });
  }
  /** Server → client, after the current task, in order. */
  push(frame: Frame): void {
    if (!this.open) return;
    const data = JSON.stringify(frame);
    setTimeout(() => {
      if (this.open) for (const cb of [...this.msgs]) cb(JSON.parse(data));
    });
  }
}

export class MemoryServer {
  readonly groups = new Map<string, Group>();
  readonly sockets = new Set<MemorySocket>();
  /** By `auth_pub`. */
  private readonly accounts = new Map<string, MemoryAccount>();
  /** Signed-in sessions, for `/v1/sessions`. */
  private readonly sessions = new Set<Session>();
  /** By invite id. */
  readonly invites = new Map<string, Invite>();
  private seq = 0;
  /** Every frame clients sent, for tests that look at the wire. */
  readonly received: Frame[] = [];
  /** Set to make every request and socket fail, as if offline. */
  offline = false;

  /** A device's transport to this server, with its own session. */
  transport(): Transport {
    const session: Session = {};
    return {
      request: async (method, path, body) => {
        if (this.offline) throw new Error('offline');
        return this.request(session, method, path, body as Record<string, string> | undefined);
      },
      openSocket: async () => {
        if (this.offline || session.account === undefined) throw new Error(this.offline ? 'offline' : 'unauthenticated');
        const s = new MemorySocket(this, session.account.signPub);
        this.sockets.add(s);
        for (const [id, g] of this.groups) if (!g.deleted && g.members.has(s.member)) s.push({ t: 'group', group: id, head_seq: g.log.length - 1 });
        return s;
      },
    };
  }

  /** Closes every socket, as a server restart or a network drop would. */
  drop(): void {
    for (const s of [...this.sockets]) s.close();
  }

  private request(session: Session, method: string, path: string, body: Record<string, string> = {}): HttpResponse {
    const ok = (data: unknown, status = 200): HttpResponse => ({ status, headers: {}, body: { data } });
    const fail = (status: number, code: string): HttpResponse => ({ status, headers: {}, body: { error: { code, message: code } } });
    const [p, query] = path.split('?') as [string, string | undefined];
    const route = `${method} ${p}`;
    const from = Number(new URLSearchParams(query).get('from') ?? 0);
    if (route === 'POST /v1/auth/challenge') return ok({ nonce: b64(randomBytes(32)), pow_bits: 0 });
    if (route === 'POST /v1/auth/login') {
      if (body.inbox_entry !== undefined) {
        const inbox = this.create(body.inbox_entry, body.inbox_sig!);
        const account = { authPub: body.auth_pub!, signPub: body.identity_sign_pub!, bundle: { identity_bundle: body.identity_bundle!, bundle_gen: 1 }, inbox, settingsSealed: null, settingsGen: 0 };
        this.accounts.set(account.authPub, account);
      }
      session.account = this.accounts.get(body.auth_pub!);
      if (session.account === undefined) return fail(401, 'invalid_credentials');
      session.revoked = false;
      session.id = b64(randomBytes(16));
      session.created = new Date().toISOString();
      this.sessions.add(session);
      return ok({ bundle_gen: session.account.bundle.bundle_gen, inbox_id: session.account.inbox });
    }
    const pkg = /^\/v1\/invites\/([^/]+)$/.exec(p);
    if (method === 'GET' && pkg !== null) {
      // No session needed: the invite id is the credential.
      const inv = this.invites.get(pkg[1]!);
      return inv === undefined || inv.used ? fail(410, 'invite_not_usable') : ok({ package: inv.package });
    }
    const me = session.account;
    if (session.revoked) return fail(401, 'session_revoked');
    if (me === undefined) return fail(401, 'unauthenticated');
    if (route === 'POST /v1/auth/logout') return ok({});
    if (route === 'GET /v1/sessions') {
      const mine = [...this.sessions].filter((s) => s.account === me);
      return ok(mine.map((s) => ({ id: s.id, user_agent: 'test', created_at: s.created, last_seen_at: s.created, current: s === session })));
    }
    const revoke = /^\/v1\/sessions\/([^/]+)$/.exec(p);
    if (method === 'DELETE' && revoke !== null) {
      const s = [...this.sessions].find((x) => x.account === me && x.id === decodeURIComponent(revoke[1]!));
      if (s === undefined) return fail(404, 'not_found');
      this.sessions.delete(s);
      s.account = undefined;
      s.revoked = true;
      return ok({});
    }
    if (route === 'GET /v1/account') {
      return ok({ identity_sign_pub: me.signPub, inbox_id: me.inbox, settings_sealed: me.settingsSealed, settings_gen: me.settingsGen });
    }
    if (route === 'PUT /v1/account/settings') {
      if (!body.settings_sealed || body.settings_gen === undefined) return fail(400, 'bad_request');
      if (Number(body.settings_gen) !== me.settingsGen) return fail(409, 'conflict');
      me.settingsSealed = body.settings_sealed;
      return ok({ settings_gen: ++me.settingsGen });
    }
    if (route === 'GET /v1/account/bundle') return ok(me.bundle);
    if (route === 'POST /v1/account/rekey/challenge') return ok({ nonce: b64(randomBytes(32)) });
    if (route === 'POST /v1/account/rekey') {
      if (Number(body.bundle_gen) !== me.bundle.bundle_gen) return fail(409, 'conflict');
      // E2EE §9.2: the old key no longer signs in, and every other session ends.
      this.accounts.delete(me.authPub);
      me.authPub = body.new_auth_pub!;
      me.bundle = { identity_bundle: body.new_bundle!, bundle_gen: me.bundle.bundle_gen + 1 };
      this.accounts.set(me.authPub, me);
      for (const s of [...this.sessions]) {
        if (s.account !== me || s === session) continue;
        this.sessions.delete(s);
        s.account = undefined;
        s.revoked = true;
      }
      return ok({ bundle_gen: me.bundle.bundle_gen });
    }
    if (route === 'DELETE /v1/account') {
      const mine = [...this.groups].filter(([, g]) => !g.deleted && g.members.has(me.signPub));
      const owned = mine.filter(([, g]) => parseEntry(g.log[0]!.entry).author === me.signPub);
      if (owned.length < mine.length) return fail(409, 'account_has_lists');
      for (const [id, g] of owned) {
        g.deleted = true;
        for (const inv of this.invites.values()) if (inv.group === id) inv.used = true;
        this.notify(id);
      }
      this.accounts.delete(me.authPub);
      for (const s of [...this.sessions]) {
        if (s.account !== me) continue;
        this.sessions.delete(s);
        s.account = undefined;
        s.revoked = true;
      }
      return ok({ scheduled: true });
    }
    if (route === 'GET /v1/groups') {
      return ok(
        [...this.groups]
          .filter(([, g]) => !g.deleted && g.members.has(me.signPub))
          .map(([id, g]) => ({ id, head_seq: g.log.length - 1, head_hash: g.log.at(-1)!.hash, epoch: g.epoch })),
      );
    }
    if (route === 'POST /v1/groups') {
      if (this.groups.has(sha256(unb64(body.entry!)).toString('hex'))) return fail(409, 'conflict');
      const id = this.create(body.entry!, body.sig!);
      return ok({ id }, 201);
    }
    const i = /^\/v1\/invites\/([^/]+)\/(log|accept)$/.exec(p);
    if (i !== null) {
      const inv = this.invites.get(i[1]!);
      const g = inv === undefined ? undefined : this.groups.get(inv.group);
      if (inv === undefined || inv.used || g === undefined || g.deleted) return fail(410, 'invite_not_usable');
      if (method === 'GET' && i[2] === 'log') return ok(g.log.filter((r) => r.seq >= from).slice(0, 500));
      if (method === 'POST' && i[2] === 'accept') {
        const r = this.append(inv.group, g, body);
        if (r.status === 200) inv.used = true;
        return r;
      }
    }
    const m = /^\/v1\/groups\/([0-9a-f]{64})(\/log|\/invites)?$/.exec(p);
    const g = m === null ? undefined : this.groups.get(m[1]!);
    if (m !== null) {
      if (g === undefined || g.deleted || !g.members.has(me.signPub)) return fail(403, 'forbidden');
      const id = m[1]!;
      if (method === 'GET' && m[2] === '/log') return ok(g.log.filter((r) => r.seq >= from).slice(0, 500));
      if (method === 'POST' && m[2] === '/log') return this.append(id, g, body);
      if (method === 'POST' && m[2] === '/invites') {
        this.invites.set(body.invite_id!, { group: id, package: body.package!, invitePub: body.invite_pub!, used: false });
        return ok({ id: body.invite_id, expires_at: body.expires_at }, 201);
      }
      if (method === 'DELETE' && m[2] === undefined) {
        if (id === me.inbox) return fail(403, 'forbidden');
        g.deleted = true;
        this.notify(id);
        return ok({ deleted: true });
      }
    }
    return fail(404, 'not_found');
  }

  private append(id: string, g: Group, body: Record<string, string>): HttpResponse {
    const e = parseEntry(body.entry!);
    if (e.seq !== g.log.length) return { status: 409, headers: {}, body: { error: { code: 'conflict', message: 'conflict' } } };
    g.log.push({ seq: e.seq, entry: body.entry!, sig: body.sig!, hash: b64(sha256(unb64(body.entry!))) });
    for (const inv of this.invites.values()) {
      if (inv.group === id && (e.epoch !== g.epoch || (e.type === 'uninvite' && e.invite === inv.invitePub))) inv.used = true;
    }
    g.epoch = e.epoch;
    applyMembership(g.members, e);
    // A removed member's sockets are cut off from the list, as the server does.
    for (const s of this.sockets) if (!g.members.has(s.member)) s.subs.delete(id);
    this.notify(id);
    return { status: 200, headers: {}, body: { data: { id } } };
  }

  private create(entry: string, sig: string): string {
    const raw = unb64(entry);
    const id = sha256(raw).toString('hex');
    const members = new Set<string>();
    applyMembership(members, parseEntry(entry));
    this.groups.set(id, { log: [{ seq: 0, entry, sig, hash: b64(sha256(raw)) }], epoch: 0, members, deleted: false, updates: [] });
    // Sync §3: a new list sends `group` too, so the owner's other devices learn of it.
    this.notify(id);
    return id;
  }

  /** `group` to every socket of anyone who is, or just stopped being, in the list: all of them, simply. */
  private notify(id: string): void {
    const g = this.groups.get(id)!;
    for (const s of this.sockets) s.push({ t: 'group', group: id, head_seq: g.log.length - 1 });
  }

  /** Sync §3–§5. */
  async handle(s: MemorySocket, f: Frame): Promise<void> {
    this.received.push(f);
    const error = (code: string, cid?: unknown) => s.push({ t: 'error', ...(cid === undefined ? {} : { cid }), code });
    if (f.t === 'sub') {
      for (const { list, since } of f.lists as { list: string; since: number }[]) {
        const g = this.groups.get(list);
        if (g === undefined || g.deleted || !g.members.has(s.member)) {
          error('forbidden');
          continue;
        }
        s.subs.add(list);
        let head = since;
        if (g.snapshot !== undefined && g.snapshot.covers_seq > since) {
          s.push({ t: 'snapshot', list, epoch: g.snapshot.epoch, covers_seq: g.snapshot.covers_seq, msg: g.snapshot.msg });
          head = g.snapshot.covers_seq;
        }
        for (const u of g.updates.filter((u) => u.seq > head)) {
          s.push({ t: 'update', list, seq: u.seq, msg: u.msg });
          head = u.seq;
        }
        s.push({ t: 'synced', list, head });
      }
      return;
    }
    if (f.t === 'unsub') {
      for (const { list } of f.lists as { list: string }[]) s.subs.delete(list);
      return;
    }
    if (f.t === 'update' || f.t === 'snapshot') {
      const header = JSON.parse(Buffer.from(unb64(f.header as string)).toString()) as { list: string; epoch: number; covers_seq?: number };
      const g = this.groups.get(header.list);
      if (g === undefined || g.deleted) return error('forbidden', f.cid);
      if (header.epoch !== g.epoch) return error('stale-epoch', f.cid);
      const msg = { header: f.header, nonce: f.nonce, ct: f.ct, sig: f.sig };
      if (f.t === 'snapshot') {
        const head = g.updates.at(-1)?.seq ?? 0;
        if (header.covers_seq! > head) return error('bad-request', f.cid);
        g.snapshot = { epoch: header.epoch, covers_seq: header.covers_seq!, msg };
        return s.push({ t: 'ack', cid: f.cid, seq: header.covers_seq });
      }
      const seq = (this.seq += 1 + Math.floor(Math.random() * 3)); // global and with gaps, like a BIGSERIAL
      g.updates.push({ seq, msg });
      for (const other of this.sockets) if (other !== s && other.subs.has(header.list)) other.push({ t: 'update', list: header.list, seq, msg });
      s.push({ t: 'ack', cid: f.cid, seq });
    }
  }
}

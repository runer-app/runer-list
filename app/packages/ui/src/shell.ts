import type { ClientCore } from '@runer/client-core';

/**
 * What the app entry hands the UI: getting a `ClientCore`, with the
 * platform already chosen. The UI never sees a platform.
 */
export interface Shell {
  /** The server the Welcome screen suggests. */
  defaultServer: string;
  /** The app's version, shown on Welcome. */
  version?: string;
  /** The account kept on this device, or `null` for Welcome. */
  resume(): Promise<ClientCore | null>;
  /** `name` is what the people the user shares lists with see; optional, empty for none. */
  signUp(server: string, name: string): Promise<{ core: ClientCore; accountKeyWords: string[] }>;
  /** `null` for a typo in the words, caught before any request. */
  signIn(server: string, words: string[]): Promise<ClientCore | null>;
  /** Invite links the app was opened with, or that arrive while it runs. */
  links?: { initial(): Promise<string | null>; onLink(cb: (link: string) => void): () => void };
  /** Debug builds: the device's log, lines of `<unix ms> <text>`, shown under Settings. */
  debugLog?: DebugLog;
  /** Desktop builds with an update feed: an update was downloaded, and restarting runs it. */
  update?: AppUpdate;
}

export interface AppUpdate {
  /** Called once, when an update is ready. */
  onReady(cb: () => void): void;
  /** Restarts into the update; rejects if it couldn't be installed. */
  restart(): Promise<void>;
}

export interface DebugLog {
  read(): Promise<string>;
  clear(): Promise<void>;
}

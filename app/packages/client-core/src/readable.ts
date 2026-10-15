/** Minimal subscribe interface (the Svelte store contract), so client-core does not depend on Svelte. */
export interface Readable<T> {
  /** Calls `run` with the current value straight away and on every change. Returns the unsubscribe function. */
  subscribe(run: (value: T) => void): () => void;
}

/** A `Readable` that its owner sets. */
export class Writable<T> implements Readable<T> {
  private readonly subs = new Set<(value: T) => void>();

  constructor(private current: T) {}

  get value(): T {
    return this.current;
  }

  set(value: T): void {
    if (value === this.current) return;
    this.current = value;
    for (const run of [...this.subs]) run(value);
  }

  subscribe(run: (value: T) => void): () => void {
    this.subs.add(run);
    run(this.current);
    return () => void this.subs.delete(run);
  }
}

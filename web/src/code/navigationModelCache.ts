export type DisposableModel = { dispose(): void };

// Keeps Monaco models needed by navigation/peek available without creating
// visible Echo tabs. Loading is deduplicated and bounded so a large reference
// result cannot issue an unbounded burst of file reads.
export class NavigationModelCache<T extends DisposableModel> {
  private readonly limit: number;
  private readonly models = new Map<string, T>();
  private readonly loads = new Map<string, Promise<boolean>>();
  private readonly waiters: Array<() => void> = [];
  private active = 0;
  private disposed = false;

  constructor(limit = 6) {
    this.limit = Math.max(1, Math.floor(limit));
  }

  async ensure(key: string, current: () => T | null, load: () => Promise<T | null>): Promise<boolean> {
    if (this.disposed) return false;
    if (current() || this.models.has(key)) return true;
    const pending = this.loads.get(key);
    if (pending) return pending;
    const task = this.withSlot(async () => {
      if (this.disposed) return false;
      if (current() || this.models.has(key)) return true;
      const model = await load();
      if (!model) return false;
      if (this.disposed) {
        model.dispose();
        return false;
      }
      const existing = current() || this.models.get(key);
      if (existing) {
        if (existing !== model) model.dispose();
        return true;
      }
      this.models.set(key, model);
      return true;
    }).finally(() => this.loads.delete(key));
    this.loads.set(key, task);
    return task;
  }

  take(key: string): T | undefined {
    const model = this.models.get(key);
    this.models.delete(key);
    return model;
  }

  has(key: string): boolean {
    return this.models.has(key);
  }

  entries(): Array<[string, T]> {
    return [...this.models.entries()];
  }

  invalidate(predicate: (key: string, model: T) => boolean): void {
    for (const [key, model] of this.models) {
      if (!predicate(key, model)) continue;
      this.models.delete(key);
      model.dispose();
    }
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    for (const model of this.models.values()) model.dispose();
    this.models.clear();
  }

  private async withSlot<R>(run: () => Promise<R>): Promise<R> {
    if (this.active >= this.limit) await new Promise<void>((resolve) => this.waiters.push(resolve));
    this.active++;
    try {
      return await run();
    } finally {
      this.active--;
      this.waiters.shift()?.();
    }
  }
}

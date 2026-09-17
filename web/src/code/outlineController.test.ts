import { describe, expect, it, vi } from "vitest";
import { OutlineController, outlineTargets } from "./outlineController";
import type { OutlineResult, OutlineSnapshot, OutlineTarget } from "./outlineTypes";

const target = (path: string, kind: "file" | "directory" = "file", rootId = "root"): OutlineTarget => ({ ref: { rootId, path }, kind });
const empty: OutlineResult = { status: "empty", symbols: [], message: "No symbols found." };
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("Outline snapshot loading", () => {
  it("normalizes overlapping selections without mixing workspace roots", () => {
    expect(outlineTargets([target("src/a.ts"), target("src", "directory"), target("src", "directory"), target("src/sub", "directory"), target("src/a.ts", "file", "second")]))
      .toEqual([target("src", "directory"), target("src/a.ts", "file", "second")]);
    expect(outlineTargets([{ ...target("link", "directory"), isSymlink: true }, target("link/a.ts")])).toHaveLength(2);
  });

  it("recurses only on open, deduplicates files, and reports skips and per-file errors", async () => {
    let latest!: OutlineSnapshot;
    const list = vi.fn(async ({ path }: { path: string }) => path === "src"
      ? [target("src/a.ts"), target("src/deep", "directory"), { ...target("src/link", "directory"), isSymlink: true }, { ...target("src/blocked"), blockedReason: "Access denied" }]
      : [target("src/deep/b.ts"), target("src/a.ts")]);
    const resolve = vi.fn(async ({ path }: { path: string }) => {
      if (path.endsWith("b.ts")) throw new Error("File disappeared");
      return empty;
    });
    const loader = new OutlineController({ list, resolve, label: (ref) => ref.path, change: (snapshot) => { latest = snapshot; } });
    expect(list).not.toHaveBeenCalled();
    expect(resolve).not.toHaveBeenCalled();
    await loader.open([target("src", "directory"), target("src/a.ts")]);
    expect(list.mock.calls.map(([ref]) => ref.path)).toEqual(["src", "src/deep"]);
    expect(resolve.mock.calls.map(([ref]) => ref.path).sort()).toEqual(["src/a.ts", "src/deep/b.ts"]);
    expect(latest.grouped).toBe(true);
    expect(latest.loading).toBe(false);
    expect(latest.files.map((file) => file.status).sort()).toEqual(["empty", "error", "skipped", "skipped"]);
    expect(latest.completed).toBe(4);
    loader.dispose();
  });

  it("bounds work to four concurrent requests, including directories", async () => {
    let active = 0, peak = 0;
    const pending: Array<ReturnType<typeof deferred<OutlineResult>>> = [];
    const resolve = vi.fn(async () => {
      active++; peak = Math.max(peak, active);
      const task = deferred<OutlineResult>(); pending.push(task);
      const result = await task.promise;
      active--; return result;
    });
    const loader = new OutlineController({ list: async () => [], resolve, label: (ref) => ref.path, change: vi.fn() });
    const loading = loader.open(Array.from({ length: 13 }, (_, index) => target(`${index}.ts`)));
    expect(resolve).toHaveBeenCalledTimes(4);
    for (let index = 0; index < 13; index++) {
      await vi.waitFor(() => expect(pending.length).toBeGreaterThan(index));
      pending[index].resolve(empty);
    }
    await loading;
    expect(peak).toBe(4);
  });

  it("cancels queued work and ignores late results after collapsing or reopening", async () => {
    const late = deferred<OutlineResult>();
    const snapshots: string[][] = [];
    const signals: AbortSignal[] = [];
    const loader = new OutlineController({ list: async () => [], label: (ref) => ref.path,
      resolve: async (ref, signal) => { signals.push(signal); return ref.path === "old.ts" ? late.promise : empty; },
      change: (snapshot) => snapshots.push(snapshot.files.map((file) => `${file.ref.path}:${file.status}`)),
    });
    const first = loader.open([target("old.ts")]);
    loader.close();
    await first;
    expect(signals[0].aborted).toBe(true);
    await loader.open([target("new.ts")]);
    const count = snapshots.length;
    late.resolve({ status: "error", symbols: [], message: "stale" });
    await Promise.resolve(); await Promise.resolve();
    expect(snapshots).toHaveLength(count);
    expect(snapshots.at(-1)).toEqual(["new.ts:empty"]);
  });

  it("copies the selection and distinguishes no selection from an empty folder", async () => {
    const folder = deferred<OutlineTarget[]>();
    let latest!: OutlineSnapshot;
    const loader = new OutlineController({ list: () => folder.promise, resolve: async () => empty, label: (ref) => ref.path, change: (value) => { latest = value; } });
    await loader.open([]);
    expect(latest).toMatchObject({ loading: false, selectionEmpty: true, files: [] });
    const selected = target("empty", "directory");
    const loading = loader.open([selected]);
    selected.ref.path = "changed";
    folder.resolve([]);
    await loading;
    expect(latest).toMatchObject({ loading: false, selectionEmpty: false, grouped: true, files: [] });
  });
});

import { isRefWithin, refKey, type FileRef } from "./types";
import type { OutlineFile, OutlineResult, OutlineSnapshot, OutlineTarget } from "./outlineTypes";

export function outlineTargets(selection: readonly OutlineTarget[]): OutlineTarget[] {
  const unique = [...new Map(selection.map((item) => [refKey(item.ref), { ...item, ref: { ...item.ref } }])).values()];
  const directories = unique.filter((item) => item.kind === "directory" && !item.isSymlink && !item.blockedReason);
  return unique.filter((item) => !directories.some((parent) => parent !== item && isRefWithin(item.ref, parent.ref)));
}

/** A snapshot loader: only opening starts work; changing the editor never subscribes it to updates. */
export class OutlineController {
  private abort: AbortController | null = null;

  constructor(private readonly options: {
    list(ref: FileRef, signal: AbortSignal): Promise<OutlineTarget[]>;
    resolve(ref: FileRef, signal: AbortSignal): Promise<OutlineResult>;
    label(ref: FileRef): string;
    change(snapshot: OutlineSnapshot): void;
  }) {}

  open(selection: readonly OutlineTarget[]): Promise<void> {
    this.close();
    const abort = this.abort = new AbortController();
    const current = () => this.abort === abort && !abort.signal.aborted;
    const queue = outlineTargets(selection);
    const state: OutlineSnapshot = {
      grouped: selection.length !== 1 || selection[0].kind === "directory",
      loading: queue.length > 0, selectionEmpty: !selection.length, files: [], completed: 0,
    };
    const seen = new Set(queue.map((target) => refKey(target.ref)));
    let cursor = 0;
    let active = 0;
    const publish = () => { if (current()) this.options.change(state); };
    publish();
    return new Promise((done) => {
      // Finish promptly even when a provider cannot cancel its own work. Its late result is ignored.
      abort.signal.addEventListener("abort", () => done(), { once: true });
      const pump = () => {
        if (!current()) return;
        while (active < 4 && cursor < queue.length) {
          const target = queue[cursor++];
          active++;
          void process(target).finally(() => {
            active--;
            if (current()) pump();
          });
        }
        if (!active && cursor === queue.length) {
          state.loading = false;
          publish();
          done();
        }
      };
      const process = async (target: OutlineTarget) => {
        let file: OutlineFile | undefined;
        const entry = () => file ||= (() => {
          const value: OutlineFile = { ref: target.ref, label: this.options.label(target.ref), status: "loading", symbols: [] };
          state.files.push(value);
          return value;
        })();
        try {
          if (target.blockedReason || (target.kind === "directory" && target.isSymlink)) {
            Object.assign(entry(), { status: "skipped", message: target.blockedReason || "Directory symlink skipped to avoid recursive links." });
          } else if (target.kind === "directory") {
            const children = await this.options.list(target.ref, abort.signal);
            if (!current()) return;
            for (const child of children) {
              const key = refKey(child.ref);
              if (!seen.has(key)) { seen.add(key); queue.push(child); }
            }
          } else {
            entry();
            publish();
            const result = await this.options.resolve(target.ref, abort.signal);
            if (!current()) return;
            Object.assign(file!, result);
          }
        } catch (error) {
          if (!current()) return;
          Object.assign(entry(), { status: "error", message: error instanceof Error ? error.message : String(error) });
        }
        if (current()) {
          if (file) state.completed++;
          publish();
        }
      };
      pump();
    });
  }

  close(): void {
    this.abort?.abort();
    this.abort = null;
  }

  dispose(): void { this.close(); }
}

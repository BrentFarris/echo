import type * as Monaco from "monaco-editor";
import type { LSPRange } from "./lspTypes";

type LensIdentity = { range: LSPRange; title: string };
type PendingKeys = { promise: Promise<Set<string>>; complete(keys: Set<string>): void };

const pendingGoTestKeys = new Map<string, PendingKeys>();
const emptyKeys = new Set<string>();

export function beginGoTestLensComposition(model: Monaco.editor.ITextModel): (lenses: LensIdentity[]) => void {
  const key = modelKey(model);
  let completePromise!: (keys: Set<string>) => void;
  const promise = new Promise<Set<string>>((resolve) => { completePromise = resolve; });
  let completed = false;
  const entry: PendingKeys = {
    promise,
    complete(keys) {
      if (completed) return;
      completed = true;
      completePromise(keys);
    },
  };
  pendingGoTestKeys.set(key, entry);
  while (pendingGoTestKeys.size > 64) pendingGoTestKeys.delete(pendingGoTestKeys.keys().next().value!);
  return (lenses) => entry.complete(new Set(lenses.map(codeLensIdentity)));
}

export async function builtInGoTestLensKeys(model: Monaco.editor.ITextModel): Promise<Set<string>> {
  // Monaco invokes all providers synchronously before awaiting their results.
  // Yield once so the built-in provider can publish its same-version barrier
  // even if the LSP provider happened to be ordered first.
  await Promise.resolve();
  return pendingGoTestKeys.get(modelKey(model))?.promise || emptyKeys;
}

export function codeLensIdentity(lens: LensIdentity): string {
  const { start, end } = lens.range;
  return `${start.line}:${start.character}:${end.line}:${end.character}:${lens.title}`;
}

function modelKey(model: Monaco.editor.ITextModel): string {
  return `${model.uri.toString()}@${model.getVersionId()}`;
}

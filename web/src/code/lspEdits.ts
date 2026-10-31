import type { TextEdit, Position } from 'vscode-languageserver-protocol';

const compare = (a: Position, b: Position) => a.line - b.line || a.character - b.character;

// Validate the complete edit set before Monaco's synchronous multi-model apply.
// In particular, an overlap in a later file must not leave earlier files edited.
export function validateTextEdits(edits: TextEdit[]): void {
  if (edits.length > 10000) throw new Error('Too many language edits');
  const sorted = [...edits].sort((a, b) => compare(a.range.start, b.range.start) || compare(a.range.end, b.range.end));
  let previous: Position | undefined;
  for (const edit of sorted) {
    const { start, end } = edit.range;
    if (![start.line, start.character, end.line, end.character].every(n => Number.isInteger(n) && n >= 0) || compare(start, end) > 0 || typeof edit.newText !== 'string') throw new Error('Invalid language edit');
    if (previous && compare(start, previous) < 0) throw new Error('Overlapping language edits');
    previous = end;
  }
}

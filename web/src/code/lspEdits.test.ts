import { describe, expect, it } from 'vitest';
import { validateTextEdits } from './lspEdits';
const edit = (start: number, end: number, newText = '😀') => ({ range: { start: { line: 0, character: start }, end: { line: 0, character: end } }, newText });
describe('LSP edit validation', () => {
  it('accepts unordered disjoint UTF-16 edits and adjacent inserts', () => expect(() => validateTextEdits([edit(4, 6), edit(0, 2), edit(2, 2)])).not.toThrow());
  it('rejects overlapping edits before any model can be changed', () => expect(() => validateTextEdits([edit(4, 8), edit(2, 5)])).toThrow('Overlapping'));
  it('rejects invalid or reversed ranges', () => { expect(() => validateTextEdits([edit(-1, 3)])).toThrow(); expect(() => validateTextEdits([edit(3, 2)])).toThrow(); });
});

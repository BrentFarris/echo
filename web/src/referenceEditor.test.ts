// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from 'vitest';
import { ReferenceInput, snapshotEditor, trimmedDraft } from './referenceEditor';
import { displayReferences, typedBotSegments } from './references';
import type { Bot, Segment } from './types';

const bots = [{ id: 'b', handle: 'kaiju', name: 'Kaiju', roots: [], plugins: {} }] as unknown as Bot[];
let editor: ReferenceInput;
beforeEach(() => { document.body.innerHTML = '<reference-input contenteditable="true"></reference-input>'; editor = document.querySelector('reference-input') as ReferenceInput; editor.bots = () => bots; editor.focus(); });
const file = (path: string, text = 'My File.ts'): Segment => ({ type: 'file', path, text, kind: 'file', allowedBots: ['b'] });

describe('reference editor', () => {
  it('serializes each duplicate filename occurrence, Unicode and trimmed ranges', () => {
    editor.value = '  🙂 inspect ';
    editor.replace('#My File.ts ', editor.value.length, editor.value.length, file('C:/a/My File.ts'));
    editor.replace('#My File.ts  ', editor.value.length, editor.value.length, file('C:/b/My File.ts'));
    const draft = trimmedDraft(editor.snapshot());
    expect(draft.text).toBe('🙂 inspect #My File.ts #My File.ts');
    expect(draft.references.map(r => [r.path, r.range])).toEqual([['C:/a/My File.ts', { start: 11, end: 22 }], ['C:/b/My File.ts', { start: 23, end: 34 }]]);
    editor.restore(draft); expect(editor.snapshot()).toEqual(draft);
  });
  it('keeps chip insertion, atomic deletion, undo and redo in one history', () => {
    editor.value = 'look #fi'; editor.setSelectionRange(8);
    editor.replace('#My File.ts ', 5, 8, file('C:/My File.ts'));
    expect(editor.querySelectorAll('[data-reference]')).toHaveLength(1);
    editor.dispatchEvent(new KeyboardEvent('keydown', { key: 'z', ctrlKey: true, bubbles: true }));
    expect(editor.value).toBe('look #fi');
    editor.dispatchEvent(new KeyboardEvent('keydown', { key: 'z', ctrlKey: true, shiftKey: true, bubbles: true }));
    expect(editor.snapshot().references).toHaveLength(1);
    editor.setSelectionRange(16);
    editor.dispatchEvent(new InputEvent('beforeinput', { inputType: 'deleteContentBackward', cancelable: true }));
    expect(editor.value).toBe('look  '); expect(editor.snapshot().references).toHaveLength(0);
  });
  it('tracks ranges after text is inserted before a chip and restores multiline drafts', () => {
    editor.replace('#My File.ts', 0, 0, file('C:/My File.ts'));
    editor.replace('first\n🙂 ', 0, 0);
    expect(editor.snapshot().references[0].range).toEqual({ start: 9, end: 20 });
    const draft = editor.snapshot(); editor.value = 'other chat'; editor.restore(draft);
    expect(editor.snapshot()).toEqual(draft);
    editor.setSelectionRange(6, 20); editor.replaceSelection('replacement');
    expect(editor.value).toBe('first\nreplacement'); expect(editor.snapshot().references).toHaveLength(0);
  });
  it('decorates completed bots, excluding code and email addresses', () => {
    editor.value = '@kaiju `@kaiju` ```@kaiju``` user@kaiju @kaiju';
    editor.dispatchEvent(new InputEvent('input'));
    expect(editor.querySelectorAll('.bot-reference')).toHaveLength(1);
    expect(typedBotSegments(editor.value, bots)).toHaveLength(2);
  });
  it('keeps raw pasted or restored markup literal', () => {
    editor.replaceSelection('<img src=x onerror=alert(1)>\nline two');
    expect(editor.querySelector('img')).toBeNull();
    expect(snapshotEditor(editor).text).toBe('<img src=x onerror=alert(1)>\nline two');
  });
  it('defers mention decoration while composing', () => {
    editor.dispatchEvent(new CompositionEvent('compositionstart'));
    editor.textContent = '@kaiju '; editor.dispatchEvent(new InputEvent('input', { isComposing: true }));
    expect(editor.querySelector('.bot-reference')).toBeNull();
    editor.dispatchEvent(new CompositionEvent('compositionend'));
    expect(editor.querySelector('.bot-reference')).not.toBeNull();
  });
  it('does not serialize the browser caret placeholder after a chip as a newline', () => {
    editor.replace('#My File.ts', 0, 0, file('C:/My File.ts'));
    editor.append(document.createElement('br'));
    expect(editor.value).toBe('#My File.ts');
    editor.replace('\n', editor.value.length, editor.value.length);
    expect(editor.value).toBe('#My File.ts\n');
  });
});

describe('historical reference rendering', () => {
  it('uses legacy bot segments and uniquely matched file text', () => {
    const result = displayReferences({ content: '@kaiju see #My File.ts', segments: [{ type: 'bot', text: '@kaiju', botId: 'b' }, { type: 'text', text: ' see #My File.ts' }, file('C:/My File.ts')] });
    expect(result.refs.map(r => r.range)).toEqual([{ start: 0, end: 6 }, { start: 11, end: 22 }]);
    expect(result.extra).toHaveLength(0);
  });
  it('does not guess duplicate filenames or link a filename prefix', () => {
    expect(displayReferences({ content: '#My File.ts #My File.ts', segments: [file('C:/a/My File.ts'), file('C:/b/My File.ts')] }).extra).toHaveLength(2);
    expect(displayReferences({ content: '#My File.tsx', segments: [file('C:/My File.ts')] }).refs).toHaveLength(0);
  });
});

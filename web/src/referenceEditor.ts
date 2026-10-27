import type { Bot, Segment } from './types';
import { referenceFromElement, referenceText, renderReferenceText, typedBotSegments, updateReferenceChip } from './references';

export type ReferenceDraft = { text: string; references: Segment[] };
type Edit = ReferenceDraft & { start: number; end: number };

function placeholderBreak(node: HTMLElement) {
  if (node.hasAttribute('data-editor-tail')) return true;
  // Chromium adds a trailing BR when deleting the text after an inline atom.
  // Our actual line breaks are text newlines, so this is only a caret aid.
  if (node.tagName !== 'BR') return false;
  for (let next = node.nextSibling; next; next = next.nextSibling) if (next.nodeType !== Node.TEXT_NODE || next.textContent) return false;
  return true;
}

export function snapshotEditor(root: Node): ReferenceDraft {
  let text = ''; const references: Segment[] = [];
  const visit = (node: Node) => {
    if (node.nodeType === Node.TEXT_NODE) { text += node.textContent || ''; return; }
    if (!(node instanceof HTMLElement) && node.nodeType !== Node.DOCUMENT_FRAGMENT_NODE) return;
    if (node instanceof HTMLElement && placeholderBreak(node)) return;
    if (node instanceof HTMLElement && node.dataset.reference) {
      const ref = referenceFromElement(node), start = text.length; text += referenceText(ref);
      references.push({ ...ref, range: { start, end: text.length } }); return;
    }
    if (node instanceof HTMLElement && node.tagName === 'BR') { text += '\n'; return; }
    for (const child of node.childNodes) {
      if (child instanceof HTMLElement && /^(DIV|P)$/.test(child.tagName) && text && !text.endsWith('\n')) text += '\n';
      visit(child);
    }
  };
  visit(root); return { text, references };
}

export function trimmedDraft(draft: ReferenceDraft): ReferenceDraft {
  const leading = draft.text.length - draft.text.trimStart().length, text = draft.text.trim();
  return { text, references: draft.references.map(ref => ({ ...ref, range: ref.range && { start: ref.range.start - leading, end: ref.range.end - leading } })).filter(ref => ref.range && ref.range.start >= 0 && ref.range.end <= text.length) };
}

/** A plain-text editor with atomic references. All history snapshots are data,
 * never arbitrary HTML; programmatic chip insertion participates in undo. */
export class ReferenceInput extends HTMLElement {
  bots: () => Bot[] = () => [];
  private history: Edit[] = [];
  private historyIndex = -1;
  private replaying = false;
  private composing = false;
  private connected = false;
  connectedCallback() {
    if (this.connected) return; this.connected = true;
    this.resetHistory();
    this.addEventListener('beforeinput', e => {
      if (this.disabled) { e.preventDefault(); return; }
      if (e.inputType === 'historyUndo' || e.inputType === 'historyRedo') { e.preventDefault(); this.undo(e.inputType === 'historyRedo'); return; }
      this.rememberSelection();
      if (this.composing || e.isComposing) return;
      if (e.inputType === 'insertParagraph' || e.inputType === 'insertLineBreak') { e.preventDefault(); this.replaceSelection('\n'); return; }
      if (e.inputType === 'deleteContentBackward' || e.inputType === 'deleteContentForward') {
        const start = this.selectionStart, end = this.selectionEnd;
        if (start !== end) return;
        const ref = this.snapshot().references.find(r => e.inputType === 'deleteContentBackward' ? r.range?.end === start : r.range?.start === start);
        if (ref?.range) { e.preventDefault(); this.replace('', ref.range.start, ref.range.end); }
      }
    });
    this.addEventListener('input', () => {
      if (!this.composing && !this.value) { this.replaceChildren(); this.setSelectionRange(0); }
      if (!this.composing) this.decorateBots();
      if (!this.replaying && !this.composing) this.record();
      this.toggleAttribute('data-empty', !this.value);
    });
    this.addEventListener('compositionstart', () => { this.rememberSelection(); this.composing = true; });
    this.addEventListener('compositionend', () => { this.composing = false; this.changed(); });
    this.addEventListener('keydown', e => {
      if (e.isComposing) return;
      if ((e.ctrlKey || e.metaKey) && !e.altKey && ['z', 'y'].includes(e.key.toLowerCase())) { e.preventDefault(); e.stopImmediatePropagation(); this.undo(e.key.toLowerCase() === 'y' || e.shiftKey); }
    });
    this.addEventListener('paste', e => {
      if (e.clipboardData?.files.length) return;
      e.preventDefault(); if (!this.disabled) this.replaceSelection(e.clipboardData?.getData('text/plain') || '');
    });
    for (const type of ['copy', 'cut'] as const) this.addEventListener(type, e => {
      const start = this.selectionStart, end = this.selectionEnd;
      if (start === end) return;
      e.preventDefault(); e.clipboardData?.setData('text/plain', this.value.slice(start, end));
      if (type === 'cut' && !this.disabled) this.replace('', start, end);
    });
    // Rich HTML drag payloads must not introduce elements into the editor.
    this.addEventListener('drop', e => { if (!e.dataTransfer?.files.length) { e.preventDefault(); if (!this.disabled) this.replaceSelection(e.dataTransfer?.getData('text/plain') || ''); } });
  }
  get disabled() { return this.getAttribute('contenteditable') === 'false'; }
  set disabled(value: boolean) { this.contentEditable = String(!value); this.setAttribute('aria-disabled', String(value)); }
  get value() { return this.snapshot().text; }
  set value(text: string) { this.restore({ text, references: [] }); }
  snapshot() { return snapshotEditor(this); }
  restore(draft: ReferenceDraft) {
    this.paint(draft.text, draft.references);
    this.toggleAttribute('data-empty', !draft.text); this.resetHistory();
  }
  private paint(text: string, refs: Segment[]) {
    renderReferenceText(this, text, refs, this.bots());
    // The final empty line needs a line box or Chromium places subsequent input
    // before the newline. This presentation-only BR is excluded from snapshots.
    if (text.endsWith('\n')) { const tail = document.createElement('br'); tail.dataset.editorTail = ''; this.append(tail); }
  }
  private offset(container: Node, offset: number) {
    const range = document.createRange(); range.selectNodeContents(this);
    try { range.setEnd(container, offset); return snapshotEditor(range.cloneContents()).text.length; } catch { return this.value.length; }
  }
  get selectionStart() { const s = getSelection(); return s?.rangeCount && this.contains(s.getRangeAt(0).startContainer) ? this.offset(s.getRangeAt(0).startContainer, s.getRangeAt(0).startOffset) : this.value.length; }
  get selectionEnd() { const s = getSelection(); return s?.rangeCount && this.contains(s.getRangeAt(0).endContainer) ? this.offset(s.getRangeAt(0).endContainer, s.getRangeAt(0).endOffset) : this.value.length; }
  private point(at: number): [Node, number] {
    let cursor = 0;
    const walk = (node: Node): [Node, number] | undefined => {
      if (node instanceof HTMLElement && placeholderBreak(node)) return;
      if (node.nodeType === Node.TEXT_NODE) { const length = node.textContent?.length || 0; if (at <= cursor + length) return [node, Math.max(0, at - cursor)]; cursor += length; return; }
      if (node instanceof HTMLElement && (node.dataset.reference || node.tagName === 'BR')) {
        const length = node.dataset.reference ? referenceText(referenceFromElement(node)).length : 1;
        if (at <= cursor + length) return [node.parentNode!, [...node.parentNode!.childNodes].indexOf(node) + Number(at > cursor)];
        cursor += length; return;
      }
      for (const child of node.childNodes) { const found = walk(child); if (found) return found; }
    };
    return walk(this) || [this, this.childNodes.length];
  }
  setSelectionRange(start: number, end = start) {
    const range = document.createRange(); range.setStart(...this.point(start)); range.setEnd(...this.point(end));
    const selection = getSelection(); selection?.removeAllRanges(); selection?.addRange(range);
  }
  private rememberSelection() { const current = this.history[this.historyIndex]; if (current) { current.start = this.selectionStart; current.end = this.selectionEnd; } }
  private edit(): Edit { return { ...this.snapshot(), start: this.selectionStart, end: this.selectionEnd }; }
  private resetHistory() { this.history = [this.edit()]; this.historyIndex = 0; }
  private record() {
    const next = this.edit(), last = this.history[this.historyIndex];
    if (last && last.text === next.text && JSON.stringify(last.references) === JSON.stringify(next.references)) return;
    this.history.splice(this.historyIndex + 1); this.history.push(next);
    if (this.history.length > 150) this.history.shift();
    this.historyIndex = this.history.length - 1;
  }
  private undo(redo: boolean) {
    if (this.disabled) return;
    const index = this.historyIndex + (redo ? 1 : -1), edit = this.history[index]; if (!edit) return;
    this.historyIndex = index; this.replaying = true;
    this.paint(edit.text, edit.references); this.setSelectionRange(edit.start, edit.end);
    this.changed(); this.replaying = false;
  }
  private changed() { this.dispatchEvent(new InputEvent('input', { bubbles: true })); }
  replaceSelection(text: string) { this.replace(text, this.selectionStart, this.selectionEnd); }
  replace(text: string, start: number, end: number, ref?: Segment) {
    this.rememberSelection(); const draft = this.snapshot();
    // A selection intersecting a chip replaces the entire atomic reference.
    for (const r of draft.references) if (r.range && start < r.range.end && end > r.range.start) { start = Math.min(start, r.range.start); end = Math.max(end, r.range.end); }
    const delta = text.length - (end - start);
    const references = draft.references.filter(r => r.range!.end <= start || r.range!.start >= end).map(r => r.range!.start >= end ? { ...r, range: { start: r.range!.start + delta, end: r.range!.end + delta } } : r);
    if (ref) references.push({ ...ref, range: { start, end: start + referenceText(ref).length } });
    this.paint(draft.text.slice(0, start) + text + draft.text.slice(end), references.sort((a, b) => a.range!.start - b.range!.start));
    this.setSelectionRange(start + text.length); this.changed();
  }
  private decorateBots() {
    if (this.replaying) return;
    const draft = this.snapshot(), start = this.selectionStart, end = this.selectionEnd;
    const bots = typedBotSegments(draft.text, this.bots(), true).filter(ref => !draft.references.some(r => r.range!.start < ref.range!.end && r.range!.end > ref.range!.start));
    // A bot chip moved into a code span must become literal text again.
    const valid = typedBotSegments(draft.text, this.bots());
    const refs = draft.references.filter(ref => ref.type !== 'bot' || valid.some(v => v.range!.start === ref.range!.start && v.range!.end === ref.range!.end) || !this.bots().some(b => b.id === ref.botId));
    if (!bots.length && refs.length === draft.references.length) return;
    this.paint(draft.text, [...refs, ...bots].sort((a, b) => a.range!.start - b.range!.start)); this.setSelectionRange(start, end);
  }
  updateReferences(refs: Segment[]) {
    const snapshot = this.snapshot();
    for (const [index, chip] of [...this.querySelectorAll<HTMLElement>('[data-reference]')].entries()) {
      const ref = snapshot.references[index];
      const updated = refs.find(r => r.path === ref.path && r.type === ref.type && r.range?.start === ref.range?.start);
      if (updated) updateReferenceChip(chip, { ...updated, kind: updated.kind || ref.kind }, this.bots());
    }
  }
}
if (!customElements.get('reference-input')) customElements.define('reference-input', ReferenceInput);

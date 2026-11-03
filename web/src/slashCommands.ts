import type { ReferenceDraft, ReferenceInput } from './referenceEditor';
import type { SlashCommand } from './types';

export const planCommandText = '\n\n You are to plan this request out, do not make any changes until the plan is approved';
export type SlashMatch = { start: number; end: number; query: string };

export function activeSlash(draft: ReferenceDraft, caret: number, selectionEnd = caret): SlashMatch | null {
  if (selectionEnd !== caret) return null;
  const match = /(?:^|\s)\/([a-zA-Z0-9_-]*)$/.exec(draft.text.slice(0, caret));
  if (!match) return null;
  const start = caret - match[1].length - 1;
  // Consume the whole token even when the caret is in its middle. A path is
  // never a command, including when the caret happens to precede its slash.
  const suffix = /^[^\s]*/.exec(draft.text.slice(caret))![0];
  if (!/^[a-zA-Z0-9_-]*$/.test(suffix)) return null;
  const end = caret + suffix.length;
  if (draft.references.some(ref => ref.range && start < ref.range.end && end > ref.range.start)) return null;
  // Track unmatched as well as closed backtick/tilde spans and fences.
  let delimiter = '';
  for (const token of draft.text.slice(0, start).matchAll(/`+|~{3,}/g)) {
    if (!delimiter) delimiter = token[0];
    else if (token[0][0] === delimiter[0] && token[0].length >= delimiter.length) delimiter = '';
  }
  if (delimiter) return null;
  return { start, end, query: match[1] };
}

export function matchingCommands(commands: SlashCommand[], query: string): SlashCommand[] {
  query = query.toLowerCase();
  const rank = (command: SlashCommand) => command.name.toLowerCase() === query ? 0 : command.name.toLowerCase().startsWith(query) ? 1 : 2;
  return commands.filter(c => c.enabled && c.name.toLowerCase().includes(query))
    .sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
}

/** Remove the token and insert text without reparsing or losing reference data. */
export function slashEdit(draft: ReferenceDraft, match: SlashMatch, action: SlashCommand['action']): { draft: ReferenceDraft; caret: number } {
  let text = draft.text.slice(0, match.start) + draft.text.slice(match.end);
  let references = draft.references.map(ref => ref.range && ref.range.start >= match.end
    ? { ...ref, range: { start: ref.range.start - (match.end - match.start), end: ref.range.end - (match.end - match.start) } } : ref);
  let caret = match.start;
  if (action.type === 'text') {
    const at = action.position === 'append' ? text.length : action.position === 'prepend' ? 0 : match.start;
    text = text.slice(0, at) + action.text + text.slice(at);
    references = references.map(ref => ref.range && ref.range.start >= at
      ? { ...ref, range: { start: ref.range.start + action.text.length, end: ref.range.end + action.text.length } } : ref);
    if (action.position === 'insert' || action.position === 'prepend') caret += action.text.length;
  }
  return { draft: { text, references }, caret };
}

export function applySlash(input: ReferenceInput, match: SlashMatch, command: SlashCommand) {
  const edit = slashEdit(input.snapshot(), match, command.action);
  input.applyEdit(edit.draft, edit.caret);
  input.focus({ preventScroll: true });
  input.setSelectionRange(edit.caret);
}

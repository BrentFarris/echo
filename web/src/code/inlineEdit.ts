// Keep the undoable edit as small as possible, including final-newline changes.
export function replacementEdit(before: string, after: string): { start: number; end: number; text: string } {
  // Monaco expands ranges that split surrogate pairs, and cannot address the
  // middle of CRLF. Include both halves in the replacement text as well.
  const splitsPair = (text: string, offset: number): boolean => {
    const previous = text.charCodeAt(offset - 1), next = text.charCodeAt(offset);
    return (previous >= 0xd800 && previous <= 0xdbff && next >= 0xdc00 && next <= 0xdfff)
      || (previous === 13 && next === 10);
  };
  let start = 0;
  while (start < before.length && start < after.length && before[start] === after[start]) start++;
  if (splitsPair(before, start) || splitsPair(after, start)) start--;
  let end = before.length, afterEnd = after.length;
  while (end > start && afterEnd > start && before[end - 1] === after[afterEnd - 1]) { end--; afterEnd--; }
  if (splitsPair(before, end) || splitsPair(after, afterEnd)) { end++; afterEnd++; }
  return { start, end, text: after.slice(start, afterEnd) };
}

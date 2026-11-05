import { escapeHTML as esc } from './helpers';
import type { Bot, Message, Segment } from './types';
export { typedBotSegments } from './recipientMentions';

const svg = (body: string) => `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`;
export const referenceIcons = {
  group: svg('<circle cx="9" cy="8" r="3"/><path d="M3 20v-2a6 6 0 0 1 12 0v2M16 5a3 3 0 0 1 0 6m2 3a5 5 0 0 1 3 4v2"/>'),
  file: svg('<path d="m8 6-6 6 6 6m8-12 6 6-6 6"/>'),
  directory: svg('<path d="M3 7V5h7l2 3h9v12H3V7Z"/>'),
  bot: svg('<rect x="4" y="7" width="16" height="13" rx="3"/><path d="M12 3v4M2 12v4m20-4v4M9 16h6"/><circle cx="8" cy="12" r="1"/><circle cx="16" cy="12" r="1"/>'),
};
export const referenceText = (ref: Segment) => ref.type === 'group' || ref.type === 'bot' ? ref.text || '@bot' : '#' + (ref.text || ref.path?.split(/[\\/]/).at(-1) || 'file');
export const referenceIcon = (ref?: Segment) => referenceIcons[ref?.type === 'group' ? 'group' : ref?.type === 'bot' ? 'bot' : ref?.kind === 'directory' ? 'directory' : 'file'];
export function referenceFromElement(el: HTMLElement): Segment { return JSON.parse(el.dataset.reference!); }
export function referenceChip(ref: Segment, bots: Bot[] = []): HTMLElement {
  const chip = document.createElement('span');
  chip.className = 'reference-chip' + (ref.type === 'group' || ref.type === 'bot' ? ' bot-reference' : '');
  chip.contentEditable = 'false'; chip.tabIndex = 0; chip.setAttribute('role', 'button');
  chip.dataset.reference = JSON.stringify(ref);
  chip.innerHTML = `${referenceIcon(ref)}<span class="reference-label">${esc(ref.text || ref.path?.split(/[\\/]/).at(-1))}</span>`;
  updateReferenceChip(chip, ref, bots);
  return chip;
}
export function updateReferenceChip(chip: HTMLElement, ref: Segment, bots: Bot[]) {
  chip.dataset.reference = JSON.stringify(ref);
  if (ref.type === 'group') {
    chip.title = `Show members of ${ref.text}`;
    chip.setAttribute('aria-label', chip.title);
    return;
  }
  const unavailable = ref.type === 'file' && !!ref.allowedBots && !ref.allowedBots.length;
  chip.classList.toggle('reference-unavailable', unavailable);
  chip.title = ref.type === 'bot' ? `Show details for ${ref.text}` : `${ref.path}${unavailable ? '\nUnavailable to all recipients' : ref.allowedBots?.length ? '\nAccessible to ' + ref.allowedBots.map(id => bots.find(b => b.id === id)?.handle || id).join(', ') : ''}`;
  chip.setAttribute('aria-label', ref.type === 'bot' ? `Show details for ${ref.text}` : `Open ${ref.kind === 'directory' ? 'folder' : 'file'} ${ref.path}`);
}

// Older transcripts append file descriptors after the text/bot stream.
// Only promote a legacy filename when both its descriptor and occurrence are unique.
export function displayReferences(message: Pick<Message, 'content' | 'segments'>) {
  const refs: Segment[] = [], extra: Segment[] = [];
  let cursor = 0;
  for (const ref of message.segments || []) {
    if (ref.type === 'text') { cursor += ref.text?.length || 0; continue; }
    let range = ref.range;
    if (!range && ref.type === 'bot') { range = { start: cursor, end: cursor + (ref.text?.length || 0) }; cursor = range.end; }
    if (!range && ref.type === 'file') {
      const token = referenceText(ref), start = message.content.indexOf(token);
      const unique = message.segments?.filter(r => r.type === 'file' && referenceText(r) === token).length === 1;
      if (unique && start >= 0 && message.content.indexOf(token, start + 1) < 0 && !/[\w.-]/.test(message.content[start + token.length] || '')) range = { start, end: start + token.length };
    }
    if (range && range.start >= 0 && range.end > range.start && message.content.slice(range.start, range.end) === referenceText(ref)) refs.push({ ...ref, range });
    else if (ref.type === 'file') extra.push(ref);
  }
  const ordered: Segment[] = [];
  for (const ref of refs.sort((a, b) => a.range!.start - b.range!.start)) {
    if (ref.range!.start >= (ordered.at(-1)?.range!.end || 0)) ordered.push(ref);
    else if (ref.type === 'file') extra.push(ref);
  }
  return { refs: ordered, extra };
}
export function renderReferenceText(root: HTMLElement, content: string, refs: Segment[], bots: Bot[] = []) {
  root.replaceChildren(); let cursor = 0;
  for (const ref of refs) {
    if (!ref.range || ref.range.start < cursor || content.slice(ref.range.start, ref.range.end) !== referenceText(ref)) continue;
    root.append(document.createTextNode(content.slice(cursor, ref.range.start)), referenceChip(ref, bots)); cursor = ref.range.end;
  }
  root.append(document.createTextNode(content.slice(cursor)));
}
export function renderMessageReferences(root: HTMLElement, message: Message, bots: Bot[]) {
  const { refs, extra } = displayReferences(message);
  renderReferenceText(root, message.content, refs, bots);
  if (extra.length) { const fallback = document.createElement('div'); fallback.className = 'legacy-references'; fallback.setAttribute('aria-label', 'Referenced files'); extra.forEach(ref => fallback.append(referenceChip(ref, bots))); root.append(fallback); }
}

export function showBotDetails(anchor: HTMLElement, ref: Segment, bots: Bot[], edit: (bot: Bot) => void) {
  const bot = bots.find(b => b.id === ref.botId), popover = document.createElement('div');
  popover.innerHTML = `<strong>${esc(bot?.name || ref.text)}</strong><span class="subtle">${esc(bot ? '@' + bot.handle : 'This bot is no longer available.')}</span>${bot ? `<div class="bot-folders">${bot.roots?.length ? '<span>Accessible folders</span><ul>' + bot.roots.map(r => `<li>${esc(r)}</li>`).join('') + '</ul>' : 'Unrestricted host access'}</div><button type="button">Bot settings</button>` : ''}`;
  popover.querySelector('button')?.addEventListener('click', () => { popover.dispatchEvent(new Event('dismiss')); if (bot) edit(bot); });
  openDetails(anchor, popover, 'Bot details');
}

export function showGroupDetails(anchor: HTMLElement, label: string, handles: string[]) {
  const popover = document.createElement('div');
  popover.innerHTML = `<strong>${esc(label)}</strong>` + (handles.length ? `<ul>${handles.map(handle => '<li>@' + esc(handle) + '</li>').join('')}</ul>` : '<span>This group is unavailable.</span>');
  openDetails(anchor, popover, 'Group members');
}

function openDetails(anchor: HTMLElement, popover: HTMLElement, label: string) {
  document.querySelector<HTMLElement>('.bot-details')?.dispatchEvent(new Event('dismiss'));
  popover.className = 'bot-details'; popover.setAttribute('role', 'dialog'); popover.setAttribute('aria-label', label); popover.tabIndex = -1;
  const abort = new AbortController();
  const close = (restoreFocus = true) => { abort.abort(); popover.remove(); if (restoreFocus && anchor.isConnected) anchor.focus({ preventScroll: true }); };
  popover.addEventListener('dismiss', () => close(), { signal: abort.signal });
  document.addEventListener('pointerdown', e => { if (!popover.contains(e.target as Node) && !anchor.contains(e.target as Node)) close(false); }, { signal: abort.signal });
  document.addEventListener('keydown', e => { if (e.key === 'Escape') { e.preventDefault(); close(); } }, { signal: abort.signal });
  popover.addEventListener('focusout', e => {
    if (e.relatedTarget instanceof Node && popover.contains(e.relatedTarget)) return;
    setTimeout(() => { if (popover.isConnected && !popover.contains(document.activeElement)) close(false); }, 0);
  }, { signal: abort.signal });
  document.body.append(popover); const rect = anchor.getBoundingClientRect();
  popover.style.left = `${Math.max(8, Math.min(rect.left, innerWidth - popover.offsetWidth - 8))}px`;
  popover.style.top = `${Math.max(8, Math.min(rect.bottom + 6, innerHeight - popover.offsetHeight - 8))}px`;
  popover.focus();
}

import type { Bot, BotGroup, BotGroupMember, Segment } from './types';
import { entryNode, instanceOnline, machineName, resourceID } from './instances';

export function mentionTokens(text: string) {
  const masked = text.replace(/```[\s\S]*?```|`[^`]*`/g, code => ' '.repeat(code.length));
  return Array.from(masked.matchAll(/(?:^|[^A-Za-z0-9_@])@([A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z][A-Za-z0-9_-]*)*)/g), match => {
    const end = match.index! + match[0].length, start = end - match[1].length - 1;
    return { handle: match[1].toLowerCase(), text: text.slice(start, end), range: { start, end } };
  });
}

export function groupReference(group: BotGroup): Segment {
  return { type: 'group', groupId: group.id, nodeId: group.nodeId, text: '@' + group.handle };
}
export function referencedGroup(ref: Segment, groups: BotGroup[]) {
  return groups.find(g => g.id === ref.groupId && (g.nodeId || entryNode) === (ref.nodeId || entryNode));
}
export function memberHandle(member: BotGroupMember, bots: Bot[]) {
  const bot = bots.find(b => b.id === resourceID(member.nodeId, member.botId));
  return bot?.handle || (member.nodeId === entryNode ? '' : machineName(member.nodeId) + '.') + member.handle;
}

export function typedBotSegments(text: string, bots: Bot[], completedOnly = false, groups: BotGroup[] = []): Segment[] {
  const refs: Segment[] = [];
  for (const token of mentionTokens(text)) {
    if (completedOnly && token.range.end === text.length) continue;
    const bot = bots.find(b => b.handle.toLowerCase() === token.handle);
    const group = groups.find(g => g.handle.toLowerCase() === token.handle);
    if (bot) refs.push({ type: 'bot', botId: bot.id, text: token.text, range: token.range });
    else if (group) refs.push({ ...groupReference(group), text: token.text, range: token.range });
  }
  return refs;
}

// Preview and file lookup use the same group bindings. The server resolves
// membership independently when it accepts the message.
export function draftRecipients(content: string, segments: Segment[], bots: Bot[], groups: BotGroup[], strict: boolean) {
  let masked = content;
  for (const ref of segments) if (ref.range) masked = masked.slice(0, ref.range.start) + ' '.repeat(ref.range.end - ref.range.start) + masked.slice(ref.range.end);
  const typed = typedBotSegments(masked, bots, false, groups);
  if (strict) {
    for (const token of mentionTokens(masked)) {
      if (!typed.some(ref => ref.range?.start === token.range.start)) throw new Error(`unknown bot @${token.handle}`);
    }
  }
  const selected: Segment[] = [];
  for (const ref of [...segments, ...typed]) {
    if (ref.type === 'bot') selected.push(ref);
    if (ref.type !== 'group') continue;
    if (!instanceOnline(ref.nodeId || entryNode)) throw new Error(`Machine '${machineName(ref.nodeId)}' is offline; ${ref.text} is unavailable.`);
    const group = referencedGroup(ref, groups);
    if (!group) throw new Error(`Group ${ref.text} no longer exists.`);
    const before = selected.length;
    for (const member of group.members) {
      const id = resourceID(member.nodeId, member.botId), handle = memberHandle(member, bots);
      if (group.deliveryMode === 'one' && (!instanceOnline(member.nodeId) || !bots.some(b => b.id === id))) continue;
      if (!instanceOnline(member.nodeId)) throw new Error(`Machine '${machineName(member.nodeId)}' is offline; ${ref.text} member @${handle} is unavailable.`);
      if (!bots.some(b => b.id === id)) throw new Error(`${ref.text} member @${handle} is unavailable.`);
      selected.push({ type: 'bot', botId: id, nodeId: member.nodeId, text: '@' + handle });
    }
    if (group.deliveryMode === 'one' && selected.length === before) throw new Error(`No member of ${ref.text} is online and available.`);
  }
  return { selected, masked };
}

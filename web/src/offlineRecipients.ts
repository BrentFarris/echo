import type { Bot, BotGroup, Segment } from './types';
import { entryNode, instances, instanceOnline, machineName, splitID } from './instances';
import { draftRecipients, mentionTokens } from './recipientMentions';

// Mask structured references and code exactly as file-reference lookup does.
// A saved chip retains its owner even after the machine or bot is renamed.
export function assertOnlineRecipients(content: string, segments: Segment[], bots: Bot[], groups: BotGroup[] = []) {
  const { selected, masked } = draftRecipients(content, segments, bots, groups, false);
  const check = (node: string, handle: string) => {
    if (!instanceOnline(node)) throw new Error(`Machine '${machineName(node)}' is offline; @${handle.replace(/^@/, '')} is unavailable.`);
  };
  for (const ref of selected) {
    const node = ref.nodeId || splitID(ref.botId)[0];
    check(node, ref.text || bots.find(b => b.id === ref.botId)?.handle || 'bot');
  }
  // A known machine with no cached roster still deserves an offline error.
  for (const { handle } of mentionTokens(masked)) {
    for (const node of instances.keys()) {
      if (node !== entryNode && handle.startsWith(machineName(node).toLowerCase() + '.')) check(node, handle);
    }
  }
}

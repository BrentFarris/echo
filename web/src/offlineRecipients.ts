import type { Bot, Segment } from './types';
import { entryNode, instances, instanceOnline, machineName, splitID } from './instances';
import { typedBotSegments } from './references';

// Mask structured references and code exactly as file-reference lookup does.
// A saved chip retains its owner even after the machine or bot is renamed.
export function assertOnlineRecipients(content: string, segments: Segment[], bots: Bot[]) {
  let masked = content.replace(/```[\s\S]*?```|`[^`]*`/g, value => ' '.repeat(value.length));
  for (const ref of segments) if (ref.range) masked = masked.slice(0, ref.range.start) + ' '.repeat(ref.range.end - ref.range.start) + masked.slice(ref.range.end);
  const check = (node: string, handle: string) => {
    if (!instanceOnline(node)) throw new Error(`Machine '${machineName(node)}' is offline; @${handle.replace(/^@/, '')} is unavailable.`);
  };
  for (const ref of [...segments.filter(s => s.type === 'bot'), ...typedBotSegments(masked, bots)]) {
    const node = ref.nodeId || splitID(ref.botId)[0];
    check(node, ref.text || bots.find(b => b.id === ref.botId)?.handle || 'bot');
  }
  // A known machine with no cached roster still deserves an offline error.
  for (const match of masked.matchAll(/(?:^|[^A-Za-z0-9_@])@([A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z][A-Za-z0-9_-]*)*)/g)) {
    for (const node of instances.keys()) {
      if (node !== entryNode && match[1].toLowerCase().startsWith(machineName(node).toLowerCase() + '.')) check(node, match[1]);
    }
  }
}

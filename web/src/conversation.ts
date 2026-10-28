import type { Bot, Chat, Message, Run } from './types';

export function conversationThreads(chat: Chat | undefined, messages: Message[], runs: Run[], bots: Bot[]) {
  const names = new Map<string, string>();
  for (const message of messages) {
    for (const id of message.recipients || []) if (!names.has(id)) names.set(id, id);
    if (message.botId) names.set(message.botId, message.role === 'assistant' ? message.author : names.get(message.botId) || message.botId);
  }
  for (const [id, name] of Object.entries(chat?.bots || {})) names.set(id, name);
  for (const run of runs) if (!names.has(run.botId)) names.set(run.botId, run.botId);
  return [...names].map(([id, name]) => ({ id, name: bots.find(bot => bot.id === id)?.name || name, status: [...runs].reverse().find(run => run.botId === id)?.status }));
}

export function threadMessages(messages: Message[], botId?: string, runs: Run[] = []): Message[] {
  if (!botId) return messages;
  const owners = new Map(runs.map(run => [run.id, run.botId]));
  return messages.filter(message => {
    const owner = message.botId || (message.runId ? owners.get(message.runId) : undefined);
    if (owner) return owner === botId;
    return !message.recipients?.length || message.recipients.includes(botId);
  });
}

export type ResponseGroup = { key: string; messages: Message[]; author: string; status?: string; final?: Message };
export type ConversationItem = { kind: 'message'; message: Message } | { kind: 'response'; group: ResponseGroup } | { kind: 'run-error'; run: Run };

// A bot can resume the same run after a handoff. Only its latest successful
// final reply folds the work; interrupted and still-running work stays visible.
export function conversationItems(messages: Message[], runs: Run[]): ConversationItem[] {
  const result: ConversationItem[] = [], groups = new Map<string, ResponseGroup>();
  const byRun = new Map(runs.map(run => [run.id, run]));
  let turn = '';
  for (const message of messages) {
    if (!message.runId) {
      result.push({ kind: 'message', message });
      if (message.role === 'user') turn = message.id;
      continue;
    }
    const key = `${turn}:${message.runId}:${message.botId || ''}`;
    let group = groups.get(key);
    if (!group) {
      group = { key, messages: [], author: message.author, status: byRun.get(message.runId)?.status };
      groups.set(key, group); result.push({ kind: 'response', group });
    }
    group.messages.push(message);
    if (message.role === 'assistant') group.author = message.author;
  }
  for (const group of groups.values()) {
    const last = group.messages.at(-1)!;
    if ((!group.status || group.status === 'completed') && last.role === 'assistant' && last.status === 'completed' && !last.toolCalls?.length && last.content.trim()) group.final = last;
  }
  // Errors belong to the last response for their run, or to its prompt if the
  // run stopped before producing a message (for example, while queued).
  const after = new Map<ConversationItem, ConversationItem[]>();
  for (const run of runs) {
    if (!run.error || !['failed', 'cancelled', 'interrupted'].includes(run.status)) continue;
    const anchor = result.findLast(item => item.kind === 'response' && item.group.messages.some(message => message.runId === run.id))
      || result.find(item => item.kind === 'message' && item.message.id === run.userMessageId);
    if (!anchor) continue;
    const errors = after.get(anchor) || [];
    errors.push({ kind: 'run-error', run }); after.set(anchor, errors);
  }
  return result.flatMap(item => [item, ...(after.get(item) || [])]);
}

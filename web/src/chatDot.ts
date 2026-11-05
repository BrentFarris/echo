import type { Chat, PendingQuestion, Run } from './types';

export const activeRunStatuses = ['running', 'waiting', 'queued'];
export const finishedRunStatuses = ['completed', 'failed', 'cancelled', 'interrupted'];

export function chatUnread(chat: Chat, runs: Run[], selectedChatId: string, viewingChat: boolean) {
  if (chat.id === selectedChatId && viewingChat) return false;
  const lastReadAt = chat.lastReadAt;
  if (!lastReadAt) return true;
  return runs.some(run => run.chatId === chat.id && finishedRunStatuses.includes(run.status) && (run.updatedAt || '') > lastReadAt);
}

export function hasPendingQuestion(questions: PendingQuestion[] | undefined, chatId: string, botId?: string) {
  return questions?.some(question => question.chatId === chatId && (botId === undefined || question.botId === botId)) ?? false;
}

export type ChatDotState = 'question' | 'busy' | 'done' | '';

// Questions take priority even in the selected chat: opening a chat does not
// resolve its questions. Yellow and green retain their existing busy/unread rules.
export function chatDotState(chat: Chat, runs: Run[], questions: PendingQuestion[] | undefined, selectedChatId: string, viewingChat: boolean): ChatDotState {
  if (hasPendingQuestion(questions, chat.id)) return 'question';
  if (chat.waitingForGroup || runs.some(run => run.chatId === chat.id && activeRunStatuses.includes(run.status))) return 'busy';
  if (!chat.lastReadAt) return '';
  return chatUnread(chat, runs, selectedChatId, viewingChat) ? 'done' : '';
}

export function chatDotMarkup(dot: ChatDotState) {
  switch (dot) {
    case 'question': return '<span class="chat-dot question" role="img" aria-label="Waiting for your answer" title="Waiting for your answer"></span>';
    case 'busy': return '<span class="chat-dot busy" role="img" aria-label="In progress" title="In progress"></span>';
    case 'done': return '<span class="chat-dot done" role="img" aria-label="Finished, not yet opened" title="Finished — not yet opened"></span>';
    default: return '';
  }
}

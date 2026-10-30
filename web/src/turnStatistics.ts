import type { Message, Run, UsageCounts } from './types';
import type { ResponseGroup } from './conversation';

export const statisticsPreferenceKey = 'echo.showTurnStatistics';
export function showTurnStatistics() {
  try { return localStorage.getItem(statisticsPreferenceKey) !== 'false'; } catch { return true; }
}
export function setShowTurnStatistics(value: boolean) {
  try { localStorage.setItem(statisticsPreferenceKey, String(value)); } catch { /* Storage may be unavailable. */ }
}
export const exactTokens = (value: number) => value.toLocaleString('en-US', { maximumFractionDigits: 0 });
export function compactTokens(value: number) {
  if (value < 1_000_000) return exactTokens(value);
  const divisor = value >= 1_000_000_000 ? 1_000_000_000 : 1_000_000;
  return String(Math.floor(value / (divisor / 100)) / 100) + (divisor === 1_000_000 ? 'M' : 'B');
}
export function legacyUsage(messages: Message[]): UsageCounts {
  const counts = { inputTokens: 0, outputTokens: 0, modelCalls: 0, inputReports: 0, outputReports: 0 };
  for (const message of messages) {
    if (message.role !== 'assistant') continue;
    counts.modelCalls++;
    const usage = message.usage;
    if (usage && !usage.inputMissing && usage.inputTokens !== undefined && usage.inputTokens >= 0) { counts.inputTokens += usage.inputTokens; counts.inputReports++; }
    if (usage && !usage.outputMissing && usage.outputTokens !== undefined && usage.outputTokens >= 0) { counts.outputTokens += usage.outputTokens; counts.outputReports++; }
  }
  return counts;
}
export function turnStatisticsText(group: ResponseGroup, run?: Run): string {
  if (!group.final || (run && run.status !== 'completed')) return '';
  const stats = run?.statistics, counts = stats || legacyUsage(group.messages), parts: string[] = [];
  if (stats?.completedAt) {
    const seconds = Math.max(0, Math.round((Date.parse(stats.completedAt) - Date.parse(stats.startedAt)) / 1000));
    if (Number.isFinite(seconds)) parts.push(seconds < 60 ? `${seconds}s` : seconds < 3600 ? `${Math.floor(seconds / 60)}m ${seconds % 60}s` : `${Math.floor(seconds / 3600)}h ${Math.floor(seconds % 3600 / 60)}m`);
  }
  if (counts.inputReports) parts.push(`${compactTokens(counts.inputTokens)} in${counts.inputReports < counts.modelCalls ? ' (partial)' : ''}`);
  if (counts.outputReports) parts.push(`${compactTokens(counts.outputTokens)} out${counts.outputReports < counts.modelCalls ? ' (partial)' : ''}`);
  const tools = stats?.toolCalls ?? group.messages.filter(m => m.role === 'tool').length;
  parts.push(`${exactTokens(tools)} tool ${tools === 1 ? 'call' : 'calls'}`);
  parts.push(`${exactTokens(counts.modelCalls)} model ${counts.modelCalls === 1 ? 'call' : 'calls'}`);
  if (!stats) parts.push('partial history');
  return parts.join(' · ');
}
export function turnStatisticsFooter(group: ResponseGroup, run?: Run) {
  if (!showTurnStatistics()) return null;
  const text = turnStatisticsText(group, run);
  if (!text) return null;
  const footer = document.createElement('div'); footer.className = 'turn-statistics';
  footer.setAttribute('aria-label', 'Turn statistics'); footer.textContent = text;
  footer.title = 'Entire agent run, including model requests for context summaries. Elapsed time includes tools and waiting. Token counts are provider-reported; unavailable counts are omitted.';
  return footer;
}

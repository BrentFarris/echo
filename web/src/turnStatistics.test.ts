import { afterEach, expect, it } from 'vitest';
import { compactTokens, setShowTurnStatistics, showTurnStatistics, turnStatisticsFooter, turnStatisticsText } from './turnStatistics';
import type { Message, Run } from './types';
import type { ResponseGroup } from './conversation';

const message = (id: string, role: string, usage?: Message['usage']): Message => ({ id, role, usage, runId: 'run', chatId: 'chat', author: 'Echo', status: 'completed', content: 'Done', createdAt: '' });
const final = message('final', 'assistant', { outputTokens: 10, inputMissing: true });
const group: ResponseGroup = { key: 'group', author: 'Echo', status: 'completed', messages: [message('work', 'assistant', { inputTokens: 100, outputTokens: 20 }), message('tool', 'tool'), final], final };
afterEach(() => localStorage.clear());

it('formats counts without rounding upward at M and B boundaries', () => {
  expect([0, 1000, 999999, 1000000, 1315134, 999999999, 1250000000].map(compactTokens)).toEqual(['0', '1,000', '999,999', '1M', '1.31M', '999.99M', '1.25B']);
});
it('summarizes full run statistics and distinguishes unknown usage', () => {
  const run: Run = { id: 'run', chatId: 'chat', botId: 'echo', status: 'completed', statistics: { startedAt: '2026-01-01T00:00:00Z', completedAt: '2026-01-01T00:00:24Z', inputTokens: 12340, outputTokens: 846, inputReports: 3, outputReports: 4, modelCalls: 4, toolCalls: 3 } };
  expect(turnStatisticsText(group, run)).toBe('24s · 12,340 in (partial) · 846 out · 3 tool calls · 4 model calls');
  run.statistics!.inputReports = 0;
  expect(turnStatisticsText(group, run)).not.toContain(' in');
  run.status = 'running'; expect(turnStatisticsText(group, run)).toBe('');
  expect(turnStatisticsText({ ...group, final: undefined })).toBe('');
});
it('uses only available historical metadata and labels it partial', () => {
  expect(turnStatisticsText(group)).toBe('100 in (partial) · 30 out · 1 tool call · 2 model calls · partial history');
});
it('defaults on and persists the display preference', () => {
  expect(showTurnStatistics()).toBe(true); expect(turnStatisticsFooter(group)).not.toBeNull();
  setShowTurnStatistics(false); expect(showTurnStatistics()).toBe(false); expect(turnStatisticsFooter(group)).toBeNull();
  setShowTurnStatistics(true); expect(turnStatisticsFooter(group)?.textContent).toContain('partial history');
});

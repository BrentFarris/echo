import { afterEach, expect, it } from 'vitest';
import { compactTokens, inputCacheText, legacyUsage, setShowTurnStatistics, showTurnStatistics, turnStatisticsFooter, turnStatisticsText } from './turnStatistics';
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
  expect(turnStatisticsText(group, run)).toBe('24s · 12,340 in (partial) (cache breakdown unavailable) · 846 out · 3 tool calls · 4 model calls');
  run.statistics!.inputReports = 0;
  expect(turnStatisticsText(group, run)).not.toContain(' in');
  run.status = 'running'; expect(turnStatisticsText(group, run)).toBe('');
  expect(turnStatisticsText({ ...group, final: undefined })).toBe('');
});
it('uses only available historical metadata and labels it partial', () => {
  expect(turnStatisticsText(group)).toBe('100 in (partial) (cache breakdown unavailable) · 30 out · 1 tool call · 2 model calls · partial history');
});
it('shows cached, uncached and unknown input without treating missing reports as misses', () => {
  const counts = legacyUsage([
    message('cached', 'assistant', { inputTokens: 100, cachedInputTokens: 60 }),
    message('miss', 'assistant', { inputTokens: 40, cachedInputTokens: 0 }),
    message('old', 'assistant', { inputTokens: 20 }),
    message('invalid', 'assistant', { inputTokens: 10, cachedInputTokens: 11 }),
    message('negative', 'assistant', { inputTokens: 5, cachedInputTokens: -1 }),
    message('no-input', 'assistant', { cachedInputTokens: 5 }),
  ]);
  expect(counts).toMatchObject({ inputTokens: 175, cachedInputTokens: 60, uncachedInputTokens: 80, cacheReports: 2 });
  expect(inputCacheText(counts)).toBe('60 cached, 80 uncached, 35 cache unknown');
  expect(inputCacheText(legacyUsage([message('zero', 'assistant', { inputTokens: 40, cachedInputTokens: 0 })]))).toBe('0 cached, 40 uncached');
  expect(inputCacheText({ ...counts, inputTokens: 140 })).toBe('60 cached, 80 uncached, partial cache data');
});
it('uses the run cache totals once instead of adding them to message usage', () => {
  const run: Run = { id: 'run', chatId: 'chat', botId: 'echo', status: 'completed', statistics: { startedAt: '', inputTokens: 1000, outputTokens: 30, modelCalls: 2, inputReports: 2, outputReports: 2, toolCalls: 1, cachedInputTokens: 700, uncachedInputTokens: 300, cacheReports: 2 } };
  expect(turnStatisticsText(group, run)).toBe('1,000 in (700 cached, 300 uncached) · 30 out · 1 tool call · 2 model calls');
});
it('defaults on and persists the display preference', () => {
  expect(showTurnStatistics()).toBe(true); expect(turnStatisticsFooter(group)).not.toBeNull();
  setShowTurnStatistics(false); expect(showTurnStatistics()).toBe(false); expect(turnStatisticsFooter(group)).toBeNull();
  setShowTurnStatistics(true); expect(turnStatisticsFooter(group)?.textContent).toContain('partial history');
});

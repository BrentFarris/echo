import { beforeEach, expect, it } from 'vitest';
import { combineState, instances } from './instances';
import { assertOnlineRecipients } from './offlineRecipients';
import type { Bot, State } from './types';

const local = 'a'.repeat(32), remote = 'b'.repeat(32);
const empty = (): State => ({ bots: [], chats: [], endpoints: [], plugins: [], appDock: [], schedules: [], occurrences: [], runs: [] });
const bots = [{ id: 'echo', handle: 'echo' }, { id: `${remote}:echo`, handle: 'linux.echo' }] as Bot[];
beforeEach(() => combineState(empty(), { node: { nodeId: local, name: 'desktop', epoch: '1', version: 1 }, enabled: true, peers: [], instances: [{ node: { nodeId: remote, name: 'linux', epoch: '2', version: 1 }, online: false, state: empty() }] }));

it('blocks mixed typed recipients and names the offline machine', () => {
  expect(() => assertOnlineRecipients('@echo @linux.echo hello', [], bots)).toThrow("Machine 'linux' is offline; @linux.echo is unavailable.");
  expect(() => assertOnlineRecipients('@echo hello', [], bots)).not.toThrow();
});
it('checks saved references using stable ownership after a rename', () => {
  expect(() => assertOnlineRecipients('hello', [{ type: 'bot', botId: `${remote}:echo`, text: '@old.echo' }], bots)).toThrow("Machine 'linux' is offline");
});
it('recognizes offline machines even without a cached bot roster', () => {
  expect(() => assertOnlineRecipients('@linux.newbot hello', [], [])).toThrow('offline');
});
it('ignores code examples and permits recipients after reconnection', () => {
  expect(() => assertOnlineRecipients('`@linux.echo`\n```\n@linux.echo\n```', [], bots)).not.toThrow();
  instances.get(remote)!.online = true;
  expect(() => assertOnlineRecipients('@linux.echo hello', [], bots)).not.toThrow();
});

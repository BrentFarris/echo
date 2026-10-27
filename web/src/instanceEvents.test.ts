import { describe, expect, it } from 'vitest';
import { InstanceEvents } from './instanceEvents';
import type { EchoEvent } from './types';

const event = (sequence: number, type = 'delta'): EchoEvent => ({ sequence, type, payload: {} });
const tick = () => new Promise(resolve => setTimeout(resolve, 0));
describe('instance stream recovery', () => {
  it('replays only deltas newer than a source snapshot without blocking other machines', async () => {
    let snapshot!: (sequence: number) => void;
    const delivered: string[] = [];
    const events = new InstanceEvents(() => new Promise(resolve => { snapshot = resolve; }), (node, e) => delivered.push(`${node}:${e.sequence}`), e => { throw e; });
    events.push('linux', event(0, 'reset'));
    events.push('linux', event(10)); events.push('linux', event(11));
    events.push('mac', event(3)); snapshot(10); await tick();
    events.push('linux', event(11)); events.push('linux', event(12));
    expect(delivered).toEqual(['mac:3', 'linux:11', 'linux:12']);
  });
  it('ignores old recovery promises after another reset or socket close', async () => {
    const snapshots: ((sequence: number) => void)[] = [], delivered: number[] = [];
    const events = new InstanceEvents(() => new Promise(resolve => snapshots.push(resolve)), (_, e) => delivered.push(e.sequence), e => { throw e; });
    events.push('linux', event(0, 'reset')); events.push('linux', event(80));
    events.push('linux', event(0, 'reset')); events.push('linux', event(2));
    snapshots[0](79); await tick(); expect(delivered).toEqual([]);
    snapshots[1](1); await tick(); expect(delivered).toEqual([2]);
    events.push('linux', event(0, 'reset')); events.push('linux', event(4));
    events.close(); snapshots[2](3); await tick(); expect(delivered).toEqual([2]);
  });
});

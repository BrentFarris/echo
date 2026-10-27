import { describe, expect, it } from 'vitest';
import { canUpdate, updateConfirmation, type Rollout } from './developmentRollouts';

const preview: Rollout = { id: 'r', sourceNodeId: 'local', scope: 'swarm', state: 'checked', target: '1234567890abcdef', cancelRequested: false, members: [
  { nodeId: 'local', name: 'desktop', status: 'succeeded', check: { current: true, revision: '1234567890abcdef' } },
  { nodeId: 'remote', name: 'linux', status: 'queued', check: { current: false, revision: 'old' } },
  { nodeId: 'offline', name: 'mac', status: 'skipped', error: 'offline', check: { current: false, revision: '' } },
] };
describe('coordinated update controls', () => {
  it('offers an update for a lagging peer even when the selected machine is current', () => {
    expect(canUpdate(preview)).toBe(true);
    expect(canUpdate({ ...preview, members: [preview.members[0]] })).toBe(false);
    expect(canUpdate({ ...preview, state: 'running' })).toBe(false);
  });
  it('confirms the exact revision, participants, exclusions and drain behavior', () => {
    const text = updateConfirmation(preview);
    for (const value of [preview.target, 'desktop: already current', 'linux: update and relaunch', 'mac: offline', 'wait for active work', 'explicit retry']) expect(text).toContain(value);
  });
});

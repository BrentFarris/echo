import { expect, it } from 'vitest';
import { presentationFor } from './sourceControlPresentation';
import { normalizeStatus } from './sourceControlTypes';

it('updates visible native groups immediately after Git staging and unstaging', () => {
  const initial = normalizeStatus({
    workspaceId: 'echoapp', repositoryId: 'repo', providerId: 'git', revision: 1,
    branch: 'main', detached: false, ahead: 0, behind: 0, totalChangeCount: 1, state: {},
    groups: [
      { id: 'conflicts', label: 'Merge Changes', role: 'conflicts', actions: ['stage'], changes: [] },
      { id: 'staged', label: 'Staged Changes', role: 'included', actions: ['unstage'], changes: [] },
      { id: 'unstaged', label: 'Changes', role: 'working', actions: ['stage'], changes: [
        { path: 'file.txt', groupId: 'unstaged', status: 'modified', statusCode: 'M' },
      ] },
    ],
  });
  const predict = presentationFor({id:'repo', providerId:'git', providerLabel:'Git', label:'repo', parent:false, scopes:[], revision:1, available:true, capabilities:['stage']}).predictStatus!;
  const staged = predict(initial, {requestId: 'stage', action: 'stage', paths: ['file.txt']}, {
    requestId: 'stage', repositoryId: 'repo', revision: 2, affectedPaths: ['file.txt'],
  });
  expect(staged.groups[1].changes.map(change => change.path)).toEqual(['file.txt']);
  expect(staged.groups[2].changes).toEqual([]);
  expect(staged.staged[0].groupId).toBe('staged');
  const unstaged = predict(staged, {requestId: 'unstage', action: 'unstage', paths: ['file.txt']}, {
    requestId: 'unstage', repositoryId: 'repo', revision: 3, affectedPaths: ['file.txt'],
  });
  expect(unstaged.groups[1].changes).toEqual([]);
  expect(unstaged.groups[2].changes.map(change => change.path)).toEqual(['file.txt']);
  expect(unstaged.unstaged[0].groupId).toBe('unstaged');
  expect(initial.groups[2].changes).toHaveLength(1);
});

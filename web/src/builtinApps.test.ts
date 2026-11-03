import { afterEach, expect, it } from 'vitest';
import { builtinApp } from './builtinApps';
import { instances } from './instances';
import type { Plugin } from './types';

afterEach(() => instances.clear());
it('requires the Notes capability independently of Monaco', () => {
  const nodeId = 'b'.repeat(32);
  const plugin = { id: `${nodeId}:notes`, nodeId, builtin: true } as Plugin;
  const node = { nodeId, name: 'linux', epoch: '', version: 1, capabilities: ['native-monaco/1'] };
  instances.set(nodeId, { node, online: true });
  expect(builtinApp(plugin, 'main')).toBeUndefined();
  node.capabilities = ['native-notes/1'];
  expect(builtinApp(plugin, 'main')).toBeTypeOf('function');
  expect(builtinApp({ ...plugin, id: `${nodeId}:monaco-editor` }, 'main')).toBeUndefined();
});
it('does not let an installed package claim the native Notes module', () => {
  expect(builtinApp({ id: 'notes', builtin: false } as Plugin, 'main')).toBeUndefined();
});

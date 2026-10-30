import type { NativeApp, NativeAppContext } from './pluginViews';
import type { Plugin } from './types';
import { localID, instances } from './instances';

// This allowlist belongs to the host bundle. Manifest fields can never opt an
// installed HTML package into native execution or grant access to the host DOM.
const modules: Record<string, () => Promise<{ mount(root: HTMLElement, context: NativeAppContext): Promise<NativeApp> }>> = {
  'monaco-editor/main': () => import('./code/app'),
  'notes/main': () => import('./notes/app'),
};
export function builtinApp(plugin: Plugin | undefined, viewId: string) {
  const capability = localID(plugin?.id || '') === 'notes' ? 'native-notes/1' : 'native-monaco/1';
  const compatible = !plugin?.nodeId || instances.get(plugin.nodeId)?.node.capabilities?.includes(capability);
  return plugin?.builtin && compatible ? modules[`${localID(plugin.id)}/${viewId}`] : undefined;
}

// Dock presentation is host-owned; installed manifests cannot claim this slot.
export function isBuiltinDock(plugin: Plugin | undefined, viewId: string): boolean {
  return !!plugin?.builtin && localID(plugin.id) === 'terminal' && viewId === 'main' &&
    (!plugin.nodeId || instances.get(plugin.nodeId)?.node.capabilities?.includes('native-terminal/1') === true);
}

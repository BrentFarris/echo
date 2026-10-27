import type { NativeApp, NativeAppContext } from './pluginViews';
import type { Plugin } from './types';
import { localID, instances } from './instances';

// This allowlist belongs to the host bundle. Manifest fields can never opt an
// installed HTML package into native execution or grant access to the host DOM.
const modules: Record<string, () => Promise<{ mount(root: HTMLElement, context: NativeAppContext): Promise<NativeApp> }>> = {
  'monaco-editor/main': () => import('./code/app'),
};
export function builtinApp(plugin: Plugin | undefined, viewId: string) {
  const compatible = !plugin?.nodeId || instances.get(plugin.nodeId)?.node.capabilities?.includes('native-monaco/1');
  return plugin?.builtin && compatible ? modules[`${localID(plugin.id)}/${viewId}`] : undefined;
}

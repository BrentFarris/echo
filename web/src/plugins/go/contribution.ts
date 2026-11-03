import { registerLanguageContribution } from '../../code/languageContribution';
import { GoLanguage } from '../../code/goLanguage';
import { api } from '../../code/api';
import { debugHoverExpression } from './debugExpressions';

registerLanguageContribution({
 pluginID: 'go', name: 'Go', languages: ['go'], debugTypes: ['go'], sourceSchemes: ['echo-go'],
 selector: [{ language: 'go', scheme: 'file' }, { scheme: 'file', pattern: '**/go.mod' }, { scheme: 'file', pattern: '**/go.work' }],
 acquire: GoLanguage.acquire, hoverExpression: debugHoverExpression,
 launchTemplate: JSON.stringify({ version: '0.2.0', configurations: [
  { name: 'Go: Launch package', type: 'go', request: 'launch', mode: 'auto', program: '${fileDirname}' },
  { name: 'Go: Test package', type: 'go', request: 'launch', mode: 'test', program: '${fileDirname}' },
  { name: 'Go: Attach', type: 'go', request: 'attach', mode: 'local', processId: '${command:pickGoProcess}' },
 ] }, null, 2) + '\n',
 debuggerStatus: (botID, directory) => api(`/api/v1/plugins/go/bots/${encodeURIComponent(botID)}/debug/status`, { query: directory ? { directory } : {} }),
 installDebugger: async (botID, directory) => { await api(`/api/v1/plugins/go/bots/${encodeURIComponent(botID)}/debug/install`, { method: 'POST', body: { directory } }); },
});

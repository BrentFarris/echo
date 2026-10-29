import { entryNode, resourceID, localID } from '../instances';
import type { NativeApp, NativeAppContext } from '../pluginViews';
import type { Bot, Plugin } from '../types';
import { CodeView } from './codeView';
import { toast } from './ui';
import '@vscode/codicons/dist/codicon.css';
import './code.css';
import './markdownPreview.css';
import './app.css';

export async function mount(root: HTMLElement, context: NativeAppContext): Promise<NativeApp> {
  root.classList.add('monaco-app');
  const toolbar = document.createElement('div'); toolbar.className = 'editor-bot-toolbar';
  const label = document.createElement('label'); label.textContent = 'Bot ';
  const select = document.createElement('select'); select.setAttribute('aria-label', 'Editor bot'); label.append(select);
  const configure = document.createElement('button'); configure.textContent = 'Configure Folders';
  const settings = document.createElement('button'); settings.textContent = 'Editor Settings';
  toolbar.append(label, configure, settings); root.append(toolbar);
  const sessions = new Map<string, { view: CodeView; element: HTMLElement; bot: Bot; ready: Promise<void> }>();
  const node = context.plugin.nodeId || entryNode, echoID = resourceID(node, 'echo'), preferenceKey = node && node !== entryNode ? `echo2:editor:${node}:bot` : 'echo2:editor:bot';
  let bots = context.bots, selected = context.botId, visible = true, disposed = false, generation = 0;
  let preferences = JSON.stringify(context.plugin.config);
  try { selected = localStorage.getItem(preferenceKey) || selected; } catch { /* Optional preference. */ }
  if (!bots.some(b => b.id === selected)) selected = bots.some(b => b.id === context.botId) ? context.botId : echoID;
  const renderBots = () => {
    select.replaceChildren(...[...bots].sort((a,b) => Number(localID(b.id) === 'echo') - Number(localID(a.id) === 'echo') || a.name.localeCompare(b.name)).map(bot => {
      const option = document.createElement('option'); option.value = bot.id; option.textContent = `${bot.name} (@${bot.handle})`; return option;
    })); select.value = selected;
  };
  const choose = async (id: string) => {
    const current = ++generation;
    const bot = bots.find(b => b.id === id); if (!bot || disposed) return;
    selected = id; select.value = id;
    try { localStorage.setItem(preferenceKey, id); } catch { /* Optional preference. */ }
    for (const session of sessions.values()) { session.element.hidden = true; session.view.setVisible(false); }
    let session = sessions.get(id);
    if (!session) {
      const element = document.createElement('div'); element.className = 'editor-bot-session'; element.dataset.botId = id; root.append(element);
      const view = new CodeView(element, bot, context);
      session = { element, view, bot, ready: view.start() }; sessions.set(id, session);
    }
    session.element.hidden = !visible;
    await session.ready;
    if (disposed || current !== generation) return;
    session.view.setVisible(visible);
    root.classList.toggle('editor-no-folders', !bot.roots.length);
  };
  select.onchange = () => { void choose(select.value).catch(error => toast(String(error))); };
  configure.onclick = () => context.configureBot(selected);
  settings.onclick = () => sessions.get(selected)?.view.showSettings();
  renderBots(); await choose(selected);
  return {
    async openFile(target) {
      await choose(target.botId);
      if (disposed || selected !== target.botId) return;
      await sessions.get(selected)?.view.openEntry(target.ref, target.kind);
    },
    update(nextBots: Bot[], plugin: Plugin) {
      bots = nextBots; renderBots();
      const changed = preferences !== JSON.stringify(plugin.config); preferences = JSON.stringify(plugin.config);
      for (const [id,session] of sessions) {
        const bot = bots.find(b => b.id === id);
        if (!bot) { session.view.dispose(); session.element.remove(); sessions.delete(id); continue; }
        if (changed) void session.ready.then(() => { if (!disposed) session.view.applyPreferences(plugin.config); });
        if (JSON.stringify(bot.roots) !== JSON.stringify(session.bot.roots)) { session.bot = bot; void session.ready.then(() => session.view.updateBot(bot)).catch(error => toast(String(error))); }
      }
      if (!bots.some(b => b.id === selected)) void choose(echoID);
      root.classList.toggle('editor-no-folders', !bots.find(b => b.id === selected)?.roots.length);
    },
    visible(value) { visible = value; for (const [id,s] of sessions) { s.element.hidden = !visible || id !== selected; void s.ready.then(() => { if (!disposed) s.view.setVisible(visible && id === selected); }); } },
    async beforeClose() {
      for (const [id,s] of sessions) { await choose(id); if (!await s.view.beforeClose()) return false; }
      return true;
    },
    dispose() { disposed = true; ++generation; for (const s of sessions.values()) s.view.dispose(); root.replaceChildren(); },
  };
}

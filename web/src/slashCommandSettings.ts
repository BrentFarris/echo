import { escapeHTML as esc } from './helpers';
import { localID } from './instances';
import type { AppAPI } from './pluginViews';
import type { AppFeature, Bot, SlashAction, SlashCommand } from './types';

type CatalogPlugin = { id: string; name: string; unavailable?: string; tools: { name: string; description: string; parameters: Record<string, unknown>; unavailable?: string }[]; views: { id: string; title: string; features?: AppFeature[] }[] };
const changes = new EventTarget();
export function notifySlashCommandsChanged() { changes.dispatchEvent(new Event('change')); }

export function mountSlashCommandSettings(root: HTMLElement, api: AppAPI, bots: Bot[], changed: () => Promise<void>, supported: boolean) {
  root.innerHTML = '<h4>Slash commands</h4><p class="subtle">Saved on this machine and shared across browsers. In chat, commands follow the selected bot’s machine. Click a suggestion or press Enter to invoke; Space keeps literal text.</p>';
  if (!supported) { root.insertAdjacentHTML('beforeend', '<p role="status">Update this machine to manage slash commands.</p>'); return; }
  root.insertAdjacentHTML('beforeend', '<button type="button" data-add-command>＋ Add command</button><p role="status" data-command-status></p><div data-command-list></div><div data-command-editor></div>');
  const q = <T extends HTMLElement>(selector: string) => root.querySelector<T>(selector)!;
  const status = q<HTMLElement>('[data-command-status]');
  let commands: SlashCommand[] = [], catalog: CatalogPlugin[] = [], loading = false, reload = false, disposed = false;
  const fail = (error: unknown) => { status.textContent = error instanceof Error ? error.message : String(error); };
  const render = () => {
    const list = q<HTMLElement>('[data-command-list]');
    list.innerHTML = commands.map(command => `<article class="slash-command-card" data-command="${esc(command.id)}"><div><strong>/${esc(command.name)}</strong> <small>${command.builtin ? 'Built-in' : 'Custom'}${command.enabled ? '' : ' · Removed'}</small><p>${esc(command.description)}</p><details><summary>Action details</summary><pre>${esc(command.action.type === 'text' ? command.action.text : JSON.stringify(command.action, null, 2))}</pre></details></div><div class="button-row">${!command.builtin ? '<button type="button" data-edit-command>Edit</button>' : ''}<button type="button" data-remove-command>${!command.enabled ? 'Restore' : command.builtin ? 'Remove' : 'Delete'}</button></div></article>`).join('');
    for (const card of list.querySelectorAll<HTMLElement>('[data-command]')) {
      const command = commands.find(c => c.id === card.dataset.command)!;
      card.querySelector<HTMLButtonElement>('[data-edit-command]')?.addEventListener('click', () => edit(command));
      card.querySelector<HTMLButtonElement>('[data-remove-command]')!.onclick = async event => {
        const button = event.currentTarget as HTMLButtonElement; button.disabled = true;
        try {
          await api(`/slash-commands/${encodeURIComponent(command.id)}${command.enabled ? '' : '/restore'}`, command.enabled ? 'DELETE' : 'POST');
          status.textContent = ''; await changed(); await load();
        } catch (error) { fail(error); button.disabled = false; }
      };
    }
  };
  const load = async () => {
    if (disposed) return;
    if (loading) { reload = true; return; }
    loading = true;
    try {
      const values = await Promise.all([api<SlashCommand[]>('/slash-commands'), api<CatalogPlugin[]>('/slash-commands/catalog')]);
      if (!disposed) { [commands, catalog] = values; render(); }
    } catch (error) { if (!disposed) fail(error); }
    finally { loading = false; if (reload) { reload = false; void load(); } }
  };
  function edit(command?: SlashCommand) {
    const host = q<HTMLElement>('[data-command-editor]');
    host.innerHTML = `<form class="form slash-command-form"><h4>${command ? 'Edit command' : 'New command'}</h4><label>Command name<input name="name" aria-label="Command name" required pattern="[A-Za-z][A-Za-z0-9_\\-]{0,63}" maxlength="64" placeholder="review" value="${esc(command?.name || '')}"><small>Enter the name without /.</small></label><label>Description<input name="description" maxlength="1000" value="${esc(command?.description || '')}"></label><label>Action<select name="type"><option value="text">Insert text</option><option value="plugin">Plugin action</option><option value="app">Open Echo app</option></select></label><div data-action-fields></div><p data-editor-error role="alert"></p><div class="button-row"><button class="primary" type="submit">Save command</button><button type="button" data-cancel-command>Cancel</button></div></form>`;
    const form = host.querySelector('form')!;
    const field = <T extends HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(name: string) => form.elements.namedItem(name) as T;
    field('type').value = command?.action.type || 'text';
    const actionFields = form.querySelector<HTMLElement>('[data-action-fields]')!;
    const renderAction = (initial?: SlashAction) => {
      const type = field('type').value;
      if (type === 'text') {
        actionFields.innerHTML = `<label>Position<select name="position"><option value="insert">At command location</option><option value="append">End of prompt</option><option value="prepend">Beginning of prompt</option></select></label><label>Text<textarea name="text" rows="5" required></textarea></label>`;
        if (initial?.type === 'text') { field('position').value = initial.position; field('text').value = initial.text; }
        return;
      }
      const available = catalog.filter(p => type === 'plugin' ? p.tools.length : p.views.length);
      actionFields.innerHTML = `<label>${type === 'plugin' ? 'Plugin' : 'App'}<select name="pluginId" required>${available.map(p => `<option value="${esc(p.id)}">${esc(p.name)}${p.unavailable ? ' · ' + esc(p.unavailable) : ''}</option>`).join('')}</select></label><label>Bot<select name="botId"><option value="">Selected chat bot</option>${bots.map(bot => `<option value="${esc(localID(bot.id))}">${esc(bot.name)} (@${esc(bot.handle)})</option>`).join('')}</select></label><div data-destination-fields></div>`;
      if (initial && initial.type !== 'text') { field('pluginId').value = initial.pluginId; field('botId').value = initial.botId || ''; }
      const destination = form.querySelector<HTMLElement>('[data-destination-fields]')!;
      const renderDestination = (initial?: SlashAction) => {
        const plugin = available.find(p => p.id === field('pluginId').value);
        if (!plugin) { destination.innerHTML = '<p>No compatible plugins/apps are installed.</p>'; return; }
        if (type === 'plugin') {
          destination.innerHTML = `<label>Plugin action<select name="tool" required>${plugin.tools.map(tool => `<option value="${esc(tool.name)}" ${tool.unavailable ? 'disabled' : ''}>${esc(tool.name)}${tool.unavailable ? ' · ' + esc(tool.unavailable) : ''}</option>`).join('')}</select></label><p data-tool-description></p><details><summary>Argument schema</summary><pre data-tool-schema></pre></details><label>Arguments (JSON object)<textarea name="arguments" rows="6" spellcheck="false" required>{}</textarea></label>`;
          if (initial?.type === 'plugin') { field('tool').value = initial.tool; field('arguments').value = JSON.stringify(initial.arguments, null, 2); }
          const describe = () => {
            const tool = plugin.tools.find(tool => tool.name === field('tool').value);
            destination.querySelector('[data-tool-description]')!.textContent = tool?.description || 'No standalone actions available.';
            destination.querySelector('[data-tool-schema]')!.textContent = JSON.stringify(tool?.parameters || {}, null, 2);
          };
          field('tool').onchange = () => { field('arguments').value = '{}'; describe(); }; describe();
        } else {
          destination.innerHTML = `<label>View<select name="viewId" required>${plugin.views.map(view => `<option value="${esc(view.id)}">${esc(view.title || view.id)}</option>`).join('')}</select></label><label>Feature<select name="feature"></select></label>`;
          if (initial?.type === 'app') field('viewId').value = initial.viewId;
          const features = () => {
            const view = plugin.views.find(view => view.id === field('viewId').value);
            field<HTMLSelectElement>('feature').innerHTML = '<option value="">Default view</option>' + (view?.features || []).map(f => `<option value="${esc(f.id)}">${esc(f.title)}</option>`).join('');
          };
          field('viewId').onchange = features; features();
          if (initial?.type === 'app') field('feature').value = initial.feature || '';
        }
      };
      field('pluginId').onchange = () => renderDestination(); renderDestination(initial);
    };
    field('type').onchange = () => renderAction(); renderAction(command?.action);
    form.querySelector<HTMLButtonElement>('[data-cancel-command]')!.onclick = () => host.replaceChildren();
    form.onsubmit = async event => {
      event.preventDefault();
      const error = form.querySelector<HTMLElement>('[data-editor-error]')!, save = form.querySelector<HTMLButtonElement>('[type=submit]')!;
      error.textContent = ''; save.disabled = true;
      try {
        let action: SlashAction;
        const type = field('type').value;
        if (type === 'text') action = { type, text: field('text').value, position: field('position').value as 'insert' | 'append' | 'prepend' };
        else {
          const pluginId = field('pluginId').value, botId = field('botId').value || undefined;
          if (type === 'plugin') {
            const args = JSON.parse(field('arguments')?.value || '{}');
            if (!args || typeof args !== 'object' || Array.isArray(args)) throw new Error('Arguments must be a JSON object.');
            action = { type, pluginId, botId, tool: field('tool')?.value || '', arguments: args };
          } else action = { type: 'app', pluginId, botId, viewId: field('viewId')?.value || '', feature: field('feature')?.value || '' };
        }
        await api(command ? `/slash-commands/${encodeURIComponent(command.id)}` : '/slash-commands', command ? 'PUT' : 'POST', { name: field('name').value, description: field('description').value, action });
        if (!disposed) { host.replaceChildren(); status.textContent = 'Command saved.'; await changed(); await load(); }
      } catch (value) { error.textContent = value instanceof Error ? value.message : String(value); }
      finally { save.disabled = false; }
    };
    field('name').focus();
  }
  q<HTMLButtonElement>('[data-add-command]').onclick = () => edit();
  const update = () => { void load(); };
  changes.addEventListener('change', update);
  const observer = new MutationObserver(() => { if (!root.isConnected) { disposed = true; changes.removeEventListener('change', update); observer.disconnect(); } });
  observer.observe(document.body, { childList: true, subtree: true });
  void load();
}

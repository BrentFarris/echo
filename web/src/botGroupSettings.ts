import { escapeHTML as esc } from './helpers';
import { InstanceClient, entryNode, instanceOnline, localID, machineName, resourceID } from './instances';
import { memberHandle } from './recipientMentions';
import type { Bot, BotGroup, BotGroupMember } from './types';

type Dialog = (title: string, body: string) => HTMLDialogElement;

export function mountBotGroups(root: HTMLElement, owner: string, currentBots: () => Bot[], groups: BotGroup[], dialog: Dialog, changed: () => Promise<void>, report: (error: unknown) => void) {
  const client = new InstanceClient(owner);
  const draw = () => {
    const bots = currentBots();
    root.innerHTML = '<h3>Groups</h3><button type="button" class="primary" data-add-group>＋ Add group</button>';
    root.querySelector('button')!.addEventListener('click', () => edit());
    for (const group of groups) {
      const row = document.createElement('div'); row.className = 'list-card';
      row.innerHTML = `<div class="grow"><strong>@${esc(group.handle)}</strong><p class="subtle">${group.members.map(m => '@' + esc(memberHandle(m, bots))).join(', ')}</p></div><button>Edit</button><button class="danger">Delete</button>`;
      const [modify, remove] = row.querySelectorAll('button');
      modify.onclick = () => edit(group);
      remove.onclick = async () => {
        remove.disabled = true;
        try {
          await client.json('/bot-groups/' + encodeURIComponent(group.id), 'DELETE');
          groups = groups.filter(g => g.id !== group.id); draw(); await changed();
        } catch (error) { remove.disabled = false; report(error); }
      };
      root.append(row);
    }
  };
  const edit = (group?: BotGroup) => {
    const bots = currentBots();
    const selected = new Set(group?.members.map(m => resourceID(m.nodeId, m.botId)));
    const choices = new Map<string, BotGroupMember>();
    for (const bot of bots) choices.set(bot.id, { nodeId: bot.nodeId || entryNode, botId: localID(bot.id), handle: bot.handle });
    for (const m of group?.members || []) {
      const id = resourceID(m.nodeId, m.botId);
      if (!choices.has(id)) choices.set(id, m);
    }
    const options = [...choices].map(([id, m]) => {
      const unavailable = !bots.some(b => b.id === id) || !instanceOnline(m.nodeId);
      return `<label class="check"><input type="checkbox" value="${esc(id)}" ${selected.has(id) ? 'checked' : ''} ${unavailable && !selected.has(id) ? 'disabled' : ''}> @${esc(memberHandle(m, bots))} · ${esc(machineName(m.nodeId))}${unavailable ? ' · Unavailable' : ''}</label>`;
    }).join('');
    const editor = dialog(group ? 'Edit group' : 'Add group', `<form class="form"><label>Group handle<input name="handle" required maxlength="48" pattern="[A-Za-z][A-Za-z0-9_\\-]*" placeholder="modbots"></label><fieldset><legend>Members</legend>${options}</fieldset><p class="subtle" role="alert" hidden></p><button class="primary" type="submit">Save group</button></form>`);
    const form = editor.querySelector('form')!;
    const handle = form.elements.namedItem('handle') as HTMLInputElement;
    handle.value = group?.handle.split('.').at(-1) || ''; handle.focus();
    form.onsubmit = async event => {
      event.preventDefault();
      const save = form.querySelector('button')!, error = form.querySelector<HTMLElement>('[role="alert"]')!;
      const checked = new Set([...form.querySelectorAll<HTMLInputElement>('input:checked')].map(input => input.value));
      const members = [...new Set([...selected, ...checked])].filter(id => checked.has(id)).map(id => choices.get(id)!);
      save.disabled = true;
      try {
        const saved = await client.json<BotGroup>(group ? '/bot-groups/' + encodeURIComponent(group.id) : '/bot-groups', group ? 'PUT' : 'POST', { handle: handle.value.trim(), members });
        const displayHandle = owner === entryNode ? saved.handle : machineName(owner) + '.' + saved.handle;
        groups = [...groups.filter(g => g.id !== saved.id), { ...saved, handle: displayHandle }];
        editor.close(); draw(); await changed();
      } catch (reason) {
        error.textContent = reason instanceof Error ? reason.message : String(reason); error.hidden = false;
      } finally { save.disabled = false; }
    };
  };
  draw();
}

import { entryNode, instances, InstanceClient, type Peer } from './instances';
import { escapeHTML as esc } from './helpers';

export const accessLabels = {
  runBots: 'Run shared bots', readFiles: 'Read files directly', writeFiles: 'Change files directly', execute: 'Run scoped commands directly',
  manageBots: 'Manage bots, groups, and folders', managePlugins: 'Manage plugins and slash commands', manageModels: 'Manage models and credentials',
  manageSchedules: 'Manage and run schedules', changeChats: 'Change chats and stop work', manageMachine: 'Manage machine settings',
} as const;
export type Permission = keyof typeof accessLabels;
export type Policy = { permissions?: Partial<Record<Permission, boolean>> | null; bots?: Record<string, boolean>; plugins?: Record<string, boolean> };
export type EffectiveAccess = Policy & { full: boolean; owner: boolean };
type PolicyResponse = { policy: Policy; effective: EffectiveAccess };
type Invite = { id: string; expiresAt: string; redeemedBy?: string; revoked?: boolean };

export function swarmCan(node: string, permission: Permission | 'full'): boolean {
  if (!node || node === entryNode) return true;
  const access = instances.get(node)?.state?.swarmAccess;
  return permission === 'full' ? access?.full !== false : access?.permissions?.[permission] !== false;
}

function policyEditor(saved: Policy, defaults: Policy, inherit: boolean) {
  const root = document.createElement('div'); root.className = 'swarm-policy';
  const state = instances.get(entryNode)?.state;
  const resources = (['bots', 'plugins'] as const).map(kind => {
    const items = state?.[kind] || [];
    return `<details><summary>Exclude individual ${kind}</summary>${items.map(item => `<label class="check"><input type="checkbox" data-kind="${kind}" value="${esc(item.id)}" ${saved[kind]?.[item.id] === false ? 'checked' : ''}>${esc(item.name)}${item.swarmAvailable === false ? ' · not shared by owner' : ''}</label>`).join('')}</details>`;
  }).join('');
  root.innerHTML = `<p><strong data-summary></strong></p><div class="button-row"><button type="button" data-preset="inherit">${inherit ? 'Use machine defaults' : 'Allow all'}</button><button type="button" data-preset="full" ${inherit ? '' : 'hidden'}>Allow all permissions</button><button type="button" data-preset="coworker">Use coworker settings</button></div><details><summary>Permissions</summary>${Object.entries(accessLabels).map(([key, label]) => `<label>${label}<select data-permission="${key}">${inherit ? `<option value="inherit">Inherit (${defaults.permissions?.[key as Permission] === false ? 'deny' : 'allow'})</option>` : ''}<option value="allow">Allow</option><option value="deny">Deny</option></select></label>`).join('')}</details>${inherit ? resources : ''}`;
  const selects = Array.from(root.querySelectorAll<HTMLSelectElement>('select'));
  function value(): Policy {
    const permissions: Partial<Record<Permission, boolean>> = {}, bots: Record<string, boolean> = {}, plugins: Record<string, boolean> = {};
    for (const select of selects) if (select.value !== 'inherit') permissions[select.dataset.permission as Permission] = select.value === 'allow';
    for (const input of root.querySelectorAll<HTMLInputElement>('input:checked')) (input.dataset.kind === 'bots' ? bots : plugins)[input.value] = false;
    return { permissions, bots, plugins };
  }
  function summarize() {
    const p = value(), combined = { ...defaults.permissions, ...p.permissions };
    const limited = Object.values(combined).includes(false) || [p.bots, p.plugins, defaults.bots, defaults.plugins].some(group => Object.values(group || {}).includes(false)) || [...(state?.bots || []), ...(state?.plugins || [])].some(item => item.swarmAvailable === false);
    root.querySelector<HTMLElement>('[data-summary]')!.textContent = limited ? 'Custom access' : 'Full access';
  }
  for (const select of selects) { const v = saved.permissions?.[select.dataset.permission as Permission]; select.value = v === undefined && inherit ? 'inherit' : v === false ? 'deny' : 'allow'; }
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-preset]')) button.onclick = () => {
    for (const select of selects) select.value = button.dataset.preset === 'coworker' ? select.dataset.permission === 'runBots' ? 'allow' : 'deny' : button.dataset.preset === 'inherit' && inherit ? 'inherit' : 'allow';
    if (button.dataset.preset !== 'coworker') for (const box of root.querySelectorAll<HTMLInputElement>('input[type=checkbox]')) box.checked = false;
    summarize();
  };
  root.onchange = summarize; summarize();
  return { root, value };
}

export async function mountSwarmAccess(root: HTMLElement, client: InstanceClient, peers: Peer[], changed: () => Promise<void>, notice: (v: unknown, error?: boolean) => void, paired?: () => Promise<void>) {
  if (client.nodeId && client.nodeId !== entryNode) { root.textContent = 'Open Echo on this machine to change what it shares.'; return; }
  const defaults = await client.json<PolicyResponse>('/swarm/access'); if (!root.isConnected) return;
  root.innerHTML = `<hr><h3>Access to this machine</h3><p>Permissions apply to the connected computer. Shared bots keep their configured tools and folders. Blocking bot management also prevents a shared bot from changing those folders. Owner terminals require Full access. To share a specific CLI with peers, pin its connection in the bot’s settings and enable “Allow peers to run this bot’s CLI without Full access”.</p><p>Existing chat history stays visible to paired machines.</p><label>Edit permissions for<select data-target><option value="">Machine defaults</option>${peers.filter(p => p.authorized && !p.blocked).map(p => `<option value="${esc(p.node.nodeId)}">${esc(p.node.name)} · ${esc(p.node.nodeId.slice(0, 8))}</option>`).join('')}</select></label><form class="form" data-access-form><div data-editor></div><button class="primary">Save access</button></form><hr><h3>One-time invitations</h3><p>Pair a coworker without sharing your owner password. Codes expire after 15 minutes. Each owner chooses what the other computer may access.</p><details><summary>Create an invitation</summary><form class="form" data-issue><div data-issue-policy></div><button class="primary">Create invitation</button><label hidden data-code-label>Pairing code<input readonly data-code autocomplete="off"></label></form></details><details><summary>Pair with an invitation</summary><form class="form" data-redeem><label>Remote Echo address<input required type="url" name="url"></label><label>Pairing code<input required name="token" autocomplete="off" spellcheck="false"></label><p>Access the other computer will have to this machine:</p><div data-return-policy></div><button class="primary">Pair computers</button></form></details><div data-issued></div>`;
  const target = root.querySelector<HTMLSelectElement>('[data-target]')!;
  let revision = 0, editor = policyEditor(defaults.policy, {}, false);
  const showPolicy = async () => {
    const id = target.value, generation = ++revision;
    const result = id ? await client.json<PolicyResponse>(`/swarm/peers/${id}/access`) : defaults;
    if (generation !== revision || !root.isConnected) return;
    editor = policyEditor(result.policy, id ? defaults.policy : {}, !!id); root.querySelector('[data-editor]')!.replaceChildren(editor.root);
  };
  target.onchange = () => { void showPolicy().catch(e => notice(e, true)); }; await showPolicy();
  const submit = (selector: string, fn: (form: HTMLFormElement) => Promise<void>) => {
    const form = root.querySelector<HTMLFormElement>(selector)!;
    form.onsubmit = async e => { e.preventDefault(); const button = form.querySelector<HTMLButtonElement>('button.primary')!; button.disabled = true; try { await fn(form); } catch (err) { notice(err, true); } finally { button.disabled = false; } };
  };
  submit('[data-access-form]', async () => {
    const id = target.value; const saved = await client.json<PolicyResponse>(id ? `/swarm/peers/${id}/access` : '/swarm/access', 'PUT', editor.value());
    if (!id) Object.assign(defaults, saved); await changed(); await showPolicy(); notice('Swarm access saved');
  });
  const issue = policyEditor({}, defaults.policy, true), reciprocal = policyEditor({}, defaults.policy, true);
  root.querySelector('[data-issue-policy]')!.append(issue.root); root.querySelector('[data-return-policy]')!.append(reciprocal.root);
  const showInvites = async () => {
    const invites = await client.json<Invite[]>('/swarm/invitations'); const list = root.querySelector<HTMLElement>('[data-issued]')!;
    list.innerHTML = invites.filter(i => !i.revoked && !i.redeemedBy && Date.parse(i.expiresAt) > Date.now()).map(i => `<p>Invitation expires ${esc(new Date(i.expiresAt).toLocaleTimeString())} <button type="button" data-revoke-invite="${esc(i.id)}">Revoke invitation</button></p>`).join('');
    for (const button of list.querySelectorAll<HTMLButtonElement>('button')) button.onclick = async () => { try { await client.json(`/swarm/invitations/${button.dataset.revokeInvite}`, 'DELETE'); await showInvites(); } catch (err) { notice(err, true); } };
  };
  submit('[data-issue]', async form => {
    const result = await client.json<{ token: string }>('/swarm/invitations', 'POST', { policy: issue.value() });
    form.querySelector<HTMLElement>('[data-code-label]')!.hidden = false; const code = form.querySelector<HTMLInputElement>('[data-code]')!; code.value = result.token; code.select(); await showInvites();
  });
  submit('[data-redeem]', async form => { const data = new FormData(form); await client.json('/swarm/invitations/redeem', 'POST', { url: data.get('url'), token: String(data.get('token')).trim(), policy: reciprocal.value() }); form.reset(); await changed(); await paired?.(); notice('Computers paired'); });
  await showInvites();
}

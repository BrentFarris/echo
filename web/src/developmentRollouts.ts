import { InstanceClient, entryNode } from './instances';
import { escapeHTML as esc } from './helpers';

export interface RolloutMember {
  nodeId: string; name: string; status: string; error?: string;
  check: { revision: string; current: boolean; error?: string };
  update?: { status: string; stage: string; revision: string; error?: string; logPath: string; logTail: string; checkoutUpdated: boolean };
}
export interface Rollout {
  id: string; sourceNodeId: string; scope: 'swarm' | 'machine'; target: string;
  state: string; cancelRequested: boolean; members: RolloutMember[];
}
export function updateConfirmation(r: Rollout) {
  return `Update to ${r.target}?\n\n${r.members.map(m => `${m.name}: ${m.error || (m.check.current ? 'already current' : 'update and relaunch')}`).join('\n')}\n\nEach machine will wait for active work to finish before restarting. New work is temporarily paused during its restart. Failed or offline machines will require an explicit retry.`;
}
export const canUpdate = (r?: Rollout) => r?.state === 'checked' && r.members.some(m => m.status === 'queued');

// Always use the entry client for orchestration, regardless of the selected
// settings machine. Its grants define the complete scope of this action.
export async function mountRollouts(root: HTMLElement, source: InstanceClient, busyChanged: (busy: boolean) => void, replaced: () => Promise<void>) {
  const entry = new InstanceClient(entryNode);
  let scope: 'swarm' | 'machine' = 'machine', current: Rollout | undefined;
  let busy = false, message = '', unavailable = '', entryEpoch = '', poll = false;
  let refreshDone = '';
  root.innerHTML = `<label>Update scope<select data-update-scope><option value="swarm">Connected swarm</option><option value="machine">This machine only</option></select></label>
    <div class="button-row"><button data-rollout-check>Check for Updates</button><button data-rollout-start hidden>Update &amp; Relaunch</button></div>
    <p role="status" aria-live="polite" data-rollout-status></p><div data-rollout-members></div>
    <div class="button-row"><button data-rollout-retry hidden>Retry unsuccessful machines</button><button data-rollout-cancel hidden>Cancel pending updates</button></div>`;
  const q = <T extends HTMLElement>(selector: string) => root.querySelector<T>(selector)!;
  const render = () => {
    if (!root.isConnected) return;
    const opened = new Set([...root.querySelectorAll<HTMLDetailsElement>('details[open][data-update-node]')].map(d => d.dataset.updateNode));
    const running = current?.state === 'running';
    busyChanged(busy || !!running);
    q<HTMLSelectElement>('[data-update-scope]').value = scope;
    q<HTMLSelectElement>('[data-update-scope]').disabled = busy || !!running;
    q<HTMLButtonElement>('[data-rollout-check]').disabled = busy || !!running || !!unavailable;
    q<HTMLButtonElement>('[data-rollout-start]').hidden = !canUpdate(current);
    q<HTMLButtonElement>('[data-rollout-start]').disabled = busy || !!unavailable;
    q<HTMLButtonElement>('[data-rollout-retry]').hidden = !current || !['partial', 'cancelled'].includes(current.state);
    q<HTMLButtonElement>('[data-rollout-retry]').disabled = busy;
    q<HTMLButtonElement>('[data-rollout-cancel]').hidden = !running;
    q<HTMLButtonElement>('[data-rollout-cancel]').disabled = busy || !!current?.cancelRequested;
    q('[data-rollout-status]').textContent = message || unavailable || (current ? `${current.state === 'checked' ? canUpdate(current) ? 'Update available' : 'Check complete' : current.state === 'running' ? current.cancelRequested ? 'Cancelling pending updates…' : 'Updating machines…' : `Update ${current.state}`} · Target ${current.target.slice(0, 12)}` : 'Check the selected machine’s upstream and connected peers.');
    q('[data-rollout-members]').innerHTML = current?.members.map(m => `<div class="list-card"><div class="grow"><strong>${esc(m.name)}</strong><span class="machine-chip">${esc(m.status)}</span><p>${esc(m.error || m.update?.stage || (m.check.current ? 'Already running the target revision' : 'Ready for update'))}</p><small>Running: ${esc(m.update?.revision || m.check.revision || 'unknown')}</small>${m.update?.checkoutUpdated ? '<p class="subtle">The checkout was fast-forwarded.</p>' : ''}${m.update?.logPath ? `<details data-update-node="${esc(m.nodeId)}" ${opened.has(m.nodeId) ? 'open' : ''}><summary>Update log — ${esc(m.name)}</summary><p>${esc(m.update.logPath)}</p><pre>${esc(m.update.logTail)}</pre></details>` : ''}</div></div>`).join('') || '';
  };
  const monitor = async () => {
    if (poll) return; poll = true;
    try {
      while (root.isConnected && current?.state === 'running') {
        await new Promise(resolve => setTimeout(resolve, 1000));
        try { current = await entry.json<Rollout>(`/development/rollouts/${current.id}`); message = ''; }
        catch { message = 'Connection lost; the server continues the update. Waiting to reconnect…'; }
        render();
      }
      if (root.isConnected && current && current.state !== 'checked' && current.state !== 'running' && refreshDone !== current.id) {
        refreshDone = current.id;
        const own = current.members.find(m => m.nodeId === entryNode);
        if (own?.update?.status === 'succeeded' && entryEpoch) {
          const health = await entry.json<{ instanceId: string; revision: string }>('/health');
          if (health.instanceId !== entryEpoch && health.revision === current.target) { window.location.reload(); return; }
        }
        await replaced();
      }
    } catch (error) { message = String(error); render(); }
    finally { poll = false; }
  };
  const action = async (run: () => Promise<Rollout>) => {
    busy = true; message = 'Working…'; render();
    try { current = await run(); message = ''; }
    catch (error) { message = error instanceof Error ? error.message : String(error); }
    finally { busy = false; render(); void monitor(); }
  };
  q<HTMLSelectElement>('[data-update-scope]').onchange = event => { scope = (event.target as HTMLSelectElement).value as typeof scope; current = undefined; message = ''; render(); };
  q('[data-rollout-check]').onclick = () => void action(() => entry.json('/development/rollouts/check', 'POST', { sourceNodeId: source.nodeId || entryNode, scope }));
  q('[data-rollout-start]').onclick = () => { if (current && window.confirm(updateConfirmation(current))) void action(() => entry.json('/development/rollouts', 'POST', { checkId: current!.id })); };
  q('[data-rollout-retry]').onclick = () => { if (current && window.confirm(`Retry unsuccessful machines at the same target ${current.target}? Machines will wait until idle.`)) { refreshDone = ''; void action(() => entry.json(`/development/rollouts/${current!.id}/retry`, 'POST', {})); } };
  q('[data-rollout-cancel]').onclick = () => { if (current) void action(() => entry.json(`/development/rollouts/${current!.id}/cancel`, 'POST', {})); };
  try {
    const directory = await entry.json<{ enabled: boolean; node: { epoch: string } }>('/swarm/peers');
    scope = directory.enabled ? 'swarm' : 'machine'; entryEpoch = directory.node.epoch;
    const saved = await entry.json<Rollout[]>('/development/rollouts');
    current = saved.find(r => r.sourceNodeId === (source.nodeId || entryNode));
    if (current) scope = current.scope;
  } catch (error) { message = String(error); }
  render(); void monitor();
  return { setUnavailable(reason: string) { if (unavailable !== reason) { unavailable = reason; render(); } } };
}

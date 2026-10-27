import { InstanceClient, type Instance, type Peer } from './instances';
import { escapeHTML as esc } from './helpers';

export async function mountSwarm(root: HTMLElement, client: InstanceClient, changed: () => Promise<void>, notice: (value: unknown, error?: boolean) => void) {
  root.innerHTML = `<hr><h3>Connected machines</h3><p>Connect using the remote Echo address and owner password. Both machines need this version of Echo. Each bot uses the models, tools, and folders on its own machine.</p><form class="form" data-connect><label>Remote Echo address<input name="url" type="url" required placeholder="http://linux:8080" autocomplete="url"></label><label>Machine name<input name="name" pattern="[a-z][a-z0-9_-]{0,47}" placeholder="linux"></label><label>Remote owner password<input name="password" type="password" required autocomplete="off"></label><button class="primary">Connect machine</button><p role="status" data-status></p></form><div data-peers></div>`;
  const list = root.querySelector<HTMLElement>('[data-peers]')!;
  const reload = async () => {
    const state = await client.json<{ node: Instance; peers: Peer[] }>('/swarm/peers');
    if (!root.isConnected) return;
    list.innerHTML = state.peers.map(p => `<div class="list-card"><div class="grow"><strong>${esc(p.node.name)}</strong><small class="machine-chip">${esc(p.node.nodeId.slice(0, 8))}</small><p>${p.blocked ? 'Revoked' : p.disconnected ? 'Disconnected' : p.authorized ? p.connected ? 'Connected' : 'Waiting for connection' : 'Discovered · authentication required'}</p><small>${esc(p.node.url || 'Connected through another machine')}</small></div>${p.authorized ? `<button data-toggle="${p.node.nodeId}">${p.disconnected ? 'Reconnect' : 'Disconnect'}</button><button data-revoke="${p.node.nodeId}">Revoke access</button>` : `<button data-pair="${p.node.nodeId}">Authenticate</button>`}</div>`).join('') || '<p class="subtle">No connected machines yet.</p>';
    list.querySelectorAll<HTMLButtonElement>('[data-toggle]').forEach(button => { button.onclick = async () => { button.disabled = true; try { const peer = state.peers.find(p => p.node.nodeId === button.dataset.toggle)!; await client.json(`/swarm/peers/${peer.node.nodeId}`, 'PATCH', { disconnected: !peer.disconnected }); await changed(); await reload(); } catch (e) { notice(e, true); button.disabled = false; } }; });
    list.querySelectorAll<HTMLButtonElement>('[data-revoke]').forEach(button => { button.onclick = async () => { button.disabled = true; try { await client.json(`/swarm/peers/${button.dataset.revoke}`, 'DELETE'); await changed(); await reload(); } catch (e) { notice(e, true); button.disabled = false; } }; });
    list.querySelectorAll<HTMLButtonElement>('[data-pair]').forEach(button => { button.onclick = () => {
      const peer = state.peers.find(p => p.node.nodeId === button.dataset.pair)!;
      const form = root.querySelector<HTMLFormElement>('[data-connect]')!; form.dataset.node = peer.node.nodeId;
      form.querySelector<HTMLInputElement>('[name=url]')!.required = false; form.querySelector<HTMLInputElement>('[name=url]')!.value = peer.node.url || '';
      form.querySelector<HTMLInputElement>('[name=name]')!.value = peer.node.name; form.querySelector<HTMLInputElement>('[name=password]')!.focus();
    }; });
  };
  const form = root.querySelector<HTMLFormElement>('[data-connect]')!;
  form.querySelector<HTMLInputElement>('[name=url]')!.oninput = () => { delete form.dataset.node; };
  form.onsubmit = async event => {
    event.preventDefault(); const button = form.querySelector<HTMLButtonElement>('button')!; button.disabled = true;
    const status = form.querySelector<HTMLElement>('[data-status]')!; status.textContent = 'Connecting…';
    try { const data = new FormData(form); await client.json('/swarm/peers', 'POST', { url: data.get('url'), nodeId: form.dataset.node || '', name: data.get('name'), password: data.get('password') }); form.reset(); delete form.dataset.node; status.textContent = 'Connected'; await changed(); await reload(); }
    catch (e) { status.textContent = e instanceof Error ? e.message : String(e); }
    finally { button.disabled = false; }
  };
  try { await reload(); } catch (e) { notice(e, true); }
}

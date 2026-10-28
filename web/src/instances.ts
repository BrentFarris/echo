import type { Attachment, Bot, Chat, EchoEvent, Generation, Message, Plugin, State } from './types';

export type Instance = { nodeId: string; name: string; epoch: string; version: number; url?: string; capabilities?: string[] };
export type Peer = { node: Instance; authorized: boolean; connected: boolean; blocked: boolean; disconnected?: boolean; error?: string };
export type SwarmState = { node: Instance; enabled: boolean; peers: Peer[]; instances: { node: Instance; state?: State; online: boolean; error?: string }[] };
export let entryNode = '';
export const instances = new Map<string, { node: Instance; online: boolean; state?: State }>();

export function splitID(id = ''): [string, string] {
  const match = /^([a-f0-9]{32}):(.*)$/.exec(id);
  return match ? [match[1], match[2]] : [entryNode, id];
}
export function localID(id = '') { return splitID(id)[1]; }
export function resourceID(node: string, id: string): string {
  if (!id) return id;
  const [owner, raw] = splitID(id);
  if (id !== raw) return owner === entryNode ? raw : id;
  return !node || node === entryNode ? id : `${node}:${id}`;
}
export function machineName(node = entryNode) {
  const instance = instances.get(node)?.node;
  const same = [...instances.values()].filter(i => i.node.name === instance?.name);
  return (instance?.name || node.slice(0, 8) || 'This machine') + (same.length > 1 ? '-' + node.slice(0, 8) : '');
}
export function instanceOnline(node: string) { return !node || node === entryNode || instances.get(node)?.online === true; }

// A client is permanently bound to an owner. It is safe to keep one in an app
// window while another window changes its selected machine.
export class InstanceClient {
  constructor(readonly nodeId = entryNode) {}
  url(path: string) {
    if (!path.startsWith('/') || path.startsWith('//') || /(^|\/)\.\.?(\/|$)/.test(path.split('?')[0]) || path.split('?')[0].includes('\\')) throw new Error('Invalid instance API path');
    const relative = path.replace(/^\/api\/v1(?=\/)/, '');
    return '/api/v1' + (this.nodeId && this.nodeId !== entryNode ? `/swarm/instances/${encodeURIComponent(this.nodeId)}` : '') + relative;
  }
  fetch(path: string, options?: RequestInit) { return fetch(this.url(path), options); }
  async json<T = any>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<T> {
    const response = await this.fetch(path, { method, signal, headers: body === undefined ? {} : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    const text = await response.text(); let payload: any;
    try { payload = JSON.parse(text); } catch { throw Object.assign(new Error(text || `HTTP ${response.status}`), { status: response.status }); }
    if (!response.ok) throw Object.assign(new Error(payload.error || `HTTP ${response.status}`), { status: response.status, payload });
    return payload;
  }
  events(path = '/events') {
    const url = new URL(this.url(path), location.href); url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    return new WebSocket(url);
  }
}

// Existing local IDs remain unchanged, including browser recovery keys. Remote
// resources acquire a stable node prefix, independent of their machine name.
export function ownedGeneration(node: string, generation: Generation): Generation {
  return { ...generation, endpointId: resourceID(node, generation?.endpointId || '') };
}
export function ownedMessage(node: string, message: Message): Message {
  const key = (id: string) => resourceID(node, id);
  return { ...message, storedId: message.id, id: key(message.id), chatId: key(message.chatId), runId: message.runId && key(message.runId), botId: message.botId && key(message.botId),
    recipients: message.recipients?.map(key), nodeId: message.nodeId || node,
    segments: message.segments?.map(s => ({ ...s, botId: s.botId && resourceID(s.nodeId || node, s.botId), nodeId: s.nodeId || node, allowedBots: s.allowedBots?.map(key) })),
    attachments: message.attachments?.map(a => ({ ...a, nodeId: a.nodeId || node, chatId: key(a.chatId) })) };
}
export function ownedState(node: string, value: State): State {
  const key = (id: string) => resourceID(node, id);
  return { bots: (value.bots || []).map(b => ({ ...b, id: key(b.id), nodeId: node, handle: node === entryNode ? b.handle : `${machineName(node)}.${b.handle}`, plugins: Object.fromEntries(Object.entries(b.plugins || {}).map(([id, enabled]) => [key(id), enabled])) })),
    chats: (value.chats || []).map(c => ({ ...c, id: key(c.id), nodeId: node, generation: ownedGeneration(node, c.generation), bots: Object.fromEntries(Object.entries(c.bots || {}).map(([id, v]) => [key(id), v])) })),
    plugins: (value.plugins || []).map(p => ({ ...p, id: key(p.id), nodeId: node })),
    endpoints: (value.endpoints || []).map(e => ({ ...e, id: key(e.id), nodeId: node })),
    schedules: (value.schedules || []).map(s => ({ ...s, id: key(s.id), botId: key(s.botId), nodeId: node, generation: ownedGeneration(node, s.generation) })),
    occurrences: (value.occurrences || []).map(o => ({ ...o, id: key(o.id), scheduleId: key(o.scheduleId), botId: key(o.botId), nodeId: node })),
    runs: (value.runs || []).map(r => ({ ...r, id: key(r.id), chatId: key(r.chatId), botId: key(r.botId), userMessageId: r.userMessageId && key(r.userMessageId) })), appDock: (value.appDock || []).map(key) };
}
export function combineState(local: State, swarm: SwarmState): State {
  entryNode = swarm.node.nodeId;
  instances.clear(); instances.set(entryNode, { node: swarm.node, online: true, state: local });
  for (const entry of swarm.instances) instances.set(entry.node.nodeId, entry);
  const combined = ownedState(entryNode, local);
  for (const entry of swarm.instances) {
    if (!entry.state) continue;
    const state = ownedState(entry.node.nodeId, entry.state);
    for (const field of Object.keys(combined) as (keyof State)[]) (combined[field] as unknown[]).push(...state[field]);
  }
  combined.chats.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
  return combined;
}
export function ownedEvent(node: string, e: EchoEvent): EchoEvent {
  const key = (id: string) => resourceID(node, id);
  let payload = e.payload as any;
  if (e.type === 'message') payload = ownedMessage(node, payload);
  else if (e.type === 'run') payload = { ...payload, id: key(payload.id), chatId: key(payload.chatId), botId: key(payload.botId), userMessageId: payload.userMessageId && key(payload.userMessageId) };
  else if (e.type !== 'notification' && payload?.id) payload = { ...payload, id: key(payload.id) };
  return { ...e, chatId: e.chatId && key(e.chatId), botId: e.botId && key(e.botId), runId: e.runId && key(e.runId), payload };
}

export function route(path: string, defaultNode = entryNode) {
  let node = defaultNode;
  path = path.replace(/\/(chats|bots|plugins|endpoints|schedules)\/([^/?]+)/g, (_, kind: string, encoded: string) => {
    const id = decodeURIComponent(encoded), [owner, raw] = splitID(id);
    if (id !== raw) { if (node !== defaultNode && node !== owner) throw new Error('Resources belong to different machines'); node = owner; }
    return `/${kind}/${encodeURIComponent(raw)}`;
  });
  // Query context belongs to the same app owner; it cannot change that owner.
  const [base, query] = path.split('?');
  if (query) { const params = new URLSearchParams(query); for (const field of ['botId', 'workspaceId', 'messageId']) if (params.has(field)) params.set(field, localID(params.get(field)!)); path = base + '?' + params; }
  return { client: new InstanceClient(node), path };
}
export function resourceURL(path: string, defaultNode = entryNode) { const target = route(path, defaultNode); return target.client.url(target.path); }
export function attachmentURL(message: Message, attachment: Attachment) {
  const [home, chat] = splitID(message.chatId), owner = attachment.nodeId || home;
  return new InstanceClient(owner).url(`/chats/${encodeURIComponent(attachment.originChatId || chat)}/attachments/${encodeURIComponent(attachment.id)}`);
}

export async function resourceAPI<T = any>(path: string, method = 'GET', body?: any, defaultNode = entryNode): Promise<T> {
  const target = route(path, defaultNode), node = target.client.nodeId;
  if (body && typeof body === 'object') {
    body = { ...body };
    for (const key of ['id', 'botId', 'workspaceId', 'messageId']) if (typeof body[key] === 'string') body[key] = localID(body[key]);
    if (body.endpoint) body.endpoint = { ...body.endpoint, id: localID(body.endpoint.id) };
    if (body.plugins) body.plugins = Object.fromEntries(Object.entries(body.plugins).map(([id, value]) => [localID(id), value]));
    if (body.generation) body.generation = { ...body.generation, endpointId: localID(body.generation.endpointId) };
    if (body.endpointId) body.endpointId = localID(body.endpointId);
    if (body.generationByNode) body.generationByNode = Object.fromEntries(Object.entries(body.generationByNode as Record<string, Generation>).map(([id, g]) => [id, { ...g, endpointId: localID(g.endpointId) }]));
  }
  let value: any = await target.client.json(target.path, method, body);
  if (/^\/chats\/[^/]+\/messages$/.test(target.path) && method === 'GET') value = value.map((m: Message) => ownedMessage(node, m));
  if (/^\/chats\/[^/]+\/snapshot$/.test(target.path)) value = { ...value, messages: value.messages.map((m: Message) => ownedMessage(node, m)) };
  if (/^\/chats$/.test(target.path) && method === 'POST') value = { ...value, id: resourceID(node, value.id), nodeId: node, generation: ownedGeneration(node, value.generation) };
  if (/\/app\/dock$/.test(target.path)) value = value.map((id: string) => resourceID(node, id));
  return value as T;
}

export function sameMachine(resource: { nodeId?: string }, node: string) { return (resource.nodeId || entryNode) === node; }
export function ownerEcho(plugin: Plugin, bots: Bot[]) { return bots.find(b => b.id === resourceID(plugin.nodeId || entryNode, 'echo')); }

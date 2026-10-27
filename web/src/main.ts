import { Renderers } from './renderers';
import { conversationItems, type ResponseGroup } from './conversation';
import { endpointForm, endpointValues } from './modelSettings';
import { composerMarkup, icons, autosizeComposer, wireComposerMenus, attachmentCard, previewAttachment, releaseAttachments, type DraftAttachment } from './composer';
import { activeMention, escapeHTML as esc, groupChats } from './helpers';
import type { Attachment, Bot, Chat, Endpoint, FileEntry, Generation, Message, Plugin, Schedule, Segment, State } from './types';
import './style.css';
import './composer.css';
import './conversation.css';

const app = document.querySelector<HTMLDivElement>('#app')!;
let state: State = { bots: [], chats: [], endpoints: [], plugins: [], schedules: [], occurrences: [], runs: [] };
let chatId = location.hash.slice(1), messages: Message[] = [], references: Segment[] = [];
let generation: Generation = { endpointId: '', model: '', thinking: '', temperature: .6 };
let refreshing: Promise<void> | null = null, refreshTimer = 0, pickerVersion = 0, selectedSuggestion = 0;
let suggestions: { label: string; hint: string; insert: string; ref?: Segment }[] = [];
let referenceTimer = 0, referenceVersion = 0;
let sidebarMarkup = '';
let attachments: DraftAttachment[] = [], submitting = false;
const drafts = new Map<string, { text: string; references: Segment[]; generation: Generation; attachments: DraftAttachment[] }>();
const closeWindows = new Map<HTMLElement, () => void>();
const liveToolOutput = new Map<string, string>();
const discoveredModels = new Map<string, string[]>();
const generationSaves = new Map<string, Promise<unknown>>();
const q = <T extends HTMLElement = HTMLElement>(selector: string, root: ParentNode = document) => root.querySelector<T>(selector)!;
const thinking = ['', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];

async function api<T = any>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch('/api/v1' + path, { method, headers: body === undefined ? {} : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await response.text(); let data: any;
  try { data = JSON.parse(text); } catch { throw new Error(text || `HTTP ${response.status}`); }
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data as T;
}
function notice(value: unknown, error = false) {
  document.querySelector('.notice')?.remove(); const el = document.createElement('div'); el.className = 'notice' + (error ? ' error' : ''); el.setAttribute('role', error ? 'alert' : 'status'); el.textContent = value instanceof Error ? value.message : String(value); document.body.append(el); setTimeout(() => el.remove(), error ? 12000 : 5000);
}
function action(fn: () => unknown | Promise<unknown>) { return () => { Promise.resolve().then(fn).catch(e => notice(e, true)); }; }
function busy() { return state.runs.some(r => r.chatId === chatId && ['running', 'waiting', 'queued'].includes(r.status)); }
function currentBot(id?: string) { return state.bots.find(b => b.id === id); }
function pluginEnabled(id: string, botId?: string) { const plugin = state.plugins.find(p => p.id === id); return !!plugin?.enabled && !plugin.pending && (plugin.scope !== 'echo' || botId === 'echo') && currentBot(botId)?.plugins[id] !== false; }

function shell() {
  app.innerHTML = `<aside class="sidebar"><div class="brand"><span class="brand-mark" aria-hidden="true">E</span>Echo</div><div class="sidebar-actions"><button id="new-chat">＋ New chat</button></div><nav class="chat-groups" aria-label="Chats"></nav><div class="sidebar-footer"><button id="missed-button" hidden>Missed jobs</button><button id="settings-button">⚙ Settings</button></div></aside>
  <main class="main"><header class="chat-header"><button class="icon mobile-menu" aria-label="Toggle sidebar">☰</button><strong id="chat-title">Echo</strong><span id="connection" class="subtle">Connecting…</span></header><div class="chat-log" role="log" aria-label="Conversation"></div>
  ${composerMarkup}</main>`;
  q('#new-chat').onclick = action(newChat); q('#settings-button').onclick = () => settings(); q('#missed-button').onclick = showMissed;
  q('.mobile-menu').onclick = () => q('.sidebar').classList.toggle('open');
  q<HTMLFormElement>('.composer').onsubmit = e => { e.preventDefault(); action(async () => { if (busy()) { await api(`/chats/${chatId}/stop`, 'POST', {}); await refresh(); } else await send(); })(); };
  wireComposerMenus(addFiles);
  q('#manage-models').onclick = () => { q<HTMLDetailsElement>('.model-settings').open = false; settings('models'); };
  const composer = q<HTMLTextAreaElement>('#composer'); composer.oninput = () => { autosizeComposer(); rememberDraft(); updateSendButton(); void showPicker(); clearTimeout(referenceTimer); referenceTimer = window.setTimeout(() => action(validateReferences)(), 180); };
  composer.onkeydown = e => {
    if (!q('.picker').hidden && suggestions.length) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); selectedSuggestion = (selectedSuggestion + (e.key === 'ArrowDown' ? 1 : -1) + suggestions.length) % suggestions.length; renderPicker(); return; }
      if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); chooseSuggestion(selectedSuggestion); return; }
      if (e.key === 'Escape') { e.preventDefault(); q('.picker').hidden = true; return; }
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); action(send)(); }
  };
  q<HTMLInputElement>('#model').oninput = () => { generation.model = q<HTMLInputElement>('#model').value; q('#model-name').textContent = generation.model; rememberDraft(); };
  q<HTMLInputElement>('#thinking').oninput = () => { generation.thinking = thinking[Number(q<HTMLInputElement>('#thinking').value)]; q('#thinking-label').textContent = thinkingLabel(); rememberDraft(); };
  q<HTMLInputElement>('#temperature').oninput = () => { generation.temperature = Number(q<HTMLInputElement>('#temperature').value); q('#temperature-label').textContent = generation.temperature.toFixed(2); rememberDraft(); };
  for (const selector of ['#model', '#thinking', '#temperature']) q<HTMLInputElement>(selector).onchange = saveGeneration;
  document.addEventListener('click', e => { if (!(e.target as HTMLElement).closest('.picker, #composer')) q('.picker').hidden = true; });
}
function rememberDraft() { drafts.set(chatId, { text: q<HTMLTextAreaElement>('#composer').value, references: [...references], generation: { ...generation }, attachments: [...attachments] }); }
function saveGeneration() {
  if (!chatId || !generation.endpointId) return;
  const id = chatId, value = { ...generation };
  const pending = (generationSaves.get(id) || Promise.resolve()).then(() => api(`/chats/${id}/generation`, 'PUT', value)).catch(e => notice(e, true));
  generationSaves.set(id, pending); void pending.finally(() => { if (generationSaves.get(id) === pending) generationSaves.delete(id); });
}
async function newChat() {
  if (submitting) return;
  rememberDraft(); const composer = q<HTMLTextAreaElement>('#composer'); composer.disabled = true;
  try { const c = await api<Chat>('/chats', 'POST', {}); await refresh(); await selectChat(c.id); }
  finally { composer.disabled = false; composer.focus(); }
}
async function selectChat(id: string) {
  if (submitting) return;
  if (id !== chatId) rememberDraft(); chatId = id; history.replaceState(null, '', '#' + id); const c = state.chats.find(c => c.id === id), draft = drafts.get(id);
  generation = { ...(draft?.generation || c?.generation || generation) }; references = [...(draft?.references || [])]; attachments = [...(draft?.attachments || [])]; q<HTMLTextAreaElement>('#composer').value = draft?.text || ''; renderModels(); renderRefs(); renderAttachments(); renderSidebar(); updateSendButton(); autosizeComposer(); q('.sidebar').classList.remove('open');
  messages = id ? await api<Message[]>(`/chats/${id}/messages`) : []; renderMessages(true); q<HTMLTextAreaElement>('#composer').focus();
}
async function send() {
  if (busy() || submitting) return; const content = q<HTMLTextAreaElement>('#composer').value.trim(); if (!content && !attachments.length) return;
  submitting = true; q<HTMLTextAreaElement>('#composer').disabled = true; updateSendButton();
  try {
    if (!chatId) { const c = await api<Chat>('/chats', 'POST', {}); chatId = c.id; drafts.delete(''); history.replaceState(null, '', '#' + chatId); }
    for (const draft of attachments) {
      if (draft.uploaded?.chatId === chatId) continue;
      const data = new FormData(); data.append('file', draft.file);
      const response = await fetch(`/api/v1/chats/${chatId}/attachments`, { method: 'POST', body: data });
      const result = await response.json(); if (!response.ok) throw new Error(result.error || 'Unable to upload attachment');
      draft.uploaded = result as Attachment; rememberDraft();
    }
    const segments = references.filter(ref => content.includes('#' + ref.text));
    await generationSaves.get(chatId);
    await api(`/chats/${chatId}/messages`, 'POST', { content, segments, attachments: attachments.map(a => a.uploaded!.id), generation, requestId: crypto.randomUUID() });
    q<HTMLTextAreaElement>('#composer').value = ''; references = []; releaseAttachments(attachments); attachments = []; drafts.delete(chatId); renderRefs(); renderAttachments(); autosizeComposer(); q('.picker').hidden = true; await refresh(); messages = await api(`/chats/${chatId}/messages`); renderMessages(true);
  } finally { submitting = false; q<HTMLTextAreaElement>('#composer').disabled = false; updateSendButton(); q<HTMLTextAreaElement>('#composer').focus(); }
}
function updateSendButton() {
  const button = q<HTMLButtonElement>('#send'), active = busy();
  const label = active ? 'Stop response' : submitting ? 'Sending message' : 'Send message';
  button.innerHTML = active ? icons.stop : icons.send; button.setAttribute('aria-label', label); button.title = label;
  button.disabled = submitting || (!active && !q<HTMLTextAreaElement>('#composer').value.trim() && !attachments.length);
}
function addFiles(files: File[]) {
  if (submitting) return;
  for (const file of files) {
    if (attachments.length >= 8 || attachments.reduce((n, a) => n + a.file.size, 0) + file.size > 64 * 1024 * 1024) { notice('Attach up to 8 files totaling 64 MB per message.', true); break; }
    if (file.size > 32 * 1024 * 1024) { notice(`${file.name} exceeds 32 MB.`, true); continue; }
    // Only raster images get a live thumbnail; HTML/SVG uploads never execute.
    const safePreview = /^image\/(png|jpeg|gif|webp)$/.test(file.type);
    attachments.push({ file, url: URL.createObjectURL(safePreview || file.type.startsWith('video/') ? file : new Blob([file], { type: 'application/octet-stream' })) });
  }
  renderAttachments(); rememberDraft(); autosizeComposer(); updateSendButton(); q<HTMLTextAreaElement>('#composer').focus();
}
function renderAttachments() {
  const root = q('.attachment-drafts'); root.hidden = !attachments.length; root.replaceChildren();
  for (const draft of attachments) {
    const type = /^(image\/(png|jpeg|gif|webp)|video\/)/.test(draft.file.type) ? draft.file.type : 'application/octet-stream';
    const card = document.createElement('div'); card.className = 'attachment-card'; card.innerHTML = attachmentCard(draft.file.name, draft.file.size, type, draft.url, true);
    q('.attachment-preview', card).onclick = () => previewAttachment(draft.file.name, type, draft.url);
    q('.attachment-remove', card).onclick = action(async () => { if (submitting) return; if (draft.uploaded) await api(`/chats/${draft.uploaded.chatId}/attachments/${draft.uploaded.id}`, 'DELETE'); attachments = attachments.filter(a => a !== draft); releaseAttachments([draft]); renderAttachments(); rememberDraft(); autosizeComposer(); updateSendButton(); });
    root.append(card);
  }
}
async function refresh() {
  if (refreshing) return refreshing;
  const rendering = () => JSON.stringify([state.plugins.filter(p => p.renderers?.length).map(p => [p.id, p.enabled, p.pending, p.digest]), state.bots.map(b => b.plugins)]), beforeRendering = rendering();
  refreshing = (async () => { state = await api<State>('/state'); if (!state.chats.some(c => c.id === chatId)) {
      if (chatId) { releaseAttachments(drafts.get(chatId)?.attachments || attachments); drafts.delete(chatId); }
      chatId = state.chats[0]?.id || ''; history.replaceState(null, '', chatId ? '#' + chatId : location.pathname);
      const draft = drafts.get(chatId);
      if (chatId) generation = { ...(draft?.generation || state.chats[0].generation) };
      references = [...(draft?.references || [])]; attachments = [...(draft?.attachments || [])]; q<HTMLTextAreaElement>('#composer').value = draft?.text || '';
      renderRefs(); renderAttachments(); autosizeComposer(); messages = chatId ? await api(`/chats/${chatId}/messages`) : []; renderMessages();
    }
    renderSidebar(); renderModels(); renderRunErrors(); if (rendering() !== beforeRendering) { renderers.sync(state.plugins); renderMessages(false, true); } updateSendButton();
    for (const win of document.querySelectorAll<HTMLElement>('.floating-plugin')) { const p = state.plugins.find(p => p.id === win.dataset.pluginId); if (!p?.enabled || p.pending || !pluginEnabled(p.id, win.dataset.botId)) closeWindows.get(win)?.(); }
  })().finally(() => { refreshing = null; }); return refreshing;
}
function queueRefresh() { clearTimeout(refreshTimer); refreshTimer = window.setTimeout(() => action(refresh)(), 150); }
function renderSidebar() {
  q('#chat-title').textContent = state.chats.find(c => c.id === chatId)?.title || 'Echo';
  const markup = groupChats(state.chats).map(group => `<section><div class="group-label">${esc(group.label)}</div>${group.chats.map(c => `<div class="chat-row ${c.id === chatId ? 'active' : ''}"><button class="chat-link" data-chat="${esc(c.id)}" title="${esc(c.title)}">${esc(c.title)}</button><button class="icon chat-remove" data-delete="${esc(c.id)}" aria-label="Delete ${esc(c.title)}">×</button></div>`).join('')}</section>`).join('') || '<p class="empty-list">Your chats will appear here.</p>';
  if (markup !== sidebarMarkup) { q('.chat-groups').innerHTML = markup; sidebarMarkup = markup; }
  for (const el of document.querySelectorAll<HTMLButtonElement>('[data-chat]')) el.onclick = action(() => selectChat(el.dataset.chat!));
  for (const el of document.querySelectorAll<HTMLButtonElement>('[data-delete]')) el.onclick = action(async () => { if (submitting) return; await api(`/chats/${el.dataset.delete}`, 'DELETE'); releaseAttachments(drafts.get(el.dataset.delete!)?.attachments || []); drafts.delete(el.dataset.delete!); if (chatId === el.dataset.delete) { attachments = []; references = []; q<HTMLTextAreaElement>('#composer').value = ''; renderAttachments(); renderRefs(); autosizeComposer(); } await refresh(); });
  const count = state.occurrences.filter(j => j.status === 'missed').length; q('#missed-button').hidden = !count; q('#missed-button').textContent = `Missed jobs (${count})`;
}
function chooseEndpoint(endpoint: Endpoint) { generation = { endpointId: endpoint.id, model: endpoint.model, temperature: endpoint.temperature ?? .6, thinking: '' }; renderModels(); rememberDraft(); saveGeneration(); }
function thinkingLabel() { const value = generation.thinking || state.endpoints.find(e => e.id === generation.endpointId)?.reasoningEffort; return value ? (generation.thinking ? '' : 'Default · ') + value[0].toUpperCase() + value.slice(1) : 'Default'; }
function renderModels() {
  if (!generation.endpointId && state.endpoints[0]) { generation.endpointId = state.endpoints[0].id; generation.model = state.endpoints[0].model; generation.temperature = state.endpoints[0].temperature ?? .6; }
  const list = q('#endpoint-options'), markup = state.endpoints.map(e => `<button type="button" role="option" aria-selected="${e.id === generation.endpointId}" data-endpoint="${esc(e.id)}" title="${esc(e.name)} — ${esc(e.model)}">${esc(e.name)} <span>— ${esc(e.model)}</span></button>`).join('') || '<p class="subtle">Add a model connection in Settings.</p>';
  if (list.innerHTML !== markup) { list.innerHTML = markup; for (const button of list.querySelectorAll<HTMLButtonElement>('[data-endpoint]')) button.onclick = () => { chooseEndpoint(state.endpoints.find(e => e.id === button.dataset.endpoint)!); q<HTMLButtonElement>(`[data-endpoint="${CSS.escape(button.dataset.endpoint!)}"]`).focus(); }; }
  if (document.activeElement !== q('#model')) q<HTMLInputElement>('#model').value = generation.model;
  q<HTMLInputElement>('#thinking').value = String(Math.max(0, thinking.indexOf(generation.thinking))); q('#thinking-label').textContent = thinkingLabel(); q<HTMLInputElement>('#temperature').value = String(generation.temperature); q('#temperature-label').textContent = generation.temperature.toFixed(2); q('#model-name').textContent = state.endpoints.find(e => e.id === generation.endpointId)?.name || generation.model || 'Choose a model';
  let options = document.querySelector<HTMLDataListElement>('#model-options'); if (!options) { options = document.createElement('datalist'); options.id = 'model-options'; q('#model').after(options); q<HTMLInputElement>('#model').setAttribute('list', 'model-options'); }
  const endpoint = state.endpoints.find(e => e.id === generation.endpointId), key = endpoint ? endpoint.id + ':' + endpoint.url : '';
  options.innerHTML = (discoveredModels.get(key) || []).map(model => `<option value="${esc(model)}"></option>`).join('');
  if (endpoint && !discoveredModels.has(key)) { discoveredModels.set(key, []); api<{ data: { id: string }[] }>(`/endpoints/${endpoint.id}/models`).then(result => { discoveredModels.set(key, (result.data || []).map(m => m.id)); if (generation.endpointId === endpoint.id) renderModels(); }).catch(() => { /* Manual model identifiers remain available when discovery is unsupported. */ }); }
}

const messageMarkup = new Map<string, { message: Message; element: HTMLElement; grouped: boolean; final: boolean; output?: string }>();
const renderers = new Renderers(pluginEnabled, () => renderMessages(false, true));
const disclosureState = new Map<string, boolean>();
let renderedChatId = '';
function statusBadge(status?: string) { return status && status !== 'completed' ? `<span class="badge status-${esc(status)}">${esc(({ running: 'Working', waiting: 'Waiting', queued: 'Queued', failed: 'Failed', cancelled: 'Stopped', interrupted: 'Interrupted' } as Record<string, string>)[status] || status)}</span>` : ''; }
function reasoningMarkup(m: Message) { return `<details class="reasoning" data-disclosure="${esc(m.id)}:reasoning"><summary>Thinking</summary><pre>${esc(m.reasoning)}</pre></details>`; }
function restoreDisclosures(root: HTMLElement) { for (const details of root.querySelectorAll<HTMLDetailsElement>('details[data-disclosure]')) { const open = disclosureState.get(details.dataset.disclosure!); if (open !== undefined) details.open = open; } }
function rememberDisclosures(root: HTMLElement) { for (const details of root.querySelectorAll<HTMLDetailsElement>('details[data-disclosure]')) disclosureState.set(details.dataset.disclosure!, details.open); }
function paintMessage(m: Message, existing?: HTMLElement, grouped = existing?.dataset.grouped === 'true', final = existing?.dataset.final === 'true') {
  const previous = messageMarkup.get(m.id), output = liveToolOutput.get(m.id);
  const el = existing || previous?.element || document.createElement('article');
  if (previous?.element === el && previous.grouped === grouped && previous.final === final && previous.output === output && (['content', 'reasoning', 'status', 'author', 'role', 'toolCalls', 'attachments'] as const).every(key => previous.message[key] === m[key])) return el;
  messageMarkup.set(m.id, { message: { ...m }, element: el, grouped, final, output });
  el.id = 'message-' + m.id; el.className = 'message ' + m.role;
  el.dataset.grouped = String(grouped); el.dataset.final = String(final); el.setAttribute('aria-label', m.author);
  rememberDisclosures(el);
  el.innerHTML = !grouped && m.role !== 'user' && m.role !== 'tool' ? `<div class="message-meta"><strong>${esc(m.author)}</strong>${statusBadge(m.status)}</div>` : '';
  if (m.role === 'tool') {
    const name = grouped ? m.author.split(' · ').slice(1).join(' · ') || m.author : m.author;
    el.innerHTML += `<details data-disclosure="${esc(m.id)}:output" ${m.status === 'running' ? 'open' : ''}><summary><span>${esc(name)}</span>${statusBadge(m.status)}</summary><pre>${esc(liveToolOutput.get(m.id) || m.content)}</pre></details>`;
    restoreDisclosures(el); return el;
  }
  if (m.role === 'user' && grouped) el.innerHTML += `<div class="handoff-label">${esc(m.author)}</div>`;
  if (m.reasoning && !final) el.innerHTML += reasoningMarkup(m);
  if (m.toolCalls?.length) el.innerHTML += `<details data-disclosure="${esc(m.id)}:calls"><summary>Tool calls (${m.toolCalls.length})</summary><pre>${esc(JSON.stringify(m.toolCalls, null, 2))}</pre></details>`;
  if (m.content || !m.reasoning && !m.toolCalls?.length && m.status === 'running') el.innerHTML += `<div class="message-content" id="body-${esc(m.id)}">${esc(m.content)}</div>`;
  if (m.attachments?.length) {
    const root = document.createElement('div'); root.className = 'message-attachments';
    for (const attachment of m.attachments) {
      const url = `/api/v1/chats/${encodeURIComponent(m.chatId)}/attachments/${encodeURIComponent(attachment.id)}`;
      const card = document.createElement('div'); card.className = 'attachment-card'; card.innerHTML = attachmentCard(attachment.name, attachment.size, attachment.mediaType, url);
      q('.attachment-preview', card).onclick = () => previewAttachment(attachment.name, attachment.mediaType, url); root.append(card);
    }
    el.append(root);
  }
  restoreDisclosures(el);
  if (m.content) renderers.paint(m);
  return el;
}
function paintResponse(group: ResponseGroup) {
  const el = document.createElement('section'); el.className = 'response-group'; el.dataset.timelineKey = group.key; el.setAttribute('aria-label', group.author + ' response');
  el.innerHTML = `<div class="message-meta"><strong>${esc(group.author)}</strong>${statusBadge(group.status || group.messages.at(-1)?.status)}</div>`;
  if (group.final) {
    const work = group.messages.slice(0, -1);
    if (work.length || group.final.reasoning) {
      const disclosure = document.createElement('details'); disclosure.className = 'work-disclosure'; disclosure.dataset.disclosure = group.key + ':' + group.final.id + ':work';
      disclosure.innerHTML = '<summary>Show work</summary><div class="work-content"></div>';
      const body = q('.work-content', disclosure);
      for (const m of work) body.append(paintMessage(m, undefined, true));
      if (group.final.reasoning) body.insertAdjacentHTML('beforeend', reasoningMarkup(group.final));
      el.append(disclosure);
    }
    el.append(paintMessage(group.final, undefined, true, true));
  } else {
    for (const m of group.messages) el.append(paintMessage(m, undefined, true));
  }
  restoreDisclosures(el); return el;
}
function renderMessages(tail = false, force = false) {
  const log = document.querySelector<HTMLElement>('.chat-log'); if (!log) return;
  const nearBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 100, previousTop = log.scrollTop;
  const focused = log.contains(document.activeElement) ? (document.activeElement as HTMLElement).closest<HTMLDetailsElement>('details[data-disclosure]')?.dataset.disclosure : undefined;
  const anchor = [...log.children].find(child => child.getBoundingClientRect().bottom > log.getBoundingClientRect().top) as HTMLElement | undefined;
  const anchorKey = anchor?.dataset.timelineKey || anchor?.id, anchorOffset = anchor?.offsetTop;
  if (renderedChatId !== chatId) { disclosureState.clear(); messageMarkup.clear(); renderers.clearPending(); renderedChatId = chatId; } else rememberDisclosures(log);
  if (force) { messageMarkup.clear(); renderers.clearPending(); }
  log.replaceChildren();
  if (!messages.length) log.innerHTML = `<div class="welcome"><p>Ask Echo to inspect, plan, or build.</p>${!state.endpoints.length ? '<button id="setup-model">Connect a local model</button>' : ''}</div>`;
  for (const item of conversationItems(messages, state.runs)) log.append(item.kind === 'response' ? paintResponse(item.group) : paintMessage(item.message));
  renderRunErrors(); document.querySelector<HTMLButtonElement>('#setup-model')?.addEventListener('click', () => settings('models'));
  if (focused) [...log.querySelectorAll<HTMLDetailsElement>('details[data-disclosure]')].find(d => d.dataset.disclosure === focused)?.querySelector('summary')?.focus({ preventScroll: true });
  if (tail || nearBottom) log.scrollTop = log.scrollHeight;
  else { const replacement = [...log.children].find(child => (child as HTMLElement).dataset.timelineKey === anchorKey || child.id === anchorKey) as HTMLElement | undefined; log.scrollTop = previousTop + (replacement && anchorOffset !== undefined ? replacement.offsetTop - anchorOffset : 0); }
}
function renderRunErrors() {
  for (const old of document.querySelectorAll('.run-error')) old.remove();
  const log = document.querySelector('.chat-log'); if (!log) return;
  for (const run of state.runs.filter(r => r.chatId === chatId && r.error)) { const div = document.createElement('div'); div.className = 'run-error'; div.textContent = `${currentBot(run.botId)?.name || 'Bot'}: ${run.error}`; log.append(div); }
}
function connect() {
  const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/v1/events`);
  let recovering = false, backlog: any[] = [];
  ws.onopen = () => { q('#connection').textContent = ''; };
  ws.onclose = () => { q('#connection').textContent = 'Reconnecting…'; setTimeout(connect, 1500); };
  ws.onmessage = event => {
    const e = JSON.parse(event.data);
    if (e.type === 'reset') {
      recovering = true; backlog = [];
      action(async () => { try { await refresh(); let sequence = 0; const selected = chatId;
        if (selected) { const snapshot = await api<{ messages: Message[]; sequence: number }>(`/chats/${selected}/snapshot`); if (chatId === selected) { messages = snapshot.messages; sequence = snapshot.sequence; renderMessages(); } }
        recovering = false; for (const event of backlog) if (event.sequence > sequence) receive(event); backlog = []; queueRefresh();
      } finally { recovering = false; } })(); return;
    }
    if (recovering) { backlog.push(e); return; } receive(e);
  };
  function receive(e: any) {
    if (e.type === 'plugin-open') { action(() => openPlugin(e.payload.id, e.payload.viewId, e.botId))(); return; }
    if (e.type === 'run') { state.runs = [...state.runs.filter(r => r.id !== e.payload.id), e.payload]; updateSendButton(); if (e.chatId === chatId) renderMessages(); queueRefresh(); }
    if (e.chatId === chatId && e.type === 'tool-output') {
      const m = messages.find(m => m.id === e.payload.id); if (m) { liveToolOutput.set(m.id, (liveToolOutput.get(m.id) || '') + e.payload.output); const old = document.getElementById('message-' + m.id); if (old) paintMessage(m, old); } return;
    }
    if (e.chatId === chatId && (e.type === 'message' || e.type === 'delta')) {
      let m = messages.find(m => m.id === e.payload.id);
      if (e.type === 'message') { liveToolOutput.delete(e.payload.id); if (m) Object.assign(m, e.payload); else { m = e.payload; messages.push(m!); } }
      else if (m) { m.content += e.payload.content || ''; m.reasoning = (m.reasoning || '') + (e.payload.reasoning || ''); }
      if (m) { const log = q('.chat-log'); const tail = log.scrollHeight - log.scrollTop - log.clientHeight < 120; const old = document.getElementById('message-' + m.id); if (old && e.type === 'delta') paintMessage(m, old); else renderMessages(); if (tail) log.scrollTop = log.scrollHeight; }
    }
    if (e.type === 'changed') queueRefresh();
  }
}

async function validateReferences() {
  if (!references.length) return; const version = ++referenceVersion;
  const content = q<HTMLTextAreaElement>('#composer').value;
  references = references.filter(ref => content.includes('#' + ref.text));
  const updated = await api<Segment[]>('/references/validate', 'POST', { content, segments: references });
  if (version === referenceVersion) { references = updated; renderRefs(); rememberDraft(); }
}

async function showPicker() {
  const composer = q<HTMLTextAreaElement>('#composer'), match = activeMention(composer.value, composer.selectionStart), version = ++pickerVersion;
  if (!match) { q('.picker').hidden = true; return; }
  selectedSuggestion = 0;
  if (match.type === '@') suggestions = state.bots.filter(b => `${b.handle} ${b.name}`.toLowerCase().includes(match.query.toLowerCase())).map(b => ({ label: '@' + b.handle, hint: b.roots?.length ? b.roots.join(', ') : 'Unbound', insert: '@' + b.handle }));
  else { try { const entries = await api<FileEntry[]>('/references', 'POST', { content: composer.value, query: match.query }); if (version !== pickerVersion) return; suggestions = entries.map(e => ({ label: (e.directory ? '▣ ' : '') + e.name, hint: e.allowedBots.map(id => currentBot(id)?.handle || id).join(', ') + ' · ' + e.path, insert: '#' + e.name, ref: { type: 'file', path: e.path, text: e.name, allowedBots: e.allowedBots } })); } catch { suggestions = []; } }
  renderPicker();
}
function renderPicker() { const picker = q('.picker'); picker.hidden = !suggestions.length; picker.innerHTML = suggestions.map((s, i) => `<button type="button" role="option" aria-selected="${i === selectedSuggestion}" class="${i === selectedSuggestion ? 'selected' : ''}" data-suggestion="${i}"><span>${esc(s.label)}</span><small title="${esc(s.hint)}">${esc(s.hint)}</small></button>`).join(''); for (const el of picker.querySelectorAll<HTMLButtonElement>('button')) el.onmousedown = e => { e.preventDefault(); chooseSuggestion(Number(el.dataset.suggestion)); }; }
function chooseSuggestion(index: number) { const composer = q<HTMLTextAreaElement>('#composer'), match = activeMention(composer.value, composer.selectionStart), item = suggestions[index]; if (!match || !item) return; composer.setRangeText(item.insert + ' ', match.start, match.end, 'end'); if (item.ref && !references.some(r => r.path === item.ref!.path)) references.push(item.ref); q('.picker').hidden = true; renderRefs(); rememberDraft(); composer.focus(); }
function renderRefs() { q('.refs').innerHTML = references.map(r => `<span class="ref" title="${esc(r.path)}">#${esc(r.text)} · ${esc(r.allowedBots?.map(id => currentBot(id)?.handle || id).join(', '))}</span>`).join(''); }

function dialog(title: string, body: string) { const el = document.createElement('dialog'); el.innerHTML = `<div class="dialog-heading"><h2>${esc(title)}</h2><button class="icon" aria-label="Close">×</button></div>${body}`; document.body.append(el); q('.dialog-heading button', el).onclick = () => el.close(); el.addEventListener('close', () => el.remove()); el.showModal(); return el; }
function settings(section = 'bots') {
  const previous = document.querySelector<HTMLDialogElement>('#settings'); if (previous) { previous.close(); previous.remove(); }
  const el = dialog('Settings', '<div class="settings-layout"><nav class="settings-nav" aria-label="Settings sections"></nav><div class="settings-content"></div></div>'); el.id = 'settings';
  const nav = q('.settings-nav', el), content = q('.settings-content', el);
  const entries = [{ id: 'bots', name: 'Bots' }, { id: 'models', name: 'Local Models' }, ...state.plugins.map(p => ({ id: p.id, name: p.name }))];
  nav.innerHTML = entries.map(e => `<button data-section="${esc(e.id)}" class="${section === e.id ? 'active' : ''}">${esc(e.name)}</button>`).join('');
  for (const button of nav.querySelectorAll<HTMLButtonElement>('button')) button.onclick = () => settings(button.dataset.section);
  if (section === 'bots') botSettings(content);
  else if (section === 'models' || section === 'cloud-models') modelSettings(content, section === 'cloud-models');
  else pluginSettings(content, state.plugins.find(p => p.id === section)!);
}
function botSettings(root: HTMLElement) {
  root.innerHTML = `<button class="primary" id="add-bot">＋ Add bot</button>${state.bots.map(b => `<div class="list-card"><div class="grow"><strong>${esc(b.name)}</strong><p class="subtle">@${esc(b.handle)} · ${esc(b.roots?.length ? b.roots.join(', ') : 'Unbound')}</p></div><button data-edit="${esc(b.id)}">Edit</button>${b.id !== 'echo' ? `<button class="danger" data-remove="${esc(b.id)}">Delete</button>` : ''}</div>`).join('')}`;
  q('#add-bot', root).onclick = () => editBot();
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-edit]')) button.onclick = () => editBot(currentBot(button.dataset.edit));
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-remove]')) button.onclick = action(async () => { await api(`/bots/${button.dataset.remove}`, 'DELETE'); await refresh(); botSettings(root); });
}
function editBot(bot?: Bot) {
  const el = dialog(bot ? 'Edit bot' : 'Create a bot', `<form class="form"><label>Handle<input name="handle" required pattern="[A-Za-z][A-Za-z0-9_-]*" value="${esc(bot?.handle)}" ${bot?.id === 'echo' ? 'readonly' : ''}></label><label>Name<input name="name" value="${esc(bot?.name)}"></label><label>System prompt<textarea name="prompt">${esc(bot?.prompt)}</textarea></label><label>Folders, one absolute path per line<textarea name="roots" placeholder="C:\\Users\\you\\project">${esc(bot?.roots?.join('\n'))}</textarea><small>Leave empty for unrestricted host access. Assigned folders apply to file tools and shell commands.</small></label><h3>Plugins</h3>${state.plugins.filter(p => p.scope === 'bot' || p.id === 'workspaces' || bot?.id === 'echo' && p.scope === 'echo').map(p => `<label class="check"><input name="plugin:${esc(p.id)}" type="checkbox" ${bot?.plugins[p.id] !== false ? 'checked' : ''}>${esc(p.name)}${!p.enabled ? ' (disabled globally)' : ''}</label>`).join('')}<button class="primary">Save bot</button></form>`);
  q<HTMLFormElement>('form', el).onsubmit = e => { e.preventDefault(); action(async () => { const f = new FormData(q('form', el) as HTMLFormElement), overrides: Record<string, boolean> = {}; for (const input of el.querySelectorAll<HTMLInputElement>('[name^="plugin:"]')) overrides[input.name.slice(7)] = input.checked; await api('/bots', 'POST', { id: bot?.id || '', handle: f.get('handle'), name: f.get('name'), prompt: f.get('prompt'), roots: String(f.get('roots')).split('\n').map(v => v.trim()).filter(Boolean), plugins: overrides, revision: bot?.revision || 0 }); el.close(); await refresh(); settings('bots'); })(); };
}
function modelSettings(root: HTMLElement, cloud: boolean) {
  const plugin = state.plugins.find(p => p.id === 'cloud-models')!;
  root.innerHTML = `${cloud ? `<label class="check"><input id="cloud-enabled" type="checkbox" ${plugin.enabled ? 'checked' : ''}> Enable Cloud Models</label><hr>` : '<p class="subtle">Connect an existing Ollama, LM Studio, llama.cpp, or compatible server.</p>'}<button class="primary" id="add-model">＋ Add connection</button>${state.endpoints.filter(e => e.cloud === cloud).map(e => `<div class="list-card"><div class="grow"><strong>${esc(e.name)}</strong><p class="subtle">${esc(e.model)}<br>${esc(e.url)}</p></div><button data-model="${esc(e.id)}">Edit</button><button data-discover="${esc(e.id)}">Models</button><button class="danger" data-remove="${esc(e.id)}">×</button></div>`).join('')}`;
  if (cloud) q<HTMLInputElement>('#cloud-enabled', root).onchange = action(async () => { await api('/plugins/cloud-models', 'PATCH', { enabled: q<HTMLInputElement>('#cloud-enabled', root).checked, config: plugin.config }); await refresh(); });
  q('#add-model', root).onclick = () => editModel(undefined, cloud);
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-model]')) button.onclick = () => editModel(state.endpoints.find(e => e.id === button.dataset.model), cloud);
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-discover]')) button.onclick = action(async () => { const models = await api(`/endpoints/${button.dataset.discover}/models`); dialog('Available models', `<pre>${esc(JSON.stringify(models, null, 2))}</pre>`); });
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-remove]')) button.onclick = action(async () => { await api(`/endpoints/${button.dataset.remove}`, 'DELETE'); await refresh(); modelSettings(root, cloud); });
}
function editModel(endpoint?: Endpoint, cloud = false) {
  const el = dialog(endpoint ? 'Edit Endpoint' : 'Add Endpoint', endpointForm(endpoint, cloud)); el.classList.add('endpoint-editor');
  const effort = q<HTMLSelectElement>('[name=reasoningEffort]', el), budget = q<HTMLInputElement>('[name=thinkingTokenBudget]', el);
  effort.onchange = () => { budget.disabled = !!effort.value; }; effort.onchange(new Event('change'));
  q<HTMLInputElement>('[name=compressionThreshold]', el).oninput = event => { q('.compression-setting output', el).textContent = (event.target as HTMLInputElement).value + '%'; };
  const provider = document.createElement('label'); provider.innerHTML = `Provider<select name="provider"><option value="">Compatible HTTP</option>${state.plugins.filter(p => !p.builtin && p.enabled && !p.pending).flatMap(p => (p.providers || []).map(name => `<option value="${esc(name)}">${esc(p.name)} · ${esc(name)}</option>`)).join('')}</select>`;
  q('form', el).prepend(provider); q<HTMLSelectElement>('select', provider).value = endpoint?.provider || '';
  q<HTMLFormElement>('form', el).onsubmit = e => { e.preventDefault(); action(async () => { const form = q<HTMLFormElement>('form', el), key = q<HTMLInputElement>('[name=key]', el).value; const value = await api<Endpoint>('/endpoints', 'POST', { endpoint: { id: endpoint?.id || '', cloud, ...endpointValues(form) }, ...(key ? { apiKey: key } : {}) }); el.close(); await refresh(); chooseEndpoint(value); settings(cloud ? 'cloud-models' : 'models'); })(); };
}
function pluginSettings(root: HTMLElement, plugin: Plugin) {
  if (!plugin) return;
  root.innerHTML = `<form class="form"><label class="check"><input name="enabled" type="checkbox" ${plugin.enabled ? 'checked' : ''}> Enable ${esc(plugin.name)}</label>${plugin.pending ? `<p>Additional permissions requested: ${esc(plugin.capabilities.join(', '))}</p><button type="button" id="approve-plugin">Approve permissions</button>` : ''}${Object.entries(plugin.settings?.properties || {}).map(([key, schema]) => { const value = plugin.config[key]; if (schema.type === 'boolean') return `<label class="check"><input name="${esc(key)}" type="checkbox" ${value ? 'checked' : ''}>${esc(schema.description || key)}</label>`; if (schema.type === 'array') return `<label>${esc(schema.description || key)}<textarea name="${esc(key)}">${esc(Array.isArray(value) ? value.join('\n') : '')}</textarea></label>`; return `<label>${esc(schema.description || key)}<input name="${esc(key)}" type="${schema.type === 'integer' ? 'number' : 'text'}" value="${esc(value)}"></label>`; }).join('')}<button class="primary">Save settings</button></form>${!plugin.builtin ? '<div class="button-row"><button id="rollback-plugin">Restore previous version</button></div>' : ''}${(plugin.views || []).filter(v => v.kind !== 'renderer').map(v => `<button class="open-view" data-view="${esc(v.id)}">Open ${esc(v.title)}</button>`).join('')}<div id="plugin-extra"></div>`;
  q<HTMLFormElement>('form', root).onsubmit = e => { e.preventDefault(); action(async () => { const f = new FormData(q('form', root) as HTMLFormElement), config: Record<string, unknown> = {}; for (const [key, schema] of Object.entries(plugin.settings?.properties || {})) { if (schema.type === 'boolean') config[key] = f.has(key); else if (schema.type === 'integer') config[key] = Number(f.get(key)); else if (schema.type === 'array') config[key] = String(f.get(key)).split('\n').map(v => v.trim()).filter(Boolean); else config[key] = String(f.get(key)); } await api(`/plugins/${plugin.id}`, 'PATCH', { enabled: f.has('enabled'), config }); await refresh(); notice(plugin.id === 'hosted-server' ? 'Saved. Restart Echo to change its listening address.' : 'Settings saved'); })(); };
  root.querySelector<HTMLButtonElement>('#approve-plugin')?.addEventListener('click', action(async () => { await api(`/plugins/${plugin.id}/approve`, 'POST', { digest: plugin.digest }); await refresh(); settings(plugin.id); }));
  root.querySelector<HTMLButtonElement>('#rollback-plugin')?.addEventListener('click', action(async () => { await api(`/plugins/${plugin.id}/rollback`, 'POST', {}); await refresh(); settings(plugin.id); }));
  for (const button of root.querySelectorAll<HTMLButtonElement>('.open-view')) button.onclick = action(() => openPlugin(plugin.id, button.dataset.view!, 'echo'));
  if (plugin.id === 'hosted-server') {
    q('#plugin-extra', root).innerHTML = '<hr><form class="form" id="password-form"><label>Remote access password<input name="password" type="password" minlength="12" maxlength="72" required autocomplete="new-password"></label><button>Set password</button><small>A password is required before remote hosting can start. TLS certificate and key fields enable HTTPS.</small></form>';
    q<HTMLFormElement>('#password-form', root).onsubmit = e => { e.preventDefault(); action(async () => { const f = q<HTMLFormElement>('#password-form', root); await api('/hosting/password', 'POST', { password: new FormData(f).get('password') }); f.reset(); notice('Password saved'); })(); };
  }
  if (plugin.id === 'calendar') calendarSettings(q('#plugin-extra', root));
  if (plugin.id === 'workspaces') q('#plugin-extra', root).innerHTML = '<p class="subtle">Assign folders in Bots → Edit, or ask Echo to bind a bot. Existing folder restrictions stay enforced when this plugin is disabled.</p>';
  if (plugin.id === 'plugin-authoring') q('#plugin-extra', root).innerHTML = '<p class="subtle">Ask Echo to create a plugin—for example, “Create a GUI calculator I can drag around.” UI plugins need no compiler. Go backend plugins require an installed Go toolchain.</p>';
  if (['git', 'fossil', 'p4'].includes(plugin.id)) q('#plugin-extra', root).innerHTML = `<p class="subtle">Uses the installed CLI, independently of Shell. Ask a bot to inspect changes, create commits or manage its repository. Commands stream into chat and stop with the run. Bound bots retain their folder restrictions, including when network access is granted; these network grants also apply to native child processes. Unbound bots retain unrestricted host access.</p>${plugin.id === 'git' ? '<p class="subtle">Set the author name/email for commits. Scoped Git uses reftable for new repositories; some older loose-ref repositories are unsupported.</p>' : ''}${plugin.id === 'fossil' ? '<p class="subtle">Grant access to both the checkout and its Fossil repository database. Interactive editors are unavailable; provide commit messages in chat.</p>' : ''}`;
  if (plugin.id === 'p4') {
    q('#plugin-extra', root).insertAdjacentHTML('beforeend', `<hr><p class="subtle">Install p4.exe and save the server, user and client above. Save a ticket for that server/user below; it is kept in Windows Credential Manager and redacted from command output. A bound bot needs the appropriate network grant to reach Perforce. SSL servers need a trusted fingerprint file within the bot's folders.</p><form class="form" id="p4-secret-form"><label>P4 ticket or password<input name="secret" type="password" autocomplete="new-password" required placeholder="${plugin.config.credential ? 'Credential is saved' : 'No credential saved'}"></label><div class="button-row"><button class="primary">Save P4 credential</button><button type="button" id="clear-p4-secret">Clear credential</button></div></form>`);
    q<HTMLFormElement>('#p4-secret-form', root).onsubmit = event => { event.preventDefault(); action(async () => { const form = q<HTMLFormElement>('#p4-secret-form', root); await api('/plugins/p4/credential', 'POST', { secret: new FormData(form).get('secret') }); form.reset(); await refresh(); settings('p4'); notice('P4 credential saved'); })(); };
    q('#clear-p4-secret', root).onclick = action(async () => { await api('/plugins/p4/credential', 'POST', { secret: '' }); await refresh(); settings('p4'); });
  }
}
function calendarSettings(root: HTMLElement) {
  root.innerHTML = `<hr><h3>Schedules</h3><button id="new-schedule">＋ Add schedule</button>${state.schedules.map(s => `<div class="list-card"><div class="grow"><strong>${esc(currentBot(s.botId)?.name || s.botId)}</strong><p>${esc(s.prompt)}</p><small>${esc(s.recurrence)} · ${esc(s.timezone)} ${s.paused ? '· Paused' : ''}</small></div><button data-schedule="${esc(s.id)}">Edit</button><button class="danger" data-delete-schedule="${esc(s.id)}">×</button></div>`).join('')}`;
  q('#new-schedule', root).onclick = () => editSchedule();
  for (const b of root.querySelectorAll<HTMLButtonElement>('[data-schedule]')) b.onclick = () => editSchedule(state.schedules.find(s => s.id === b.dataset.schedule));
  for (const b of root.querySelectorAll<HTMLButtonElement>('[data-delete-schedule]')) b.onclick = action(async () => { await api(`/schedules/${b.dataset.deleteSchedule}`, 'DELETE'); await refresh(); calendarSettings(root); });
}
function editSchedule(schedule?: Schedule) {
  const el = dialog('Scheduled work', `<form class="form"><label>Bot<select name="botId">${state.bots.map(b => `<option value="${esc(b.id)}" ${b.id === schedule?.botId ? 'selected' : ''}>${esc(b.name)}</option>`).join('')}</select></label><label>Prompt<textarea name="prompt" required>${esc(schedule?.prompt)}</textarea></label><label>Recurrence<input name="recurrence" value="${esc(schedule?.recurrence || 'daily 15:00')}"><small>daily 15:00 · weekly MON 09:00 · every 2 hours · ISO date/time</small></label><label>Timezone<input name="timezone" value="${esc(schedule?.timezone || 'Local')}"><small>Local uses the Echo server's timezone; an IANA name such as America/Chicago is also accepted.</small></label><label class="check"><input name="paused" type="checkbox" ${schedule?.paused ? 'checked' : ''}>Paused</label><p class="subtle">Model: ${esc((schedule?.generation || generation).model || 'Select a model in the chat first')}</p><button class="primary">Save schedule</button></form>`);
  q<HTMLFormElement>('form', el).onsubmit = e => { e.preventDefault(); action(async () => { const f = new FormData(q('form', el) as HTMLFormElement); await api('/schedules', 'POST', { id: schedule?.id || '', botId: f.get('botId'), prompt: f.get('prompt'), recurrence: f.get('recurrence'), timezone: f.get('timezone'), paused: f.has('paused'), generation: schedule?.generation || generation }); el.close(); await refresh(); settings('calendar'); })(); };
}
function showMissed() {
  const jobs = state.occurrences.filter(o => o.status === 'missed').sort((a, b) => a.intendedAt.localeCompare(b.intendedAt));
  const el = dialog('Missed scheduled work', `<p class="subtle">Choose which missed jobs to run. Unselected jobs will be skipped.</p><form class="form"><div class="missed-jobs">${jobs.map(o => `<label class="missed-job"><input type="checkbox" name="job" value="${esc(o.id)}"><div><strong>${esc(currentBot(o.botId)?.name || o.botId)}</strong> · ${esc(new Date(o.intendedAt).toLocaleString(undefined, { timeZone: o.timezone || 'UTC' }))}<p>${esc(o.prompt)}</p><small>${esc(o.recurrence || 'Scheduled work')} · ${esc(o.timezone || 'UTC')} · ${esc(o.id)}</small></div></label>`).join('')}</div><label>Concurrent jobs<input name="concurrency" type="number" min="0" step="1" value="1" required><small>0 means unlimited.</small></label><div class="button-row"><button class="primary">Run selected</button><button id="skip-all" type="button">Skip all</button></div></form>`);
  const submit = async (skip: boolean) => { const f = new FormData(q('form', el) as HTMLFormElement); await api('/missed/review', 'POST', { ids: skip ? [] : f.getAll('job'), concurrency: Number(f.get('concurrency')) }); el.close(); await refresh(); };
  q<HTMLFormElement>('form', el).onsubmit = e => { e.preventDefault(); action(() => submit(false))(); }; q('#skip-all', el).onclick = action(() => submit(true));
}

async function openPlugin(id: string, viewId: string, botId = 'echo') {
  const view = await api<{ title: string; html: string }>(`/plugins/${id}/views/${viewId}`), nonce = crypto.randomUUID();
  const win = document.createElement('section'); win.className = 'floating-plugin'; win.dataset.pluginId = id; win.dataset.botId = botId; win.innerHTML = `<header tabindex="0" aria-label="Move ${esc(view.title)} with arrow keys"><strong>${esc(view.title)}</strong><button class="icon" aria-label="Close plugin">×</button></header>`;
  const frame = document.createElement('iframe'); frame.title = view.title; frame.sandbox.add('allow-scripts');
  frame.srcdoc = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'"><style>:root{font-family:system-ui;background:${getComputedStyle(document.documentElement).getPropertyValue('--surface')};color:${getComputedStyle(document.documentElement).getPropertyValue('--text')}}</style>${view.html}`;
  win.append(frame); document.body.append(win);
  const key = `echo2:window:${id}:${viewId}`; try { const p = JSON.parse(localStorage.getItem(key) || 'null'); if (p) { win.style.left = Math.max(0, Math.min(p.x, innerWidth - 240)) + 'px'; win.style.top = Math.max(0, Math.min(p.y, innerHeight - 100)) + 'px'; win.style.width = Math.min(p.w, innerWidth) + 'px'; win.style.height = Math.min(p.h, innerHeight) + 'px'; } } catch { /* Invalid layout only affects this window. */ }
  const save = () => { const r = win.getBoundingClientRect(); localStorage.setItem(key, JSON.stringify({ x: r.x, y: r.y, w: r.width, h: r.height })); };
  const header = q('header', win); let drag: { x: number; y: number; left: number; top: number } | null = null;
  header.onpointerdown = e => { if ((e.target as HTMLElement).closest('button')) return; const r = win.getBoundingClientRect(); drag = { x: e.clientX, y: e.clientY, left: r.left, top: r.top }; header.setPointerCapture(e.pointerId); };
  header.onpointermove = e => { if (!drag) return; win.style.left = Math.max(0, Math.min(innerWidth - 120, drag.left + e.clientX - drag.x)) + 'px'; win.style.top = Math.max(0, Math.min(innerHeight - 50, drag.top + e.clientY - drag.y)) + 'px'; };
  header.onpointerup = () => { drag = null; save(); }; header.onkeydown = e => { const vectors: Record<string, number[]> = { ArrowLeft: [-10, 0], ArrowRight: [10, 0], ArrowUp: [0, -10], ArrowDown: [0, 10] }; const v = vectors[e.key]; if (!v) return; e.preventDefault(); const r = win.getBoundingClientRect(); if (e.shiftKey) { win.style.width = Math.max(240, r.width + v[0]) + 'px'; win.style.height = Math.max(180, r.height + v[1]) + 'px'; } else { win.style.left = Math.max(0, r.left + v[0]) + 'px'; win.style.top = Math.max(0, r.top + v[1]) + 'px'; } save(); };
  const observer = new ResizeObserver(save); observer.observe(win);
  const listener = async (event: MessageEvent) => {
    if (event.source !== frame.contentWindow || !event.data || !win.isConnected) return;
    if (event.data.type === 'echo-plugin-ready') { frame.contentWindow!.postMessage({ type: 'echo-plugin-init', nonce, pluginId: id, viewId, botId, theme: { background: getComputedStyle(document.documentElement).getPropertyValue('--surface'), text: getComputedStyle(document.documentElement).getPropertyValue('--text') } }, '*'); return; }
    if (event.data.nonce !== nonce || event.data.pluginId !== id || event.data.viewId !== viewId || event.data.type !== 'echo-plugin-request') return;
    if (event.data.method === 'window.close') { close(); return; }
    try { const result = await api(`/plugins/${id}/bridge`, 'POST', { botId, method: event.data.method, params: event.data.params }); frame.contentWindow!.postMessage({ type: 'echo-plugin-response', nonce, id: event.data.id, result }, '*'); }
    catch (error) { frame.contentWindow!.postMessage({ type: 'echo-plugin-response', nonce, id: event.data.id, error: String(error) }, '*'); }
  };
  const close = () => { save(); observer.disconnect(); removeEventListener('message', listener); closeWindows.delete(win); win.remove(); };
  closeWindows.set(win, close);
  q('button', header).onclick = close; addEventListener('message', listener);
}

async function start() {
  try { await api('/session', 'POST', {}); }
  catch { app.innerHTML = '<form class="login form"><div class="brand">Echo</div><label>Password<input name="password" type="password" autocomplete="current-password" required></label><button class="primary">Sign in</button></form>'; q<HTMLFormElement>('form').onsubmit = e => { e.preventDefault(); action(async () => { const f = new FormData(q('form') as HTMLFormElement); await api('/session', 'POST', { password: f.get('password') }); await ready(); })(); }; return; }
  await ready();
}
async function ready() { shell(); await refresh(); if (chatId) await selectChat(chatId); else renderMessages(); connect(); }
void start().catch(e => notice(e, true));



import { updateTerminalDock, openTerminalDock } from './terminal';
import { pluginNetworkNotice } from './pluginNetwork';
import { GenerationSettings, endpointGeneration } from './generationSettings';
import { InstanceClient, resourceAPI, resourceURL, resourceID, localID, splitID, entryNode, instances, machineName, sameMachine, combineState, ownedEvent, ownedGeneration, instanceOnline, attachmentURL, type SwarmState } from './instances';
import { mountSwarm } from './swarmSettings';
import { InstanceEvents } from './instanceEvents';
import { ReferenceInput, trimmedDraft } from './referenceEditor';
import { referenceIcons, referenceFromElement, renderMessageReferences, showBotDetails } from './references';
import { Renderers } from './renderers';
import { updateStreamedText } from './messageContent';
import { conversationItems, conversationThreads, threadMessages, type ResponseGroup } from './conversation';
import { botAvatarMarkup, threadIdentityMarkup } from './botIdentity';
import { fileChangesFooter } from './fileChanges';
import { turnStatisticsFooter, statisticsPreferenceKey } from './turnStatistics';
import { mountGeneral, notifyUsageMachinesChanged } from './usage';
import { MessageActionUI } from './messageActions';
import { createChatScrollFollower, type ChatScrollFollower } from './chatScroll';
import { assertOnlineRecipients } from './offlineRecipients';
import { ReferenceSearch, validateInstanceReferences } from './referenceSearch';
import { endpointForm, endpointValues } from './modelSettings';
import { mountDevelopment } from './development';
import { developmentUpdateNode, refreshDevelopmentUpdates, renderDevelopmentUpdates, startDevelopmentUpdates } from './developmentUpdates';
import { mountHosting } from './hosting';
import { mountJiraSettings } from './jiraSettings';
import { mountConfluenceSettings } from './confluenceSettings';
import { setupPWA } from './pwa';
import { ChatNotifications } from './notifications';
import { parseChatRoute } from './notificationRoutes';
import { composerMarkup, icons, autosizeComposer, wireComposerMenus, attachmentCard, previewAttachment, releaseAttachments, prefixBotMention, type DraftAttachment } from './composer';
import { generatedMedia } from './generatedMedia';
import { isQuestionMessage, questionCard, type QuestionSubmission } from './planQuestions';
import { Apps, botPluginSelected, pluginAvailable } from './apps';
import { builtinApp } from './builtinApps';
import { PluginWindows, parseAppRoute, type NativeFileTarget } from './pluginWindows';
import { activeMention, escapeHTML as esc, groupChats } from './helpers';
import type { Attachment, Bot, Chat, Endpoint, Generation, Message, Plugin, Schedule, Segment, State } from './types';
import './style.css';
import './composer.css';
import './references.css';
import './conversation.css';
import './botIdentity.css';
import './apps.css';

const app = document.querySelector<HTMLDivElement>('#app')!;
const pwa = setupPWA(notice);
let state: State = { bots: [], chats: [], endpoints: [], plugins: [], appDock: [], schedules: [], occurrences: [], runs: [] };
let showingApps = location.hash === '#apps';
let showingApp = !!parseAppRoute(location.hash) && localID(parseAppRoute(location.hash)!.pluginId) !== "terminal";
let chatId = (showingApps || showingApp) ? '' : parseChatRoute(location.hash).chatId, messages: Message[] = [], references: Segment[] = [];
let pendingNotificationHash: string | null = parseChatRoute(location.hash).messageId ? location.hash : null;
let openingNotification = false;
const notifications = new ChatNotifications({
  notice,
  navigate: hash => { pendingNotificationHash = hash; history.replaceState(null, '', hash || '/'); if (document.querySelector('#composer')) void openNotification(hash).catch(e => notice(e, true)); },
  viewing: alert => document.visibilityState === 'visible' && document.hasFocus() && !showingApps && !showingApp && !document.querySelector('dialog[open]') && chatId === resourceID(alert.homeNodeId, alert.chatId) && (!visibleBotId || visibleBotId === resourceID(alert.homeNodeId, alert.botId))
});
let settingsNode = '', machineFilter = '', modelNode = '';
let chatNavigation = 0;
let generationByNode: Record<string, Generation> = {};
let generation: Generation = { endpointId: '', model: '', thinking: '', temperature: .6 };
let latestLocal: State | null = null, latestSwarm: SwarmState | null = null;
let swarmRefreshing: Promise<void> | null = null;
let refreshing: Promise<void> | null = null, refreshTimer = 0, selectedSuggestion = 0;
const referenceSearch = new ReferenceSearch();
let pickerStatus = '', pickerLoading = false;
let suggestions: { label: string; hint: string; insert: string; offline?: boolean; ref?: Segment }[] = [];
let referenceTimer = 0, referenceVersion = 0;
let sidebarMarkup = '';
const collapsedChatGroups = new Set<string>();
const deletedChats = new Set<string>();
let attachments: DraftAttachment[] = [], submitting = false;
const stoppingChats = new Set<string>();
const drafts = new Map<string, { text: string; references: Segment[]; generation: Generation; generationByNode: Record<string, Generation>; modelNode: string; attachments: DraftAttachment[] }>();
const automaticMentions = new Map<string, Segment>();
let pluginWindows: PluginWindows;
let apps: Apps;
let chatScroll: ChatScrollFollower;
const liveToolOutput = new Map<string, string>();
let chatGenerationSettings: GenerationSettings;
const generationSaves = new Map<string, Promise<unknown>>();
const q = <T extends HTMLElement = HTMLElement>(selector: string, root: ParentNode = document) => root.querySelector<T>(selector)!;

async function api<T = any>(path: string, method = 'GET', body?: unknown): Promise<T> {
  return resourceAPI<T>(path, method, body);
}
function notice(value: unknown, error = false) {
  document.querySelector('.notice')?.remove(); const el = document.createElement('div'); el.className = 'notice' + (error ? ' error' : ''); el.setAttribute('role', error ? 'alert' : 'status'); el.textContent = value instanceof Error ? value.message : String(value); document.body.append(el); setTimeout(() => el.remove(), error ? 12000 : 5000);
}
function action(fn: () => unknown | Promise<unknown>) { return () => { Promise.resolve().then(fn).catch(e => notice(e, true)); }; }
function busy() { return state.runs.some(r => r.chatId === chatId && ['running', 'waiting', 'queued'].includes(r.status)); }
function currentBot(id?: string) { return state.bots.find(b => b.id === id); }
function pluginEnabled(id: string, botId?: string) { return pluginAvailable(state.plugins.find(p => p.id === id), currentBot(botId)); }
const activeRunStatuses = ['running', 'waiting', 'queued'];
const finishedRunStatuses = ['completed', 'failed', 'cancelled', 'interrupted'];
function markRead(id: string) { if (id) void api(`/chats/${id}/read`, 'POST', {}).catch(() => { /* Sidebar state recovers on the next refresh. */ }); }
// Yellow dot: work is in progress. Green dot: the latest run finished after the
// chat was last opened, so the outcome has not been seen yet.
function chatDot(c: Chat) {
  if (state.runs.some(r => r.chatId === c.id && activeRunStatuses.includes(r.status))) return '<span class="chat-dot busy" role="img" aria-label="In progress" title="In progress"></span>';
  if (c.id === chatId || !c.lastReadAt) return '';
  const finishedAt = state.runs.filter(r => r.chatId === c.id && finishedRunStatuses.includes(r.status)).map(r => r.updatedAt || '').sort().at(-1) || '';
  return finishedAt > c.lastReadAt ? '<span class="chat-dot done" role="img" aria-label="Finished, not yet opened" title="Finished — not yet opened"></span>' : '';
}

function shell() {
  chatGenerationSettings?.dispose();
  app.innerHTML = `<aside id="sidebar" class="sidebar"><div class="brand"><div class="brand-identity"></div><button class="icon sidebar-close" aria-label="Close sidebar">×</button></div><div class="sidebar-actions"><button id="new-chat">＋ New chat</button></div><label class="machine-filter" hidden>Machine<select aria-label="Filter machines"></select></label><nav class="chat-groups" aria-label="Chats"></nav><div class="sidebar-footer"><button id="install-echo" hidden>Install Echo</button><button id="missed-button" hidden>Missed jobs</button><nav class="app-dock" aria-label="Docked apps" hidden></nav><div class="sidebar-footer-actions"><button id="apps-button">▦ Apps</button><button id="settings-button" data-update-notice="*">⚙ Settings</button></div></div></aside><div class="sidebar-backdrop" aria-hidden="true"></div>
  <main class="main"><header class="chat-header"><button class="icon mobile-menu" aria-label="Toggle sidebar" aria-controls="sidebar" aria-expanded="false">☰</button><strong id="chat-title">Echo</strong><span id="connection" class="subtle">Connecting…</span></header><div class="chat-view"><div class="bot-tabs-shell" hidden><button type="button" class="bot-tabs-scroll previous" aria-label="Scroll tabs left" title="Scroll tabs left">${icons.chevron}</button><div class="bot-tabs" role="tablist" aria-label="Bot threads"></div><button type="button" class="bot-tabs-scroll next" aria-label="Scroll tabs right" title="Scroll tabs right">${icons.chevron}</button></div><div id="bot-thread-panel" class="conversation-panel"><div class="chat-log" role="log" aria-label="Conversation"></div></div>
  ${composerMarkup}</div><section class="apps-view" aria-labelledby="apps-heading" hidden><h1 id="apps-heading" tabindex="-1">Apps</h1><p class="subtle">Open your plugin apps or dock them for quick access.</p><div id="apps-catalog"></div></section><section class="embedded-app-view" hidden aria-label="App"></section><div class="terminal-region" hidden></div></main>`;
  q('.chat-log').tabIndex = 0;
  chatScroll = createChatScrollFollower(q('.chat-log'));
  pluginWindows = new PluginWindows(api, { openDock: async (plugin, botId) => { updateTerminalDock(q(".terminal-region"), state.bots, state.plugins); openTerminalDock(plugin, botId); }, embeddedHost: q('.embedded-app-view'), navigate: route => { if (route) { rememberDraft(); showingApp = true; showingApps = false; history.replaceState(null, '', route); renderMainView(); renderSidebar(); } else { void selectChat(chatId); } }, configureBot: id => editBot(currentBot(id)), generationFor: node => structuredClone(node === modelNode ? generation : generationByNode[node] || endpointGeneration(state.endpoints.find(e => sameMachine(e, node)))), openModelSettings: node => settings('models', node, false) });
  apps = new Apps(q('#apps-catalog'), q('.app-dock'), {
    open: async plugin => { setSidebarOpen(false, false); await openPlugin(plugin.id, plugin.app!.viewId); },
    dock: async (id, docked) => { await api<string[]>(`/plugins/${encodeURIComponent(id)}/app/dock`, 'PUT', { docked }); await refresh(); },
    settings: (plugin, botDisabled) => { if (botDisabled) editBot(currentBot(resourceID(plugin.nodeId || entryNode, 'echo'))); else settings(localID(plugin.id), plugin.nodeId); },
    error: error => notice(error, true),
  });
  q('#new-chat').onclick = action(newChat); q('#apps-button').onclick = showApps; q('#settings-button').onclick = () => settings('general', developmentUpdateNode(settingsNode || entryNode)); q('#missed-button').onclick = showMissed;
  renderMainView();
  addEventListener('hashchange', () => { if (location.hash === '#apps') { pendingNotificationHash = null; showApps(); } else if (parseAppRoute(location.hash)) { pendingNotificationHash = null; const r = parseAppRoute(location.hash)!; action(() => pluginWindows.open(r.pluginId, r.viewId, r.botId, 'embedded'))(); } else if (parseChatRoute(location.hash).messageId) { pendingNotificationHash = location.hash; action(() => openNotification(location.hash))(); } else { pendingNotificationHash = null; action(() => selectChat(parseChatRoute(location.hash).chatId))(); } });
  pwa.mount(q<HTMLButtonElement>('#install-echo'));
  q('.mobile-menu').onclick = () => setSidebarOpen(!q('.sidebar').classList.contains('open'));
  q('.sidebar-close').onclick = q('.sidebar-backdrop').onclick = () => setSidebarOpen(false);
  app.onkeydown = e => { if (e.key === 'Escape' && q('.sidebar').classList.contains('open')) { e.preventDefault(); setSidebarOpen(false); } };
  q<HTMLFormElement>('.composer').onsubmit = e => { e.preventDefault(); action(async () => { if (busy()) await stop(); else await send(); })(); };
  wireComposerMenus(addFiles);
  chatGenerationSettings = new GenerationSettings(q<HTMLDetailsElement>('[data-generation-settings]'), {
    state: () => ({ generation, node: modelNode || entryNode, endpoints: state.endpoints.filter(e => sameMachine(e, modelNode || entryNode)), machines: [...instances.keys()].map(id => ({ id, name: machineName(id) })) }),
    change: (next, commit) => { generation = next; renderModels(); rememberDraft(); if (commit) void saveGeneration(); },
    selectMachine: node => { generationByNode[modelNode] = { ...generation }; modelNode = node; generation = ownedGeneration(node, generationByNode[node] || endpointGeneration(state.endpoints.find(e => sameMachine(e, node)))); renderModels(); rememberDraft(); },
    manage: () => settings('models', modelNode || entryNode),
    discover: async endpoint => (await api<{ data: { id: string }[] }>(`/endpoints/${endpoint.id}/models`)).data?.map(m => m.id) || [],
  });
  const composer = q<ReferenceInput>('#composer'); composer.bots = () => state.bots; composer.oninput = e => {
    ++referenceVersion; references = composer.snapshot().references; autosizeComposer(); rememberDraft(); updateSendButton(); clearTimeout(referenceTimer);
    const automatic = automaticMentions.get(chatId);
    if (automatic && !references.some(ref => ref.range?.start === 0 && ref.botId === automatic.botId && ref.text === automatic.text)) automaticMentions.delete(chatId);
    if ((e as InputEvent).isComposing) { closePicker(); return; }
    void showPicker(); referenceTimer = window.setTimeout(() => action(validateReferences)(), 180);
  };
  composer.onkeydown = e => {
    if (e.isComposing) return;
    const chip = (e.target as HTMLElement).closest<HTMLElement>('[data-reference]');
    if (chip && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); e.stopPropagation(); void activateReference(chip); return; }
    if (!q('.picker').hidden) {
      if (e.key === 'Escape') { e.preventDefault(); closePicker(); return; }
      if (suggestions.length && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) { e.preventDefault(); selectedSuggestion = (selectedSuggestion + (e.key === 'ArrowDown' ? 1 : -1) + suggestions.length) % suggestions.length; renderPicker(); q('[aria-selected="true"]', q('.picker')).scrollIntoView({ block: 'nearest' }); return; }
      if ((e.key === 'Enter' && !e.shiftKey) || (e.key === 'Tab' && suggestions.length)) { e.preventDefault(); if (suggestions.length) chooseSuggestion(selectedSuggestion); return; }
      if (e.key === 'Tab') closePicker();
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); action(send)(); }
  };
  composer.onclick = e => { const chip = (e.target as HTMLElement).closest<HTMLElement>('[data-reference]'); if (chip) { closePicker(); void activateReference(chip); } else showPicker(); };
  q('.chat-log').addEventListener('click', e => { const chip = (e.target as HTMLElement).closest<HTMLElement>('[data-reference]'); if (chip) void activateReference(chip); });
  q('.chat-log').addEventListener('keydown', e => { const chip = (e.target as HTMLElement).closest<HTMLElement>('[data-reference]'); if (chip && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); void activateReference(chip); } });
  q('.bot-tabs').onclick = e => { const tab = (e.target as HTMLElement).closest<HTMLElement>('[data-bot-thread]'); if (tab) selectBotThread(tab.dataset.botThread!); };
  q('.bot-tabs').onkeydown = e => {
    const tabs = [...q('.bot-tabs').querySelectorAll<HTMLElement>('[role="tab"]')], index = tabs.indexOf(document.activeElement as HTMLElement);
    if (index < 0 || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return;
    e.preventDefault();
    const next = e.key === 'Home' ? 0 : e.key === 'End' ? tabs.length - 1 : (index + (e.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
    selectBotThread(tabs[next].dataset.botThread!);
  };
  q('.bot-tabs').addEventListener('wheel', e => {
    if (!q('.bot-tabs-shell').classList.contains('has-overflow')) return;
    const delta = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
    if (!delta) return;
    q('.bot-tabs').scrollLeft += delta; e.preventDefault(); updateBotTabOverflow();
  }, { passive: false });
  q('.bot-tabs').onscroll = updateBotTabOverflow;
  for (const direction of ['previous', 'next']) q('.bot-tabs-scroll.' + direction).onclick = () => {
    const tabs = q('.bot-tabs');
    tabs.scrollBy({ left: (direction === 'previous' ? -1 : 1) * Math.max(140, Math.floor(tabs.clientWidth * .7)), behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' });
  };
  new ResizeObserver(updateBotTabOverflow).observe(q('.bot-tabs'));
  composer.onkeyup = e => { if (['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) showPicker(); };
  document.addEventListener('click', e => { if (!(e.target as HTMLElement).closest('.picker, #composer')) closePicker(); });
}
function setSidebarOpen(open: boolean, restoreFocus = true) {
  const sidebar = q('.sidebar'), wasOpen = sidebar.classList.contains('open');
  sidebar.classList.toggle('open', open);
  q('.mobile-menu').setAttribute('aria-expanded', String(open));
  if (open) q('.sidebar-close').focus();
  else if (wasOpen && restoreFocus) q('.mobile-menu').focus();
}
function rememberDraft() { if (modelNode) generationByNode[modelNode] = { ...generation }; const draft = q<ReferenceInput>('#composer').snapshot(); references = draft.references; drafts.set(chatId, { ...draft, generation: { ...generation }, generationByNode: { ...generationByNode }, modelNode, attachments: [...attachments] }); }
function renderApps() { apps.render(state.plugins, state.appDock || [], currentBot('echo'), state.bots); }
function renderMainView() {
  q('.chat-view').hidden = showingApps || showingApp; q('.embedded-app-view').hidden = !showingApp; if (!showingApp) pluginWindows.hideEmbedded(); q('.apps-view').hidden = !showingApps;
  if (showingApps) q('#apps-button').setAttribute('aria-current', 'page'); else q('#apps-button').removeAttribute('aria-current');
}
function showApps() {
  if (submitting) return;
  rememberDraft(); closePicker(); document.querySelector('.bot-details')?.dispatchEvent(new Event('dismiss'));
  showingApp = false; showingApps = true; history.replaceState(null, '', '#apps'); renderMainView(); renderSidebar(); renderApps();
  setSidebarOpen(false, false); q('#apps-heading').focus();
}
function saveGeneration() {
  generationByNode[modelNode || entryNode] = { ...generation };
  if (!chatId || !generation.endpointId || (modelNode || entryNode) !== splitID(chatId)[0]) return;
  const id = chatId, value = { ...generation };
  const pending = (generationSaves.get(id) || Promise.resolve()).then(() => api(`/chats/${id}/generation`, 'PUT', value)).catch(e => notice(e, true));
  generationSaves.set(id, pending); void pending.finally(() => { if (generationSaves.get(id) === pending) generationSaves.delete(id); });
}
async function newChat() {
  if (submitting) return;
  rememberDraft(); const composer = q<ReferenceInput>('#composer'); composer.disabled = true;
  try { const c = await api<Chat>('/chats', 'POST', {}); await refresh(true); await selectChat(c.id); }
  finally { composer.disabled = false; composer.focus(); }
}
async function selectChat(id: string) {
  if (submitting) return;
  if (!openingNotification) pendingNotificationHash = null;
  const navigation = ++chatNavigation, previous = chatId;
  showingApp = false; showingApps = false; renderMainView();
  closePicker(); ++referenceVersion; clearTimeout(referenceTimer); document.querySelector('.bot-details')?.dispatchEvent(new Event('dismiss'));
  if (id !== chatId) rememberDraft(); chatId = id; if (id !== previous) markRead(previous); markRead(id); history.replaceState(null, '', '#' + id); const c = state.chats.find(c => c.id === id), draft = drafts.get(id);
  generationByNode = { ...(c?.generationByNode || {}), ...(draft?.generationByNode || {}) }; modelNode = draft?.modelNode || c?.nodeId || splitID(id)[0] || entryNode;
  generation = { ...(draft?.generation || c?.generation || endpointGeneration(state.endpoints.find(e => sameMachine(e, modelNode)))) }; references = [...(draft?.references || [])]; attachments = [...(draft?.attachments || [])]; q<ReferenceInput>('#composer').restore({ text: draft?.text || '', references }); renderModels(); renderRefs(); renderAttachments(); renderSidebar(); updateSendButton(); autosizeComposer(); setSidebarOpen(false, false);
  const initialBot = selectedThreadBot([]);
  if (initialBot) addressThread(initialBot);
  // Focus before the network request so a slow remote transcript cannot steal
  // focus or replace the selection after the user has started editing.
  const composer = q<ReferenceInput>('#composer'); composer.focus(); composer.setSelectionRange(composer.value.length);
  const loaded = id ? await api<Message[]>(`/chats/${id}/messages`) : [];
  if (navigation !== chatNavigation || chatId !== id) return;
  messages = loaded; renderMessages(true);
  const loadedBot = selectedThreadBot(messages);
  if (!initialBot || loadedBot?.id !== initialBot.id) addressThread(loadedBot);
}
async function openNotification(hash: string) {
  if (openingNotification || submitting) return;
  openingNotification = true; pendingNotificationHash = hash;
  try {
    await refresh();
    const target = parseChatRoute(hash);
    const [node, raw] = splitID(target.chatId), id = resourceID(node, raw);
    if (!target.chatId) { pendingNotificationHash = null; return; }
    if (!state.chats.some(c => c.id === id)) {
      if (!instanceOnline(node)) { notice('This chat’s machine is unavailable. Echo will keep this destination and retry when it reconnects.', true); return; }
      pendingNotificationHash = null; notice('This chat is no longer available.', true); return;
    }
    document.querySelectorAll<HTMLDialogElement>('dialog[open]').forEach(dialog => dialog.close());
    await selectChat(id);
    if (target.botId) { const [owner, bot] = splitID(target.botId); selectBotThread(resourceID(owner, bot)); }
    if (target.messageId) {
      const [owner, message] = splitID(target.messageId);
      const element = document.getElementById('message-' + resourceID(owner, message));
      if (element) {
        for (let parent: HTMLElement | null = element; parent; parent = parent.parentElement) if (parent instanceof HTMLDetailsElement) parent.open = true;
        element.querySelectorAll<HTMLDetailsElement>('details.question-card').forEach(card => card.open = true);
        element.scrollIntoView({ block: 'center' }); element.tabIndex = -1; element.focus({ preventScroll: true });
      }
    }
    pendingNotificationHash = null; history.replaceState(null, '', hash);
  } finally { openingNotification = false; }
}
async function send() {
  if (busy() || submitting) return; const draft = trimmedDraft(q<ReferenceInput>('#composer').snapshot()), content = draft.text; if (!content && !attachments.length) return;
  assertOnlineRecipients(content, draft.references, state.bots);
  submitting = true; q<ReferenceInput>('#composer').disabled = true; updateSendButton();
  try {
    if (!chatId) { const c = await api<Chat>('/chats', 'POST', {}); chatId = c.id; drafts.delete(''); history.replaceState(null, '', '#' + chatId); }
    for (const draft of attachments) {
      if (draft.uploaded?.chatId === chatId) continue;
      const data = new FormData(); data.append('file', draft.file);
      const response = await fetch(resourceURL(`/chats/${chatId}/attachments`), { method: 'POST', body: data });
      const result = await response.json(); if (!response.ok) throw new Error(result.error || 'Unable to upload attachment');
      draft.uploaded = { ...result, chatId } as Attachment; rememberDraft();
    }
    const segments = draft.references;
    await generationSaves.get(chatId);
    await api(`/chats/${chatId}/messages`, 'POST', { content, segments, attachments: attachments.map(a => a.uploaded!.id), generation: generationByNode[splitID(chatId)[0]] || state.chats.find(c => c.id === chatId)?.generation || generation, generationByNode: { ...generationByNode, [modelNode || entryNode]: generation }, requestId: crypto.randomUUID() });
    q<ReferenceInput>('#composer').value = ''; references = []; automaticMentions.delete(chatId); releaseAttachments(attachments); attachments = []; drafts.delete(chatId); renderRefs(); renderAttachments(); autosizeComposer(); closePicker(); await refresh(); messages = await api(`/chats/${chatId}/messages`);
    const recipients = [...messages].reverse().find(m => m.role === 'user' && !m.runId)?.recipients;
    if (recipients?.length && !recipients.includes(selectedBotThreads.get(chatId) || '')) {
      selectedBotThreads.set(chatId, recipients[0]);
      try { sessionStorage.setItem('echo:bot-thread:' + chatId, recipients[0]); } catch { /* In-memory selection remains available. */ }
    }
    renderMessages(true);
    addressThread(selectedThreadBot(messages));
  } finally { submitting = false; const composer = q<ReferenceInput>('#composer'); composer.disabled = false; updateSendButton(); composer.focus(); composer.setSelectionRange(composer.value.length); }
}
async function stop() {
  const id = chatId;
  if (stoppingChats.has(id)) return;
  stoppingChats.add(id); updateSendButton(); notice('Stopping response…');
  try { await api(`/chats/${id}/stop`, 'POST', {}); await refresh(); }
  catch (error) { stoppingChats.delete(id); updateSendButton(); throw error; }
}
function updateSendButton() {
  const button = q<HTMLButtonElement>('#send'), active = busy();
  if (!active) stoppingChats.delete(chatId);
  const stopping = stoppingChats.has(chatId);
  const label = stopping ? 'Stopping response' : active ? 'Stop response' : submitting ? 'Sending message' : 'Send message';
  button.innerHTML = active ? icons.stop : icons.send; button.setAttribute('aria-label', label); button.title = label;
  button.disabled = stopping || !instanceOnline(splitID(chatId)[0]) || submitting || (!active && !q<ReferenceInput>('#composer').value.trim() && !attachments.length);
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
  renderAttachments(); rememberDraft(); autosizeComposer(); updateSendButton(); q<ReferenceInput>('#composer').focus();
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
function applyFreshState() {
  if (!latestLocal) return;
  const selectedWasListed = state.chats.some(c => c.id === chatId);
  const rendering = () => JSON.stringify([state.plugins.filter(p => p.renderers?.length).map(p => [p.id, p.enabled, p.pending, p.digest]), state.bots.map(b => b.plugins)]), beforeRendering = rendering();
  state = combineState(latestLocal, latestSwarm || { node: { nodeId: '', name: 'This machine', epoch: '', version: 1 }, enabled: false, peers: [], instances: [] });
  notifyUsageMachinesChanged();
  state.chats = state.chats.filter(c => !deletedChats.has(c.id));
  chatId = resourceID(...splitID(chatId)); settingsNode ||= entryNode; modelNode ||= entryNode;
  const fallbackChatId = state.chats[0]?.id || '';
  // Restore drafts only when the selected chat changes. Repainting the welcome
  // draft on background updates resets its caret and undo history.
  if (!pendingNotificationHash && (!chatId || splitID(chatId)[0] === entryNode || selectedWasListed) && !state.chats.some(c => c.id === chatId) && chatId !== fallbackChatId) {
      if (chatId) { releaseAttachments(drafts.get(chatId)?.attachments || attachments); drafts.delete(chatId); }
      chatId = fallbackChatId; if (!showingApps && !showingApp) history.replaceState(null, '', chatId ? '#' + chatId : location.pathname);
      const draft = drafts.get(chatId);
      if (chatId) generation = { ...(draft?.generation || state.chats[0].generation) };
      references = [...(draft?.references || [])]; attachments = [...(draft?.attachments || [])]; q<ReferenceInput>('#composer').restore({ text: draft?.text || '', references });
      renderRefs(); renderAttachments(); autosizeComposer(); messages = []; ++chatNavigation; renderMessages(); if (chatId) void reloadTranscript(chatId).catch(e => notice(e, true));
    }
    renderSidebar(); renderModels(); if (rendering() !== beforeRendering) { renderers.sync(state.plugins); renderMessages(false, true); } else renderMessages(); updateSendButton();
    renderApps(); pluginWindows.sync(state.plugins, state.bots); updateTerminalDock(q(".terminal-region"), state.bots, state.plugins);
}
async function refresh(fresh = false) {
  if (refreshing) { await refreshing; if (!fresh) return; }
  // Remote inventory and transcript loading never hold the local refresh lock.
  if (!swarmRefreshing) {
    swarmRefreshing = api<SwarmState>('/swarm/state').then(swarm => {
      latestSwarm = swarm; applyFreshState();
    }).catch(e => notice(e, true)).finally(() => { swarmRefreshing = null; });
  }
  refreshing = (async () => {
    latestLocal = await api<State>('/state'); applyFreshState(); void refreshDevelopmentUpdates(false);
  })().finally(() => { refreshing = null; if (pendingNotificationHash && !openingNotification && instanceOnline(splitID(parseChatRoute(pendingNotificationHash).chatId)[0])) { const hash = pendingNotificationHash; setTimeout(() => { if (pendingNotificationHash === hash) void openNotification(hash).catch(e => notice(e, true)); }, 0); } });
  return refreshing;
}
function queueRefresh() { clearTimeout(refreshTimer); refreshTimer = window.setTimeout(() => action(refresh)(), 150); }
function renderSidebar() {
  renderThreadIdentity();
  q('#chat-title').textContent = showingApp ? 'App' : showingApps ? 'Apps' : state.chats.find(c => c.id === chatId)?.title || 'Echo';
  const filter = q<HTMLSelectElement>('.machine-filter select'); q('.machine-filter').hidden = instances.size < 2;
  const options = '<option value="">All machines</option>' + [...instances.keys()].map(id => `<option value="${id}">${esc(machineName(id))}${instanceOnline(id) ? '' : ' · Offline'}</option>`).join('');
  if (filter.innerHTML !== options) { filter.innerHTML = options; filter.value = machineFilter; } filter.onchange = () => { machineFilter = filter.value; renderSidebar(); };
  const machines = (c: Chat) => [c.nodeId || entryNode, ...Object.keys(c.bots || {}).map(id => splitID(id)[0])];
  const chips = (nodes: string[]) => [...new Set(nodes)].filter(n => n !== entryNode).map(n => `<small class="machine-chip" title="${esc(n)}">${esc(machineName(n))}${instanceOnline(n) ? '' : ' · Offline'}</small>`).join('');
  const markup = groupChats(state.chats.filter(c => !machineFilter || machines(c).includes(machineFilter))).map((group, index) => {
    const expanded = !collapsedChatGroups.has(group.key), rowsId = `chat-group-rows-${index}`;
    return `<section><button type="button" class="group-label" data-group="${esc(group.key)}" aria-expanded="${expanded}" aria-controls="${rowsId}"><span class="group-chevron" aria-hidden="true"></span><span class="group-heading">${esc(group.label)}<span class="chat-machines">${chips(group.chats.flatMap(machines))}</span></span></button><div class="chat-group-rows" id="${rowsId}"${expanded ? '' : ' hidden'}>${group.chats.map(c => `<div class="chat-row ${!showingApps && !showingApp && c.id === chatId ? 'active' : ''}">${chatDot(c)}<button class="chat-link" data-chat="${esc(c.id)}" title="${esc(c.title)}"><span class="chat-link-title">${esc(c.title)}</span><span class="chat-machines">${chips(machines(c))}</span></button><button class="icon chat-remove" data-delete="${esc(c.id)}" aria-label="Delete ${esc(c.title)}">×</button></div>`).join('')}</div></section>`;
  }).join('') || '<p class="empty-list">Your chats will appear here.</p>';
  if (markup !== sidebarMarkup) { q('.chat-groups').innerHTML = markup; sidebarMarkup = markup; }
  for (const el of q('.chat-groups').querySelectorAll<HTMLButtonElement>('[data-group]')) el.onclick = () => {
    const key = el.dataset.group!, collapsed = !collapsedChatGroups.has(key);
    if (collapsed) collapsedChatGroups.add(key); else collapsedChatGroups.delete(key);
    el.setAttribute('aria-expanded', String(!collapsed));
    q<HTMLElement>('.chat-group-rows', el.parentElement!).hidden = collapsed;
  };
  for (const el of document.querySelectorAll<HTMLButtonElement>('[data-chat]')) el.onclick = action(() => selectChat(el.dataset.chat!));
  for (const el of document.querySelectorAll<HTMLButtonElement>('[data-delete]')) el.onclick = action(() => deleteChat(el.dataset.delete!));
  const count = state.occurrences.filter(j => j.status === 'missed').length; q('#missed-button').hidden = !count; q('#missed-button').textContent = `Missed jobs (${count})`;
}
async function deleteChat(id: string) {
  if (submitting) return;
  await api(`/chats/${id}`, 'DELETE');
  deletedChats.add(id);
  releaseAttachments(drafts.get(id)?.attachments || []); drafts.delete(id);
  if (chatId === id) {
    chatId = ''; ++chatNavigation; messages = []; attachments = []; references = [];
    q<ReferenceInput>('#composer').value = ''; renderAttachments(); renderRefs(); autosizeComposer();
  }
  applyFreshState();
  await refresh(true);
}

function chooseEndpoint(endpoint: Endpoint) { if (modelNode) generationByNode[modelNode] = { ...generation }; modelNode = endpoint.nodeId || splitID(endpoint.id)[0]; generation = { endpointId: endpoint.id, model: endpoint.model, temperature: endpoint.temperature ?? .6, thinking: '' }; renderModels(); rememberDraft(); saveGeneration(); }
function renderModels() {
  const endpoints = state.endpoints.filter(e => sameMachine(e, modelNode || entryNode));
  if (!generation.endpointId && endpoints[0]) generation = endpointGeneration(endpoints[0]);
  chatGenerationSettings?.render();
}

const messageMarkup = new Map<string, { message: Message; element: HTMLElement; grouped: boolean; final: boolean; output?: string }>();
async function reloadTranscript(id: string) {
  if (id !== chatId) return;
  const navigation = chatNavigation;
  const loaded = await api<Message[]>(`/chats/${encodeURIComponent(id)}/messages`);
  if (id === chatId && navigation === chatNavigation) { messages = loaded; renderMessages(); }
}
const messageActionUI = new MessageActionUI({
  busy: id => state.runs.some(run => run.chatId === id && activeRunStatuses.includes(run.status)) || (id === chatId && submitting),
  notice,
  rerun: async (message, requestId) => {
    await api(`/chats/${encodeURIComponent(message.chatId)}/messages/${encodeURIComponent(message.storedId || message.id)}/rerun`, 'POST', { requestId });
    await refresh();
    await reloadTranscript(message.chatId);
  },
  fork: async (message, requestId) => {
    const navigation = chatNavigation, thread = visibleBotId || rememberedBotThread();
    await generationSaves.get(message.chatId);
    const result = await api<{ chatId: string }>(`/chats/${encodeURIComponent(message.chatId)}/messages/${encodeURIComponent(message.storedId || message.id)}/fork`, 'POST', { requestId });
    const id = resourceID(splitID(message.chatId)[0], result.chatId);
    await refresh();
    if (chatNavigation === navigation && chatId === message.chatId && !showingApps && !showingApp) {
      if (thread) selectedBotThreads.set(id, thread);
      await selectChat(id);
    } else notice('Fork created. It is available in the chat list.');
  },
  save: async (message, content, requestId) => {
    const path = `/chats/${encodeURIComponent(message.chatId)}/messages/${encodeURIComponent(message.storedId || message.id)}`;
    await api(path + (message.role === 'user' ? '/resubmit' : ''), message.role === 'user' ? 'POST' : 'PATCH', message.role === 'user' ? { content, requestId } : { content });
    await refresh();
    await reloadTranscript(message.chatId);
  },
  remove: async message => {
    await api(`/chats/${encodeURIComponent(message.chatId)}/messages/${encodeURIComponent(message.storedId || message.id)}`, 'DELETE');
    await refresh();
    await reloadTranscript(message.chatId);
  },
});
function mountMessageActions(el: HTMLElement, message: Message, grouped: boolean) {
  if (!grouped && ['user', 'assistant'].includes(message.role) && finishedRunStatuses.includes(message.status)) messageActionUI.mount(el, el, message);
}
const renderers = new Renderers(pluginEnabled, () => renderMessages(false, true), () => chatScroll.contentChanged());
const disclosureState = new Map<string, boolean>();
// Pending question cards keep their in-progress form state across repaints;
// the completed card is rebuilt from the result instead.
const questionCards = new Map<string, HTMLElement>();
let renderedChatId = '';
const selectedBotThreads = new Map<string, string>();
const threadScroll = new Map<string, { top: number; following: boolean }>();
let renderedThreadKey = '', visibleBotId: string | undefined;
function rememberedBotThread() {
  let selected = selectedBotThreads.get(chatId);
  if (!selected) { try { selected = sessionStorage.getItem('echo:bot-thread:' + chatId) || undefined; } catch { /* Use the first thread. */ } }
  return selected;
}
function selectedThreadBot(transcript: Message[]) {
  const threads = conversationThreads(state.chats.find(chat => chat.id === chatId), transcript, state.runs.filter(run => run.chatId === chatId), state.bots);
  return currentBot(threads.find(thread => thread.id === rememberedBotThread())?.id || threads[0]?.id);
}
function renderThreadIdentity() {
  const chat = state.chats.find(chat => chat.id === chatId);
  const threads = showingApps || showingApp ? [] : conversationThreads(chat, messages.filter(message => message.chatId === chatId), state.runs.filter(run => run.chatId === chatId), state.bots);
  const echoId = resourceID(showingApps || showingApp ? entryNode : chat?.nodeId || splitID(chatId)[0], 'echo');
  const markup = threadIdentityMarkup(threads.length ? threads : [currentBot(echoId) || { id: echoId, name: 'Echo' }], rememberedBotThread());
  const identity = q('.brand-identity');
  if (identity.innerHTML !== markup) identity.innerHTML = markup;
}
function addressThread(bot: Bot | undefined) {
  if (!bot) return;
  const automatic = prefixBotMention(q<ReferenceInput>('#composer'), bot, automaticMentions.get(chatId));
  if (automatic) automaticMentions.set(chatId, automatic); else automaticMentions.delete(chatId);
  rememberDraft();
}
function selectBotThread(id: string) {
  if (submitting) return;
  selectedBotThreads.set(chatId, id);
  try { sessionStorage.setItem('echo:bot-thread:' + chatId, id); } catch { /* Storage may be unavailable. */ }
  renderMessages();
  addressThread(currentBot(id));
  const tab = [...q('.bot-tabs').querySelectorAll<HTMLElement>('[role="tab"]')].find(tab => tab.dataset.botThread === id);
  tab?.focus({ preventScroll: true }); tab?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}
function renderBotTabs() {
  const threads = conversationThreads(state.chats.find(chat => chat.id === chatId), messages, state.runs.filter(run => run.chatId === chatId), state.bots);
  const selected = rememberedBotThread();
  visibleBotId = threads.length > 1 ? threads.find(thread => thread.id === selected)?.id || threads[0].id : undefined;
  if (visibleBotId) selectedBotThreads.set(chatId, visibleBotId);
  renderThreadIdentity();
  const tabs = q('.bot-tabs'), panel = q('#bot-thread-panel');
  q('.bot-tabs-shell').hidden = !visibleBotId;
  const markup = visibleBotId ? threads.map(thread => {
    const status = statusLabel(thread.status), label = thread.name + (status ? ' ' + status : '');
    const working = ['running', 'waiting', 'queued'].includes(thread.status || '');
    return `<button type="button" role="tab" id="bot-tab-${esc(thread.id)}" data-bot-thread="${esc(thread.id)}" aria-controls="bot-thread-panel" aria-label="${esc(label)}" title="${esc(label)}" aria-selected="${thread.id === visibleBotId}" tabindex="${thread.id === visibleBotId ? 0 : -1}">${botAvatarMarkup(thread)}${working ? '<span class="bot-tab-busy-dot" aria-hidden="true"></span>' : ''}<span class="bot-tab-label">${esc(thread.name)}</span>${splitID(thread.id)[0] !== entryNode ? `<small class="machine-chip">${esc(machineName(splitID(thread.id)[0]))}</small>` : ''}</button>`;
  }).join('') : '';
  if (tabs.innerHTML !== markup) {
    const focused = tabs.contains(document.activeElement) ? (document.activeElement as HTMLElement).dataset.botThread : undefined;
    tabs.innerHTML = markup;
    if (focused) [...tabs.querySelectorAll<HTMLElement>('[role="tab"]')].find(tab => tab.dataset.botThread === focused)?.focus({ preventScroll: true });
  }
  if (visibleBotId) { panel.setAttribute('role', 'tabpanel'); panel.setAttribute('aria-labelledby', 'bot-tab-' + visibleBotId); panel.tabIndex = 0; }
  else { panel.removeAttribute('role'); panel.removeAttribute('aria-labelledby'); panel.removeAttribute('tabindex'); }
  requestAnimationFrame(updateBotTabOverflow);
}
function updateBotTabOverflow() {
  const shell = q('.bot-tabs-shell'), tabs = q('.bot-tabs');
  if (shell.hidden) return;
  const overflow = tabs.scrollWidth > tabs.clientWidth + 1;
  shell.classList.toggle('has-overflow', overflow);
  q<HTMLButtonElement>('.bot-tabs-scroll.previous').disabled = !overflow || tabs.scrollLeft <= 1;
  q<HTMLButtonElement>('.bot-tabs-scroll.next').disabled = !overflow || tabs.scrollLeft + tabs.clientWidth >= tabs.scrollWidth - 1;
}
function statusLabel(status?: string) { return status && status !== 'completed' ? ({ running: 'Working', waiting: 'Waiting', queued: 'Queued', failed: 'Failed', cancelled: 'Stopped', interrupted: 'Interrupted' } as Record<string, string>)[status] || status : ''; }
function statusBadge(status?: string) { const label = statusLabel(status); return label ? `<span class="badge status-${esc(status)}">${esc(label)}</span>` : ''; }
function reasoningMarkup(m: Message) { return `<details class="reasoning" data-disclosure="${esc(m.id)}:reasoning"><summary>Thinking</summary><pre>${esc(m.reasoning)}</pre></details>`; }
function restoreDisclosures(root: HTMLElement) { for (const details of root.querySelectorAll<HTMLDetailsElement>('details[data-disclosure]')) { const open = disclosureState.get(details.dataset.disclosure!); if (open !== undefined) details.open = open; } }
function rememberDisclosures(root: HTMLElement) { for (const details of root.querySelectorAll<HTMLDetailsElement>('details[data-disclosure]')) disclosureState.set(details.dataset.disclosure!, details.open); }
async function answerQuestions(m: Message, payload: QuestionSubmission) {
  await api(`/chats/${encodeURIComponent(m.chatId)}/questions/${encodeURIComponent(m.id)}`, 'POST', payload);
}
function paintStreamedMessage(el: HTMLElement, m: Message, previous: Message, final: boolean, output?: string) {
  // Keep the native details, summary and scroll container mounted. Replacing
  // them between pointer down/up loses clicks and resets scroll on every token.
  if (m.role === 'tool') {
    updateStreamedText(q('details > pre', el), output || m.content);
    return;
  }
  let reasoning = el.querySelector<HTMLElement>(':scope > .reasoning');
  if (m.reasoning && !final) {
    if (!reasoning) {
      const template = document.createElement('template'); template.innerHTML = reasoningMarkup(m);
      reasoning = template.content.firstElementChild as HTMLElement;
      el.insertBefore(reasoning, el.querySelector(':scope > details, :scope > .message-content, :scope > .message-attachments, :scope > .message-actions'));
      restoreDisclosures(el);
    } else updateStreamedText(q('pre', reasoning), m.reasoning);
  } else reasoning?.remove();
  let body = el.querySelector<HTMLElement>(':scope > .message-content');
  if (m.content || !m.reasoning && !m.toolCalls?.length && m.status === 'running') {
    if (!body) {
      body = document.createElement('div'); body.className = 'message-content'; body.id = 'body-' + m.id;
      el.insertBefore(body, el.querySelector(':scope > .message-attachments, :scope > .message-actions'));
    }
    if (m.content !== previous.content) {
      renderers.paint(m, body);
    }
  } else body?.remove();
}
function paintMessage(m: Message, existing?: HTMLElement, grouped = existing?.dataset.grouped === 'true', final = existing?.dataset.final === 'true') {
  const previous = messageMarkup.get(m.id), output = liveToolOutput.get(m.id);
  const el = existing || previous?.element || document.createElement('article');
  if (previous?.element === el && previous.grouped === grouped && previous.final === final && previous.output === output && (['content', 'segments', 'reasoning', 'status', 'author', 'role', 'toolCalls', 'attachments'] as const).every(key => previous.message[key] === m[key])) { mountMessageActions(el, m, grouped); return el; }
  messageMarkup.set(m.id, { message: { ...m }, element: el, grouped, final, output });
  if (previous?.element === el && previous.grouped === grouped && previous.final === final
    && (m.role === 'assistant' || m.role === 'tool' && !isQuestionMessage(m) && previous.message.content === m.content)
    && (['segments', 'status', 'author', 'role', 'toolCalls', 'attachments'] as const).every(key => previous.message[key] === m[key])) {
    paintStreamedMessage(el, m, previous.message, final, output);
    mountMessageActions(el, m, grouped); return el;
  }
  el.id = 'message-' + m.id; el.className = 'message ' + m.role;
  el.dataset.grouped = String(grouped); el.dataset.final = String(final); el.setAttribute('aria-label', m.author);
  rememberDisclosures(el);
  // Status/group changes must not flash an already formatted answer as source.
  const previousBody = m.role === 'assistant' ? el.querySelector<HTMLElement>(':scope > .message-content') : null;
  el.innerHTML = !grouped && m.role !== 'user' && m.role !== 'tool' ? `<div class="message-meta"><strong>${esc(m.author)}</strong>${statusBadge(m.status)}</div>` : '';
  if (m.role === 'tool') {
    if (isQuestionMessage(m)) {
      const cached = m.status === 'running' ? questionCards.get(m.id) : undefined;
      const card = cached || questionCard(m, payload => answerQuestions(m, payload));
      questionCards.set(m.id, card);
      el.append(card);
      restoreDisclosures(el); return el;
    }
    const name = grouped ? m.author.split(' · ').slice(1).join(' · ') || m.author : m.author;
    el.innerHTML += `<details data-disclosure="${esc(m.id)}:output"><summary><span>${esc(name)}</span>${statusBadge(m.status)}</summary><pre>${esc(liveToolOutput.get(m.id) || m.content)}</pre></details>`;
    if (!grouped) { const media = generatedMedia([m]); if (media) el.append(media); }
    restoreDisclosures(el); return el;
  }
  if (m.role === 'user' && grouped) el.innerHTML += `<div class="handoff-label">${esc(m.author)}</div>`;
  if (m.reasoning && !final) el.innerHTML += reasoningMarkup(m);
  if (m.toolCalls?.length) el.innerHTML += `<details data-disclosure="${esc(m.id)}:calls"><summary>Tool calls (${m.toolCalls.length})</summary><pre>${esc(JSON.stringify(m.toolCalls, null, 2))}</pre></details>`;
  if (m.content || !m.reasoning && !m.toolCalls?.length && m.status === 'running') {
    const body = previousBody || document.createElement('div');
    body.classList.add('message-content'); body.id = 'body-' + m.id;
    if (m.role !== 'assistant') updateStreamedText(body, m.content);
    el.append(body);
  }
  if (m.attachments?.length) {
    const root = document.createElement('div'); root.className = 'message-attachments';
    for (const attachment of m.attachments) {
      const url = attachmentURL(m, attachment);
      const card = document.createElement('div'); card.className = 'attachment-card'; card.innerHTML = attachmentCard(attachment.name, attachment.size, attachment.mediaType, url);
      q('.attachment-preview', card).onclick = () => previewAttachment(attachment.name, attachment.mediaType, url); root.append(card);
    }
    el.append(root);
  }
  restoreDisclosures(el);
  if (m.content && m.role === 'user') renderMessageReferences(q('.message-content', el), m, state.bots);
  if (m.content) renderers.paint(m, q('.message-content', el));
  mountMessageActions(el, m, grouped);
  return el;
}
function paintResponse(group: ResponseGroup) {
  const el = document.createElement('section'); el.className = 'response-group'; el.dataset.timelineKey = group.key; el.setAttribute('aria-label', group.author + ' response');
  el.innerHTML = `<div class="message-meta"><strong>${esc(group.author)}</strong>${statusBadge(group.status || group.messages.at(-1)?.status)}</div>`;
  if (group.final) {
    const work = group.messages.slice(0, -1);
    // Answered question cards sit above the final response instead of inside
    // the collapsed work disclosure (OLD finalizeSuccessfulResponse).
    const promoted = work.filter(m => isQuestionMessage(m) && m.status === 'completed');
    const rest = work.filter(m => !promoted.includes(m));
    if (rest.length || group.final.reasoning) {
      const disclosure = document.createElement('details'); disclosure.className = 'work-disclosure'; disclosure.dataset.disclosure = group.key + ':' + group.final.id + ':work';
      disclosure.innerHTML = '<summary>Show work</summary><div class="work-content"></div>';
      const body = q('.work-content', disclosure);
      for (const m of rest) body.append(paintMessage(m, undefined, true));
      if (group.final.reasoning) body.insertAdjacentHTML('beforeend', reasoningMarkup(group.final));
      el.append(disclosure);
    }
    for (const m of promoted) el.append(paintMessage(m, undefined, true));
    const final = paintMessage(group.final, undefined, true, true);
    el.append(final);
    messageActionUI.mount(el, final, group.final);
  } else {
    for (const m of group.messages) el.append(paintMessage(m, undefined, true));
  }
  const media = generatedMedia(group.messages); if (media) el.append(media);
  const node = splitID(group.messages[0].chatId)[0], editorID = resourceID(node, 'monaco-editor');
  const editorEnabled = pluginEnabled(editorID, group.messages[0].botId) && !!builtinApp(state.plugins.find(p => p.id === editorID), 'main');
  const files = fileChangesFooter(group, async file => {
    try {
      const result = await api<{ select: boolean; editor?: NativeFileTarget }>(`/chats/${encodeURIComponent(group.messages[0].chatId)}/files/${editorEnabled ? 'open' : 'reveal'}`, 'POST', { messageId: file.messageId, path: file.path });
      if (result.editor) {
        await refresh();
        await pluginWindows.openFile(editorID, 'main', { ...result.editor, botId: resourceID(node, result.editor.botId) });
      } else if (!result.select) notice('Opened the containing folder in File Explorer.');
    } catch (error) { notice(error, true); }
  }, editorEnabled);
  if (files) el.append(files);
  const statistics = turnStatisticsFooter(group, state.runs.find(run => run.id === group.final?.runId));
  restoreDisclosures(el);
  if (!statistics) return el;
  const turn = document.createElement('div'); turn.className = 'response-turn'; turn.dataset.timelineKey = group.key;
  delete el.dataset.timelineKey; turn.append(el, statistics); return turn;
}
function renderMessages(tail = false, force = false) {
  const log = document.querySelector<HTMLElement>('.chat-log'); if (!log) return;
  if (renderedThreadKey) threadScroll.set(renderedThreadKey, { top: log.scrollTop, following: chatScroll.isFollowing() });
  renderBotTabs();
  const threadKey = JSON.stringify([chatId, visibleBotId]), switchedThread = threadKey !== renderedThreadKey;
  renderedThreadKey = threadKey;
  const previousTop = log.scrollTop;
  const contentScroll = [...log.querySelectorAll<HTMLElement>('.message-content pre, .message-content table')]
    .filter(element => element.scrollTop || element.scrollLeft)
    .map(element => ({ element, top: element.scrollTop, left: element.scrollLeft }));
  const editing = document.activeElement instanceof HTMLTextAreaElement && log.contains(document.activeElement) && document.activeElement.closest('.message-edit-form') ? document.activeElement : undefined;
  const editSelection = editing ? [editing.selectionStart, editing.selectionEnd] : undefined;
  chatScroll.contentChanged();
  const focused = log.contains(document.activeElement) ? (document.activeElement as HTMLElement).closest<HTMLDetailsElement>('details[data-disclosure]')?.dataset.disclosure : undefined;
  const focusedFile = log.contains(document.activeElement) ? (document.activeElement as HTMLElement).dataset.fileChange : undefined;
  const anchor = [...log.children].find(child => child.getBoundingClientRect().bottom > log.getBoundingClientRect().top) as HTMLElement | undefined;
  const anchorKey = anchor?.dataset.timelineKey || anchor?.id, anchorOffset = anchor?.offsetTop;
  if (renderedChatId !== chatId) { disclosureState.clear(); messageMarkup.clear(); questionCards.clear(); renderers.clearPending(); renderedChatId = chatId; } else rememberDisclosures(log);
  // Repaint on tab changes: renderer replies for an offscreen thread may have been discarded.
  if (force || switchedThread) { messageMarkup.clear(); renderers.clearPending(); }
  log.replaceChildren();
  if (!messages.length) log.innerHTML = `<div class="welcome"><p>Ask Echo to inspect, plan, or build.</p>${!state.endpoints.length ? '<button id="setup-model">Connect a local model</button>' : ''}</div>`;
  const visibleMessages = threadMessages(messages, visibleBotId, state.runs);
  messageActionUI.prune(new Set(visibleMessages.map(message => message.id)));
  const visibleRuns = state.runs.filter(r => r.chatId === chatId && (!visibleBotId || r.botId === visibleBotId));
  for (const item of conversationItems(visibleMessages, visibleRuns)) {
    if (item.kind === 'run-error') {
      const div = document.createElement('div'); div.className = 'run-error'; div.dataset.timelineKey = 'run-error:' + item.run.id;
      div.textContent = `${currentBot(item.run.botId)?.name || 'Bot'}: ${item.run.error}`; log.append(div);
    } else log.append(item.kind === 'response' ? paintResponse(item.group) : paintMessage(item.message));
  }
  if (messages.length && !visibleMessages.length) log.innerHTML = '<p class="empty-list">No messages in this bot’s thread yet.</p>';
  renderQueuedRuns(); document.querySelector<HTMLButtonElement>('#setup-model')?.addEventListener('click', () => settings('models'));
  // Browsers reset nested scroll offsets when a retained answer is remounted.
  for (const { element, top, left } of contentScroll) if (element.isConnected) { element.scrollTop = top; element.scrollLeft = left; }
  if (focused) [...log.querySelectorAll<HTMLDetailsElement>('details[data-disclosure]')].find(d => d.dataset.disclosure === focused)?.querySelector('summary')?.focus({ preventScroll: true });
  if (focusedFile) [...log.querySelectorAll<HTMLButtonElement>('.file-change')].find(button => button.dataset.fileChange === focusedFile)?.focus({ preventScroll: true });
  if (editing?.isConnected && editSelection) { editing.focus({ preventScroll: true }); editing.setSelectionRange(editSelection[0], editSelection[1]); }
  if (switchedThread) {
    const saved = threadScroll.get(threadKey);
    log.scrollTop = saved?.top ?? log.scrollHeight;
    chatScroll.reset(saved?.following ?? true);
    if (chatScroll.isFollowing()) chatScroll.forceFollow();
  } else if (tail) chatScroll.forceFollow();
  else if (!chatScroll.isFollowing()) {
    const replacement = [...log.children].find(child => (child as HTMLElement).dataset.timelineKey === anchorKey || child.id === anchorKey) as HTMLElement | undefined;
    log.scrollTop = previousTop + (replacement && anchorOffset !== undefined ? replacement.offsetTop - anchorOffset : 0);
    // Restoring the reading anchor is not a request to resume following.
    chatScroll.reset(false);
  }
  chatScroll.contentChanged();
}
function renderQueuedRuns() {
  const log = document.querySelector('.chat-log'); if (!log) return;
  const runs = state.runs.filter(r => r.chatId === chatId && (!visibleBotId || r.botId === visibleBotId));
  for (const run of runs.filter(r => r.status === 'queued')) { const div = document.createElement('div'); div.className = 'run-queued subtle'; div.setAttribute('role', 'status'); div.innerHTML = `${esc(currentBot(run.botId)?.name || 'Bot')} ${statusBadge('queued')}`; log.append(div); }
}
async function recoverInstance(node: string, current: () => boolean) {
  await refresh();
  const selected = chatId;
  let sequence = 0;
  if (selected && splitID(selected)[0] === node) {
    const snapshot = await api<{ messages: Message[]; sequence: number }>(`/chats/${selected}/snapshot`);
    sequence = snapshot.sequence;
    if (current() && chatId === selected) { messages = snapshot.messages; renderMessages(); }
  }
  queueRefresh(); return sequence;
}
function connect() {
  const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/v1/events`);
  const events = new InstanceEvents(recoverInstance, (_, event) => receive(event), () => ws.close());
  ws.onopen = () => { q('#connection').textContent = ''; };
  ws.onclose = async () => {
    events.close();
    q('#connection').textContent = 'Reconnecting…';
    try {
      const response = await fetch('/api/v1/session');
      if (response.status === 401) { location.reload(); return; }
    } catch { /* Retry when the server or network returns. */ }
    setTimeout(connect, 1500);
  };
  ws.onmessage = event => events.push(entryNode, ownedEvent(entryNode, JSON.parse(event.data)));
}
function receive(e: any) {
    if (e.type === 'notification') { notifications.receive(e.payload); return; }
    if (e.type === 'plugin-open') { action(() => openPlugin(e.payload.id, e.payload.viewId, e.botId))(); return; }
    if (e.type === 'run') { state.runs = [...state.runs.filter(r => r.id !== e.payload.id), e.payload]; updateSendButton(); if (e.chatId === chatId) renderMessages(); queueRefresh(); }
    if (e.chatId === chatId && e.type === 'tool-output') {
      const m = messages.find(m => m.id === e.payload.id); if (m) { liveToolOutput.set(m.id, (liveToolOutput.get(m.id) || '') + e.payload.output); const old = document.getElementById('message-' + m.id); if (old) paintMessage(m, old); } return;
    }
    if (e.chatId === chatId && (e.type === 'message' || e.type === 'delta')) {
      let m = messages.find(m => m.id === e.payload.id);
      if (e.type === 'message') { liveToolOutput.delete(e.payload.id); if (m) Object.assign(m, e.payload); else { m = e.payload; messages.push(m!); } }
      else if (m) { m.content += e.payload.content || ''; m.reasoning = (m.reasoning || '') + (e.payload.reasoning || ''); }
      if (m) { const old = document.getElementById('message-' + m.id); if (old && e.type === 'delta') { chatScroll.contentChanged(); paintMessage(m, old); } else if (e.type !== 'delta') renderMessages(); }
    }
    if (e.type === 'changed') { if (e.payload?.messagesChanged) action(() => reloadTranscript(e.chatId))(); queueRefresh(); }
}
function connectSwarm() {
  const ws = new InstanceClient().events('/swarm/events');
  let directory = '';
  const events = new InstanceEvents(recoverInstance, (_, event) => receive(event), () => ws.close());
  ws.onmessage = event => { const envelope = JSON.parse(event.data);
    if (envelope.type === 'directory') { const next = JSON.stringify([envelope.node, envelope.peers?.map((p: any) => [p.node, p.authorized, p.blocked, p.connected, p.error])]); if (directory !== next) { directory = next; queueRefresh(); } return; }
    if (envelope.error) { queueRefresh(); return; }
    if (envelope.event) events.push(envelope.nodeId, ownedEvent(envelope.nodeId, envelope.event));
  };
  ws.onclose = () => { events.close(); setTimeout(connectSwarm, 2000); };
}

async function validateReferences() {
  const composer = q<ReferenceInput>('#composer'), draft = composer.snapshot();
  if (!draft.references.some(ref => ref.type === 'file')) return;
  const version = ++referenceVersion, selectedChat = chatId;
  try {
    const updated = await validateInstanceReferences(draft.text, draft.references);
    if (version !== referenceVersion || selectedChat !== chatId || composer.value !== draft.text) return;
    composer.updateReferences(updated.map(ref => ({ ...ref, allowedBots: ref.allowedBots || [] }))); rememberDraft();
  } catch (error) { if (version === referenceVersion && selectedChat === chatId) notice(error, true); }
}

async function activateReference(chip: HTMLElement) {
  const ref = referenceFromElement(chip);
  if (ref.type === 'bot') { showBotDetails(chip, ref, state.bots, bot => editBot(bot)); return; }
  try {
    const article = chip.closest<HTMLElement>('.message');
    const message = article && messages.find(m => 'message-' + m.id === article.id);
    const draft = q<ReferenceInput>('#composer').snapshot();
    const result = await api<{ fallback: boolean; path: string }>(message ? `/chats/${encodeURIComponent(message.chatId)}/references/reveal` : '/references/reveal', 'POST', message ? { messageId: message.id, path: ref.path } : { content: draft.text, segments: draft.references, path: ref.path });
    if (result.fallback) notice('The reference no longer exists. Opened its containing folder in File Explorer.');
  } catch (error) { notice(error, true); }
}

function closePicker() {
  referenceSearch.close(); suggestions = []; pickerStatus = ''; pickerLoading = false;
  q('.picker').hidden = true;
  q('#composer').setAttribute('aria-expanded', 'false'); q('#composer').removeAttribute('aria-activedescendant');
}
function showPicker() {
  const composer = q<ReferenceInput>('#composer'), match = activeMention(composer.value, composer.selectionStart);
  closePicker();
  if (!match) return;
  if (composer.snapshot().references.some(ref => ref.range && ref.range.start <= match.start && ref.range.end > match.start)) return;
  selectedSuggestion = 0;
  if (match.type === '@') {
    suggestions = state.bots.filter(b => `${b.handle} ${b.name}`.toLowerCase().includes(match.query.toLowerCase())).map(b => ({ label: '@' + b.handle, offline: !instanceOnline(b.nodeId || splitID(b.id)[0]), hint: b.name + (instanceOnline(b.nodeId || splitID(b.id)[0]) ? '' : ' · Offline') + ' · ' + (b.roots?.length ? b.roots.join(', ') : 'Unrestricted host access'), insert: '@' + b.handle, ref: { type: 'bot', botId: b.id, text: '@' + b.handle } }));
    pickerStatus = suggestions.length ? '' : 'No matching bots.'; renderPicker();
  } else referenceSearch.search(composer.value, match.query, result => {
    const selected = suggestions[selectedSuggestion]?.ref?.path;
    suggestions = result.entries.map(e => ({ label: e.name, hint: machineName(e.nodeId) + ' · ' + e.allowedBots.map(id => currentBot(id)?.handle || id).join(', ') + ' · ' + e.path, insert: '#' + e.name, ref: { nodeId: e.nodeId, type: 'file', kind: e.directory ? 'directory' : 'file', path: e.path, text: e.name, allowedBots: e.allowedBots } }));
    selectedSuggestion = Math.max(0, suggestions.findIndex(s => s.ref?.path === selected));
    pickerLoading = !!result.loading || result.indexing;
    pickerStatus = result.error || (result.loading ? 'Searching files and folders…' : result.indexing ? 'Searching workspace… more matches may appear.' : result.incomplete ? 'Some folders could not be searched. Try a full path.' : suggestions.length ? '' : 'No matching files or folders. Try a name or full path.');
    renderPicker();
  }, composer.snapshot().references);
}
function renderPicker() {
  const picker = q('.picker'); picker.hidden = false;
  picker.setAttribute('aria-busy', String(pickerLoading)); q('#composer').setAttribute('aria-expanded', 'true');
  picker.innerHTML = (pickerStatus ? `<p class="picker-status" role="status">${esc(pickerStatus)}</p>` : '') + suggestions.map((s, i) => `<button type="button" id="reference-option-${i}" role="option" aria-disabled="${!!s.offline}" aria-selected="${i === selectedSuggestion}" class="${i === selectedSuggestion ? 'selected' : ''}" data-suggestion="${i}">${referenceIcons[s.ref?.type === 'bot' ? 'bot' : s.ref?.kind === 'directory' ? 'directory' : 'file']}<span class="picker-name"><strong>${esc(s.label)}</strong><small title="${esc(s.hint)}">${esc(s.hint)}</small></span><span class="picker-kind">${s.ref?.type === 'bot' ? 'Bot' : s.ref?.kind === 'directory' ? 'Folder' : 'File'}</span></button>`).join('');
  if (suggestions.length) q('#composer').setAttribute('aria-activedescendant', `reference-option-${selectedSuggestion}`); else q('#composer').removeAttribute('aria-activedescendant');
  for (const el of picker.querySelectorAll<HTMLButtonElement>('button')) { el.onmousedown = e => e.preventDefault(); el.onclick = () => chooseSuggestion(Number(el.dataset.suggestion)); }
}
function chooseSuggestion(index: number) {
  const composer = q<ReferenceInput>('#composer'), match = activeMention(composer.value, composer.selectionStart), item = suggestions[index];
  if (!match || !item) return;
  if (item.offline) { notice(`Machine '${machineName(splitID(item.ref?.botId)[0])}' is offline; ${item.label} is unavailable.`, true); return; }
  composer.replace(item.insert + ' ', match.start, match.end, item.ref); closePicker(); composer.focus();
}
function renderRefs() { q<ReferenceInput>('#composer').updateReferences(references); }

function dialog(title: string, body: string) { const el = document.createElement('dialog'); el.innerHTML = `<div class="dialog-heading"><h2>${esc(title)}</h2><button class="icon" aria-label="Close">×</button></div>${body}`; document.body.append(el); q('.dialog-heading button', el).onclick = () => el.close(); el.addEventListener('close', () => el.remove()); el.showModal(); return el; }
window.addEventListener('storage', event => { if (event.key === statisticsPreferenceKey) renderMessages(false, true); });
function settings(section = 'general', node = settingsNode || entryNode, selectModel = true) {
  settingsNode = node; section = localID(section); const ownerPlugins = state.plugins.filter(p => sameMachine(p, node)); const boundAPI = <T = any>(path: string, method = 'GET', body?: unknown) => resourceAPI<T>(path, method, body, node);
  const previous = document.querySelector<HTMLDialogElement>('#settings'); if (previous) { previous.close(); previous.remove(); }
  const el = dialog(`Settings · ${machineName(node)}`, '<div class="settings-layout"><nav class="settings-nav" aria-label="Settings sections"></nav><div class="settings-content"></div></div>'); el.id = 'settings';
  const nav = q('.settings-nav', el), content = q('.settings-content', el);
  const entries = [{ id: 'general', name: 'General' }, { id: 'bots', name: 'Bots' }, { id: 'models', name: 'Local Models' }, { id: 'cloud-models', name: 'Cloud Models' }, { id: 'notifications', name: 'Notifications' }, ...ownerPlugins.map(p => ({ id: localID(p.id), name: p.name }))];
  const picker = document.createElement('select'); picker.setAttribute('aria-label', 'Settings machine'); picker.innerHTML = [...instances.keys()].map(id => `<option value="${id}">${esc(machineName(id))}</option>`).join(''); picker.value = node; picker.onchange = () => settings(section, picker.value, selectModel); q('.dialog-heading button', el).before(picker);
  nav.innerHTML = `<div class="settings-nav-main">${entries.map(e => `<button data-section="${esc(e.id)}" class="${section === e.id ? 'active' : ''}">${esc(e.name)}</button>`).join('')}</div><hr><button data-section="development" data-update-notice="*" class="${section === 'development' ? 'active' : ''}">Development</button>`;
  renderDevelopmentUpdates();
  for (const button of nav.querySelectorAll<HTMLButtonElement>('button')) button.onclick = () => settings(button.dataset.section, node, selectModel);
  if (section === 'general') mountGeneral(content, () => renderMessages(false, true));
  else if (section === 'bots') botSettings(content);
  else if (section === 'notifications') void notifications.mount(content);
  else if (section === 'models' || section === 'cloud-models') modelSettings(content, section === 'cloud-models', selectModel);
  else if (section === 'development') void mountDevelopment(content, boundAPI, new InstanceClient(node), async () => { await refresh(true); await swarmRefreshing; });
  else pluginSettings(content, ownerPlugins.find(p => localID(p.id) === section)!);
}
function botSettings(root: HTMLElement) { const bots = state.bots.filter(b => sameMachine(b, settingsNode));
  root.innerHTML = `<button class="primary" id="add-bot">＋ Add bot</button>${bots.map(b => `<div class="list-card"><div class="grow"><strong>${esc(b.name)}</strong><p class="subtle">@${esc(b.handle)} · ${esc(b.roots?.length ? b.roots.join(', ') : 'Unbound')}</p></div><button data-edit="${esc(b.id)}">Edit</button>${localID(b.id) !== 'echo' ? `<button class="danger" data-remove="${esc(b.id)}">Delete</button>` : ''}</div>`).join('')}`;
  q('#add-bot', root).onclick = () => editBot();
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-edit]')) button.onclick = () => editBot(currentBot(button.dataset.edit));
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-remove]')) button.onclick = action(async () => { await api(`/bots/${button.dataset.remove}`, 'DELETE'); await refresh(); botSettings(root); });
}
function editBot(bot?: Bot) {
  const node = bot?.nodeId || settingsNode || entryNode; const api = <T = any>(path: string, method = 'GET', body?: unknown) => resourceAPI<T>(path, method, body, node); const plugins = state.plugins.filter(p => sameMachine(p, node));
  const descriptions: Record<string, string> = {
    calendar: 'Create, update, and delete scheduled tasks for this bot.',
    comfyui: 'Generate and edit images, videos, and animated GIFs using the configured ComfyUI server.',
    confluence: 'Search Confluence and read pages, blog posts, and comments.',
    fossil: 'Inspect changes, commit, branch, and sync Fossil repositories using the installed Fossil CLI.',
    git: 'Inspect changes, commit, manage branches, and fetch, pull, or push Git repositories using the installed Git CLI.',
    jira: 'Search Jira issues and read their fields, links, and comments.',
    markdown: 'Display formatted Markdown in this bot\'s messages.',
    p4: 'Inspect, sync, edit, and submit files and changelists using the installed Perforce CLI.',
    searxng: 'Search the web using the configured SearXNG search engine.',
    shell: 'Run shell commands on this machine within this bot\'s assigned folders, or with unrestricted host access when no folders are assigned.',
    'web-get': 'Read web pages and other content from HTTP or HTTPS URLs.',
    workspaces: 'Let Echo assign or change folder restrictions for bots. Existing restrictions remain enforced when this plugin is disabled.',
    'plugin-authoring': 'Let Echo create, install, configure, and manage plugins.',
  };
  const multitaskingDescription = 'Allow this bot to work in multiple chats simultaneously. When disabled, requests wait their turn.';
  const checkbox = (name: string, label: string, description: string, checked: boolean) => `<label class="check" title="${esc(description)}"><input name="${esc(name)}" type="checkbox" title="${esc(description)}" ${checked ? 'checked' : ''}>${esc(label)}</label>`;
  const pluginCheckboxes = plugins.filter(p => p.scope === 'bot' || localID(p.id) === 'workspaces' || localID(bot?.id) === 'echo' && p.scope === 'echo').map(p => {
    const description = descriptions[localID(p.id)] || `Allow this bot to use ${p.name} tools and features.`;
    const status = p.enabled ? '' : ` Disabled globally; enable it in Settings → ${p.name} to use it.`;
    return checkbox(`plugin:${p.id}`, p.name, description + status, botPluginSelected(bot, p.id));
  }).join('');
  const el = dialog(bot ? 'Edit bot' : 'Create a bot', `<form class="form"><label>Handle<input name="handle" required pattern="[A-Za-z][A-Za-z0-9_-]*" value="${esc(bot?.handle.split('.').pop())}" ${localID(bot?.id) === 'echo' ? 'readonly' : ''}></label><label>Name<input name="name" value="${esc(bot?.name)}"></label><label>System prompt<textarea name="prompt">${esc(bot?.prompt)}</textarea></label><label>Folders, one absolute path per line<textarea name="roots" placeholder="C:\\Users\\you\\project">${esc(bot?.roots?.join('\n'))}</textarea><small>Leave empty for unrestricted host access. Assigned folders apply to file tools and shell commands.</small></label>${checkbox('enableMultitasking', 'Enable Multitasking', multitaskingDescription, !!bot?.enableMultitasking)}<small>${esc(multitaskingDescription)}</small><h3>Plugins</h3>${pluginCheckboxes}<button class="primary">Save bot</button></form>`);
  q<HTMLFormElement>('form', el).onsubmit = e => { e.preventDefault(); action(async () => { const f = new FormData(q('form', el) as HTMLFormElement), overrides: Record<string, boolean> = {}; for (const input of el.querySelectorAll<HTMLInputElement>('[name^="plugin:"]')) overrides[input.name.slice(7)] = input.checked; await api('/bots', 'POST', { id: bot?.id || '', handle: f.get('handle'), name: f.get('name'), prompt: f.get('prompt'), roots: String(f.get('roots')).split('\n').map(v => v.trim()).filter(Boolean), plugins: overrides, enableMultitasking: f.has('enableMultitasking'), revision: bot?.revision || 0 }); el.close(); await refresh(); settings('bots', node); })(); };
}
function modelSettings(root: HTMLElement, cloud: boolean, selectModel = true) {
  root.innerHTML = `${cloud ? `<p class="subtle">Connect a compatible cloud model service, then select it for the chat.</p>` : '<p class="subtle">Connect an existing Ollama, LM Studio, llama.cpp, or compatible server.</p>'}<button class="primary" id="add-model">＋ Add connection</button>${state.endpoints.filter(e => sameMachine(e, settingsNode) && e.cloud === cloud).map(e => `<div class="list-card"><div class="grow"><strong>${esc(e.name)}</strong><p class="subtle">${esc(e.model)}<br>${esc(e.url)}</p></div><button data-model="${esc(e.id)}">Edit</button><button data-discover="${esc(e.id)}">Models</button><button class="danger" data-remove="${esc(e.id)}">×</button></div>`).join('')}`;
  q('#add-model', root).onclick = () => editModel(undefined, cloud, selectModel);
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-model]')) button.onclick = () => editModel(state.endpoints.find(e => e.id === button.dataset.model), cloud, selectModel);
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-discover]')) button.onclick = action(async () => { const models = await api(`/endpoints/${button.dataset.discover}/models`); dialog('Available models', `<pre>${esc(JSON.stringify(models, null, 2))}</pre>`); });
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-remove]')) button.onclick = action(async () => { await api(`/endpoints/${button.dataset.remove}`, 'DELETE'); await refresh(); modelSettings(root, cloud, selectModel); });
}
function editModel(endpoint?: Endpoint, cloud = false, selectModel = true) {
  const node = endpoint?.nodeId || settingsNode; const api = <T = any>(path: string, method = 'GET', body?: unknown) => resourceAPI<T>(path, method, body, node);
  const el = dialog(endpoint ? 'Edit Endpoint' : 'Add Endpoint', endpointForm(endpoint, cloud)); el.classList.add('endpoint-editor');
  const effort = q<HTMLSelectElement>('[name=reasoningEffort]', el), budget = q<HTMLInputElement>('[name=thinkingTokenBudget]', el);
  effort.onchange = () => { budget.disabled = !!effort.value; }; effort.onchange(new Event('change'));
  q<HTMLInputElement>('[name=compressionThreshold]', el).oninput = event => { q('.compression-setting output', el).textContent = (event.target as HTMLInputElement).value + '%'; };
  const provider = document.createElement('label'); provider.innerHTML = `Provider<select name="provider"><option value="">Compatible HTTP</option>${state.plugins.filter(p => sameMachine(p, node) && !p.builtin && p.enabled && !p.pending).flatMap(p => (p.providers || []).map(name => `<option value="${esc(name)}">${esc(p.name)} · ${esc(name)}</option>`)).join('')}</select>`;
  q('form', el).prepend(provider); q<HTMLSelectElement>('select', provider).value = endpoint?.provider || '';
  q<HTMLFormElement>('form', el).onsubmit = e => { e.preventDefault(); action(async () => { const form = q<HTMLFormElement>('form', el), key = q<HTMLInputElement>('[name=key]', el).value; const value = await api<Endpoint>('/endpoints', 'POST', { endpoint: { id: endpoint?.id || '', cloud, ...endpointValues(form) }, ...(key ? { apiKey: key } : {}) }); el.close(); await refresh(); if (selectModel) chooseEndpoint({ ...value, id: resourceID(node, value.id), nodeId: node }); settings(cloud ? 'cloud-models' : 'models', node, selectModel); })(); };
}
function pluginSettings(root: HTMLElement, plugin: Plugin) {
  if (!plugin) return;
  const node = plugin.nodeId || entryNode; const api = <T = any>(path: string, method = 'GET', body?: unknown) => resourceAPI<T>(path, method, body, node); const id = localID(plugin.id);
  if (id === 'jira') { mountJiraSettings(root, plugin, api, async () => { await refresh(); settings(plugin.id); }, notice); return; }
  if (id === 'confluence') { mountConfluenceSettings(root, plugin, api, async () => { await refresh(); settings(plugin.id); }, notice); return; }
  root.innerHTML = `${pluginNetworkNotice(plugin)}<form class="form"><label class="check"><input name="enabled" type="checkbox" ${plugin.enabled ? 'checked' : ''}> Enable ${esc(plugin.name)}</label>${plugin.pending ? `<p>Additional permissions requested: ${esc((plugin.capabilities || []).join(', ') || 'Network destinations listed above')}</p><button type="button" id="approve-plugin">Approve permissions</button>` : ''}${Object.entries(plugin.settings?.properties || {}).map(([key, schema]) => { const value = plugin.config[key]; if (schema.type === 'boolean') return `<label class="check"><input name="${esc(key)}" type="checkbox" ${value ? 'checked' : ''}>${esc(schema.description || key)}</label>`; if (schema.type === 'array') return `<label>${esc(schema.description || key)}<textarea name="${esc(key)}">${esc(Array.isArray(value) ? value.join('\n') : '')}</textarea></label>`; return `<label>${esc(schema.description || key)}<input name="${esc(key)}" type="${schema.type === 'integer' ? 'number' : 'text'}" value="${esc(value)}"></label>`; }).join('')}<button class="primary">Save settings</button></form>${!plugin.builtin ? '<div class="button-row"><button id="rollback-plugin">Restore previous version</button></div>' : ''}${(plugin.views || []).filter(v => v.kind !== 'renderer').map(v => `<button class="open-view" data-view="${esc(v.id)}">Open ${esc(v.title)}</button>`).join('')}<div id="plugin-extra"></div>`;
  q<HTMLFormElement>('form', root).onsubmit = e => { e.preventDefault(); action(async () => { const f = new FormData(q('form', root) as HTMLFormElement), config: Record<string, unknown> = {}; for (const [key, schema] of Object.entries(plugin.settings?.properties || {})) { if (schema.type === 'boolean') config[key] = f.has(key); else if (schema.type === 'integer' || schema.type === 'number') config[key] = Number(f.get(key)); else if (schema.type === 'array') config[key] = String(f.get(key)).split('\n').map(v => v.trim()).filter(Boolean); else config[key] = String(f.get(key)); } await api(`/plugins/${plugin.id}`, 'PATCH', { enabled: f.has('enabled'), config }); await refresh(); notice(id === 'hosted-server' ? 'Saved. Restart Echo to change its listening address.' : 'Settings saved'); })(); };
  root.querySelector<HTMLButtonElement>('#approve-plugin')?.addEventListener('click', action(async () => { await api(`/plugins/${plugin.id}/approve`, 'POST', { digest: plugin.digest }); await refresh(); settings(plugin.id); }));
  root.querySelector<HTMLButtonElement>('#rollback-plugin')?.addEventListener('click', action(async () => { await api(`/plugins/${plugin.id}/rollback`, 'POST', {}); await refresh(); settings(plugin.id); }));
  for (const button of root.querySelectorAll<HTMLButtonElement>('.open-view')) button.onclick = action(() => openPlugin(plugin.id, button.dataset.view!, 'echo'));
  if (id === 'hosted-server') {
    mountHosting(q('#plugin-extra', root), api, notice);
  }
  if (id === 'swarm') void mountSwarm(q('#plugin-extra', root), new InstanceClient(node), refresh, notice);
  if (id === 'calendar') calendarSettings(q('#plugin-extra', root));
  if (id === 'workspaces') q('#plugin-extra', root).innerHTML = '<p class="subtle">Assign folders in Bots → Edit, or ask Echo to bind a bot. Existing folder restrictions stay enforced when this plugin is disabled.</p>';
  if (id === 'comfyui') q('#plugin-extra', root).innerHTML = '<p class="subtle">Connect an existing ComfyUI server and choose a checkpoint. Export custom workflows in ComfyUI API format. Image generation has a built-in fallback; video generation needs a workflow. Ask a bot to generate an image or video, or attach an image and ask it to edit or animate it.</p><p class="subtle">Templates support {{PROMPT}}, {{NEGATIVE_PROMPT}}, {{MODEL}}, {{IMAGE}}, {{WIDTH}}, {{HEIGHT}}, {{STEPS}}, {{CFG_SCALE}}, {{SEED}}, {{FRAMES}}, {{FPS}}, {{FORMAT}}, {{DURATION}}, {{ASPECT_RATIO}} and {{MEGAPIXELS}}. Workflow and input-image paths must be accessible to the bot. Results remain in the chat; ask to save them when you want a workspace file.</p>';
  if (id === 'plugin-authoring') q('#plugin-extra', root).innerHTML = '<p class="subtle">Ask Echo to create a plugin—for example, “Create a GUI calculator I can drag around.” UI plugins need no compiler. Go backend plugins require an installed Go toolchain.</p>';
  if (['git', 'fossil', 'p4'].includes(id)) q('#plugin-extra', root).innerHTML = `<p class="subtle">Uses the installed CLI, independently of Shell. Ask a bot to inspect changes, create commits or manage its repository. Commands stream into chat and stop with the run. Bound bots retain their folder restrictions, including when network access is granted; these network grants also apply to native child processes. Unbound bots retain unrestricted host access.</p>${id === 'git' ? '<p class="subtle">Set the author name/email for commits. Scoped Git uses reftable for new repositories; some older loose-ref repositories are unsupported.</p>' : ''}${id === 'fossil' ? '<p class="subtle">Grant access to both the checkout and its Fossil repository database. Interactive editors are unavailable; provide commit messages in chat.</p>' : ''}`;
  if (id === 'p4') {
    q('#plugin-extra', root).insertAdjacentHTML('beforeend', `<hr><p class="subtle">Install the native p4 CLI and save the server, user and client above. Save a ticket for that server/user below; it is kept in the host credential vault and redacted from command output. A bound bot needs the appropriate network grant to reach Perforce. SSL servers need a trusted fingerprint file within the bot's folders.</p><form class="form" id="p4-secret-form"><label>P4 ticket or password<input name="secret" type="password" autocomplete="new-password" required placeholder="${plugin.config.credential ? 'Credential is saved' : 'No credential saved'}"></label><div class="button-row"><button class="primary">Save P4 credential</button><button type="button" id="clear-p4-secret">Clear credential</button></div></form>`);
    q<HTMLFormElement>('#p4-secret-form', root).onsubmit = event => { event.preventDefault(); action(async () => { const form = q<HTMLFormElement>('#p4-secret-form', root); await api('/plugins/p4/credential', 'POST', { secret: new FormData(form).get('secret') }); form.reset(); await refresh(); settings('p4'); notice('P4 credential saved'); })(); };
    q('#clear-p4-secret', root).onclick = action(async () => { await api('/plugins/p4/credential', 'POST', { secret: '' }); await refresh(); settings('p4'); });
  }
}
function calendarSettings(root: HTMLElement) { const schedules = state.schedules.filter(s => sameMachine(s, settingsNode));
  root.innerHTML = `<hr><h3>Schedules</h3><button id="new-schedule">＋ Add schedule</button>${schedules.map(s => `<div class="list-card"><div class="grow"><strong>${esc(currentBot(s.botId)?.name || s.botId)}</strong><p>${esc(s.prompt)}</p><small>${esc(s.recurrence)} · ${esc(s.timezone)} ${s.paused ? '· Paused' : ''}</small></div><button data-schedule="${esc(s.id)}">Edit</button><button class="danger" data-delete-schedule="${esc(s.id)}">×</button></div>`).join('')}`;
  q('#new-schedule', root).onclick = () => editSchedule();
  for (const b of root.querySelectorAll<HTMLButtonElement>('[data-schedule]')) b.onclick = () => editSchedule(state.schedules.find(s => s.id === b.dataset.schedule));
  for (const b of root.querySelectorAll<HTMLButtonElement>('[data-delete-schedule]')) b.onclick = action(async () => { await api(`/schedules/${b.dataset.deleteSchedule}`, 'DELETE'); await refresh(); calendarSettings(root); });
}
function editSchedule(schedule?: Schedule) { const node = schedule?.nodeId || settingsNode; const api = <T = any>(path: string, method = 'GET', body?: unknown) => resourceAPI<T>(path, method, body, node); const bots = state.bots.filter(b => sameMachine(b, node)); const endpoint = state.endpoints.find(e => sameMachine(e, node)); const selectedGeneration = generationByNode[node] || (node === modelNode && generation.endpointId ? generation : { endpointId: endpoint?.id || '', model: endpoint?.model || '', thinking: '', temperature: endpoint?.temperature ?? .6 });
  const el = dialog('Scheduled work', `<form class="form"><label>Bot<select name="botId">${bots.map(b => `<option value="${esc(b.id)}" ${b.id === schedule?.botId ? 'selected' : ''}>${esc(b.name)}</option>`).join('')}</select></label><label>Prompt<textarea name="prompt" required>${esc(schedule?.prompt)}</textarea></label><label>Recurrence<input name="recurrence" value="${esc(schedule?.recurrence || 'daily 15:00')}"><small>daily 15:00 · weekly MON 09:00 · every 2 hours · ISO date/time</small></label><label>Timezone<input name="timezone" value="${esc(schedule?.timezone || 'Local')}"><small>Local uses the Echo server's timezone; an IANA name such as America/Chicago is also accepted.</small></label><label class="check"><input name="paused" type="checkbox" ${schedule?.paused ? 'checked' : ''}>Paused</label><p class="subtle">Model: ${esc((schedule?.generation || selectedGeneration).model || 'Select a model in the chat first')}</p><button class="primary">Save schedule</button></form>`);
  q<HTMLFormElement>('form', el).onsubmit = e => { e.preventDefault(); action(async () => { const f = new FormData(q('form', el) as HTMLFormElement); await api('/schedules', 'POST', { id: schedule?.id || '', botId: f.get('botId'), prompt: f.get('prompt'), recurrence: f.get('recurrence'), timezone: f.get('timezone'), paused: f.has('paused'), generation: schedule?.generation || selectedGeneration }); el.close(); await refresh(); settings('calendar'); })(); };
}
function showMissed() {
  const jobs = state.occurrences.filter(o => o.status === 'missed').sort((a, b) => a.intendedAt.localeCompare(b.intendedAt));
  const el = dialog('Missed scheduled work', `<p class="subtle">Choose which missed jobs to run. Unselected jobs will be skipped.</p><form class="form"><div class="missed-jobs">${jobs.map(o => `<label class="missed-job"><input type="checkbox" name="job" value="${esc(o.id)}"><div><strong>${esc(currentBot(o.botId)?.name || o.botId)}</strong> · ${esc(new Date(o.intendedAt).toLocaleString(undefined, { timeZone: o.timezone || 'UTC' }))}<p>${esc(o.prompt)}</p><small>${esc(o.recurrence || 'Scheduled work')} · ${esc(o.timezone || 'UTC')} · ${esc(o.id)}</small></div></label>`).join('')}</div><label>Concurrent jobs<input name="concurrency" type="number" min="0" step="1" value="1" required><small>0 means unlimited.</small></label><div class="button-row"><button class="primary">Run selected</button><button id="skip-all" type="button">Skip all</button></div></form>`);
  const submit = async (skip: boolean) => { const f = new FormData(q('form', el) as HTMLFormElement); await Promise.all([...instances.keys()].map(node => resourceAPI('/missed/review', 'POST', { ids: skip ? [] : f.getAll('job').map(String).filter(id => splitID(id)[0] === node).map(localID), concurrency: Number(f.get('concurrency')) }, node))); el.close(); await refresh(); };
  q<HTMLFormElement>('form', el).onsubmit = e => { e.preventDefault(); action(() => submit(false))(); }; q('#skip-all', el).onclick = action(() => submit(true));
}

async function openPlugin(id: string, viewId: string, botId = 'echo') {
  await refresh();
  await pluginWindows.open(id, viewId, botId);
}

async function start() {
  try { await api('/session', 'POST', {}); }
  catch { app.innerHTML = '<form class="login form"><div class="brand">Echo</div><label>Password<input name="password" type="password" autocomplete="current-password" required></label><button class="primary">Sign in</button></form>'; q<HTMLFormElement>('form').onsubmit = e => { e.preventDefault(); action(async () => { const f = new FormData(q('form') as HTMLFormElement); await api('/session', 'POST', { password: f.get('password') }); await ready(); })(); }; return; }
  await ready();
}
async function ready() { const route = parseAppRoute(location.hash); shell(); await refresh(); if (pendingNotificationHash) await openNotification(pendingNotificationHash); else if (route) await pluginWindows.open(route.pluginId, route.viewId, route.botId, 'embedded'); else if (chatId && !showingApps) await selectChat(chatId); else renderMessages(); connect(); connectSwarm(); startDevelopmentUpdates(); void notifications.start(); }
void start().catch(e => notice(e, true));

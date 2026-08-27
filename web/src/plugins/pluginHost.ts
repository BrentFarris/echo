import { post } from "../../js/api.js";
import * as socket from "../../js/ws.js";
import { codeRouteHash } from "../navigation";
import { installChatMap } from "../chatMap";
import { renderMobilePrimaryNav, renderPrimaryNav } from "../primaryNav";
import {
  clearPluginCatalog,
  getEffectivePluginViews,
  getPluginCatalog,
  getPluginWorkspaceId,
  refreshPluginCatalog,
  renderDesktopPluginButtons,
  renderMobilePluginOverflowButton,
} from "./catalog";
import type { CatalogPlugin, PluginUISession, PluginView } from "./types";

type FrameContext = {
  iframe: HTMLIFrameElement;
  plugin: CatalogPlugin;
  view: PluginView;
  session: PluginUISession;
  close: () => void;
  setTitle: (title: string) => void;
};

let hostRoot: HTMLElement | null = null;
let mobileMenu: HTMLElement | null = null;
let initialized = false;
let zIndex = 40;
let themeObserver: MutationObserver | null = null;
const frames = new Set<FrameContext>();
const floating = new Map<string, { element: HTMLElement; context: FrameContext }>();
const refreshingFrames = new Set<FrameContext>();

export async function initializePluginHost(): Promise<void> {
  if (!hostRoot) {
    hostRoot = document.createElement("div");
    hostRoot.id = "echo-plugin-host";
    hostRoot.setAttribute("aria-live", "polite");
    document.body.append(hostRoot);
  }
  if (!initialized) {
    initialized = true;
    document.addEventListener("click", onDocumentClick);
    window.addEventListener("message", onPluginMessage);
    window.addEventListener("resize", clampFloatingWindows);
    window.addEventListener("echo:plugin-catalog", onCatalogUpdated);
    window.addEventListener("echo:workspace-changed", () => { void refreshAndReconcile(); });
    socket.on("plugins_changed", () => { void refreshAndReconcile(); });
    socket.on("plugin_runtime_event", onRuntimeEvent);
    window.addEventListener("echo:theme-changed", broadcastTheme);
    themeObserver = new MutationObserver(broadcastTheme);
    themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "style", "data-theme"] });
    if (typeof window.matchMedia === "function") window.matchMedia("(prefers-color-scheme: dark)").addEventListener?.("change", broadcastTheme);
  }
  await refreshAndReconcile();
}

export function resetPluginHost(): void {
  for (const entry of [...floating.values()]) entry.context.close();
  for (const context of [...frames]) context.close();
  closeMobileMenu();
  clearPluginCatalog();
}

async function refreshAndReconcile(): Promise<void> {
  try {
    await refreshPluginCatalog();
  } catch (error) {
    console.error("failed to refresh plugin catalog", error);
  }
}

function onCatalogUpdated(): void {
  document.querySelectorAll<HTMLElement>("[data-plugin-nav-section]").forEach(section => { section.innerHTML = renderDesktopPluginButtons(); });
  document.querySelectorAll<HTMLElement>("[data-plugin-mobile-slot]").forEach(section => { section.innerHTML = renderMobilePluginOverflowButton(); });
	for (const context of [...frames]) {
		const match = getView(context.plugin.id, context.view.id, context.view.kind);
		if (!match) {
			context.close();
			continue;
		}
		if (context.session.workspaceId !== getPluginWorkspaceId() || context.session.digest !== match.plugin.digest || context.session.revision !== match.plugin.revision) {
			void refreshFrameSession(context, match.plugin, match.view);
		} else {
			context.plugin = match.plugin;
			context.view = match.view;
		}
	}
  const routeMatch = routePluginMatch();
  if (routeMatch && !getView(routeMatch.pluginId, routeMatch.viewId, "page")) location.hash = "#/home";
}

async function refreshFrameSession(context: FrameContext, plugin: CatalogPlugin, view: PluginView): Promise<void> {
	if (refreshingFrames.has(context)) return;
	refreshingFrames.add(context);
	const oldToken = context.session.bridgeToken;
	try {
		const session = await createSession(plugin, view);
		if (!frames.has(context)) {
			void fetch(`/api/plugins/ui-sessions/${encodeURIComponent(session.bridgeToken)}`, { method: "DELETE" });
			return;
		}
		context.plugin = plugin;
		context.view = view;
		context.session = session;
		context.setTitle(view.title);
		context.iframe.src = session.entryUrl;
		void fetch(`/api/plugins/ui-sessions/${encodeURIComponent(oldToken)}`, { method: "DELETE" });
	} catch (error) {
		context.close();
		showPluginNotice(error instanceof Error ? error.message : String(error), true);
	} finally {
		refreshingFrames.delete(context);
		if (frames.has(context) && context.session.workspaceId !== getPluginWorkspaceId()) {
			const latest = getView(context.plugin.id, context.view.id, context.view.kind);
			if (latest) void refreshFrameSession(context, latest.plugin, latest.view);
		}
	}
}

function onDocumentClick(event: MouseEvent): void {
  const target = event.target instanceof Element ? event.target : null;
  const pluginButton = target?.closest<HTMLElement>("[data-plugin-id][data-plugin-view-id]");
  if (pluginButton) {
    event.preventDefault();
    const pluginId = pluginButton.dataset.pluginId || "";
    const viewId = pluginButton.dataset.pluginViewId || "";
    const match = getView(pluginId, viewId);
    if (match?.view.kind === "page") location.hash = `#/plugins/${encodeURIComponent(pluginId)}/${encodeURIComponent(viewId)}`;
    else if (match) void toggleFloating(match.plugin, match.view);
    closeMobileMenu();
    return;
  }
  const overflow = target?.closest<HTMLElement>("[data-plugin-overflow]");
  if (overflow) {
    event.preventDefault();
    event.stopPropagation();
    if (mobileMenu) closeMobileMenu(); else openMobileMenu(overflow);
    return;
  }
  if (mobileMenu && !target?.closest("[data-plugin-mobile-menu]")) closeMobileMenu();
}

function openMobileMenu(anchor: HTMLElement): void {
  const views = getEffectivePluginViews();
  if (!views.length) return;
  mobileMenu = document.createElement("div");
  mobileMenu.className = "plugin-mobile-menu";
  mobileMenu.dataset.pluginMobileMenu = "";
  mobileMenu.innerHTML = `<strong>Plugins</strong>${views.map(({ plugin, view }) => `
    <button type="button" data-plugin-id="${escapeHTML(plugin.id)}" data-plugin-view-id="${escapeHTML(view.id)}">
      ${view.icon ? `<img src="${escapeHTML(view.icon)}" alt="">` : '<span class="codicon codicon-extensions"></span>'}
      <span>${escapeHTML(view.title)}</span><small>${view.kind === "floating" ? "Window" : plugin.name}</small>
    </button>`).join("")}`;
  document.body.append(mobileMenu);
  const rect = anchor.getBoundingClientRect();
  mobileMenu.style.right = `${Math.max(8, innerWidth - rect.right)}px`;
  mobileMenu.style.bottom = `${Math.max(68, innerHeight - rect.top + 8)}px`;
  anchor.setAttribute("aria-expanded", "true");
}

function closeMobileMenu(): void {
  mobileMenu?.remove();
  mobileMenu = null;
  document.querySelectorAll("[data-plugin-overflow]").forEach(button => button.setAttribute("aria-expanded", "false"));
}

function getView(pluginId: string, viewId: string, kind?: PluginView["kind"]): { plugin: CatalogPlugin; view: PluginView } | null {
  return getEffectivePluginViews().find(entry => entry.plugin.id === pluginId && entry.view.id === viewId && (!kind || entry.view.kind === kind)) || null;
}

async function createSession(plugin: CatalogPlugin, view: PluginView): Promise<PluginUISession> {
  const data = await post(`/api/plugins/${encodeURIComponent(plugin.id)}/views/${encodeURIComponent(view.id)}/sessions`, { workspaceId: getPluginWorkspaceId() }) as { session: PluginUISession };
  return data.session;
}

async function createFrame(plugin: CatalogPlugin, view: PluginView, close: () => void, setTitle: (title: string) => void): Promise<FrameContext> {
  const session = await createSession(plugin, view);
  const iframe = document.createElement("iframe");
  iframe.className = "plugin-frame";
  iframe.title = `${view.title} plugin`;
  iframe.setAttribute("sandbox", "allow-scripts");
  iframe.setAttribute("referrerpolicy", "no-referrer");
  iframe.src = session.entryUrl;
  const context = { iframe, plugin, view, session, close, setTitle };
  frames.add(context);
  return context;
}

async function toggleFloating(plugin: CatalogPlugin, view: PluginView): Promise<void> {
  const key = `${plugin.id}/${view.id}`;
  const existing = floating.get(key);
  if (existing) {
    existing.context.close();
    return;
  }
  if (!hostRoot) return;
  const element = document.createElement("section");
  element.className = "plugin-floating-window";
  element.tabIndex = -1;
  element.dataset.pluginWindow = key;
  element.innerHTML = `<header class="plugin-window-header" tabindex="0"><span class="plugin-window-title">${escapeHTML(view.title)}</span><button type="button" aria-label="Close ${escapeHTML(view.title)}" data-plugin-close><span class="codicon codicon-close"></span></button></header><div class="plugin-window-body"></div><button class="plugin-resize-handle" type="button" aria-label="Resize ${escapeHTML(view.title)}"></button>`;
  const close = () => {
    floating.delete(key);
    const context = floatingContext;
    frames.delete(context);
    element.remove();
    void fetch(`/api/plugins/ui-sessions/${encodeURIComponent(context.session.bridgeToken)}`, { method: "DELETE" });
  };
  let floatingContext: FrameContext;
  try {
    floatingContext = await createFrame(plugin, view, close, title => {
      element.querySelector<HTMLElement>(".plugin-window-title")!.textContent = title;
    });
  } catch (error) {
    showPluginNotice(error instanceof Error ? error.message : String(error), true);
    return;
  }
  element.querySelector(".plugin-window-body")!.append(floatingContext.iframe);
  hostRoot.append(element);
  floating.set(key, { element, context: floatingContext });
  restoreLayout(element, plugin, view);
  wireFloatingWindow(element, plugin, view, close);
  focusWindow(element);
}

function wireFloatingWindow(element: HTMLElement, plugin: CatalogPlugin, view: PluginView, close: () => void): void {
  const header = element.querySelector<HTMLElement>(".plugin-window-header")!;
  const resize = element.querySelector<HTMLElement>(".plugin-resize-handle")!;
  element.querySelector("[data-plugin-close]")?.addEventListener("click", close);
  element.addEventListener("pointerdown", () => focusWindow(element));
  pointerOperation(header, element, "move", plugin, view);
  pointerOperation(resize, element, "resize", plugin, view);
  header.addEventListener("keydown", event => {
    if (event.key === "Escape") { close(); return; }
    if (!event.altKey || !event.key.startsWith("Arrow")) return;
    const amount = event.shiftKey ? 20 : 8;
    const rect = element.getBoundingClientRect();
    if (event.ctrlKey || event.metaKey) {
      const width = rect.width + (event.key === "ArrowRight" ? amount : event.key === "ArrowLeft" ? -amount : 0);
      const height = rect.height + (event.key === "ArrowDown" ? amount : event.key === "ArrowUp" ? -amount : 0);
      setWindowRect(element, rect.left, rect.top, width, height, view);
    } else {
      const left = rect.left + (event.key === "ArrowRight" ? amount : event.key === "ArrowLeft" ? -amount : 0);
      const top = rect.top + (event.key === "ArrowDown" ? amount : event.key === "ArrowUp" ? -amount : 0);
      setWindowRect(element, left, top, rect.width, rect.height, view);
    }
    persistLayout(element, plugin, view);
    event.preventDefault();
  });
}

function pointerOperation(handle: HTMLElement, element: HTMLElement, operation: "move" | "resize", plugin: CatalogPlugin, view: PluginView): void {
  handle.addEventListener("pointerdown", event => {
    if (operation === "move" && (event.target as Element).closest("button")) return;
    const start = element.getBoundingClientRect();
    const originX = event.clientX; const originY = event.clientY;
    handle.setPointerCapture(event.pointerId);
    const move = (next: PointerEvent) => {
      const dx = next.clientX - originX; const dy = next.clientY - originY;
      setWindowRect(element, start.left, start.top, operation === "resize" ? start.width + dx : start.width, operation === "resize" ? start.height + dy : start.height, view, operation === "move" ? dx : 0, operation === "move" ? dy : 0);
    };
    const up = () => {
      handle.removeEventListener("pointermove", move);
      persistLayout(element, plugin, view);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", up, { once: true });
    event.preventDefault();
  });
}

function setWindowRect(element: HTMLElement, left: number, top: number, width: number, height: number, view: PluginView, dx = 0, dy = 0): void {
  const minWidth = view.minimumSize?.width || 260; const minHeight = view.minimumSize?.height || 320;
  width = Math.min(innerWidth - 16, Math.max(minWidth, width));
  height = Math.min(innerHeight - 16, Math.max(minHeight, height));
  left = Math.max(8, Math.min(innerWidth - width - 8, left + dx));
  top = Math.max(8, Math.min(innerHeight - height - 8, top + dy));
  Object.assign(element.style, { left: `${left}px`, top: `${top}px`, width: `${width}px`, height: `${height}px` });
}

function restoreLayout(element: HTMLElement, plugin: CatalogPlugin, view: PluginView): void {
  const width = Math.min(innerWidth - 16, view.defaultSize?.width || 360);
  const height = Math.min(innerHeight - 16, view.defaultSize?.height || 500);
	const layout = readLayout(layoutKey(plugin, view));
  setWindowRect(element, layout?.left ?? innerWidth - width - 28, layout?.top ?? 48, layout?.width ?? width, layout?.height ?? height, view);
}

function readLayout(key: string): { left: number; top: number; width: number; height: number } | null {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key) || "null");
    if (!value || typeof value !== "object") return null;
    const candidate = value as Record<string, unknown>;
    if ([candidate.left, candidate.top, candidate.width, candidate.height].every(item => typeof item === "number")) {
      return candidate as { left: number; top: number; width: number; height: number };
    }
  } catch { /* ignore corrupt browser state */ }
  return null;
}

function persistLayout(element: HTMLElement, plugin: CatalogPlugin, view: PluginView): void {
  const rect = element.getBoundingClientRect();
  localStorage.setItem(layoutKey(plugin, view), JSON.stringify({ left: rect.left, top: rect.top, width: rect.width, height: rect.height }));
}

function layoutKey(plugin: CatalogPlugin, view: PluginView): string { return `echo.plugin-layout.v1.${plugin.id}.${view.id}`; }
function focusWindow(element: HTMLElement): void { element.style.zIndex = String(++zIndex); }
function clampFloatingWindows(): void { for (const { element, context } of floating.values()) { const rect = element.getBoundingClientRect(); setWindowRect(element, rect.left, rect.top, rect.width, rect.height, context.view); } }

export async function mountPluginPage(root: HTMLElement, pluginId: string, viewId: string): Promise<{ unmount: () => void }> {
  await refreshPluginCatalog();
  const match = getView(pluginId, viewId, "page");
  if (!match) throw new Error("This plugin view is not installed or enabled for the active workspace.");
  root.innerHTML = `<div class="plugin-page-shell"><div data-region="left-nav">${renderPrimaryNav({ active: "plugin", workspaceName: match.plugin.name })}</div><main class="plugin-page-main"><div class="plugin-page-loading">Opening ${escapeHTML(match.view.title)}…</div></main>${renderMobilePrimaryNav({ active: "plugin", workspaceName: match.plugin.name })}</div>`;
  const main = root.querySelector<HTMLElement>(".plugin-page-main")!;
  let disposed = false;
  let context: FrameContext | null = null;
  const close = () => { if (!disposed) location.hash = "#/home"; };
  context = await createFrame(match.plugin, match.view, close, title => { context!.iframe.title = `${title} plugin`; });
  if (disposed) {
    frames.delete(context);
    return { unmount: () => undefined };
  }
  main.replaceChildren(context.iframe);
  bindPageCoreNavigation(root);
  const disposeChatMap = installChatMap(root);
  return { unmount: () => {
    disposed = true;
    disposeChatMap();
    if (!context) return;
    frames.delete(context);
    context.iframe.remove();
    void fetch(`/api/plugins/ui-sessions/${encodeURIComponent(context.session.bridgeToken)}`, { method: "DELETE" });
  } };
}

function bindPageCoreNavigation(root: HTMLElement): void {
  root.querySelectorAll<HTMLElement>("[data-nav]").forEach(button => button.addEventListener("click", () => {
    const nav = button.dataset.nav;
    if (nav === "chat" || nav === "workspace") location.hash = "#/home";
    else if (nav === "settings") location.hash = "#/settings";
    else if (nav === "code") location.hash = "#/code";
    else if (nav === "search") location.hash = codeRouteHash("search");
    else if (nav === "git") location.hash = codeRouteHash("git");
  }));
}

function onPluginMessage(event: MessageEvent): void {
  const context = [...frames].find(candidate => event.source === candidate.iframe.contentWindow);
  if (!context || !event.data || typeof event.data !== "object") return;
  const message = event.data as Record<string, unknown>;
  if (message.type === "echo-plugin-ready" && message.protocol === "echo-ui-bridge-1") {
    context.iframe.contentWindow?.postMessage({
      type: "echo-plugin-init", protocol: "echo-ui-bridge-1", nonce: context.session.nonce,
      pluginId: context.plugin.id, viewId: context.view.id, sessionId: context.session.id,
      workspaceId: context.session.workspaceId || "", config: context.session.config || {}, theme: themeTokens(),
    }, "*");
    return;
  }
  if (message.type !== "echo-plugin-request" || message.nonce !== context.session.nonce || message.pluginId !== context.plugin.id || message.viewId !== context.view.id) return;
  const id = typeof message.id === "string" ? message.id.slice(0, 128) : "";
  const method = typeof message.method === "string" ? message.method : "";
  const params = message.params && typeof message.params === "object" ? message.params as Record<string, unknown> : {};
  if (!id) return;
  void handleBridgeRequest(context, id, method, params);
}

async function handleBridgeRequest(context: FrameContext, id: string, method: string, params: Record<string, unknown>): Promise<void> {
  try {
    let result: unknown;
    if (method === "window.close") { context.close(); result = { closed: true }; }
    else if (method === "window.setTitle") {
      const title = String(params.title || "").trim().slice(0, 100);
      if (!title) throw new Error("A window title is required.");
      context.setTitle(title); result = { title };
    } else if (method === "notification.show") {
      requirePermission(context.plugin, "ui.notifications");
      showPluginNotice(String(params.message || "").slice(0, 500)); result = { shown: true };
    } else if (method === "clipboard.write") {
      requirePermission(context.plugin, "ui.clipboard-write");
      await navigator.clipboard.writeText(String(params.text || "").slice(0, 1 << 20)); result = { written: true };
    } else if (method === "external.open") {
      requirePermission(context.plugin, "ui.external-links");
      const url = new URL(String(params.url || ""));
      if (url.protocol !== "https:" && url.protocol !== "http:") throw new Error("Only HTTP links can be opened.");
      if (!window.confirm(`${context.plugin.name} wants to open:\n\n${url.toString()}`)) throw new Error("Opening the link was canceled.");
      window.open(url, "_blank", "noopener,noreferrer"); result = { opened: true };
    } else if (["rpc.invoke", "storage.get", "storage.set", "storage.delete"].includes(method)) {
      const data = await post(`/api/plugins/ui-sessions/${encodeURIComponent(context.session.bridgeToken)}/bridge`, { nonce: context.session.nonce, method, params }) as { result: unknown };
      result = data.result;
    } else throw new Error("UI bridge method is not allowed.");
    respond(context, id, { result });
  } catch (error) {
    respond(context, id, { error: error instanceof Error ? error.message : String(error) });
  }
}

function respond(context: FrameContext, id: string, response: Record<string, unknown>): void {
  context.iframe.contentWindow?.postMessage({ type: "echo-plugin-response", nonce: context.session.nonce, pluginId: context.plugin.id, viewId: context.view.id, id, ...response }, "*");
}

function requirePermission(plugin: CatalogPlugin, name: string): void {
  if (!(plugin.permissions || []).some(permission => permission.name === name)) throw new Error(`Plugin permission ${name} was not approved.`);
}

function onRuntimeEvent(message: unknown): void {
  const envelope = message as { event?: { pluginId?: string; type?: string; sessionId?: string; topic?: string; data?: unknown } };
  const event = envelope.event;
  if (!event || event.type !== "ui_event") return;
  for (const context of frames) {
    if (context.plugin.id === event.pluginId && context.session.id === event.sessionId) context.iframe.contentWindow?.postMessage({ type: "echo-plugin-event", nonce: context.session.nonce, pluginId: context.plugin.id, viewId: context.view.id, topic: event.topic, data: event.data }, "*");
  }
}

function broadcastTheme(): void {
  const theme = themeTokens();
  for (const context of frames) {
    context.iframe.contentWindow?.postMessage({
      type: "echo-plugin-theme", nonce: context.session.nonce,
      pluginId: context.plugin.id, viewId: context.view.id, theme,
    }, "*");
  }
}

function themeTokens(): Record<string, string> {
  const style = getComputedStyle(document.documentElement);
  return {
    "--echo-background": style.getPropertyValue("--color-bg").trim(),
    "--echo-surface": style.getPropertyValue("--color-surface").trim(),
    "--echo-surface-raised": style.getPropertyValue("--color-surface-muted").trim(),
    "--echo-border": style.getPropertyValue("--color-border").trim(),
    "--echo-text": style.getPropertyValue("--color-text").trim(),
    "--echo-text-muted": style.getPropertyValue("--color-text-muted").trim(),
    "--echo-accent": style.getPropertyValue("--color-accent").trim(),
    "--echo-accent-contrast": style.getPropertyValue("--color-on-accent").trim(),
    "--echo-danger": style.getPropertyValue("--color-danger").trim(),
  };
}

function showPluginNotice(message: string, error = false): void {
  if (!hostRoot) return;
  const notice = document.createElement("div");
  notice.className = `plugin-notice${error ? " is-error" : ""}`;
  notice.textContent = message;
  hostRoot.append(notice);
  setTimeout(() => notice.remove(), 5000);
}

function routePluginMatch(): { pluginId: string; viewId: string } | null {
  const match = location.hash.replace(/^#/, "").split("?")[0].match(/^\/plugins\/([^/]+)\/([^/]+)$/);
  if (!match) return null;
  try { return { pluginId: decodeURIComponent(match[1]), viewId: decodeURIComponent(match[2]) }; } catch { return null; }
}

function escapeHTML(value: string): string {
  return value.replace(/[&<>"']/g, character => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]!);
}

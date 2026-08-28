import RFB from "@novnc/novnc";
import { del, get, post, put } from "../../js/api.js";
import {
  getActive, loadWorkspaces, openAddWorkspaceModal, openWorkspaceDropdown,
  renderWorkspaceIcon, setActiveWorkspace,
} from "../../js/workspaces.js";
import * as ws from "../../js/ws.js";
import { codeRouteHash } from "../navigation";
import { escapeHTML } from "../code/ui";
import { renderMobilePrimaryNav, renderPrimaryNav } from "../primaryNav";

type SandboxState = "disabled" | "unavailable" | "pulling" | "creating" | "starting" | "ready" | "stopping" | "stopped" | "error";
type SandboxConfig = { enabled: boolean; cpuLimit: number; memoryMiB: number; idleTimeoutMinutes: number };
type DesktopLease = { owner: "none" | "ai" | "user"; revision: number; browserSessionId?: string; chatTurnId?: string; expiresAt?: string };
type SandboxStatus = {
  state: SandboxState; enabled: boolean; errorCode?: string; message?: string; protocolVersion: string;
  resources?: { memoryBytes?: number; memoryLimitBytes?: number; activeProcesses?: number; diskBytes?: number };
  setup?: { state?: string; recipeDigest?: string; approvedDigest?: string; message?: string; lastRunAt?: string };
  activeViewers?: number; controlOwner?: "none" | "ai" | "user"; desktopLease?: DesktopLease;
};
type HostStatus = {
  available: boolean; supported: boolean; linuxEngine: boolean; architecture?: string; operatingSystem?: string;
  serverVersion?: string; errorCode?: string; message?: string; images: Record<string, { reference: string; present: boolean }>;
};
type NetworkGrant = { id: string; host: string; port: number; label: string; sandboxAlias?: string };
type Workspace = { id: string; name: string; iconExt?: string; sandbox?: SandboxConfig };

const defaultConfig: SandboxConfig = { enabled: false, cpuLimit: 4, memoryMiB: 6144, idleTimeoutMinutes: 30 };

class SandboxView {
  private root: HTMLElement;
  private abort = new AbortController();
  private workspace: Workspace | null = null;
  private host: HostStatus | null = null;
  private config: SandboxConfig = { ...defaultConfig };
  private status: SandboxStatus | null = null;
  private grants: NetworkGrant[] = [];
  private rfb: RFB | null = null;
  private desktopSessionID = "";
  private localControl = false;
	private localLeaseRevision = 0;
  private desktopConnecting = false;
  private busy = false;
  private closeWorkspaceDropdown: (() => void) | null = null;
  private closeAddWorkspace: (() => void) | null = null;
  private unsubscribers: Array<() => void> = [];
  private refreshTimer = 0;
	private reconnectTimer = 0;
  private usageTimer = 0;

  constructor(root: HTMLElement) { this.root = root; }

  async start(): Promise<void> {
    await loadWorkspaces();
    this.workspace = getActive() as Workspace | null;
    this.renderShell();
    this.bindNavigation();
    this.bindActions();
    this.subscribe();
    await this.refresh();
    this.usageTimer = window.setInterval(() => { if (this.status?.state === "ready" && !this.busy) void this.refresh(); }, 5000);
  }

  destroy(): void {
    this.abort.abort();
    window.clearTimeout(this.refreshTimer);
	window.clearTimeout(this.reconnectTimer);
    window.clearInterval(this.usageTimer);
    this.unsubscribers.forEach((unsubscribe) => unsubscribe());
    this.unsubscribers = [];
    this.closeWorkspaceDropdown?.();
    this.closeAddWorkspace?.();
    this.disconnectDesktop();
    if (this.workspace) ws.send({ type: "sandbox_unsubscribe", workspaceId: this.workspace.id });
  }

  private renderShell(): void {
    const name = this.workspace?.name || "No workspace";
    const icon = this.workspace?.iconExt ? `/api/workspaces/${encodeURIComponent(this.workspace.id)}/icon` : undefined;
    this.root.innerHTML = `
      <div class="sandbox-shell">
        ${renderPrimaryNav({ active: "sandbox", workspaceName: name, workspaceSelector: true, workspaceIconUrl: icon })}
        <main class="sandbox-main">
          <header class="sandbox-header">
            <div><span class="sandbox-eyebrow">WORKSPACE RUNTIME</span><h1>Linux Sandbox</h1></div>
            <div class="sandbox-header-status"><span class="sandbox-state" data-sandbox-state>Loading</span><span data-sandbox-message></span></div>
          </header>
          <div class="sandbox-notice sandbox-security-notice">
            <span class="codicon codicon-shield" aria-hidden="true"></span>
            <p>Containers isolate tools from the host, but are not a hardware VM. Only registered workspace folders and the shared exchange volume are mounted. Host home folders, credentials, devices, and the Docker socket are never mounted.</p>
          </div>
          <section class="sandbox-layout">
            <div class="sandbox-desktop-column">
              <div class="sandbox-toolbar" data-sandbox-toolbar></div>
              <section class="sandbox-desktop-card" aria-label="Sandbox desktop">
                <div class="sandbox-desktop-stage" data-desktop-stage>
                  <div class="sandbox-desktop-placeholder" data-desktop-placeholder>
                    <span class="codicon codicon-vm-connect" aria-hidden="true"></span>
                    <h2>Desktop is not connected</h2><p data-desktop-help>Enable and start the sandbox to view its Linux desktop.</p>
                  </div>
                  <div class="sandbox-signin-warning" data-signin-warning hidden>
                    <span class="codicon codicon-warning" aria-hidden="true"></span>
                    <h2>Before you sign in</h2>
                    <p>Keystrokes entered here are never sent to chat or Trajectory logs. After you return control, the AI can use the resulting signed-in browser session with the same authority as you.</p>
                    <button type="button" class="primary-button" data-action="acknowledge-signin">I understand — open desktop</button>
                  </div>
                  <div class="sandbox-rfb" data-rfb-host></div>
                </div>
                <footer class="sandbox-desktop-footer">
                  <span data-desktop-connection>Disconnected</span>
                  <span data-desktop-viewers></span>
                  <button type="button" class="secondary-button compact-button" data-action="fullscreen"><span class="codicon codicon-screen-full"></span> Full screen</button>
                </footer>
              </section>
              <section class="sandbox-log-card"><header><h2>Activity</h2><button type="button" data-action="clear-log">Clear</button></header><ol data-sandbox-log aria-live="polite"></ol></section>
            </div>
            <aside class="sandbox-side-panel">
              <section class="sandbox-panel" data-host-panel></section>
              <section class="sandbox-panel" data-config-panel></section>
              <section class="sandbox-panel" data-resource-panel></section>
              <section class="sandbox-panel" data-network-panel></section>
              <section class="sandbox-panel sandbox-danger-panel" data-reset-panel></section>
            </aside>
          </section>
        </main>
        ${renderMobilePrimaryNav({ active: "sandbox", workspaceName: name, workspaceSelector: true })}
      </div>`;
  }

  private bindNavigation(): void {
    const signal = this.abort.signal;
    this.root.querySelectorAll("[data-nav=chat]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/home"; }, { signal }));
    this.root.querySelectorAll("[data-nav=code]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/code"; }, { signal }));
    this.root.querySelectorAll("[data-nav=search]").forEach((item) => item.addEventListener("click", () => { location.hash = codeRouteHash("search"); }, { signal }));
    this.root.querySelectorAll("[data-nav=git]").forEach((item) => item.addEventListener("click", () => { location.hash = codeRouteHash("git"); }, { signal }));
    this.root.querySelectorAll("[data-nav=settings]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/settings"; }, { signal }));
    this.root.querySelectorAll<HTMLElement>(".workspace-dropdown-trigger").forEach((trigger) => trigger.addEventListener("click", (event) => {
      event.stopPropagation();
      if (this.closeWorkspaceDropdown) { this.closeWorkspaceDropdown(); return; }
      this.closeWorkspaceDropdown = openWorkspaceDropdown(trigger, {
        selectedId: this.workspace?.id || "",
        onClose: () => { this.closeWorkspaceDropdown = null; },
        onSelect: async (id: string) => {
          await setActiveWorkspace(id);
          const root = this.root;
          this.destroy();
          view = new SandboxView(root);
          await view.start();
        },
        onAdd: () => { this.closeAddWorkspace = openAddWorkspaceModal({ onCreate: async (workspace: Workspace) => { await setActiveWorkspace(workspace.id); const root = this.root; this.destroy(); view = new SandboxView(root); await view.start(); } }); },
      });
    }, { signal }));
  }

  private bindActions(): void {
    this.root.addEventListener("click", (event) => {
      const button = (event.target as Element).closest<HTMLButtonElement>("[data-action]");
      if (!button || button.disabled) return;
      void this.action(button.dataset.action || "", button);
    }, { signal: this.abort.signal });
    this.root.addEventListener("submit", (event) => {
      const form = (event.target as Element).closest<HTMLFormElement>("[data-network-form]");
      if (!form) return;
      event.preventDefault();
      void this.addGrant(new FormData(form));
    }, { signal: this.abort.signal });
  }

  private subscribe(): void {
    this.unsubscribers.push(ws.on("sandbox_event", (event: any) => {
      if (!this.workspace || event.workspaceId !== this.workspace.id) return;
		const logData = typeof event.data === "string" ? event.data.trim().slice(-4000) : "";
      this.appendLog(logData || event.message || event.event, event.progress);
		if (event.status) {
			if (this.localControl && (event.status.desktopLease?.owner !== "user" || event.status.desktopLease?.revision !== this.localLeaseRevision)) {
				this.localControl = false;
			}
			this.status = event.status;
			this.renderState();
		}
      else this.scheduleRefresh();
    }));
    this.unsubscribers.push(ws.onState((state) => { if (state === "open") this.subscribeWorkspace(); }));
    this.subscribeWorkspace();
  }

  private subscribeWorkspace(): void {
    if (this.workspace) ws.send({ type: "sandbox_subscribe", workspaceId: this.workspace.id });
  }

  private scheduleRefresh(): void {
    window.clearTimeout(this.refreshTimer);
    this.refreshTimer = window.setTimeout(() => { void this.refresh(); }, 150);
  }

  private async refresh(): Promise<void> {
    if (!this.workspace) { this.renderState(); return; }
    try {
      const [host, sandboxData, grantData] = await Promise.all([
        get("/api/sandbox/host"), get(`/api/workspaces/${encodeURIComponent(this.workspace.id)}/sandbox`),
        get(`/api/workspaces/${encodeURIComponent(this.workspace.id)}/sandbox/network-grants`),
      ]);
      this.host = host;
      this.config = { ...defaultConfig, ...(sandboxData.config || {}) };
      this.status = sandboxData.status;
      this.grants = grantData.grants || [];
      this.renderState();
    } catch (error) { this.showError(error); }
  }

  private renderState(): void {
    const status = this.status;
    const state = status?.state || (this.workspace ? "unavailable" : "disabled");
    const stateElement = this.root.querySelector<HTMLElement>("[data-sandbox-state]");
    if (stateElement) { stateElement.textContent = state; stateElement.dataset.state = state; }
    const message = this.root.querySelector<HTMLElement>("[data-sandbox-message]");
    if (message) message.textContent = status?.message || "";
    this.renderToolbar();
    this.renderHost();
    this.renderConfig();
    this.renderResources();
    this.renderNetwork();
    this.renderReset();
    this.updateDesktop();
  }

  private renderToolbar(): void {
    const host = this.root.querySelector<HTMLElement>("[data-sandbox-toolbar]");
    if (!host) return;
    const enabled = this.config.enabled;
    const ready = this.status?.state === "ready";
    const userControl = this.localControl;
    host.innerHTML = `
      <div class="sandbox-toolbar-primary">
        <button type="button" class="primary-button" data-action="${ready ? "stop" : "start"}" ${!enabled || this.busy ? "disabled" : ""}><span class="codicon codicon-${ready ? "debug-stop" : "play"}"></span>${ready ? "Stop" : "Start"}</button>
        <button type="button" class="secondary-button" data-action="pull" ${this.busy ? "disabled" : ""}><span class="codicon codicon-cloud-download"></span>Pull images</button>
        <button type="button" class="secondary-button" data-action="run-setup" ${!ready || this.busy ? "disabled" : ""}><span class="codicon codicon-run-all"></span>Run setup</button>
      </div>
      <div class="sandbox-toolbar-control">
        <span class="sandbox-lease"><i data-owner="${escapeHTML(this.status?.controlOwner || "none")}"></i>${this.controlLabel()}</span>
        <button type="button" class="${userControl ? "secondary-button" : "primary-button"}" data-action="${userControl ? "return-control" : "take-control"}" ${!ready || this.busy ? "disabled" : ""}>${userControl ? "Return Control" : "Take Control"}</button>
      </div>`;
  }

  private controlLabel(): string {
    if (this.localControl) return "You have control";
    if (this.status?.controlOwner === "ai") return "AI is controlling";
    if (this.status?.controlOwner === "user") return "Another device is controlling";
    return "View only";
  }

  private renderHost(): void {
    const panel = this.root.querySelector<HTMLElement>("[data-host-panel]");
    if (!panel) return;
    const missing = Object.entries(this.host?.images || {}).filter(([, image]) => !image.present).map(([role]) => role);
    panel.innerHTML = `<header><h2>Docker host</h2><span class="sandbox-check ${this.host?.supported ? "is-ok" : "is-error"}">${this.host?.supported ? "Ready" : "Needs attention"}</span></header>
      <dl><div><dt>Engine</dt><dd>${escapeHTML(this.host?.serverVersion || "Unavailable")}</dd></div><div><dt>Platform</dt><dd>${escapeHTML([this.host?.operatingSystem, this.host?.architecture].filter(Boolean).join(" · ") || "—")}</dd></div><div><dt>Images</dt><dd>${missing.length ? `Missing: ${escapeHTML(missing.join(", "))}` : "Installed"}</dd></div></dl>
      ${this.host?.message ? `<p class="sandbox-panel-error">${escapeHTML(this.host.message)}</p>` : ""}`;
  }

  private renderConfig(): void {
    const panel = this.root.querySelector<HTMLElement>("[data-config-panel]");
    if (!panel) return;
    panel.innerHTML = `<header><h2>Configuration</h2><span>${this.config.enabled ? "Enabled" : "Opt in"}</span></header>
      <div class="sandbox-fields">
        <label>CPUs<input type="number" min="1" max="16" value="${this.config.cpuLimit}" data-config="cpu"></label>
        <label>Memory (MiB)<input type="number" min="4096" max="32768" step="512" value="${this.config.memoryMiB}" data-config="memory"></label>
        <label>Idle stop (minutes)<input type="number" min="0" max="1440" value="${this.config.idleTimeoutMinutes}" data-config="idle"><small>0 keeps it running.</small></label>
      </div>
      <div class="sandbox-panel-actions"><button type="button" class="secondary-button" data-action="save-config" ${this.busy ? "disabled" : ""}>Save</button><button type="button" class="${this.config.enabled ? "danger-button" : "primary-button"}" data-action="${this.config.enabled ? "disable" : "enable"}" ${this.busy ? "disabled" : ""}>${this.config.enabled ? "Disable" : "Enable sandbox"}</button></div>`;
  }

  private renderResources(): void {
    const panel = this.root.querySelector<HTMLElement>("[data-resource-panel]");
    if (!panel) return;
    const resource = this.status?.resources || {};
    const memory = resource.memoryLimitBytes ? `${formatBytes(resource.memoryBytes || 0)} / ${formatBytes(resource.memoryLimitBytes)}` : "—";
    panel.innerHTML = `<header><h2>Runtime</h2><span>Protocol ${escapeHTML(this.status?.protocolVersion || "—")}</span></header><dl><div><dt>Memory</dt><dd>${memory}</dd></div><div><dt>Disk</dt><dd>${resource.diskBytes ? formatBytes(resource.diskBytes) : "—"}</dd></div><div><dt>Processes</dt><dd>${resource.activeProcesses ?? 0}</dd></div><div><dt>Viewers</dt><dd>${this.status?.activeViewers ?? 0}</dd></div><div><dt>Setup</dt><dd>${escapeHTML(this.status?.setup?.state || "Not run")}</dd></div></dl>`;
  }

  private renderNetwork(): void {
    const panel = this.root.querySelector<HTMLElement>("[data-network-panel]");
    if (!panel) return;
    panel.innerHTML = `<header><h2>Network grants</h2><span>Exact targets only</span></header><p class="sandbox-panel-copy">Host, LAN, link-local, and metadata destinations stay blocked unless an owner grants one exact host and TCP port.</p>
      <ul class="sandbox-grants">${this.grants.map((grant) => `<li><span><strong>${escapeHTML(grant.label || grant.host)}</strong><small>${escapeHTML(grant.host)}:${grant.port}${grant.sandboxAlias ? ` · ${escapeHTML(grant.sandboxAlias)}` : ""}</small></span><button type="button" data-action="delete-grant" data-grant-id="${escapeHTML(grant.id)}" aria-label="Revoke ${escapeHTML(grant.label || grant.host)}"><span class="codicon codicon-close"></span></button></li>`).join("") || "<li class='is-empty'>No private-network grants.</li>"}</ul>
      <form class="sandbox-grant-form" data-network-form><input name="host" required placeholder="host or IP"><input name="port" required type="number" min="1" max="65535" placeholder="port"><input name="label" required placeholder="label"><input name="sandboxAlias" placeholder="optional alias"><button class="secondary-button" type="submit">Grant</button></form>`;
  }

  private renderReset(): void {
    const panel = this.root.querySelector<HTMLElement>("[data-reset-panel]");
    if (!panel) return;
    panel.innerHTML = `<header><h2>Reset & remove</h2></header><p class="sandbox-panel-copy">These actions never delete registered host workspace files.</p><div class="sandbox-reset-grid"><button type="button" data-action="reset-workbench">Reset workbench</button><button type="button" data-action="reset-browser">Reset browser data</button><button type="button" data-action="recreate">Recreate containers</button><button type="button" class="is-danger" data-action="delete-sandbox">Delete sandbox data</button></div>`;
  }

  private updateDesktop(): void {
    const ready = this.status?.state === "ready";
    const placeholder = this.root.querySelector<HTMLElement>("[data-desktop-placeholder]");
    const warning = this.root.querySelector<HTMLElement>("[data-signin-warning]");
    const acknowledged = this.signInWarningAcknowledged();
    if (placeholder) placeholder.hidden = ready;
    if (warning) warning.hidden = !ready || acknowledged;
    const viewers = this.root.querySelector<HTMLElement>("[data-desktop-viewers]");
    if (viewers) viewers.textContent = `${this.status?.activeViewers || 0} viewer${this.status?.activeViewers === 1 ? "" : "s"}`;
    if (!ready) this.disconnectDesktop();
    else if (acknowledged) void this.connectDesktop();
    if (this.rfb) this.rfb.viewOnly = !this.localControl;
  }

  private signInWarningAcknowledged(): boolean {
    return Boolean(this.workspace && localStorage.getItem(`echo:sandbox-signin-warning:v1:${this.workspace.id}`));
  }

  private async connectDesktop(): Promise<void> {
    if (!this.workspace || this.rfb || this.desktopConnecting || this.status?.state !== "ready") return;
    this.desktopConnecting = true;
    this.setDesktopConnection("Connecting…");
    try {
      const data = await post(`/api/workspaces/${encodeURIComponent(this.workspace.id)}/sandbox/desktop-sessions`, {});
      const session = data.session as { id: string; credential: string };
      this.desktopSessionID = session.id;
      const protocol = location.protocol === "https:" ? "wss" : "ws";
      const url = `${protocol}://${location.host}/api/workspaces/${encodeURIComponent(this.workspace.id)}/sandbox/desktop-ws?sessionId=${encodeURIComponent(session.id)}`;
      const host = this.root.querySelector<HTMLElement>("[data-rfb-host]");
      if (!host) return;
      const rfb = new RFB(host, url, { credentials: { password: session.credential }, shared: true, wsProtocols: ["binary"] });
      session.credential = "";
      rfb.scaleViewport = true; rfb.clipViewport = true; rfb.resizeSession = false; rfb.qualityLevel = 7; rfb.compressionLevel = 3; rfb.viewOnly = !this.localControl;
      rfb.addEventListener("connect", () => this.setDesktopConnection(this.localControl ? "Connected · interactive" : "Connected · view only"));
      rfb.addEventListener("disconnect", () => {
			if (this.rfb !== rfb) return;
			this.rfb = null;
			this.setDesktopConnection("Disconnected");
			const unexpected = this.desktopSessionID === session.id && !this.abort.signal.aborted && this.status?.state === "ready" && this.signInWarningAcknowledged();
			if (unexpected) {
				this.desktopSessionID = "";
				window.clearTimeout(this.reconnectTimer);
				this.reconnectTimer = window.setTimeout(() => { void this.connectDesktop(); }, 1000);
			}
		});
      rfb.addEventListener("securityfailure", () => this.setDesktopConnection("Desktop authentication failed"));
      this.rfb = rfb;
    } catch (error) { this.showError(error); this.setDesktopConnection("Connection failed"); }
    finally { this.desktopConnecting = false; }
  }

  private disconnectDesktop(): void {
    const workspaceID = this.workspace?.id;
    const sessionID = this.desktopSessionID;
	window.clearTimeout(this.reconnectTimer);
	this.reconnectTimer = 0;
	this.desktopSessionID = "";
	this.localControl = false;
	this.localLeaseRevision = 0;
	this.rfb?.disconnect(); this.rfb = null;
    this.setDesktopConnection("Disconnected");
    if (workspaceID && sessionID) void del(`/api/workspaces/${encodeURIComponent(workspaceID)}/sandbox/desktop-sessions?id=${encodeURIComponent(sessionID)}`).catch(() => {});
  }

  private setDesktopConnection(value: string): void { const node = this.root.querySelector<HTMLElement>("[data-desktop-connection]"); if (node) node.textContent = value; }

  private async action(action: string, button: HTMLButtonElement): Promise<void> {
    if (!this.workspace) return;
    if (action === "clear-log") { const log = this.root.querySelector("[data-sandbox-log]"); if (log) log.innerHTML = ""; return; }
    if (action === "fullscreen") { await this.root.querySelector<HTMLElement>("[data-desktop-stage]")?.requestFullscreen(); return; }
    if (action === "acknowledge-signin") { localStorage.setItem(`echo:sandbox-signin-warning:v1:${this.workspace.id}`, "acknowledged"); this.updateDesktop(); return; }
    if (action === "take-control" || action === "return-control") { await this.changeControl(action === "take-control" ? "take" : "release"); return; }
    if (action === "delete-grant") { await this.deleteGrant(button.dataset.grantId || ""); return; }
    if (action === "save-config" || action === "enable" || action === "disable") { await this.saveConfig(action); return; }
    const destructive: Record<string, string> = {
      "reset-workbench": "Reset the persistent Linux home and recreate the workbench? Workspace files are retained.",
      "reset-browser": "Delete browser cookies, profiles, and signed-in sessions? Workspace and workbench data are retained.",
      recreate: "Recreate sandbox containers? Persistent workbench and browser volumes are retained.",
      "delete-sandbox": "Delete all sandbox containers, volumes, browser data, workbench state, grants, and machine state? Host workspace files are retained.",
    };
    if (destructive[action] && !window.confirm(destructive[action])) return;
    const actionMap: Record<string, string> = { start: "start", stop: "stop", pull: "pull", "run-setup": "run_setup", "reset-workbench": "reset_workbench", "reset-browser": "reset_browser", recreate: "recreate" };
    if (action === "delete-sandbox") {
      await this.runBusy(async () => {
        this.disconnectDesktop();
        await del(`/api/workspaces/${encodeURIComponent(this.workspace!.id)}/sandbox`);
        localStorage.removeItem(`echo:sandbox-signin-warning:v1:${this.workspace!.id}`);
      });
      return;
    }
    const backendAction = actionMap[action];
    if (!backendAction) return;
    await this.runBusy(async () => {
      try {
        if (backendAction === "reset_browser") this.disconnectDesktop();
        await post(`/api/workspaces/${encodeURIComponent(this.workspace!.id)}/sandbox/actions`, { action: backendAction });
        if (backendAction === "reset_browser") localStorage.removeItem(`echo:sandbox-signin-warning:v1:${this.workspace!.id}`);
      }
      catch (error: any) {
        if (backendAction === "run_setup" && error?.payload?.code === "setup_approval_required" && error.payload.details?.recipeDigest) {
          const digest = error.payload.details.recipeDigest;
          if (window.confirm(`The setup recipe changed (${digest}). Approve and run it as root in both sandbox roles?`)) await post(`/api/workspaces/${encodeURIComponent(this.workspace!.id)}/sandbox/actions`, { action: backendAction, approvedDigest: digest });
          return;
        }
        throw error;
      }
    });
  }

  private async saveConfig(action: string): Promise<void> {
    if (!this.workspace) return;
    if (action === "disable" && !window.confirm("Disable sandbox routing and return this workspace to host execution? Active sandbox tools, terminals, Git, and LSP processes will be stopped.")) return;
    const value = (selector: string) => Number(this.root.querySelector<HTMLInputElement>(selector)?.value);
    const config: SandboxConfig = {
      enabled: action === "enable" ? true : action === "disable" ? false : this.config.enabled,
      cpuLimit: value("[data-config=cpu]"), memoryMiB: value("[data-config=memory]"), idleTimeoutMinutes: value("[data-config=idle]"),
    };
    await this.runBusy(async () => { const data = await put(`/api/workspaces/${encodeURIComponent(this.workspace!.id)}/sandbox`, { config }); this.config = data.config; this.status = data.status; });
  }

  private async changeControl(action: "take" | "release"): Promise<void> {
    if (!this.workspace) return;
    try {
      const data = await post(`/api/workspaces/${encodeURIComponent(this.workspace.id)}/sandbox/desktop-control`, { action });
      this.localControl = action === "take";
		this.localLeaseRevision = this.localControl ? Number(data.lease.revision || 0) : 0;
      if (this.rfb) { this.rfb.viewOnly = !this.localControl; if (this.localControl) this.rfb.focus(); }
      this.status = { ...(this.status as SandboxStatus), controlOwner: data.lease.owner, desktopLease: data.lease };
      this.renderToolbar();
      this.setDesktopConnection(this.localControl ? "Connected · interactive" : "Connected · view only");
    } catch (error: any) {
      if (action === "take" && error?.payload?.code === "desktop_control_conflict" && window.confirm("Another authenticated device controls this desktop. Take control from it?")) {
		const data = await post(`/api/workspaces/${encodeURIComponent(this.workspace.id)}/sandbox/desktop-control`, { action, confirm: true });
		this.localControl = true; this.localLeaseRevision = Number(data.lease.revision || 0); if (this.rfb) { this.rfb.viewOnly = false; this.rfb.focus(); } this.scheduleRefresh(); return;
      }
      this.showError(error);
    }
  }

  private async addGrant(form: FormData): Promise<void> {
    if (!this.workspace) return;
    await this.runBusy(async () => { await post(`/api/workspaces/${encodeURIComponent(this.workspace!.id)}/sandbox/network-grants`, { host: form.get("host"), port: Number(form.get("port")), label: form.get("label"), sandboxAlias: form.get("sandboxAlias") }); });
  }
  private async deleteGrant(id: string): Promise<void> { if (!this.workspace || !id) return; await this.runBusy(async () => { await del(`/api/workspaces/${encodeURIComponent(this.workspace!.id)}/sandbox/network-grants?id=${encodeURIComponent(id)}`); }); }

  private async runBusy(operation: () => Promise<void>): Promise<void> {
    if (this.busy) return;
    this.busy = true; this.renderState();
    try { await operation(); await this.refresh(); }
    catch (error) { this.showError(error); }
    finally { this.busy = false; this.renderState(); }
  }

  private appendLog(message: string, progress?: number): void {
    const log = this.root.querySelector<HTMLOListElement>("[data-sandbox-log]");
    if (!log) return;
    const row = document.createElement("li");
    row.innerHTML = `<time>${new Date().toLocaleTimeString()}</time><span>${escapeHTML(message || "Sandbox update")}${typeof progress === "number" && progress > 0 ? ` · ${progress}%` : ""}</span>`;
    log.append(row); while (log.children.length > 200) log.firstElementChild?.remove(); log.scrollTop = log.scrollHeight;
  }
  private showError(error: any): void { const message = String(error?.message || error || "Sandbox operation failed"); this.appendLog(`Error: ${message}`); const node = this.root.querySelector<HTMLElement>("[data-sandbox-message]"); if (node) node.textContent = message; }
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return `${(value / 1024 ** index).toFixed(index > 1 ? 1 : 0)} ${units[index]}`;
}

let view: SandboxView | null = null;
export function mount(root: HTMLElement): void { view = new SandboxView(root); void view.start(); }
export function unmount(): void { view?.destroy(); view = null; }

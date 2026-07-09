export namespace llm {
	
	export class EndpointSelection {
	    chat: string;
	    kanbanDecompose: string;
	    kanban: string;
	    inlineCode: string;
	
	    static createFrom(source: any = {}) {
	        return new EndpointSelection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chat = source["chat"];
	        this.kanbanDecompose = source["kanbanDecompose"];
	        this.kanban = source["kanban"];
	        this.inlineCode = source["inlineCode"];
	    }
	}
	export class LLMEndpoint {
	    id: string;
	    name: string;
	    endpoint: string;
	    model: string;
	    temperature: number;
	    topK: number;
	    topP: number;
	    minP: number;
	    contextLength: number;
	    maxTokens: number;
	    frequencyPenalty: number;
	    presencePenalty: number;
	    repetitionPenalty: number;
	    timeoutSeconds: number;
	    thinkingTokenBudget: number;
	    thinkingCorrection?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LLMEndpoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.endpoint = source["endpoint"];
	        this.model = source["model"];
	        this.temperature = source["temperature"];
	        this.topK = source["topK"];
	        this.topP = source["topP"];
	        this.minP = source["minP"];
	        this.contextLength = source["contextLength"];
	        this.maxTokens = source["maxTokens"];
	        this.frequencyPenalty = source["frequencyPenalty"];
	        this.presencePenalty = source["presencePenalty"];
	        this.repetitionPenalty = source["repetitionPenalty"];
	        this.timeoutSeconds = source["timeoutSeconds"];
	        this.thinkingTokenBudget = source["thinkingTokenBudget"];
	        this.thinkingCorrection = source["thinkingCorrection"];
	    }
	}
	export class Theme {
	    light?: Record<string, string>;
	    dark?: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new Theme(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.light = source["light"];
	        this.dark = source["dark"];
	    }
	}
	export class Settings {
	    endpoint: string;
	    model: string;
	    endpoints?: LLMEndpoint[];
	    endpointSelection?: EndpointSelection;
	    temperature: number;
	    topK: number;
	    topP: number;
	    minP: number;
	    contextLength: number;
	    maxTokens: number;
	    frequencyPenalty: number;
	    presencePenalty: number;
	    repetitionPenalty: number;
	    timeoutSeconds: number;
	    searxngUrl: string;
	    thinkingTokenBudget: number;
	    thinkingCorrection?: boolean;
	    hideLeadingWhitespaceIndicators?: boolean;
	    disableNotificationSounds?: boolean;
	    enableChatCompletionNotifications?: boolean;
	    enableKanbanCompleteNotifications?: boolean;
	    limitKanbanConcurrency?: boolean;
	    disableGitSplitDiffView?: boolean;
	    theme?: Theme;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.endpoint = source["endpoint"];
	        this.model = source["model"];
	        this.endpoints = this.convertValues(source["endpoints"], LLMEndpoint);
	        this.endpointSelection = this.convertValues(source["endpointSelection"], EndpointSelection);
	        this.temperature = source["temperature"];
	        this.topK = source["topK"];
	        this.topP = source["topP"];
	        this.minP = source["minP"];
	        this.contextLength = source["contextLength"];
	        this.maxTokens = source["maxTokens"];
	        this.frequencyPenalty = source["frequencyPenalty"];
	        this.presencePenalty = source["presencePenalty"];
	        this.repetitionPenalty = source["repetitionPenalty"];
	        this.timeoutSeconds = source["timeoutSeconds"];
	        this.searxngUrl = source["searxngUrl"];
	        this.thinkingTokenBudget = source["thinkingTokenBudget"];
	        this.thinkingCorrection = source["thinkingCorrection"];
	        this.hideLeadingWhitespaceIndicators = source["hideLeadingWhitespaceIndicators"];
	        this.disableNotificationSounds = source["disableNotificationSounds"];
	        this.enableChatCompletionNotifications = source["enableChatCompletionNotifications"];
	        this.enableKanbanCompleteNotifications = source["enableKanbanCompleteNotifications"];
	        this.limitKanbanConcurrency = source["limitKanbanConcurrency"];
	        this.disableGitSplitDiffView = source["disableGitSplitDiffView"];
	        this.theme = this.convertValues(source["theme"], Theme);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace services {
	
	export class AgentMode {
	    id: string;
	    name: string;
	    prompt: string;
	    permissions?: Record<string, tools.ToolPermission>;
	    builtIn: boolean;
	    toolPermissions?: string[];
	    pathPermissions?: string[];
	
	    static createFrom(source: any = {}) {
	        return new AgentMode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.prompt = source["prompt"];
	        this.permissions = this.convertValues(source["permissions"], tools.ToolPermission, true);
	        this.builtIn = source["builtIn"];
	        this.toolPermissions = source["toolPermissions"];
	        this.pathPermissions = source["pathPermissions"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AppInfo {
	    name: string;
	    phase: string;
	    accentHex: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.phase = source["phase"];
	        this.accentHex = source["accentHex"];
	    }
	}
	export class WatchdogConfig {
	    enabled: boolean;
	    interval: number;
	
	    static createFrom(source: any = {}) {
	        return new WatchdogConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.interval = source["interval"];
	    }
	}
	export class LivenessConfig {
	    enabled: boolean;
	    stallTimeout: number;
	    maxAutoRetries: number;
	    checkInterval: number;
	
	    static createFrom(source: any = {}) {
	        return new LivenessConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.stallTimeout = source["stallTimeout"];
	        this.maxAutoRetries = source["maxAutoRetries"];
	        this.checkInterval = source["checkInterval"];
	    }
	}
	export class HeartbeatConfig {
	    enabled: boolean;
	    interval: number;
	
	    static createFrom(source: any = {}) {
	        return new HeartbeatConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.interval = source["interval"];
	    }
	}
	export class WorkspaceFolder {
	    id: string;
	    label: string;
	    path: string;
	    useAgents: boolean;
	    missing: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceFolder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.path = source["path"];
	        this.useAgents = source["useAgents"];
	        this.missing = source["missing"];
	        this.error = source["error"];
	    }
	}
	export class Workspace {
	    id: string;
	    folders: WorkspaceFolder[];
	    displayName: string;
	    defaultPlanMode: boolean;
	    searchParentGitRepositories: boolean;
	    buildCommand?: string;
	    letter?: string;
	    iconPath?: string;
	    iconUrl?: string;
	    active: boolean;
	    missing: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new Workspace(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.folders = this.convertValues(source["folders"], WorkspaceFolder);
	        this.displayName = source["displayName"];
	        this.defaultPlanMode = source["defaultPlanMode"];
	        this.searchParentGitRepositories = source["searchParentGitRepositories"];
	        this.buildCommand = source["buildCommand"];
	        this.letter = source["letter"];
	        this.iconPath = source["iconPath"];
	        this.iconUrl = source["iconUrl"];
	        this.active = source["active"];
	        this.missing = source["missing"];
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WebAccessSettings {
	    enabled: boolean;
	    bindHost: string;
	    port: number;
	    accessToken: string;
	    enableTLS: boolean;
	
	    static createFrom(source: any = {}) {
	        return new WebAccessSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.bindHost = source["bindHost"];
	        this.port = source["port"];
	        this.accessToken = source["accessToken"];
	        this.enableTLS = source["enableTLS"];
	    }
	}
	export class AppState {
	    settings: llm.Settings;
	    webAccess: WebAccessSettings;
	    workspaces: Workspace[];
	    activeWorkspaceId: string;
	    heartbeatConfigs?: Record<string, HeartbeatConfig>;
	    livenessConfigs?: Record<string, LivenessConfig>;
	    watchdogConfigs?: Record<string, WatchdogConfig>;
	    dashboardLayouts?: Record<string, Array<DashboardWidgetJSON>>;
	
	    static createFrom(source: any = {}) {
	        return new AppState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.settings = this.convertValues(source["settings"], llm.Settings);
	        this.webAccess = this.convertValues(source["webAccess"], WebAccessSettings);
	        this.workspaces = this.convertValues(source["workspaces"], Workspace);
	        this.activeWorkspaceId = source["activeWorkspaceId"];
	        this.heartbeatConfigs = this.convertValues(source["heartbeatConfigs"], HeartbeatConfig, true);
	        this.livenessConfigs = this.convertValues(source["livenessConfigs"], LivenessConfig, true);
	        this.watchdogConfigs = this.convertValues(source["watchdogConfigs"], WatchdogConfig, true);
	        this.dashboardLayouts = this.convertValues(source["dashboardLayouts"], Array<DashboardWidgetJSON>, true);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatImageAttachment {
	    id: string;
	    source: string;
	    name: string;
	    path?: string;
	    mediaType: string;
	    bytes: number;
	    dataUrl?: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatImageAttachment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.source = source["source"];
	        this.name = source["name"];
	        this.path = source["path"];
	        this.mediaType = source["mediaType"];
	        this.bytes = source["bytes"];
	        this.dataUrl = source["dataUrl"];
	    }
	}
	export class ChatImageInput {
	    id?: string;
	    name?: string;
	    mediaType?: string;
	    dataUrl: string;
	    bytes?: number;
	
	    static createFrom(source: any = {}) {
	        return new ChatImageInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.mediaType = source["mediaType"];
	        this.dataUrl = source["dataUrl"];
	        this.bytes = source["bytes"];
	    }
	}
	export class ChatToolActivity {
	    id: string;
	    name?: string;
	    arguments?: string;
	    status: string;
	    result?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatToolActivity(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.arguments = source["arguments"];
	        this.status = source["status"];
	        this.result = source["result"];
	        this.error = source["error"];
	    }
	}
	export class ChatVideoAttachment {
	    id: string;
	    source: string;
	    name: string;
	    path?: string;
	    mediaType: string;
	    bytes: number;
	    dataUrl?: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatVideoAttachment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.source = source["source"];
	        this.name = source["name"];
	        this.path = source["path"];
	        this.mediaType = source["mediaType"];
	        this.bytes = source["bytes"];
	        this.dataUrl = source["dataUrl"];
	    }
	}
	export class ChatMessage {
	    id: string;
	    role: string;
	    content?: string;
	    images?: ChatImageAttachment[];
	    videos?: ChatVideoAttachment[];
	    reasoning?: string;
	    toolCalls?: ChatToolActivity[];
	    status: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.role = source["role"];
	        this.content = source["content"];
	        this.images = this.convertValues(source["images"], ChatImageAttachment);
	        this.videos = this.convertValues(source["videos"], ChatVideoAttachment);
	        this.reasoning = source["reasoning"];
	        this.toolCalls = this.convertValues(source["toolCalls"], ChatToolActivity);
	        this.status = source["status"];
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatVideoInput {
	    id?: string;
	    name?: string;
	    mediaType?: string;
	    dataUrl: string;
	    bytes?: number;
	
	    static createFrom(source: any = {}) {
	        return new ChatVideoInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.mediaType = source["mediaType"];
	        this.dataUrl = source["dataUrl"];
	        this.bytes = source["bytes"];
	    }
	}
	export class ChatMessageRequest {
	    content: string;
	    planMode: boolean;
	    agentModeId: string;
	    images?: ChatImageInput[];
	    videos?: ChatVideoInput[];
	
	    static createFrom(source: any = {}) {
	        return new ChatMessageRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.content = source["content"];
	        this.planMode = source["planMode"];
	        this.agentModeId = source["agentModeId"];
	        this.images = this.convertValues(source["images"], ChatImageInput);
	        this.videos = this.convertValues(source["videos"], ChatVideoInput);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatSession {
	    workspaceId: string;
	    messages: ChatMessage[];
	    busy: boolean;
	    streamId?: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatSession(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.messages = this.convertValues(source["messages"], ChatMessage);
	        this.busy = source["busy"];
	        this.streamId = source["streamId"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	export class DashboardWidgetJSON {
	    id: string;
	    view: string;
	    title: string;
	    size: string;
	    order: number;
	
	    static createFrom(source: any = {}) {
	        return new DashboardWidgetJSON(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.view = source["view"];
	        this.title = source["title"];
	        this.size = source["size"];
	        this.order = source["order"];
	    }
	}
	
	export class InlineCodePromptRequest {
	    requestId?: string;
	    filePath: string;
	    prompt: string;
	    cursorToken: string;
	    cursorLineText: string;
	    focusSubstring: string;
	    contextSubstring: string;
	    selectedText?: string;
	
	    static createFrom(source: any = {}) {
	        return new InlineCodePromptRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requestId = source["requestId"];
	        this.filePath = source["filePath"];
	        this.prompt = source["prompt"];
	        this.cursorToken = source["cursorToken"];
	        this.cursorLineText = source["cursorLineText"];
	        this.focusSubstring = source["focusSubstring"];
	        this.contextSubstring = source["contextSubstring"];
	        this.selectedText = source["selectedText"];
	    }
	}
	export class InlineCodePromptResponse {
	    content?: string;
	    reasoning?: string;
	    toolCalls?: ChatToolActivity[];
	    affectedPaths?: string[];
	
	    static createFrom(source: any = {}) {
	        return new InlineCodePromptResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.content = source["content"];
	        this.reasoning = source["reasoning"];
	        this.toolCalls = this.convertValues(source["toolCalls"], ChatToolActivity);
	        this.affectedPaths = source["affectedPaths"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class KanbanProgressEntry {
	    type: string;
	    title?: string;
	    content: string;
	    status?: string;
	    // Go type: time
	    timestamp: any;
	
	    static createFrom(source: any = {}) {
	        return new KanbanProgressEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.title = source["title"];
	        this.content = source["content"];
	        this.status = source["status"];
	        this.timestamp = this.convertValues(source["timestamp"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class KanbanDependencyStatus {
	    id: string;
	    title: string;
	    status: string;
	    done: boolean;
	
	    static createFrom(source: any = {}) {
	        return new KanbanDependencyStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.status = source["status"];
	        this.done = source["done"];
	    }
	}
	export class KanbanCard {
	    id: string;
	    workspaceId: string;
	    title: string;
	    description: string;
	    direction?: string;
	    acceptanceCriteria: string[];
	    dependencies?: string[];
	    dependencyStatuses?: KanbanDependencyStatus[];
	    blockedBy?: string[];
	    eligible: boolean;
	    lane: string;
	    status: string;
	    progressTranscript?: KanbanProgressEntry[];
	    autoRetriesUsed?: number;
	    recoveryType?: string;
	    // Go type: time
	    stalledAt?: any;
	    watchdogChecked?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new KanbanCard(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.workspaceId = source["workspaceId"];
	        this.title = source["title"];
	        this.description = source["description"];
	        this.direction = source["direction"];
	        this.acceptanceCriteria = source["acceptanceCriteria"];
	        this.dependencies = source["dependencies"];
	        this.dependencyStatuses = this.convertValues(source["dependencyStatuses"], KanbanDependencyStatus);
	        this.blockedBy = source["blockedBy"];
	        this.eligible = source["eligible"];
	        this.lane = source["lane"];
	        this.status = source["status"];
	        this.progressTranscript = this.convertValues(source["progressTranscript"], KanbanProgressEntry);
	        this.autoRetriesUsed = source["autoRetriesUsed"];
	        this.recoveryType = source["recoveryType"];
	        this.stalledAt = this.convertValues(source["stalledAt"], null);
	        this.watchdogChecked = source["watchdogChecked"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class KanbanBoard {
	    workspaceId: string;
	    ready: KanbanCard[];
	    inProgress: KanbanCard[];
	    blocked: KanbanCard[];
	    done: KanbanCard[];
	
	    static createFrom(source: any = {}) {
	        return new KanbanBoard(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.ready = this.convertValues(source["ready"], KanbanCard);
	        this.inProgress = this.convertValues(source["inProgress"], KanbanCard);
	        this.blocked = this.convertValues(source["blocked"], KanbanCard);
	        this.done = this.convertValues(source["done"], KanbanCard);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	export class RuntimeStatus {
	    activeKanbanWorkspaceIds: string[];
	
	    static createFrom(source: any = {}) {
	        return new RuntimeStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.activeKanbanWorkspaceIds = source["activeKanbanWorkspaceIds"];
	    }
	}
	export class WorkspaceTask {
	    id: string;
	    title: string;
	    details?: string;
	    epic?: string;
	    tags?: string[];
	    acceptanceCriteria?: string[];
	    priority: string;
	    sortOrder: number;
	    completed: boolean;
	    createdAt: string;
	    updatedAt: string;
	    completedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.details = source["details"];
	        this.epic = source["epic"];
	        this.tags = source["tags"];
	        this.acceptanceCriteria = source["acceptanceCriteria"];
	        this.priority = source["priority"];
	        this.sortOrder = source["sortOrder"];
	        this.completed = source["completed"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	        this.completedAt = source["completedAt"];
	    }
	}
	export class TaskBoard {
	    workspaceId: string;
	    storagePath: string;
	    doneStoragePath: string;
	    workspaceStatePath: string;
	    gitIgnored: boolean;
	    doneGitIgnored: boolean;
	    workspaceStateGitIgnored: boolean;
	    tags: string[];
	    tasks: WorkspaceTask[];
	
	    static createFrom(source: any = {}) {
	        return new TaskBoard(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.storagePath = source["storagePath"];
	        this.doneStoragePath = source["doneStoragePath"];
	        this.workspaceStatePath = source["workspaceStatePath"];
	        this.gitIgnored = source["gitIgnored"];
	        this.doneGitIgnored = source["doneGitIgnored"];
	        this.workspaceStateGitIgnored = source["workspaceStateGitIgnored"];
	        this.tags = source["tags"];
	        this.tasks = this.convertValues(source["tasks"], WorkspaceTask);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TaskInput {
	    title: string;
	    details?: string;
	    epic?: string;
	    tags?: string[];
	    acceptanceCriteria?: string[];
	    priority: string;
	
	    static createFrom(source: any = {}) {
	        return new TaskInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.title = source["title"];
	        this.details = source["details"];
	        this.epic = source["epic"];
	        this.tags = source["tags"];
	        this.acceptanceCriteria = source["acceptanceCriteria"];
	        this.priority = source["priority"];
	    }
	}
	export class TaskKanbanConversion {
	    tasks: TaskBoard;
	    kanban: KanbanBoard;
	
	    static createFrom(source: any = {}) {
	        return new TaskKanbanConversion(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tasks = this.convertValues(source["tasks"], TaskBoard);
	        this.kanban = this.convertValues(source["kanban"], KanbanBoard);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TokenBudget {
	    limit: number;
	    used: number;
	    paused: boolean;
	
	    static createFrom(source: any = {}) {
	        return new TokenBudget(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.limit = source["limit"];
	        this.used = source["used"];
	        this.paused = source["paused"];
	    }
	}
	
	
	export class WebAccessStatus {
	    enabled: boolean;
	    running: boolean;
	    bindHost: string;
	    port: number;
	    accessToken: string;
	    primaryUrl: string;
	    lanUrls: string[];
	    enableTLS: boolean;
	    lastError?: string;
	
	    static createFrom(source: any = {}) {
	        return new WebAccessStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.running = source["running"];
	        this.bindHost = source["bindHost"];
	        this.port = source["port"];
	        this.accessToken = source["accessToken"];
	        this.primaryUrl = source["primaryUrl"];
	        this.lanUrls = source["lanUrls"];
	        this.enableTLS = source["enableTLS"];
	        this.lastError = source["lastError"];
	    }
	}
	
	export class WorkspaceFileChange {
	    id: string;
	    workspaceId: string;
	    path: string;
	    operation: string;
	    source: WorkspaceChangeSource;
	    before?: WorkspaceFileSnapshot;
	    after?: WorkspaceFileSnapshot;
	    createdAt: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceFileChange(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.workspaceId = source["workspaceId"];
	        this.path = source["path"];
	        this.operation = source["operation"];
	        this.source = this.convertValues(source["source"], WorkspaceChangeSource);
	        this.before = this.convertValues(source["before"], WorkspaceFileSnapshot);
	        this.after = this.convertValues(source["after"], WorkspaceFileSnapshot);
	        this.createdAt = source["createdAt"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceChangeSource {
	    type: string;
	    cardId?: string;
	    cardTitle?: string;
	    messageId?: string;
	    requestId?: string;
	    toolCallId?: string;
	    toolName?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceChangeSource(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.cardId = source["cardId"];
	        this.cardTitle = source["cardTitle"];
	        this.messageId = source["messageId"];
	        this.requestId = source["requestId"];
	        this.toolCallId = source["toolCallId"];
	        this.toolName = source["toolName"];
	    }
	}
	export class WorkspaceFileSnapshot {
	    path: string;
	    exists: boolean;
	    bytes?: number;
	    sha256?: string;
	    textAvailable?: boolean;
	    binary?: boolean;
	    large?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceFileSnapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.exists = source["exists"];
	        this.bytes = source["bytes"];
	        this.sha256 = source["sha256"];
	        this.textAvailable = source["textAvailable"];
	        this.binary = source["binary"];
	        this.large = source["large"];
	    }
	}
	export class WorkspaceChangedFile {
	    path: string;
	    operation: string;
	    diff?: string;
	    diffAvailable: boolean;
	    before?: WorkspaceFileSnapshot;
	    after?: WorkspaceFileSnapshot;
	    sources?: WorkspaceChangeSource[];
	    changeCount: number;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceChangedFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.operation = source["operation"];
	        this.diff = source["diff"];
	        this.diffAvailable = source["diffAvailable"];
	        this.before = this.convertValues(source["before"], WorkspaceFileSnapshot);
	        this.after = this.convertValues(source["after"], WorkspaceFileSnapshot);
	        this.sources = this.convertValues(source["sources"], WorkspaceChangeSource);
	        this.changeCount = source["changeCount"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceChangeReview {
	    workspaceId: string;
	    fileCount: number;
	    changeCount: number;
	    files: WorkspaceChangedFile[];
	    changes?: WorkspaceFileChange[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceChangeReview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.fileCount = source["fileCount"];
	        this.changeCount = source["changeCount"];
	        this.files = this.convertValues(source["files"], WorkspaceChangedFile);
	        this.changes = this.convertValues(source["changes"], WorkspaceFileChange);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class WorkspaceTextEdit {
	    from: number;
	    to: number;
	    newText: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceTextEdit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.from = source["from"];
	        this.to = source["to"];
	        this.newText = source["newText"];
	    }
	}
	export class WorkspaceCompletionItem {
	    label: string;
	    kind?: number;
	    detail?: string;
	    documentation?: string;
	    insertText: string;
	    sortText?: string;
	    filterText?: string;
	    from: number;
	    to: number;
	    additionalTextEdits?: WorkspaceTextEdit[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceCompletionItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.kind = source["kind"];
	        this.detail = source["detail"];
	        this.documentation = source["documentation"];
	        this.insertText = source["insertText"];
	        this.sortText = source["sortText"];
	        this.filterText = source["filterText"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.additionalTextEdits = this.convertValues(source["additionalTextEdits"], WorkspaceTextEdit);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceCompletionRequest {
	    filePath: string;
	    content: string;
	    position: number;
	    triggerKind?: number;
	    triggerCharacter?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceCompletionRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filePath = source["filePath"];
	        this.content = source["content"];
	        this.position = source["position"];
	        this.triggerKind = source["triggerKind"];
	        this.triggerCharacter = source["triggerCharacter"];
	    }
	}
	export class WorkspaceCompletionResponse {
	    workspaceId: string;
	    filePath: string;
	    isIncomplete: boolean;
	    items: WorkspaceCompletionItem[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceCompletionResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.filePath = source["filePath"];
	        this.isIncomplete = source["isIncomplete"];
	        this.items = this.convertValues(source["items"], WorkspaceCompletionItem);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceDefinitionRequest {
	    filePath: string;
	    content: string;
	    position: number;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceDefinitionRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filePath = source["filePath"];
	        this.content = source["content"];
	        this.position = source["position"];
	    }
	}
	export class WorkspaceDefinitionResponse {
	    workspaceId: string;
	    sourcePath: string;
	    targetPath?: string;
	    position: number;
	    line: number;
	    character: number;
	    found: boolean;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceDefinitionResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.sourcePath = source["sourcePath"];
	        this.targetPath = source["targetPath"];
	        this.position = source["position"];
	        this.line = source["line"];
	        this.character = source["character"];
	        this.found = source["found"];
	        this.message = source["message"];
	    }
	}
	export class WorkspaceFileEntry {
	    name: string;
	    path: string;
	    kind: string;
	    bytes?: number;
	    modifiedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceFileEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.kind = source["kind"];
	        this.bytes = source["bytes"];
	        this.modifiedAt = source["modifiedAt"];
	    }
	}
	export class WorkspaceDirectory {
	    workspaceId: string;
	    path: string;
	    entries: WorkspaceFileEntry[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceDirectory(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.path = source["path"];
	        this.entries = this.convertValues(source["entries"], WorkspaceFileEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceFile {
	    workspaceId: string;
	    path: string;
	    content: string;
	    bytes: number;
	    modifiedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.path = source["path"];
	        this.content = source["content"];
	        this.bytes = source["bytes"];
	        this.modifiedAt = source["modifiedAt"];
	    }
	}
	
	
	export class WorkspaceFileSearchResult {
	    workspaceId: string;
	    query: string;
	    entries: WorkspaceFileEntry[];
	    truncated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceFileSearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.query = source["query"];
	        this.entries = this.convertValues(source["entries"], WorkspaceFileEntry);
	        this.truncated = source["truncated"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class WorkspaceGitBranch {
	    name: string;
	    current: boolean;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceGitBranch(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.current = source["current"];
	    }
	}
	export class WorkspaceGitChangedFile {
	    path: string;
	    oldPath?: string;
	    operation: string;
	    status: string;
	    indexStatus?: string;
	    worktreeStatus?: string;
	    staged: boolean;
	    unstaged: boolean;
	    diff?: string;
	    diffAvailable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceGitChangedFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.oldPath = source["oldPath"];
	        this.operation = source["operation"];
	        this.status = source["status"];
	        this.indexStatus = source["indexStatus"];
	        this.worktreeStatus = source["worktreeStatus"];
	        this.staged = source["staged"];
	        this.unstaged = source["unstaged"];
	        this.diff = source["diff"];
	        this.diffAvailable = source["diffAvailable"];
	    }
	}
	export class WorkspaceGitChangeReview {
	    workspaceId: string;
	    fileCount: number;
	    files: WorkspaceGitChangedFile[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceGitChangeReview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.fileCount = source["fileCount"];
	        this.files = this.convertValues(source["files"], WorkspaceGitChangedFile);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class WorkspaceGitCommit {
	    hash: string;
	    shortHash: string;
	    subject: string;
	    authorName: string;
	    authorEmail?: string;
	    authoredAt: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceGitCommit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hash = source["hash"];
	        this.shortHash = source["shortHash"];
	        this.subject = source["subject"];
	        this.authorName = source["authorName"];
	        this.authorEmail = source["authorEmail"];
	        this.authoredAt = source["authoredAt"];
	    }
	}
	export class WorkspaceGitCommitDetail {
	    workspaceId: string;
	    folderId: string;
	    commit: WorkspaceGitCommit;
	    fileCount: number;
	    files: WorkspaceGitChangedFile[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceGitCommitDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.folderId = source["folderId"];
	        this.commit = this.convertValues(source["commit"], WorkspaceGitCommit);
	        this.fileCount = source["fileCount"];
	        this.files = this.convertValues(source["files"], WorkspaceGitChangedFile);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceGitRepositoryStatus {
	    folderId: string;
	    label: string;
	    path: string;
	    currentBranch?: string;
	    upstream?: string;
	    aheadCount: number;
	    behindCount: number;
	    head?: string;
	    shortHead?: string;
	    detached: boolean;
	    dirty: boolean;
	    branches: WorkspaceGitBranch[];
	    fileCount: number;
	    stagedFileCount: number;
	    unstagedFileCount: number;
	    files: WorkspaceGitChangedFile[];
	    commits: WorkspaceGitCommit[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceGitRepositoryStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.folderId = source["folderId"];
	        this.label = source["label"];
	        this.path = source["path"];
	        this.currentBranch = source["currentBranch"];
	        this.upstream = source["upstream"];
	        this.aheadCount = source["aheadCount"];
	        this.behindCount = source["behindCount"];
	        this.head = source["head"];
	        this.shortHead = source["shortHead"];
	        this.detached = source["detached"];
	        this.dirty = source["dirty"];
	        this.branches = this.convertValues(source["branches"], WorkspaceGitBranch);
	        this.fileCount = source["fileCount"];
	        this.stagedFileCount = source["stagedFileCount"];
	        this.unstagedFileCount = source["unstagedFileCount"];
	        this.files = this.convertValues(source["files"], WorkspaceGitChangedFile);
	        this.commits = this.convertValues(source["commits"], WorkspaceGitCommit);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceGitRepositorySummary {
	    folderId: string;
	    label: string;
	    path: string;
	    currentBranch?: string;
	    upstream?: string;
	    aheadCount: number;
	    behindCount: number;
	    head?: string;
	    shortHead?: string;
	    detached: boolean;
	    dirty: boolean;
	    available: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceGitRepositorySummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.folderId = source["folderId"];
	        this.label = source["label"];
	        this.path = source["path"];
	        this.currentBranch = source["currentBranch"];
	        this.upstream = source["upstream"];
	        this.aheadCount = source["aheadCount"];
	        this.behindCount = source["behindCount"];
	        this.head = source["head"];
	        this.shortHead = source["shortHead"];
	        this.detached = source["detached"];
	        this.dirty = source["dirty"];
	        this.available = source["available"];
	        this.error = source["error"];
	    }
	}
	export class WorkspaceGitRepositoryView {
	    workspaceId: string;
	    selectedFolderId?: string;
	    repositories: WorkspaceGitRepositorySummary[];
	    repository?: WorkspaceGitRepositoryStatus;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceGitRepositoryView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.selectedFolderId = source["selectedFolderId"];
	        this.repositories = this.convertValues(source["repositories"], WorkspaceGitRepositorySummary);
	        this.repository = this.convertValues(source["repository"], WorkspaceGitRepositoryStatus);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceIconInput {
	    name?: string;
	    mediaType?: string;
	    dataUrl: string;
	    bytes?: number;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceIconInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.mediaType = source["mediaType"];
	        this.dataUrl = source["dataUrl"];
	        this.bytes = source["bytes"];
	    }
	}
	export class WorkspaceMediaFile {
	    workspaceId: string;
	    path: string;
	    mimeType: string;
	    dataUrl: string;
	    bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceMediaFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.path = source["path"];
	        this.mimeType = source["mimeType"];
	        this.dataUrl = source["dataUrl"];
	        this.bytes = source["bytes"];
	    }
	}
	export class WorkspacePrepareRenameResponse {
	    workspaceId: string;
	    filePath: string;
	    available: boolean;
	    from: number;
	    to: number;
	    placeholder?: string;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspacePrepareRenameResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.filePath = source["filePath"];
	        this.available = source["available"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.placeholder = source["placeholder"];
	        this.message = source["message"];
	    }
	}
	export class WorkspaceReferencePreviewLine {
	    line: number;
	    text: string;
	    highlightStart: number;
	    highlightEnd: number;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceReferencePreviewLine(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.line = source["line"];
	        this.text = source["text"];
	        this.highlightStart = source["highlightStart"];
	        this.highlightEnd = source["highlightEnd"];
	    }
	}
	export class WorkspaceReferencePosition {
	    line: number;
	    column: number;
	    offset: number;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceReferencePosition(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.line = source["line"];
	        this.column = source["column"];
	        this.offset = source["offset"];
	    }
	}
	export class WorkspaceReferenceRange {
	    start: WorkspaceReferencePosition;
	    end: WorkspaceReferencePosition;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceReferenceRange(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.start = this.convertValues(source["start"], WorkspaceReferencePosition);
	        this.end = this.convertValues(source["end"], WorkspaceReferencePosition);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceReferenceLocation {
	    path: string;
	    range: WorkspaceReferenceRange;
	    preview?: string;
	    previewLines?: WorkspaceReferencePreviewLine[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceReferenceLocation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.range = this.convertValues(source["range"], WorkspaceReferenceRange);
	        this.preview = source["preview"];
	        this.previewLines = this.convertValues(source["previewLines"], WorkspaceReferencePreviewLine);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	export class WorkspaceReferenceRequest {
	    filePath: string;
	    content: string;
	    position: number;
	    includeDeclaration?: boolean;
	    maxResults?: number;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceReferenceRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filePath = source["filePath"];
	        this.content = source["content"];
	        this.position = source["position"];
	        this.includeDeclaration = source["includeDeclaration"];
	        this.maxResults = source["maxResults"];
	    }
	}
	export class WorkspaceReferenceResponse {
	    workspaceId: string;
	    sourcePath: string;
	    position: number;
	    found: boolean;
	    message?: string;
	    locations?: WorkspaceReferenceLocation[];
	    resultCount?: number;
	    returnedCount?: number;
	    truncated?: boolean;
	    skippedOutsideWorkspace?: number;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceReferenceResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.sourcePath = source["sourcePath"];
	        this.position = source["position"];
	        this.found = source["found"];
	        this.message = source["message"];
	        this.locations = this.convertValues(source["locations"], WorkspaceReferenceLocation);
	        this.resultCount = source["resultCount"];
	        this.returnedCount = source["returnedCount"];
	        this.truncated = source["truncated"];
	        this.skippedOutsideWorkspace = source["skippedOutsideWorkspace"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceRenameFileContent {
	    filePath: string;
	    content: string;
	    modifiedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceRenameFileContent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filePath = source["filePath"];
	        this.content = source["content"];
	        this.modifiedAt = source["modifiedAt"];
	    }
	}
	export class WorkspaceRenameHistoryFile {
	    filePath: string;
	    beforeContent: string;
	    afterContent: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceRenameHistoryFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filePath = source["filePath"];
	        this.beforeContent = source["beforeContent"];
	        this.afterContent = source["afterContent"];
	    }
	}
	export class WorkspaceRenameReplayFile {
	    filePath: string;
	    expectedContent: string;
	    content: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceRenameReplayFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filePath = source["filePath"];
	        this.expectedContent = source["expectedContent"];
	        this.content = source["content"];
	    }
	}
	export class WorkspaceRenameReplayRequest {
	    files: WorkspaceRenameReplayFile[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceRenameReplayRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.files = this.convertValues(source["files"], WorkspaceRenameReplayFile);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceRenameReplayResponse {
	    files: WorkspaceFile[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceRenameReplayResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.files = this.convertValues(source["files"], WorkspaceFile);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceRenameRequest {
	    filePath: string;
	    content: string;
	    position: number;
	    newName: string;
	    openFiles?: WorkspaceRenameFileContent[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceRenameRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filePath = source["filePath"];
	        this.content = source["content"];
	        this.position = source["position"];
	        this.newName = source["newName"];
	        this.openFiles = this.convertValues(source["openFiles"], WorkspaceRenameFileContent);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceRenameResponse {
	    workspaceId: string;
	    sourcePath: string;
	    applied: boolean;
	    files?: WorkspaceFile[];
	    history?: WorkspaceRenameHistoryFile[];
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceRenameResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.sourcePath = source["sourcePath"];
	        this.applied = source["applied"];
	        this.files = this.convertValues(source["files"], WorkspaceFile);
	        this.history = this.convertValues(source["history"], WorkspaceRenameHistoryFile);
	        this.message = source["message"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceSkillCreationResult {
	    id: string;
	    folder: string;
	    name: string;
	    description: string;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceSkillCreationResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.folder = source["folder"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.path = source["path"];
	    }
	}
	
	
	export class WorkspaceTextSearchMatch {
	    line: number;
	    column: number;
	    endLine: number;
	    endColumn: number;
	    offset: number;
	    endOffset: number;
	    lineText: string;
	    matchText: string;
	    highlightStart: number;
	    highlightEnd: number;
	    truncated?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceTextSearchMatch(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.line = source["line"];
	        this.column = source["column"];
	        this.endLine = source["endLine"];
	        this.endColumn = source["endColumn"];
	        this.offset = source["offset"];
	        this.endOffset = source["endOffset"];
	        this.lineText = source["lineText"];
	        this.matchText = source["matchText"];
	        this.highlightStart = source["highlightStart"];
	        this.highlightEnd = source["highlightEnd"];
	        this.truncated = source["truncated"];
	    }
	}
	export class WorkspaceTextSearchFileResult {
	    path: string;
	    name: string;
	    matches: WorkspaceTextSearchMatch[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceTextSearchFileResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.matches = this.convertValues(source["matches"], WorkspaceTextSearchMatch);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class WorkspaceTextSearchRequest {
	    query: string;
	    regex: boolean;
	    caseSensitive: boolean;
	    wholeWord: boolean;
	    include?: string;
	    exclude?: string;
	    includeIgnored: boolean;
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceTextSearchRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.query = source["query"];
	        this.regex = source["regex"];
	        this.caseSensitive = source["caseSensitive"];
	        this.wholeWord = source["wholeWord"];
	        this.include = source["include"];
	        this.exclude = source["exclude"];
	        this.includeIgnored = source["includeIgnored"];
	    }
	}
	export class WorkspaceTextSearchResult {
	    workspaceId: string;
	    query: string;
	    regex: boolean;
	    caseSensitive: boolean;
	    wholeWord: boolean;
	    include?: string;
	    exclude?: string;
	    matchCount: number;
	    fileCount: number;
	    filesSearched: number;
	    filesSkipped: number;
	    truncated: boolean;
	    files: WorkspaceTextSearchFileResult[];
	
	    static createFrom(source: any = {}) {
	        return new WorkspaceTextSearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.query = source["query"];
	        this.regex = source["regex"];
	        this.caseSensitive = source["caseSensitive"];
	        this.wholeWord = source["wholeWord"];
	        this.include = source["include"];
	        this.exclude = source["exclude"];
	        this.matchCount = source["matchCount"];
	        this.fileCount = source["fileCount"];
	        this.filesSearched = source["filesSearched"];
	        this.filesSkipped = source["filesSkipped"];
	        this.truncated = source["truncated"];
	        this.files = this.convertValues(source["files"], WorkspaceTextSearchFileResult);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace tools {
	
	export class AgentModeCreationRequest {
	    name: string;
	    prompt?: string;
	    toolPermissions?: string[];
	    pathPermissions?: string[];
	    permissions?: Record<string, Array<string>>;
	
	    static createFrom(source: any = {}) {
	        return new AgentModeCreationRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.prompt = source["prompt"];
	        this.toolPermissions = source["toolPermissions"];
	        this.pathPermissions = source["pathPermissions"];
	        this.permissions = source["permissions"];
	    }
	}
	export class AgentModeCreationResult {
	    id: string;
	    name: string;
	    prompt: string;
	    toolPermissions?: string[];
	    pathPermissions?: string[];
	
	    static createFrom(source: any = {}) {
	        return new AgentModeCreationResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.prompt = source["prompt"];
	        this.toolPermissions = source["toolPermissions"];
	        this.pathPermissions = source["pathPermissions"];
	    }
	}
	export class AgentModeSummary {
	    id: string;
	    name: string;
	    toolPermissions?: string[];
	    pathPermissions?: string[];
	    builtIn: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AgentModeSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.toolPermissions = source["toolPermissions"];
	        this.pathPermissions = source["pathPermissions"];
	        this.builtIn = source["builtIn"];
	    }
	}
	export class ToolPermission {
	    name: string;
	    paths?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ToolPermission(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.paths = source["paths"];
	    }
	}

}


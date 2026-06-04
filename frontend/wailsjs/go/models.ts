export namespace llm {
	
	export class Settings {
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
	    searxngUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
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
	        this.searxngUrl = source["searxngUrl"];
	    }
	}

}

export namespace services {
	
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
	export class Workspace {
	    id: string;
	    folderPath: string;
	    displayName: string;
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
	        this.folderPath = source["folderPath"];
	        this.displayName = source["displayName"];
	        this.letter = source["letter"];
	        this.iconPath = source["iconPath"];
	        this.iconUrl = source["iconUrl"];
	        this.active = source["active"];
	        this.missing = source["missing"];
	        this.error = source["error"];
	    }
	}
	export class AppState {
	    settings: llm.Settings;
	    workspaces: Workspace[];
	    activeWorkspaceId: string;
	
	    static createFrom(source: any = {}) {
	        return new AppState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.settings = this.convertValues(source["settings"], llm.Settings);
	        this.workspaces = this.convertValues(source["workspaces"], Workspace);
	        this.activeWorkspaceId = source["activeWorkspaceId"];
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
	export class ChatMessage {
	    id: string;
	    role: string;
	    content?: string;
	    images?: ChatImageAttachment[];
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
	export class ChatMessageRequest {
	    content: string;
	    planMode: boolean;
	    images?: ChatImageInput[];
	
	    static createFrom(source: any = {}) {
	        return new ChatMessageRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.content = source["content"];
	        this.planMode = source["planMode"];
	        this.images = this.convertValues(source["images"], ChatImageInput);
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
	    toolCalls?: ChatToolActivity[];
	    affectedPaths?: string[];
	
	    static createFrom(source: any = {}) {
	        return new InlineCodePromptResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.content = source["content"];
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
	
	    static createFrom(source: any = {}) {
	        return new KanbanProgressEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.title = source["title"];
	        this.content = source["content"];
	        this.status = source["status"];
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
	    acceptanceCriteria: string[];
	    dependencies?: string[];
	    dependencyStatuses?: KanbanDependencyStatus[];
	    blockedBy?: string[];
	    eligible: boolean;
	    lane: string;
	    status: string;
	    progressTranscript?: KanbanProgressEntry[];
	
	    static createFrom(source: any = {}) {
	        return new KanbanCard(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.workspaceId = source["workspaceId"];
	        this.title = source["title"];
	        this.description = source["description"];
	        this.acceptanceCriteria = source["acceptanceCriteria"];
	        this.dependencies = source["dependencies"];
	        this.dependencyStatuses = this.convertValues(source["dependencyStatuses"], KanbanDependencyStatus);
	        this.blockedBy = source["blockedBy"];
	        this.eligible = source["eligible"];
	        this.lane = source["lane"];
	        this.status = source["status"];
	        this.progressTranscript = this.convertValues(source["progressTranscript"], KanbanProgressEntry);
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
	
	export class WorkspaceGitChangedFile {
	    path: string;
	    oldPath?: string;
	    operation: string;
	    status: string;
	    indexStatus?: string;
	    worktreeStatus?: string;
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

}


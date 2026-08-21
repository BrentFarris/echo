export type LSPDocumentSelector = {
  languageId: string;
  extensions?: string[];
  filenames?: string[];
};

export type LSPProfile = {
  id: string;
  name: string;
  command: string;
  args?: string[];
  selectors: LSPDocumentSelector[];
  environment?: Record<string, string>;
  initializationOptions?: Record<string, unknown>;
  settings?: Record<string, unknown>;
};

export type LSPProfileOverride = Partial<Omit<LSPProfile, "id">>;

export type WorkspaceLSPConfig = {
  enabledProfileIds?: string[];
  overrides?: Record<string, LSPProfileOverride>;
  formatOnSave?: boolean;
  formatOnSaveTimeoutMs?: number;
};

export type LSPStatus = {
  workspaceId: string;
  profileId: string;
  name: string;
  state: "inactive" | "starting" | "running" | "restarting" | "failed" | "stopping" | "stopped";
  message?: string;
  stderr?: string;
  restartCount?: number;
  capabilities?: Record<string, unknown>;
};

export type WorkspaceLSPResponse = {
  config: WorkspaceLSPConfig;
  profiles: LSPProfile[];
  statuses: LSPStatus[];
};

export type LSPPosition = { line: number; character: number };
export type LSPRange = { start: LSPPosition; end: LSPPosition };
export type LSPTextEdit = { range: LSPRange; newText: string };
export type LSPDocumentEdit = {
  textDocument: { uri: string; version?: number | null };
  edits: LSPTextEdit[];
};
export type LSPWorkspaceEdit = {
  changes?: Record<string, LSPTextEdit[]>;
  documentChanges?: Array<LSPDocumentEdit | { kind: string; [key: string]: unknown }>;
};

export type LSPCommand = { title: string; command: string; arguments?: unknown[] };
export type LSPCodeAction = {
  title: string;
  kind?: string;
  diagnostics?: unknown[];
  isPreferred?: boolean;
  disabled?: { reason: string };
  edit?: LSPWorkspaceEdit;
  command?: LSPCommand;
  data?: unknown;
};

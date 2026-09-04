import type { FileRef } from "../code/types";

export type DebugTransport = {
  kind: "stdio" | "server" | "connect";
  host?: string;
  port?: number;
  readyPattern?: string;
  startupTimeoutMs?: number;
};

export type AdapterProfile = {
  id: string;
  name: string;
  adapterId: string;
  command?: string;
  args?: string[];
  environment?: Record<string, string>;
  selectors?: Array<{ languageId?: string; extensions?: string[]; filenames?: string[] }>;
  transport: DebugTransport;
};

export type AdapterTemplate = {
  id: string;
  description: string;
  installGuide: string;
  profile: AdapterProfile;
};

export type LifecycleHook = {
  command: string;
  args?: string[];
  cwd?: string;
  environment?: Record<string, string>;
  timeoutMs?: number;
};

export type DebugConfiguration = {
  id: string;
  name: string;
  adapterProfileId: string;
  request: "launch" | "attach";
  arguments?: Record<string, unknown>;
  preLaunch?: LifecycleHook;
  postDebug?: LifecycleHook;
};

export type DebugCompound = {
  id: string;
  name: string;
  configurationIds: string[];
  stopAll: boolean;
};

export type DebugInput = {
  id: string;
  type: "promptString" | "secret" | "pickString" | "pickProcess";
  description?: string;
  default?: string;
  options?: string[];
};

export type WorkspaceDebugConfig = {
  version: number;
  enabledAdapterProfileIds?: string[];
  overrides?: Record<string, unknown>;
  configurations?: DebugConfiguration[];
  compounds?: DebugCompound[];
  inputs?: DebugInput[];
};

export type SourceBreakpoint = {
  id: string;
  source: FileRef;
  line: number;
  column?: number;
  enabled: boolean;
  condition?: string;
  hitCondition?: string;
  logMessage?: string;
};

export type FunctionBreakpoint = {
  id: string;
  name: string;
  enabled: boolean;
  condition?: string;
  hitCondition?: string;
};

export type InstructionBreakpoint = {
  id: string;
  instructionReference: string;
  offset?: number;
  enabled: boolean;
  condition?: string;
  hitCondition?: string;
};

export type DataBreakpoint = {
  id: string;
  dataId: string;
  name?: string;
  adapterProfileId?: string;
  accessType?: "read" | "write" | "readWrite";
  enabled: boolean;
  condition?: string;
  hitCondition?: string;
  sessionOnly?: boolean;
};

export type ExceptionBreakpoint = { filter: string; enabled: boolean; condition?: string };
export type DebugWatch = { id: string; expression: string; enabled: boolean };

export type DebugPersistentState = {
  revision: number;
  selectedConfigurationId?: string;
  sourceBreakpoints?: SourceBreakpoint[];
  functionBreakpoints?: FunctionBreakpoint[];
  instructionBreakpoints?: InstructionBreakpoint[];
  dataBreakpoints?: DataBreakpoint[];
  exceptionBreakpoints?: ExceptionBreakpoint[];
  watches?: DebugWatch[];
};

export type DebugOutput = {
  sequence: number;
  category: string;
  output: string;
  timestamp: string;
  data?: Record<string, unknown>;
};

export type DebugSource = {
  name?: string;
  path?: string;
  sourceReference?: number;
  echoRef?: FileRef;
  presentationHint?: string;
};

export type DebugLocation = DebugSource & { ref?: FileRef; line?: number; column?: number };

export type DebugSession = {
  id: string;
  workspaceId: string;
  groupId?: string;
  parentSessionId?: string;
  configurationId?: string;
  configuration: string;
  adapterProfileId: string;
  request: "launch" | "attach";
  status: "starting" | "configuring" | "running" | "stopped" | "terminating" | "terminated" | "failed";
  revision: number;
  stopGeneration: number;
  stoppedReason?: string;
  stoppedText?: string;
  threadId?: number;
  allThreadsStopped?: boolean;
  capabilities?: Record<string, unknown>;
  location?: DebugLocation;
  error?: string;
  startedAt: string;
  endedAt?: string;
  lastOutputSequence?: number;
  output?: DebugOutput[];
  breakpoints?: Array<{ stateId: string; adapterId?: number; kind: string; verified: boolean; message?: string; line?: number; column?: number; source?: DebugLocation }>;
  traceDAP?: boolean;
};

export type DebugGroup = { id: string; name: string; sessionIds: string[]; stopAll: boolean };
export type DebugSnapshot = {
  workspaceId: string;
  sequence: number;
  sessions: DebugSession[];
  groups?: DebugGroup[];
  state: DebugPersistentState;
};

export type DebugEvent = {
  type: "debug_event";
  workspaceId: string;
  sessionId?: string;
  groupId?: string;
  sequence: number;
  revision?: number;
  event: string;
  body?: unknown;
  session?: DebugSession;
  state?: DebugPersistentState;
  output?: DebugOutput;
  message?: string;
};

export type DAPThread = { id: number; name: string };
export type DAPStackFrame = {
  id: number;
  name: string;
  source?: DebugSource;
  line: number;
  column: number;
  endLine?: number;
  endColumn?: number;
  instructionPointerReference?: string;
  moduleId?: string | number;
  presentationHint?: string;
};
export type DAPScope = {
  name: string;
  variablesReference: number;
  expensive: boolean;
  namedVariables?: number;
  indexedVariables?: number;
  presentationHint?: string;
};
export type DAPVariable = {
  name: string;
  value: string;
  type?: string;
  evaluateName?: string;
  variablesReference: number;
  namedVariables?: number;
  indexedVariables?: number;
  memoryReference?: string;
  presentationHint?: Record<string, unknown>;
};

export type DebugImportPreview = {
  config: WorkspaceDebugConfig;
  warnings?: Array<{ code: string; message: string; configurationName?: string; taskLabel?: string; field?: string }>;
};

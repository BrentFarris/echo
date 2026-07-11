export type DebugStatus =
  | "idle"
  | "starting"
  | "running"
  | "paused"
  | "stopping"
  | "terminated"
  | "error";

export type DebugConfigurationSummary = {
  name: string;
  type: string;
  request: string;
};

export type WorkspaceDebugSettings = {
  workspaceId: string;
  storagePath: string;
  revision: string;
  selectedConfiguration: string;
  configurations: DebugConfigurationSummary[];
  json: string;
  implicit: boolean;
};

export type DebugSourceBreakpoint = {
  line: number;
  column?: number;
};

export type DebugBreakpoint = {
  id?: number;
  path: string;
  line: number;
  column?: number;
  verified: boolean;
  message?: string;
};

export type DebugSourceLocation = {
  path?: string;
  name?: string;
  line?: number;
  column?: number;
  external?: boolean;
  sourceReference?: number;
};

export type DebugState = {
  workspaceId?: string;
  sessionId?: string;
  revision: number;
  status: DebugStatus;
  configuration?: string;
  adapterType?: string;
  threadId?: number;
  frameId?: number;
  currentLocation?: DebugSourceLocation;
  breakpoints?: DebugBreakpoint[];
  output?: string;
  error?: string;
  capabilities?: unknown;
};

export type DebugEvent = {
  workspaceId?: string;
  sessionId?: string;
  revision: number;
  type: string;
  state?: DebugState;
  category?: string;
  output?: string;
  message?: string;
};

export type DebugThread = { id: number; name: string };

export type DebugStackFrame = {
  id: number;
  name: string;
  location: DebugSourceLocation;
};

export type DebugScope = {
  name: string;
  presentationHint?: string;
  variablesReference: number;
  namedVariables?: number;
  indexedVariables?: number;
  expensive: boolean;
  location?: DebugSourceLocation;
};

export type DebugVariable = {
  name: string;
  value: string;
  type?: string;
  evaluateName?: string;
  variablesReference: number;
  namedVariables?: number;
  indexedVariables?: number;
  memoryReference?: string;
};

export type DebugThreadsResponse = {
  workspaceId: string;
  sessionId: string;
  revision: number;
  threads: DebugThread[];
};

export type DebugStackTraceResponse = {
  workspaceId: string;
  sessionId: string;
  revision: number;
  stackFrames: DebugStackFrame[];
  totalFrames?: number;
};

export type DebugScopesResponse = {
  workspaceId: string;
  sessionId: string;
  revision: number;
  scopes: DebugScope[];
};

export type DebugVariablesResponse = {
  workspaceId: string;
  sessionId: string;
  revision: number;
  variables: DebugVariable[];
};

export type DebugEvaluateResponse = {
  workspaceId: string;
  sessionId: string;
  revision: number;
  result: string;
  type?: string;
  variablesReference?: number;
  namedVariables?: number;
  indexedVariables?: number;
  memoryReference?: string;
};

import * as Wails from "../../wailsjs/go/services/SystemService";
import { chooseWorkspaceFileSavePathWeb, chooseWorkspaceFolderForWorkspaceWeb, chooseWorkspaceFolderWeb, chooseWorkspaceGitCloneParentWeb, chooseWorkspaceIconWeb, isWailsRuntime, webRpc } from "./web";
import type {
  DebugEvaluateResponse,
  DebugScopesResponse,
  DebugStackTraceResponse,
  DebugState,
  DebugThreadsResponse,
  DebugVariablesResponse,
  WorkspaceDebugSettings,
} from "../codeView/debugTypes";

type WailsFunction = (...args: any[]) => Promise<any>;

function call<F extends WailsFunction>(method: string, wailsFn: F, args: Parameters<F>): ReturnType<F> {
  if (isWailsRuntime()) {
    return wailsFn(...args) as ReturnType<F>;
  }
  return webRpc(method, args) as ReturnType<F>;
}

export function AddKanbanCardMessage(...args: Parameters<typeof Wails.AddKanbanCardMessage>): ReturnType<typeof Wails.AddKanbanCardMessage> {
  return call("AddKanbanCardMessage", Wails.AddKanbanCardMessage, args);
}

export function AddWorkspace(...args: Parameters<typeof Wails.AddWorkspace>): ReturnType<typeof Wails.AddWorkspace> {
  return call("AddWorkspace", Wails.AddWorkspace, args);
}

export function AddWorkspaceFolder(...args: Parameters<typeof Wails.AddWorkspaceFolder>): ReturnType<typeof Wails.AddWorkspaceFolder> {
  return call("AddWorkspaceFolder", Wails.AddWorkspaceFolder, args);
}

export function AppInfo(...args: Parameters<typeof Wails.AppInfo>): ReturnType<typeof Wails.AppInfo> {
  return call("AppInfo", Wails.AppInfo, args);
}

export function ChooseWorkspaceFolder(): ReturnType<typeof Wails.ChooseWorkspaceFolder> {
  if (isWailsRuntime()) {
    return Wails.ChooseWorkspaceFolder();
  }
  return chooseWorkspaceFolderWeb() as ReturnType<typeof Wails.ChooseWorkspaceFolder>;
}

export function ChooseWorkspaceFolderForWorkspace(...args: Parameters<typeof Wails.ChooseWorkspaceFolderForWorkspace>): ReturnType<typeof Wails.ChooseWorkspaceFolderForWorkspace> {
  if (isWailsRuntime()) {
    return Wails.ChooseWorkspaceFolderForWorkspace(...args);
  }
  return chooseWorkspaceFolderForWorkspaceWeb(args[0]) as ReturnType<typeof Wails.ChooseWorkspaceFolderForWorkspace>;
}

export function ChooseWorkspaceIcon(...args: Parameters<typeof Wails.ChooseWorkspaceIcon>): ReturnType<typeof Wails.ChooseWorkspaceIcon> {
  if (isWailsRuntime()) {
    return Wails.ChooseWorkspaceIcon(...args);
  }
  return chooseWorkspaceIconWeb(args[0]) as ReturnType<typeof Wails.ChooseWorkspaceIcon>;
}

export function CreateAgentMode(...args: Parameters<typeof Wails.CreateAgentMode>): ReturnType<typeof Wails.CreateAgentMode> {
  return call("CreateAgentMode", Wails.CreateAgentMode, args);
}

export function CreateAgentModePerTool(...args: Parameters<typeof Wails.CreateAgentModePerTool>): ReturnType<typeof Wails.CreateAgentModePerTool> {
  return call("CreateAgentModePerTool", Wails.CreateAgentModePerTool, args);
}

export function CreateAgentModeFromChat(...args: Parameters<typeof Wails.CreateAgentModeFromChat>): ReturnType<typeof Wails.CreateAgentModeFromChat> {
  return call("CreateAgentModeFromChat", Wails.CreateAgentModeFromChat, args);
}

export function ClearChat(...args: Parameters<typeof Wails.ClearChat>): ReturnType<typeof Wails.ClearChat> {
  return call("ClearChat", Wails.ClearChat, args);
}

export function ClearDoneKanbanCards(...args: Parameters<typeof Wails.ClearDoneKanbanCards>): ReturnType<typeof Wails.ClearDoneKanbanCards> {
  return call("ClearDoneKanbanCards", Wails.ClearDoneKanbanCards, args);
}

export function ClearWorkspaceChangeReview(...args: Parameters<typeof Wails.ClearWorkspaceChangeReview>): ReturnType<typeof Wails.ClearWorkspaceChangeReview> {
  return call("ClearWorkspaceChangeReview", Wails.ClearWorkspaceChangeReview, args);
}

export function ClearWorkspaceIcon(...args: Parameters<typeof Wails.ClearWorkspaceIcon>): ReturnType<typeof Wails.ClearWorkspaceIcon> {
  return call("ClearWorkspaceIcon", Wails.ClearWorkspaceIcon, args);
}

export function CloseKanbanCardDetail(...args: Parameters<typeof Wails.CloseKanbanCardDetail>): ReturnType<typeof Wails.CloseKanbanCardDetail> {
  return call("CloseKanbanCardDetail", Wails.CloseKanbanCardDetail, args);
}

export function CompleteWorkspaceFile(...args: Parameters<typeof Wails.CompleteWorkspaceFile>): ReturnType<typeof Wails.CompleteWorkspaceFile> {
  return call("CompleteWorkspaceFile", Wails.CompleteWorkspaceFile, args);
}

export function CreateKanbanCardFromChatMessage(...args: Parameters<typeof Wails.CreateKanbanCardFromChatMessage>): ReturnType<typeof Wails.CreateKanbanCardFromChatMessage> {
  return call("CreateKanbanCardFromChatMessage", Wails.CreateKanbanCardFromChatMessage, args);
}

export function CreateKanbanCardFromTask(...args: Parameters<typeof Wails.CreateKanbanCardFromTask>): ReturnType<typeof Wails.CreateKanbanCardFromTask> {
  return call("CreateKanbanCardFromTask", Wails.CreateKanbanCardFromTask, args);
}

export function CreateSkillFromChat(...args: Parameters<typeof Wails.CreateSkillFromChat>): ReturnType<typeof Wails.CreateSkillFromChat> {
  return call("CreateSkillFromChat", Wails.CreateSkillFromChat, args);
}

export function CreateReadyKanbanCard(...args: Parameters<typeof Wails.CreateReadyKanbanCard>): ReturnType<typeof Wails.CreateReadyKanbanCard> {
  return call("CreateReadyKanbanCard", Wails.CreateReadyKanbanCard, args);
}

export function CreateWorkspaceFile(...args: Parameters<typeof Wails.CreateWorkspaceFile>): ReturnType<typeof Wails.CreateWorkspaceFile> {
  return call("CreateWorkspaceFile", Wails.CreateWorkspaceFile, args);
}

export function CreateWorkspaceFolder(...args: Parameters<typeof Wails.CreateWorkspaceFolder>): ReturnType<typeof Wails.CreateWorkspaceFolder> {
  return call("CreateWorkspaceFolder", Wails.CreateWorkspaceFolder, args);
}

export function CreateWorkspaceTask(...args: Parameters<typeof Wails.CreateWorkspaceTask>): ReturnType<typeof Wails.CreateWorkspaceTask> {
  return call("CreateWorkspaceTask", Wails.CreateWorkspaceTask, args);
}

export function DeleteAgentMode(...args: Parameters<typeof Wails.DeleteAgentMode>): ReturnType<typeof Wails.DeleteAgentMode> {
  return call("DeleteAgentMode", Wails.DeleteAgentMode, args);
}

export function DeleteKanbanCard(...args: Parameters<typeof Wails.DeleteKanbanCard>): ReturnType<typeof Wails.DeleteKanbanCard> {
  return call("DeleteKanbanCard", Wails.DeleteKanbanCard, args);
}

export function DeleteWorkspaceTask(...args: Parameters<typeof Wails.DeleteWorkspaceTask>): ReturnType<typeof Wails.DeleteWorkspaceTask> {
  return call("DeleteWorkspaceTask", Wails.DeleteWorkspaceTask, args);
}

export function DeleteWorkspace(...args: Parameters<typeof Wails.DeleteWorkspace>): ReturnType<typeof Wails.DeleteWorkspace> {
  return call("DeleteWorkspace", Wails.DeleteWorkspace, args);
}

export function DeleteWorkspacePaths(...args: Parameters<typeof Wails.DeleteWorkspacePaths>): ReturnType<typeof Wails.DeleteWorkspacePaths> {
  return call("DeleteWorkspacePaths", Wails.DeleteWorkspacePaths, args);
}

export function EditChatMessage(...args: Parameters<typeof Wails.EditChatMessage>): ReturnType<typeof Wails.EditChatMessage> {
  return call("EditChatMessage", Wails.EditChatMessage, args);
}

export function ExecutePlan(...args: Parameters<typeof Wails.ExecutePlan>): ReturnType<typeof Wails.ExecutePlan> {
  return call("ExecutePlan", Wails.ExecutePlan, args);
}

export function FindWorkspaceFileDefinition(...args: Parameters<typeof Wails.FindWorkspaceFileDefinition>): ReturnType<typeof Wails.FindWorkspaceFileDefinition> {
  return call("FindWorkspaceFileDefinition", Wails.FindWorkspaceFileDefinition, args);
}

export function ChooseWorkspaceFileSavePath(...args: Parameters<typeof Wails.ChooseWorkspaceFileSavePath>): ReturnType<typeof Wails.ChooseWorkspaceFileSavePath> {
  if (isWailsRuntime()) {
    return Wails.ChooseWorkspaceFileSavePath(...args);
  }
  return chooseWorkspaceFileSavePathWeb(args[0], args[1]) as ReturnType<typeof Wails.ChooseWorkspaceFileSavePath>;
}

export function FindWorkspaceFileImplementations(...args: Parameters<typeof Wails.FindWorkspaceFileImplementations>): ReturnType<typeof Wails.FindWorkspaceFileImplementations> {
  return call("FindWorkspaceFileImplementations", Wails.FindWorkspaceFileImplementations, args);
}

export function FindWorkspaceFileReferences(...args: Parameters<typeof Wails.FindWorkspaceFileReferences>): ReturnType<typeof Wails.FindWorkspaceFileReferences> {
  return call("FindWorkspaceFileReferences", Wails.FindWorkspaceFileReferences, args);
}

export function UpdateAgentMode(...args: Parameters<typeof Wails.UpdateAgentMode>): ReturnType<typeof Wails.UpdateAgentMode> {
  return call("UpdateAgentMode", Wails.UpdateAgentMode, args);
}

export function UpdateAgentModePerTool(...args: Parameters<typeof Wails.UpdateAgentModePerTool>): ReturnType<typeof Wails.UpdateAgentModePerTool> {
  return call("UpdateAgentModePerTool", Wails.UpdateAgentModePerTool, args);
}

export function GetHeartbeatConfig(...args: Parameters<typeof Wails.GetHeartbeatConfig>): ReturnType<typeof Wails.GetHeartbeatConfig> {
  return call("GetHeartbeatConfig", Wails.GetHeartbeatConfig, args);
}

export function GetDashboardLayouts(...args: Parameters<typeof Wails.GetDashboardLayouts>): ReturnType<typeof Wails.GetDashboardLayouts> {
  return call("GetDashboardLayouts", Wails.GetDashboardLayouts, args);
}

export function SaveDashboardLayout(...args: Parameters<typeof Wails.SaveDashboardLayout>): ReturnType<typeof Wails.SaveDashboardLayout> {
  return call("SaveDashboardLayout", Wails.SaveDashboardLayout, args);
}

export function StartHeartbeat(...args: Parameters<typeof Wails.StartHeartbeat>): ReturnType<typeof Wails.StartHeartbeat> {
  return call("StartHeartbeat", Wails.StartHeartbeat, args);
}

export function StopHeartbeat(...args: Parameters<typeof Wails.StopHeartbeat>): ReturnType<typeof Wails.StopHeartbeat> {
  return call("StopHeartbeat", Wails.StopHeartbeat, args);
}

export function GetWatchdogConfig(...args: Parameters<typeof Wails.GetWatchdogConfig>): ReturnType<typeof Wails.GetWatchdogConfig> {
  return call("GetWatchdogConfig", Wails.GetWatchdogConfig, args);
}

export function StartWatchdog(...args: Parameters<typeof Wails.StartWatchdog>): ReturnType<typeof Wails.StartWatchdog> {
  return call("StartWatchdog", Wails.StartWatchdog, args);
}

export function StopWatchdog(...args: Parameters<typeof Wails.StopWatchdog>): ReturnType<typeof Wails.StopWatchdog> {
  return call("StopWatchdog", Wails.StopWatchdog, args);
}

export function ListAgentModes(...args: Parameters<typeof Wails.ListAgentModes>): ReturnType<typeof Wails.ListAgentModes> {
  return call("ListAgentModes", Wails.ListAgentModes, args);
}

export function ListWorkspaceDirectory(...args: Parameters<typeof Wails.ListWorkspaceDirectory>): ReturnType<typeof Wails.ListWorkspaceDirectory> {
  return call("ListWorkspaceDirectory", Wails.ListWorkspaceDirectory, args);
}

export function LoadChatSession(...args: Parameters<typeof Wails.LoadChatSession>): ReturnType<typeof Wails.LoadChatSession> {
  return call("LoadChatSession", Wails.LoadChatSession, args);
}

export function LoadKanbanBoard(...args: Parameters<typeof Wails.LoadKanbanBoard>): ReturnType<typeof Wails.LoadKanbanBoard> {
  return call("LoadKanbanBoard", Wails.LoadKanbanBoard, args);
}

export function LoadTaskBoard(...args: Parameters<typeof Wails.LoadTaskBoard>): ReturnType<typeof Wails.LoadTaskBoard> {
  return call("LoadTaskBoard", Wails.LoadTaskBoard, args);
}

export function LoadRuntimeStatus(...args: Parameters<typeof Wails.LoadRuntimeStatus>): ReturnType<typeof Wails.LoadRuntimeStatus> {
  return call("LoadRuntimeStatus", Wails.LoadRuntimeStatus, args);
}

export function LoadState(...args: Parameters<typeof Wails.LoadState>): ReturnType<typeof Wails.LoadState> {
  return call("LoadState", Wails.LoadState, args);
}

export function LoadWebAccessStatus(...args: Parameters<typeof Wails.LoadWebAccessStatus>): ReturnType<typeof Wails.LoadWebAccessStatus> {
  return call("LoadWebAccessStatus", Wails.LoadWebAccessStatus, args);
}

export function LoadWorkspaceChangeReview(...args: Parameters<typeof Wails.LoadWorkspaceChangeReview>): ReturnType<typeof Wails.LoadWorkspaceChangeReview> {
  return call("LoadWorkspaceChangeReview", Wails.LoadWorkspaceChangeReview, args);
}

export function LoadWorkspaceGitChanges(...args: Parameters<typeof Wails.LoadWorkspaceGitChanges>): ReturnType<typeof Wails.LoadWorkspaceGitChanges> {
  return call("LoadWorkspaceGitChanges", Wails.LoadWorkspaceGitChanges, args);
}

export function LoadWorkspaceGitCommit(...args: Parameters<typeof Wails.LoadWorkspaceGitCommit>): ReturnType<typeof Wails.LoadWorkspaceGitCommit> {
  return call("LoadWorkspaceGitCommit", Wails.LoadWorkspaceGitCommit, args);
}

export function LoadWorkspaceGitFileDiff(...args: Parameters<typeof Wails.LoadWorkspaceGitFileDiff>): ReturnType<typeof Wails.LoadWorkspaceGitFileDiff> {
  return call("LoadWorkspaceGitFileDiff", Wails.LoadWorkspaceGitFileDiff, args);
}

export function LoadWorkspaceGitRepository(...args: Parameters<typeof Wails.LoadWorkspaceGitRepository>): ReturnType<typeof Wails.LoadWorkspaceGitRepository> {
  return call("LoadWorkspaceGitRepository", Wails.LoadWorkspaceGitRepository, args);
}

export function LoadWorkspaceGitStash(...args: Parameters<typeof Wails.LoadWorkspaceGitStash>): ReturnType<typeof Wails.LoadWorkspaceGitStash> {
  return call("LoadWorkspaceGitStash", Wails.LoadWorkspaceGitStash, args);
}

export function RunWorkspaceGitAction(...args: Parameters<typeof Wails.RunWorkspaceGitAction>): ReturnType<typeof Wails.RunWorkspaceGitAction> {
  return call("RunWorkspaceGitAction", Wails.RunWorkspaceGitAction, args);
}

export function ChooseWorkspaceGitCloneParent(): ReturnType<typeof Wails.ChooseWorkspaceGitCloneParent> {
  if (isWailsRuntime()) {
    return Wails.ChooseWorkspaceGitCloneParent();
  }
  return chooseWorkspaceGitCloneParentWeb() as ReturnType<typeof Wails.ChooseWorkspaceGitCloneParent>;
}

export function CloneWorkspaceGitRepository(...args: Parameters<typeof Wails.CloneWorkspaceGitRepository>): ReturnType<typeof Wails.CloneWorkspaceGitRepository> {
  return call("CloneWorkspaceGitRepository", Wails.CloneWorkspaceGitRepository, args);
}

export function CommitWorkspaceGitChanges(...args: Parameters<typeof Wails.CommitWorkspaceGitChanges>): ReturnType<typeof Wails.CommitWorkspaceGitChanges> {
  return call("CommitWorkspaceGitChanges", Wails.CommitWorkspaceGitChanges, args);
}

export function DiscardWorkspaceGitChanges(...args: Parameters<typeof Wails.DiscardWorkspaceGitChanges>): ReturnType<typeof Wails.DiscardWorkspaceGitChanges> {
  return call("DiscardWorkspaceGitChanges", Wails.DiscardWorkspaceGitChanges, args);
}

export function DiscardWorkspaceGitFile(...args: Parameters<typeof Wails.DiscardWorkspaceGitFile>): ReturnType<typeof Wails.DiscardWorkspaceGitFile> {
  return call("DiscardWorkspaceGitFile", Wails.DiscardWorkspaceGitFile, args);
}

export function CreateWorkspaceGitBranch(...args: Parameters<typeof Wails.CreateWorkspaceGitBranch>): ReturnType<typeof Wails.CreateWorkspaceGitBranch> {
  return call("CreateWorkspaceGitBranch", Wails.CreateWorkspaceGitBranch, args);
}

export function SwitchWorkspaceGitBranch(...args: Parameters<typeof Wails.SwitchWorkspaceGitBranch>): ReturnType<typeof Wails.SwitchWorkspaceGitBranch> {
  return call("SwitchWorkspaceGitBranch", Wails.SwitchWorkspaceGitBranch, args);
}

export function SyncWorkspaceGitBranch(...args: Parameters<typeof Wails.SyncWorkspaceGitBranch>): ReturnType<typeof Wails.SyncWorkspaceGitBranch> {
  return call("SyncWorkspaceGitBranch", Wails.SyncWorkspaceGitBranch, args);
}

export function MergeWorkspaceGitBranch(...args: Parameters<typeof Wails.MergeWorkspaceGitBranch>): ReturnType<typeof Wails.MergeWorkspaceGitBranch> {
  return call("MergeWorkspaceGitBranch", Wails.MergeWorkspaceGitBranch, args);
}

// Keep debug calls on the same small Wails/web RPC boundary. The generated
// bindings still describe all request and response models for consumers.
function callSystemService<T>(method: string, args: unknown[]): Promise<T> {
  if (isWailsRuntime()) {
    const service = (window as any)?.go?.services?.SystemService;
    const methodFn = service?.[method];
    if (typeof methodFn !== "function") {
      return Promise.reject(new Error(`Backend method ${method} is unavailable.`));
    }
    return methodFn(...args) as Promise<T>;
  }
  return webRpc<T>(method, args);
}

export function LoadWorkspaceDebugSettings(workspaceID: string): Promise<WorkspaceDebugSettings> {
  return callSystemService("LoadWorkspaceDebugSettings", [workspaceID]);
}

export function SaveWorkspaceDebugSettings(
  workspaceID: string,
  input: { json: string; expectedRevision: string },
): Promise<WorkspaceDebugSettings> {
  return callSystemService("SaveWorkspaceDebugSettings", [workspaceID, input]);
}

export function SetWorkspaceSelectedDebugConfiguration(
  workspaceID: string,
  name: string,
): Promise<WorkspaceDebugSettings> {
  return callSystemService("SetWorkspaceSelectedDebugConfiguration", [workspaceID, name]);
}

export function LoadDebugState(workspaceID: string): Promise<DebugState> {
  return callSystemService("LoadDebugState", [workspaceID]);
}

export function StartDebugSession(
  workspaceID: string,
  request: { configurationName?: string; currentFile?: string },
): Promise<DebugState> {
  return callSystemService("StartDebugSession", [workspaceID, request]);
}

export function ContinueDebugSession(
  workspaceID: string,
  request: { sessionId: string },
): Promise<DebugState> {
  return callSystemService("ContinueDebugSession", [workspaceID, request]);
}

export function PauseDebugSession(
  workspaceID: string,
  request: { sessionId: string },
): Promise<DebugState> {
  return callSystemService("PauseDebugSession", [workspaceID, request]);
}

export function StepOverDebugSession(
  workspaceID: string,
  request: { sessionId: string },
): Promise<DebugState> {
  return callSystemService("StepOverDebugSession", [workspaceID, request]);
}

export function StepIntoDebugSession(
  workspaceID: string,
  request: { sessionId: string },
): Promise<DebugState> {
  return callSystemService("StepIntoDebugSession", [workspaceID, request]);
}

export function StepOutDebugSession(
  workspaceID: string,
  request: { sessionId: string },
): Promise<DebugState> {
  return callSystemService("StepOutDebugSession", [workspaceID, request]);
}

export function StopDebugSession(
  workspaceID: string,
  request: { sessionId: string },
): Promise<DebugState> {
  return callSystemService("StopDebugSession", [workspaceID, request]);
}

export function SetDebugBreakpoints(
  workspaceID: string,
  request: { sessionId?: string; sourcePath: string; breakpoints: { line: number; column?: number }[] },
): Promise<DebugState> {
  return callSystemService("SetDebugBreakpoints", [workspaceID, request]);
}

export function LoadDebugThreads(
  workspaceID: string,
  request: { sessionId: string },
): Promise<DebugThreadsResponse> {
  return callSystemService("LoadDebugThreads", [workspaceID, request]);
}

export function LoadDebugStackTrace(
  workspaceID: string,
  request: { sessionId: string; threadId: number; startFrame?: number; levels?: number },
): Promise<DebugStackTraceResponse> {
  return callSystemService("LoadDebugStackTrace", [workspaceID, request]);
}

export function LoadDebugScopes(
  workspaceID: string,
  request: { sessionId: string; frameId: number },
): Promise<DebugScopesResponse> {
  return callSystemService("LoadDebugScopes", [workspaceID, request]);
}

export function LoadDebugVariables(
  workspaceID: string,
  request: { sessionId: string; variablesReference: number; filter?: string; start?: number; count?: number },
): Promise<DebugVariablesResponse> {
  return callSystemService("LoadDebugVariables", [workspaceID, request]);
}

export function EvaluateDebugExpression(
  workspaceID: string,
  request: { sessionId: string; expression: string; frameId?: number; context?: string },
): Promise<DebugEvaluateResponse> {
  return callSystemService("EvaluateDebugExpression", [workspaceID, request]);
}

export function StageWorkspaceGitChanges(...args: Parameters<typeof Wails.StageWorkspaceGitChanges>): ReturnType<typeof Wails.StageWorkspaceGitChanges> {
  return call("StageWorkspaceGitChanges", Wails.StageWorkspaceGitChanges, args);
}

export function StageWorkspaceGitFile(...args: Parameters<typeof Wails.StageWorkspaceGitFile>): ReturnType<typeof Wails.StageWorkspaceGitFile> {
  return call("StageWorkspaceGitFile", Wails.StageWorkspaceGitFile, args);
}

export function UnstageWorkspaceGitChanges(...args: Parameters<typeof Wails.UnstageWorkspaceGitChanges>): ReturnType<typeof Wails.UnstageWorkspaceGitChanges> {
  return call("UnstageWorkspaceGitChanges", Wails.UnstageWorkspaceGitChanges, args);
}

export function UnstageWorkspaceGitFile(...args: Parameters<typeof Wails.UnstageWorkspaceGitFile>): ReturnType<typeof Wails.UnstageWorkspaceGitFile> {
  return call("UnstageWorkspaceGitFile", Wails.UnstageWorkspaceGitFile, args);
}

export function MoveKanbanCard(...args: Parameters<typeof Wails.MoveKanbanCard>): ReturnType<typeof Wails.MoveKanbanCard> {
  return call("MoveKanbanCard", Wails.MoveKanbanCard, args);
}

export function MoveWorkspaceTask(...args: Parameters<typeof Wails.MoveWorkspaceTask>): ReturnType<typeof Wails.MoveWorkspaceTask> {
  return call("MoveWorkspaceTask", Wails.MoveWorkspaceTask, args);
}

export function MoveWorkspacePath(...args: Parameters<typeof Wails.MoveWorkspacePath>): ReturnType<typeof Wails.MoveWorkspacePath> {
  return call("MoveWorkspacePath", Wails.MoveWorkspacePath, args);
}

export function OpenKanbanCardDetail(...args: Parameters<typeof Wails.OpenKanbanCardDetail>): ReturnType<typeof Wails.OpenKanbanCardDetail> {
  return call("OpenKanbanCardDetail", Wails.OpenKanbanCardDetail, args);
}

export function OpenWorkspaceExplorer(...args: Parameters<typeof Wails.OpenWorkspaceExplorer>): ReturnType<typeof Wails.OpenWorkspaceExplorer> {
  return call("OpenWorkspaceExplorer", Wails.OpenWorkspaceExplorer, args);
}

export function OpenWorkspacePathExplorer(...args: Parameters<typeof Wails.OpenWorkspacePathExplorer>): ReturnType<typeof Wails.OpenWorkspacePathExplorer> {
  return call("OpenWorkspacePathExplorer", Wails.OpenWorkspacePathExplorer, args);
}

export function PruneChatMessage(...args: Parameters<typeof Wails.PruneChatMessage>): ReturnType<typeof Wails.PruneChatMessage> {
  return call("PruneChatMessage", Wails.PruneChatMessage, args);
}

export function PrepareRebuildAndRelaunch(workspaceID: string): Promise<void> {
  if (isWailsRuntime()) {
    return (window as any)["go"]["services"]["SystemService"]["PrepareRebuildAndRelaunch"](workspaceID);
  }
  return webRpc("PrepareRebuildAndRelaunch", [workspaceID]);
}

export function PrepareWorkspaceSymbolRename(...args: Parameters<typeof Wails.PrepareWorkspaceSymbolRename>): ReturnType<typeof Wails.PrepareWorkspaceSymbolRename> {
  return call("PrepareWorkspaceSymbolRename", Wails.PrepareWorkspaceSymbolRename, args);
}

export function ReadWorkspaceFile(...args: Parameters<typeof Wails.ReadWorkspaceFile>): ReturnType<typeof Wails.ReadWorkspaceFile> {
  return call("ReadWorkspaceFile", Wails.ReadWorkspaceFile, args);
}

export function ReadWorkspaceMediaFile(...args: Parameters<typeof Wails.ReadWorkspaceMediaFile>): ReturnType<typeof Wails.ReadWorkspaceMediaFile> {
  return call("ReadWorkspaceMediaFile", Wails.ReadWorkspaceMediaFile, args);
}

export function RemoveWorkspaceFolder(...args: Parameters<typeof Wails.RemoveWorkspaceFolder>): ReturnType<typeof Wails.RemoveWorkspaceFolder> {
  return call("RemoveWorkspaceFolder", Wails.RemoveWorkspaceFolder, args);
}

export function RenameWorkspacePath(...args: Parameters<typeof Wails.RenameWorkspacePath>): ReturnType<typeof Wails.RenameWorkspacePath> {
  return call("RenameWorkspacePath", Wails.RenameWorkspacePath, args);
}

export function RenameWorkspaceSymbol(...args: Parameters<typeof Wails.RenameWorkspaceSymbol>): ReturnType<typeof Wails.RenameWorkspaceSymbol> {
  return call("RenameWorkspaceSymbol", Wails.RenameWorkspaceSymbol, args);
}

export function ReplayWorkspaceSymbolRename(...args: Parameters<typeof Wails.ReplayWorkspaceSymbolRename>): ReturnType<typeof Wails.ReplayWorkspaceSymbolRename> {
  return call("ReplayWorkspaceSymbolRename", Wails.ReplayWorkspaceSymbolRename, args);
}

export function ReorderWorkspaceTasks(...args: Parameters<typeof Wails.ReorderWorkspaceTasks>): ReturnType<typeof Wails.ReorderWorkspaceTasks> {
  return call("ReorderWorkspaceTasks", Wails.ReorderWorkspaceTasks, args);
}

export function ResetKanbanCard(...args: Parameters<typeof Wails.ResetKanbanCard>): ReturnType<typeof Wails.ResetKanbanCard> {
  return call("ResetKanbanCard", Wails.ResetKanbanCard, args);
}

export function ReadExternalTextFile(...args: Parameters<typeof Wails.ReadExternalTextFile>): ReturnType<typeof Wails.ReadExternalTextFile> {
  return call("ReadExternalTextFile", Wails.ReadExternalTextFile, args);
}

export function ResolveWorkspaceTextFilePath(...args: Parameters<typeof Wails.ResolveWorkspaceTextFilePath>): ReturnType<typeof Wails.ResolveWorkspaceTextFilePath> {
  return call("ResolveWorkspaceTextFilePath", Wails.ResolveWorkspaceTextFilePath, args);
}

export function ReorderWorkspaces(...args: Parameters<typeof Wails.ReorderWorkspaces>): ReturnType<typeof Wails.ReorderWorkspaces> {
  return call("ReorderWorkspaces", Wails.ReorderWorkspaces, args);
}

export function RetryChatMessage(...args: Parameters<typeof Wails.RetryChatMessage>): ReturnType<typeof Wails.RetryChatMessage> {
  return call("RetryChatMessage", Wails.RetryChatMessage, args);
}

export function RotateWebAccessToken(...args: Parameters<typeof Wails.RotateWebAccessToken>): ReturnType<typeof Wails.RotateWebAccessToken> {
  return call("RotateWebAccessToken", Wails.RotateWebAccessToken, args);
}

export function SaveSettings(...args: Parameters<typeof Wails.SaveSettings>): ReturnType<typeof Wails.SaveSettings> {
  return call("SaveSettings", Wails.SaveSettings, args);
}

export function SaveExternalTextFile(...args: Parameters<typeof Wails.SaveExternalTextFile>): ReturnType<typeof Wails.SaveExternalTextFile> {
  return call("SaveExternalTextFile", Wails.SaveExternalTextFile, args);
}

export function SaveWebAccessSettings(...args: Parameters<typeof Wails.SaveWebAccessSettings>): ReturnType<typeof Wails.SaveWebAccessSettings> {
  return call("SaveWebAccessSettings", Wails.SaveWebAccessSettings, args);
}

export function SaveWorkspaceFile(...args: Parameters<typeof Wails.SaveWorkspaceFile>): ReturnType<typeof Wails.SaveWorkspaceFile> {
  return call("SaveWorkspaceFile", Wails.SaveWorkspaceFile, args);
}

export function SaveWorkspaceFileAs(...args: Parameters<typeof Wails.SaveWorkspaceFileAs>): ReturnType<typeof Wails.SaveWorkspaceFileAs> {
  return call("SaveWorkspaceFileAs", Wails.SaveWorkspaceFileAs, args);
}

export function SearchWorkspaceFiles(...args: Parameters<typeof Wails.SearchWorkspaceFiles>): ReturnType<typeof Wails.SearchWorkspaceFiles> {
  return call("SearchWorkspaceFiles", Wails.SearchWorkspaceFiles, args);
}

export function SearchWorkspaceText(...args: Parameters<typeof Wails.SearchWorkspaceText>): ReturnType<typeof Wails.SearchWorkspaceText> {
  return call("SearchWorkspaceText", Wails.SearchWorkspaceText, args);
}

export function SendChatMessage(...args: Parameters<typeof Wails.SendChatMessage>): ReturnType<typeof Wails.SendChatMessage> {
  return call("SendChatMessage", Wails.SendChatMessage, args);
}

export function SendChatMessageWithAttachments(...args: Parameters<typeof Wails.SendChatMessageWithAttachments>): ReturnType<typeof Wails.SendChatMessageWithAttachments> {
  return call("SendChatMessageWithAttachments", Wails.SendChatMessageWithAttachments, args);
}

export function SendChatMessageWithPlanMode(...args: Parameters<typeof Wails.SendChatMessageWithPlanMode>): ReturnType<typeof Wails.SendChatMessageWithPlanMode> {
  return call("SendChatMessageWithPlanMode", Wails.SendChatMessageWithPlanMode, args);
}

export function SetActiveWorkspace(...args: Parameters<typeof Wails.SetActiveWorkspace>): ReturnType<typeof Wails.SetActiveWorkspace> {
  return call("SetActiveWorkspace", Wails.SetActiveWorkspace, args);
}

export function SetWorkspaceBuildCommand(...args: Parameters<typeof Wails.SetWorkspaceBuildCommand>): ReturnType<typeof Wails.SetWorkspaceBuildCommand> {
  return call("SetWorkspaceBuildCommand", Wails.SetWorkspaceBuildCommand, args);
}

export function SetWorkspaceDefaultPlanMode(...args: Parameters<typeof Wails.SetWorkspaceDefaultPlanMode>): ReturnType<typeof Wails.SetWorkspaceDefaultPlanMode> {
  return call("SetWorkspaceDefaultPlanMode", Wails.SetWorkspaceDefaultPlanMode, args);
}

export function SetWorkspaceFolderUseAgents(...args: Parameters<typeof Wails.SetWorkspaceFolderUseAgents>): ReturnType<typeof Wails.SetWorkspaceFolderUseAgents> {
  return call("SetWorkspaceFolderUseAgents", Wails.SetWorkspaceFolderUseAgents, args);
}

export function SetWorkspaceIconFromPath(...args: Parameters<typeof Wails.SetWorkspaceIconFromPath>): ReturnType<typeof Wails.SetWorkspaceIconFromPath> {
  return call("SetWorkspaceIconFromPath", Wails.SetWorkspaceIconFromPath, args);
}

export function SetWorkspaceIconFromUpload(...args: Parameters<typeof Wails.SetWorkspaceIconFromUpload>): ReturnType<typeof Wails.SetWorkspaceIconFromUpload> {
  return call("SetWorkspaceIconFromUpload", Wails.SetWorkspaceIconFromUpload, args);
}

export function SetWorkspaceLetter(...args: Parameters<typeof Wails.SetWorkspaceLetter>): ReturnType<typeof Wails.SetWorkspaceLetter> {
  return call("SetWorkspaceLetter", Wails.SetWorkspaceLetter, args);
}

export function SetWorkspaceSearchParentGitRepositories(...args: Parameters<typeof Wails.SetWorkspaceSearchParentGitRepositories>): ReturnType<typeof Wails.SetWorkspaceSearchParentGitRepositories> {
  return call("SetWorkspaceSearchParentGitRepositories", Wails.SetWorkspaceSearchParentGitRepositories, args);
}

export function GetLivenessConfig(...args: Parameters<typeof Wails.GetLivenessConfig>): ReturnType<typeof Wails.GetLivenessConfig> {
  return call("GetLivenessConfig", Wails.GetLivenessConfig, args);
}

export function SetLivenessConfig(...args: Parameters<typeof Wails.SetLivenessConfig>): ReturnType<typeof Wails.SetLivenessConfig> {
  return call("SetLivenessConfig", Wails.SetLivenessConfig, args);
}

export function GetTokenBudget(...args: Parameters<typeof Wails.GetTokenBudget>): ReturnType<typeof Wails.GetTokenBudget> {
  return call("GetTokenBudget", Wails.GetTokenBudget, args);
}

export function SetTokenBudget(...args: Parameters<typeof Wails.SetTokenBudget>): ReturnType<typeof Wails.SetTokenBudget> {
  return call("SetTokenBudget", Wails.SetTokenBudget, args);
}

export function ResetTokenBudget(...args: Parameters<typeof Wails.ResetTokenBudget>): ReturnType<typeof Wails.ResetTokenBudget> {
  return call("ResetTokenBudget", Wails.ResetTokenBudget, args);
}

export function SetWorkspaceTaskCompleted(...args: Parameters<typeof Wails.SetWorkspaceTaskCompleted>): ReturnType<typeof Wails.SetWorkspaceTaskCompleted> {
  return call("SetWorkspaceTaskCompleted", Wails.SetWorkspaceTaskCompleted, args);
}

export function StartKanbanExecution(...args: Parameters<typeof Wails.StartKanbanExecution>): ReturnType<typeof Wails.StartKanbanExecution> {
  return call("StartKanbanExecution", Wails.StartKanbanExecution, args);
}

export function StopChatStream(...args: Parameters<typeof Wails.StopChatStream>): ReturnType<typeof Wails.StopChatStream> {
  return call("StopChatStream", Wails.StopChatStream, args);
}

export function StopKanbanCard(...args: Parameters<typeof Wails.StopKanbanCard>): ReturnType<typeof Wails.StopKanbanCard> {
  return call("StopKanbanCard", Wails.StopKanbanCard, args);
}

export function StopKanbanExecution(...args: Parameters<typeof Wails.StopKanbanExecution>): ReturnType<typeof Wails.StopKanbanExecution> {
  return call("StopKanbanExecution", Wails.StopKanbanExecution, args);
}

export function SubmitInlineCodePrompt(...args: Parameters<typeof Wails.SubmitInlineCodePrompt>): ReturnType<typeof Wails.SubmitInlineCodePrompt> {
  return call("SubmitInlineCodePrompt", Wails.SubmitInlineCodePrompt, args);
}

export function UpdateKanbanCardDescription(...args: Parameters<typeof Wails.UpdateKanbanCardDescription>): ReturnType<typeof Wails.UpdateKanbanCardDescription> {
  return call("UpdateKanbanCardDescription", Wails.UpdateKanbanCardDescription, args);
}

export function UpdateKanbanCardDirection(...args: Parameters<typeof Wails.UpdateKanbanCardDirection>): ReturnType<typeof Wails.UpdateKanbanCardDirection> {
  return call("UpdateKanbanCardDirection", Wails.UpdateKanbanCardDirection, args);
}

export function ClearKanbanCardRecovery(workspaceID: string, cardID: string): Promise<any> {
  if (isWailsRuntime()) {
    return (window as any)["go"]["services"]["SystemService"]["ClearKanbanCardRecovery"](workspaceID, cardID);
  }
  return webRpc("ClearKanbanCardRecovery", [workspaceID, cardID]);
}

export function UpdateWorkspaceTask(...args: Parameters<typeof Wails.UpdateWorkspaceTask>): ReturnType<typeof Wails.UpdateWorkspaceTask> {
  return call("UpdateWorkspaceTask", Wails.UpdateWorkspaceTask, args);
}

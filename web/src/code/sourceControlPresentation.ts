import type {
  SourceControlActionRequest, SourceControlActionResult, SourceControlCapability,
  SourceControlChange, SourceControlChangeGroup, SourceControlRepository, SourceControlStatus,
} from "./sourceControlTypes";
import { shouldShowSyncAction } from "./gitPrimaryAction";
import { predictGitStatus } from "./gitState";

export type CommitPresentation = {
  action: string;
  label: string;
  title: string;
  placeholder: string;
  enabled: boolean;
};

export type ActionPathSource = "none" | "row" | "selection" | "group";
export type ChangeActionPresentation = {
  action: string;
  label: string;
  icon: string;
  pathSource: ActionPathSource;
  confirmation?: { title: string; message: string; confirmLabel: string };
};

export type RepositoryActionPresentation = {
  action: string;
  label: string;
  icon: string;
};

export interface SourceControlPresentationAdapter {
  readonly id: string;
  readonly usesStagingArea: boolean;
  readonly supportsOptimisticStatus: boolean;
  readonly supportsRemoteAdministration: boolean;
  readonly supportsTagAdministration: boolean;
  readonly promotesPendingSync: boolean;
  readonly workflow: string;
  showSync(status: SourceControlStatus | undefined): boolean;
  predictStatus?(current: SourceControlStatus, request: SourceControlActionRequest, result: SourceControlActionResult): SourceControlStatus;
  commit(repository: SourceControlRepository, status: SourceControlStatus, selected: SourceControlChange[]): CommitPresentation;
  groupAction(repository: SourceControlRepository, group: SourceControlChangeGroup): ChangeActionPresentation | null;
  changeAction(repository: SourceControlRepository, group: SourceControlChangeGroup, change: SourceControlChange): ChangeActionPresentation | null;
  repositoryActions?(repository: SourceControlRepository): RepositoryActionPresentation[];
}

export function supports(repository: SourceControlRepository, capability: SourceControlCapability): boolean {
  return repository.capabilities.includes(capability);
}

function hasConflicts(status: SourceControlStatus): boolean {
  return status.groups.some((group) => group.role === "conflicts" && group.changes.length > 0);
}

const gitPresentation: SourceControlPresentationAdapter = {
  id: "git",
  workflow: "git",
  usesStagingArea: true,
  supportsOptimisticStatus: true,
  supportsRemoteAdministration: true,
  supportsTagAdministration: true,
  promotesPendingSync: true,
  showSync(status) { return shouldShowSyncAction(status as never); },
  predictStatus(current, request, result) {
    return predictGitStatus(current as never, request as never, result as never) as SourceControlStatus;
  },
  commit(_repository, status) {
    return {
      action: "commit_staged",
      label: "Commit",
      title: "Commit staged changes",
      placeholder: "Message (Ctrl+Enter to commit staged changes)",
      enabled: status.staged.length > 0,
    };
  },
  groupAction(repository, group) {
    if (!supports(repository, "stage")) return null;
    if (group.role === "included" && !group.actions.includes("unstage")) return null;
    if (group.role !== "included" && !group.actions.includes("stage")) return null;
    return group.role === "included"
      ? { action: "unstage_all", label: "Unstage All Changes", icon: "remove", pathSource: "none" }
      : { action: "stage_all", label: "Stage All Changes", icon: "add", pathSource: "none" };
  },
  changeAction(repository, group) {
    if (!supports(repository, "stage")) return null;
    if (group.role === "included" && !group.actions.includes("unstage")) return null;
    if (group.role !== "included" && !group.actions.includes("stage")) return null;
    return group.role === "included"
      ? { action: "unstage", label: "Unstage Changes", icon: "remove", pathSource: "selection" }
      : { action: "stage", label: "Stage Changes", icon: "add", pathSource: "selection" };
  },
};

const fossilPresentation: SourceControlPresentationAdapter = {
  id: "fossil",
  workflow: "fossil",
  usesStagingArea: false,
  supportsOptimisticStatus: false,
  supportsRemoteAdministration: false,
  supportsTagAdministration: false,
  promotesPendingSync: false,
  showSync() { return false; },
  repositoryActions(repository) {
    return supports(repository, "webUI") ? [{ action: "open_ui", label: "Fossil UI", icon: "globe" }] : [];
  },
  commit(repository, status, selected) {
    const protectedGroup = status.groups.find((group) => group.role === "included" && group.actions.includes("unprotect"));
    if (protectedGroup) {
      return {
        action: "commit_protected",
        label: "Commit Protected",
        title: protectedGroup.diagnostic || "Commit the exact protected file versions",
        placeholder: "Message (Ctrl+Enter to commit Protected Changes)",
        enabled: !hasConflicts(status) && !protectedGroup.diagnostic
          && protectedGroup.actions.includes("commit_protected") && supports(repository, "protect"),
      };
    }
    const selectedTracked = selected.filter((change) => change.kind !== "untracked");
    const tracked = [...status.conflicts, ...status.unstaged].filter((change) => change.kind !== "untracked");
    const selectedMode = selected.length > 0;
    return {
      action: selectedMode ? "commit_selected" : "commit_all",
      label: selectedMode ? `Commit ${selected.length} Selected` : "Commit All",
      title: selectedMode ? `Commit ${selected.length} selected paths` : "Commit all visible changes",
      placeholder: "Message (Ctrl+Enter to commit changes)",
      enabled: !hasConflicts(status) && (selectedMode
        ? supports(repository, "commitSelected") && selectedTracked.length === selected.length
        : supports(repository, "commitAll") && tracked.length > 0 && !status.hiddenChangeCount),
    };
  },
  groupAction(repository, group) {
    if (!supports(repository, "protect")) return null;
    if (group.role === "included" && group.actions.includes("unprotect")) return {
      action: "unprotect_all", label: "Clear Protection", icon: "remove", pathSource: "none",
      confirmation: {
        title: "Clear Protected Changes?",
        message: "The working files will not be changed. Their frozen versions will no longer be kept for the next commit.",
        confirmLabel: "Clear Protection",
      },
    };
    if ((group.role === "working" || group.role === "untracked") && group.actions.includes("protect")) {
      return { action: "protect_all", label: "Protect All in Group", icon: "add", pathSource: "group" };
    }
    return null;
  },
  changeAction(repository, group) {
    if (!supports(repository, "protect")) return null;
    if (group.role === "included" && group.actions.includes("unprotect")) {
      return { action: "unprotect", label: "Remove Protection", icon: "remove", pathSource: "selection" };
    }
    if ((group.role === "working" || group.role === "untracked") && group.actions.includes("protect")) {
      return { action: "protect", label: "Protect This Version", icon: "add", pathSource: "selection" };
    }
    return null;
  },
};

const genericPresentation: SourceControlPresentationAdapter = {
  id: "generic",
  workflow: "generic",
  usesStagingArea: false,
  supportsOptimisticStatus: false,
  supportsRemoteAdministration: false,
  supportsTagAdministration: false,
  promotesPendingSync: false,
  showSync() { return false; },
  commit(repository, status, selected) {
    const selectedMode = selected.length > 0 && supports(repository, "commitSelected");
    return {
      action: selectedMode ? "commit_selected" : "commit_all",
      label: selectedMode ? `Commit ${selected.length} Selected` : "Commit All",
      title: selectedMode ? "Commit selected paths" : "Commit changes",
      placeholder: "Message (Ctrl+Enter to commit changes)",
      enabled: selectedMode ? selected.length > 0 : supports(repository, "commitAll") && status.totalChangeCount > 0 && !status.hiddenChangeCount,
    };
  },
  groupAction() { return null; },
  changeAction() { return null; },
};

const presentations = new Map<string, SourceControlPresentationAdapter>([
  [gitPresentation.id, gitPresentation],
  [fossilPresentation.id, fossilPresentation],
]);

/** Register a provider-specific presentation without changing the renderer. */
export function registerSourceControlPresentation(adapter: SourceControlPresentationAdapter): void {
  presentations.set(adapter.id, adapter);
}

export function presentationFor(repository: SourceControlRepository): SourceControlPresentationAdapter {
  return presentations.get(repository.providerId) || genericPresentation;
}

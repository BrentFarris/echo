import { SetWindowTitle } from "../backend/runtime";
import { isWorkspaceDebugActive } from "../codeView/debug";
import { activeWorkspace } from "./state";

let currentWindowTitle = "";

export function updateWindowTitle(): void {
  const workspace = activeWorkspace();
  const debugging = workspace ? isWorkspaceDebugActive(workspace.id) : false;
  const title = workspace
    ? `Echo - ${workspace.displayName}${debugging ? " (debugging)" : ""}`
    : "Echo";

  if (title === currentWindowTitle) {
    return;
  }
  currentWindowTitle = title;
  SetWindowTitle(title);
}

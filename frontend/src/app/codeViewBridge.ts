
import { ensureCodeViewRootLoaded } from "../codeView";
import { activeWorkspace, state } from "./state";

export async function loadActiveCodeViewIfNeeded() {
  if (state.appMode !== "code") {
    return;
  }
  const workspace = activeWorkspace();
  if (!workspace) {
    state.appMode = "chat";
    return;
  }
  await ensureCodeViewRootLoaded(workspace.id);
}

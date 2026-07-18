import { ListWorkspaceDirectory } from "../backend/services";
import { services } from "../../wailsjs/go/models";
import { patchCodeTree, scrollSelectedCodeTreeEntryIntoView } from "./dom";
import { directoryStateFor, ensureCodeState, setSingleCodeTreeSelection } from "./state";
import type { CodeViewCallbacks } from "./types";

export async function revealCodeFileInTree(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  if (!path) {
    return;
  }
  const state = ensureCodeState(workspaceID);
  setSingleCodeTreeSelection(state, path, "file");
  state.textSearchOpen = false;
  clearWorkspaceFileSearch(workspaceID);

  const parent = parentPath(path);
  for (const ancestor of directoryAncestors(parent)) {
    state.expandedPaths.add(ancestor);
    await loadRevealDirectory(workspaceID, ancestor);
  }

  patchCodeTree(workspaceID, callbacks);
  scrollSelectedCodeTreeEntryIntoView();
}

function clearWorkspaceFileSearch(workspaceID: string) {
  const state = ensureCodeState(workspaceID);
  if (state.searchTimerID !== null) {
    window.clearTimeout(state.searchTimerID);
    state.searchTimerID = null;
  }
  state.searchQuery = "";
  state.searchResults = [];
  state.searchLoading = false;
  state.searchTruncated = false;
  state.searchFocused = false;
  state.preservingSearchFocus = false;
}

async function loadRevealDirectory(workspaceID: string, path: string) {
  const directory = directoryStateFor(ensureCodeState(workspaceID), path);
  if (directory.loaded || directory.loading) {
    return;
  }
  directory.loading = true;
  directory.error = "";
  try {
    const loaded = await ListWorkspaceDirectory(workspaceID, path);
    const model = services.WorkspaceDirectory.createFrom(loaded);
    const target = directoryStateFor(ensureCodeState(workspaceID), model.path);
    target.entries = model.entries ?? [];
    target.loaded = true;
    target.error = "";
  } catch (error) {
    directory.error = error instanceof Error ? error.message : String(error);
  } finally {
    directory.loading = false;
  }
}

function parentPath(path: string) {
  const slash = path.lastIndexOf("/");
  if (slash <= 0) {
    return ".";
  }
  return path.slice(0, slash);
}

function directoryAncestors(path: string): string[] {
  const cleanPath = path.trim().replaceAll("\\", "/").replace(/^\/+|\/+$/g, "");
  if (!cleanPath || cleanPath === ".") {
    return ["."];
  }
  const ancestors = ["."];
  let current = "";
  cleanPath.split("/").forEach((part) => {
    current = current ? `${current}/${part}` : part;
    ancestors.push(current);
  });
  return ancestors;
}

// workspaces.js — workspace selector dropdown and "add workspace" modal.
//
// The "Select workspace" button in the left nav opens a dropdown listing the
// registered workspaces with a "+ Add a workspace" action at the bottom.
// Clicking that opens a modal where the user can name the workspace, add
// folder paths (the first is the main folder), and optionally upload an icon.
// Submitting POSTs to /api/workspaces, which validates and persists it.

import { icons } from "./icons.js";
import { del, get, post, put } from "./api.js";

// ---- State ----
let workspaces = [];
let activeId = "";

// ---- Escaping ----
function esc(value) {
  return String(value ?? "").replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[ch]));
}

// arrayBufferToBase64 converts an ArrayBuffer to a base64 string so image bytes
// can be sent to the Go backend, which decodes []byte from base64 JSON.
function arrayBufferToBase64(buffer) {
  const bytes = new Uint8Array(buffer);
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}

// ---- Loading ----
export async function loadWorkspaces() {
  try {
    const data = await get("/api/workspaces");
    workspaces = data.workspaces || [];
    activeId = data.activeId || "";
  } catch (err) {
    console.error("Failed to load workspaces:", err);
    workspaces = [];
    activeId = "";
  }
  return workspaces;
}

export function getWorkspaces() {
  return workspaces;
}

// getActive returns the currently active workspace object, or null.
export function getActive() {
  return workspaces.find((w) => w.id === activeId) || null;
}

// setActiveWorkspace persists the given workspace id as the active (last
// opened) workspace on the server and updates local state. Returns the active
// workspace object on success.
export async function setActiveWorkspace(id) {
  await put("/api/workspaces/active", { id });
  activeId = id;
  window.dispatchEvent(new CustomEvent("echo:workspace-changed", { detail: { workspaceId: id } }));
  return getActive();
}

// renderWorkspaceIcon returns the markup for a workspace's icon (image if it
// has one, otherwise a letter avatar).
export function renderWorkspaceIcon(w) {
  if (!w) {
    return esc("E");
  }
  if (w.iconExt) {
    return `<img src="/api/workspaces/${encodeURIComponent(w.id)}/icon" alt="" />`;
  }
  return esc((w.name || "?").charAt(0).toUpperCase());
}

// ---- Dropdown ----
export function renderWorkspaceDropdown(items = workspaces, selectedId = activeId) {
  return `
    <div class="workspace-dropdown" role="menu" aria-label="Workspaces">
      <div class="workspace-dropdown-header">Workspaces</div>
      <div class="workspace-dropdown-list">
        ${items.length ? items.map((w) => `
          <button type="button" role="menuitem" class="workspace-dropdown-item ${w.id === selectedId ? "is-active" : ""}" data-workspace-id="${esc(w.id)}"${w.id === selectedId ? ' aria-current="true"' : ""}>
            <span class="workspace-dropdown-icon">${renderWorkspaceIcon(w)}</span>
            <span class="workspace-dropdown-main">
              <strong>${esc(w.name)}</strong>
              <span>${esc(w.mainPath)}</span>
            </span>
          </button>
        `).join("") : `<p class="workspace-dropdown-empty">No workspaces yet.</p>`}
      </div>
      <button type="button" role="menuitem" class="workspace-dropdown-add" data-action="add-workspace">
        ${icons.plus}<span>Add a workspace</span>
      </button>
    </div>
  `;
}

// ---- Modal ----
export function renderAddWorkspaceModal() {
  return renderWorkspaceModal(null);
}

export function renderEditWorkspaceModal(workspace) {
  return renderWorkspaceModal(workspace);
}

function renderWorkspaceModal(workspace) {
  const editing = Boolean(workspace);
  const title = editing ? `Configure ${workspace.name}` : "Add a workspace";
  const icon = editing && workspace.iconExt
    ? `<img src="/api/workspaces/${encodeURIComponent(workspace.id)}/icon" alt="" />`
    : icons.image;
  return `
    <div class="modal-backdrop" data-workspace-modal-backdrop ${editing ? "data-edit-workspace-backdrop" : "data-add-workspace-backdrop"}>
      <div class="modal" role="dialog" aria-modal="true" aria-label="${esc(title)}">
        <header class="modal-header">
          <h2>${esc(title)}</h2>
          <button type="button" class="icon-button" data-action="close-workspace-modal" title="Close" aria-label="Close">${icons.x}</button>
        </header>

        <div class="modal-body">
          <label class="field">
            <span>Workspace name</span>
            <input type="text" data-field="name" value="${esc(workspace?.name || "")}" placeholder="My workspace" autocomplete="off" />
          </label>

          <div class="field">
            <span>Icon</span>
            <div class="icon-upload">
              <div class="icon-upload-preview" data-icon-preview>${icon}</div>
              <div class="icon-upload-controls">
                <div class="icon-upload-actions">
                  <button type="button" class="secondary-button compact-button" data-action="pick-icon">${icons.upload}<span>${editing && workspace.iconExt ? "Replace image" : "Choose image"}</span></button>
                  <button type="button" class="secondary-button compact-button danger-button" data-action="remove-icon" ${editing && workspace.iconExt ? "" : "hidden"}>Remove image</button>
                </div>
                <span class="icon-upload-name" data-icon-name>${editing && workspace.iconExt ? `Current .${esc(workspace.iconExt)} image` : "No image selected"}</span>
              </div>
              <input type="file" accept="image/png,image/gif,image/jpeg,image/webp,image/bmp,image/svg+xml,image/x-icon" data-icon-input hidden />
            </div>
          </div>

          <div class="field">
            <span>Folders</span>
            <div class="folder-list" data-folder-list></div>
            ${editing ? `<span class="field-help">The main folder owns <code>.echo</code> and cannot be changed here.</span>` : ""}
            <button type="button" class="secondary-button compact-button folder-add" data-action="add-folder">${icons.plus}<span>Add folder</span></button>
          </div>
        </div>

        <footer class="modal-footer">
          <span class="modal-error" data-modal-error></span>
          <button type="button" class="secondary-button" data-action="cancel-workspace-modal">Cancel</button>
          <button type="button" class="primary-button" data-action="save-workspace">${icons.check}<span>${editing ? "Save" : "Create"}</span></button>
        </footer>
      </div>
    </div>
  `;
}

function folderRow(index, isMain, path = "", mainReadOnly = false) {
  return `
    <div class="folder-row" data-folder-row="${index}">
      <input type="text" data-folder-path="${index}" value="${esc(path)}" placeholder="C:\\path\\to\\folder" autocomplete="off" ${isMain && mainReadOnly ? "readonly aria-readonly=\"true\"" : ""} />
      ${isMain ? `<span class="folder-main-tag" title="Primary folder that owns the .echo directory">main</span>` : ""}
      <button type="button" class="icon-button" data-action="remove-folder" data-index="${index}" title="Remove folder" aria-label="Remove folder" ${isMain ? "hidden" : ""}>${icons.x}</button>
    </div>
  `;
}

// ---- Wiring ----
// openDropdown positions a dropdown anchored to the given trigger button and
// returns a cleanup function that removes it. onSelect is called with the
// workspace id when a workspace row is clicked; onAdd is called when "+ Add a
// workspace" is clicked.
/**
 * @param {HTMLElement} trigger
 * @param {{
 *   onSelect?: (id: string) => void,
 *   onAdd?: () => void,
 *   onClose?: () => void,
 *   items?: Array<any>,
 *   selectedId?: string,
 * }} [options]
 */
export function openWorkspaceDropdown(trigger, options = {}) {
  const {
    onSelect,
    onAdd,
    onClose,
    items = workspaces,
    selectedId = activeId,
  } = options;
  const dropdown = document.createElement("div");
  dropdown.className = "workspace-dropdown-anchor";
  dropdown.innerHTML = renderWorkspaceDropdown(items, selectedId);
  document.body.appendChild(dropdown);
  let closed = false;

  const position = () => {
    const rect = trigger.getBoundingClientRect();
    const margin = 8;
    const gap = 6;
    const width = Math.min(dropdown.offsetWidth || 240, window.innerWidth - margin * 2);
    const height = Math.min(dropdown.offsetHeight || 320, window.innerHeight - margin * 2);
    const spaceBelow = window.innerHeight - rect.bottom - gap - margin;
    const spaceAbove = rect.top - gap - margin;
    const openAbove = spaceBelow < Math.min(height, 180) && spaceAbove > spaceBelow;
    const naturalTop = openAbove ? rect.top - height - gap : rect.bottom + gap;
    const top = Math.max(margin, Math.min(naturalTop, window.innerHeight - height - margin));
    const left = Math.max(margin, Math.min(rect.left, window.innerWidth - width - margin));
    dropdown.style.top = `${top}px`;
    dropdown.style.left = `${left}px`;
    dropdown.dataset.placement = openAbove ? "above" : "below";
  };
  position();
  trigger.setAttribute("aria-expanded", "true");

  const onDocClick = (e) => {
    if (!dropdown.contains(e.target) && e.target !== trigger) {
      close();
    }
  };
  const onResize = () => position();

  const menuItems = () => [...dropdown.querySelectorAll('button[role="menuitem"]')];
  const onKeydown = (e) => {
    if (e.key === "Escape") {
      e.preventDefault();
      close(true);
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)) return;
    const buttons = menuItems();
    if (!buttons.length) return;
    e.preventDefault();
    const current = buttons.indexOf(document.activeElement);
    const next = e.key === "Home"
      ? 0
      : e.key === "End"
        ? buttons.length - 1
        : (current + (e.key === "ArrowDown" ? 1 : -1) + buttons.length) % buttons.length;
    buttons[next].focus();
  };

  const onDropdownClick = (e) => {
    const item = e.target.closest("[data-workspace-id]");
    if (item) {
      close(false);
      onSelect?.(item.dataset.workspaceId);
      return;
    }
    if (e.target.closest('[data-action="add-workspace"]')) {
      close(false);
      onAdd?.();
    }
  };

  function close(restoreFocus = false) {
    if (closed) return;
    closed = true;
    document.removeEventListener("click", onDocClick);
    document.removeEventListener("keydown", onKeydown);
    window.removeEventListener("resize", onResize);
    dropdown.remove();
    trigger.setAttribute("aria-expanded", "false");
    onClose?.();
    if (restoreFocus && trigger.isConnected) trigger.focus();
  }

  document.addEventListener("click", onDocClick);
  document.addEventListener("keydown", onKeydown);
  window.addEventListener("resize", onResize);
  dropdown.addEventListener("click", onDropdownClick);
  requestAnimationFrame(() => {
    const first = dropdown.querySelector(".workspace-dropdown-item.is-active") || menuItems()[0];
    first?.focus();
  });
  return close;
}

// openAddWorkspaceModal renders and wires the add-workspace modal. Returns a
// cleanup function. onCreate is called with the created workspace after a
// successful POST.
export function openAddWorkspaceModal({ onCreate } = {}) {
  return openWorkspaceModal({ onCreate });
}

// openEditWorkspaceModal edits a workspace without allowing its main folder to
// move. onUpdate receives the resolved workspace returned by the server.
export function openEditWorkspaceModal(workspace, { onUpdate } = {}) {
  return openWorkspaceModal({ workspace, onUpdate });
}

export async function unregisterWorkspace(id) {
  const previousActiveId = activeId;
  const data = await del(`/api/workspaces/${encodeURIComponent(id)}`);
  workspaces = workspaces.filter((workspace) => workspace.id !== id);
  activeId = data.activeId || "";
  if (id === previousActiveId || activeId !== previousActiveId) {
    window.dispatchEvent(new CustomEvent("echo:workspace-changed", { detail: { workspaceId: activeId } }));
  }
  return data;
}

function openWorkspaceModal({ workspace = null, onCreate, onUpdate } = {}) {
  const editing = Boolean(workspace);
  const root = document.createElement("div");
  root.innerHTML = editing ? renderEditWorkspaceModal(workspace) : renderAddWorkspaceModal();
  const modal = root.firstElementChild;
  document.body.appendChild(modal);

  const additionalFolders = Array.isArray(workspace?.folders)
    ? workspace.folders.filter((path, index) => index > 0 && path !== workspace.mainPath)
    : [];
  const folders = [
    { path: workspace?.mainPath || "", main: true },
    ...additionalFolders.map((path) => ({ path, main: false })),
  ];
  const folderList = modal.querySelector("[data-folder-list]");
  const errorEl = modal.querySelector("[data-modal-error]");
  let iconData = null;
  let removeIcon = false;

  const renderFolders = () => {
    folderList.innerHTML = folders.map((folder, index) => folderRow(index, folder.main, folder.path, editing)).join("");
  };
  renderFolders();

  const setError = (msg) => {
    errorEl.textContent = msg || "";
    errorEl.hidden = !msg;
  };

  const close = () => {
    document.removeEventListener("keydown", onKeydown);
    if (iconPreview._url) URL.revokeObjectURL(iconPreview._url);
    modal.remove();
  };

  const onKeydown = (e) => {
    if (e.key === "Escape") close();
  };
  document.addEventListener("keydown", onKeydown);

  // Backdrop click closes.
  modal.addEventListener("click", (e) => {
    if (e.target === modal) close();
  });

  // Folder list: add / remove.
  modal.addEventListener("click", (e) => {
    if (e.target.closest('[data-action="add-folder"]')) {
      const index = folders.length;
      folders.push({ path: "", main: false });
      folderList.insertAdjacentHTML("beforeend", folderRow(index, false));
      const last = folderList.lastElementChild?.querySelector("input");
      last?.focus();
    } else if (e.target.closest('[data-action="remove-folder"]')) {
      const btn = e.target.closest('[data-action="remove-folder"]');
      const idx = Number(btn.dataset.index);
      if (idx > 0) {
        folders.splice(idx, 1);
        renderFolders();
      }
    }
  });

  // Keep folder path values in sync on input.
  folderList.addEventListener("input", (e) => {
    const input = e.target.closest("[data-folder-path]");
    if (!input) return;
    const idx = Number(input.dataset.folderPath);
    folders[idx].path = input.value;
  });

  // Icon picker.
  const iconInput = modal.querySelector("[data-icon-input]");
  const iconPreview = modal.querySelector("[data-icon-preview]");
  const iconNameEl = modal.querySelector("[data-icon-name]");
  const removeIconButton = modal.querySelector('[data-action="remove-icon"]');
  modal.querySelector('[data-action="pick-icon"]').addEventListener("click", () => iconInput.click());
  iconInput.addEventListener("change", () => {
    const file = iconInput.files?.[0];
    if (!file) return;
    const ext = (file.name.split(".").pop() || "").toLowerCase();
    const reader = new FileReader();
    reader.onload = () => {
      // Send the raw bytes as a base64 string, which Go's []byte decodes from.
      iconData = { data: arrayBufferToBase64(reader.result), ext };
      removeIcon = false;
      iconNameEl.textContent = file.name;
      if (iconPreview._url) URL.revokeObjectURL(iconPreview._url);
      const url = URL.createObjectURL(file);
      iconPreview.innerHTML = `<img src="${url}" alt="" />`;
      iconPreview._url = url;
      removeIconButton.hidden = false;
    };
    reader.readAsArrayBuffer(file);
  });
  removeIconButton.addEventListener("click", () => {
    if (iconPreview._url) {
      URL.revokeObjectURL(iconPreview._url);
      iconPreview._url = "";
    }
    iconData = null;
    removeIcon = true;
    iconInput.value = "";
    iconPreview.innerHTML = icons.image;
    iconNameEl.textContent = "Image will be removed";
    removeIconButton.hidden = true;
  });

  // Close / cancel.
  modal.querySelector('[data-action="close-workspace-modal"]').addEventListener("click", close);
  modal.querySelector('[data-action="cancel-workspace-modal"]').addEventListener("click", close);

  // Create or update.
  modal.querySelector('[data-action="save-workspace"]').addEventListener("click", async () => {
    const name = modal.querySelector('[data-field="name"]').value.trim();
    const mainPath = folders[0].path.trim();
    const rest = folders.slice(1).map((f) => f.path.trim()).filter(Boolean);
    if (!name) {
      setError("Workspace name is required.");
      return;
    }
    if (!mainPath) {
      setError("The main folder path is required.");
      return;
    }
    setError("");
    const saveButton = modal.querySelector('[data-action="save-workspace"]');
    saveButton.disabled = true;
    try {
      const payload = { name, folders: rest };
      if (iconData) payload.icon = iconData;
      if (removeIcon) payload.removeIcon = true;
      const data = editing
        ? await put(`/api/workspaces/${encodeURIComponent(workspace.id)}`, payload)
        : await post("/api/workspaces", { ...payload, mainPath });
      const ws = data.workspace;
      const existingIndex = workspaces.findIndex((candidate) => candidate.id === ws.id);
      if (existingIndex >= 0) workspaces[existingIndex] = ws;
      else workspaces.push(ws);
      close();
      if (editing) onUpdate?.(ws);
      else onCreate?.(ws);
    } catch (err) {
      setError(err.message || `Failed to ${editing ? "update" : "create"} workspace.`);
      saveButton.disabled = false;
    }
  });

  modal.querySelector('[data-field="name"]')?.focus();

  return close;
}

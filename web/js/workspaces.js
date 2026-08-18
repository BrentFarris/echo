// workspaces.js — workspace selector dropdown and "add workspace" modal.
//
// The "Select workspace" button in the left nav opens a dropdown listing the
// registered workspaces with a "+ Add a workspace" action at the bottom.
// Clicking that opens a modal where the user can name the workspace, add
// folder paths (the first is the main folder), and optionally upload an icon.
// Submitting POSTs to /api/workspaces, which validates and persists it.

import { icons } from "./icons.js";
import { get, post, put } from "./api.js";

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
export function renderWorkspaceDropdown() {
  return `
    <div class="workspace-dropdown" role="menu" aria-label="Workspaces">
      <div class="workspace-dropdown-header">Workspaces</div>
      <div class="workspace-dropdown-list">
        ${workspaces.length ? workspaces.map((w) => `
          <button type="button" role="menuitem" class="workspace-dropdown-item ${w.id === activeId ? "is-active" : ""}" data-workspace-id="${esc(w.id)}">
            <span class="workspace-dropdown-icon">${renderWorkspaceIcon(w)}</span>
            <span class="workspace-dropdown-main">
              <strong>${esc(w.name)}</strong>
              <span>${esc(w.mainPath)}</span>
            </span>
          </button>
        `).join("") : `<p class="workspace-dropdown-empty">No workspaces yet.</p>`}
      </div>
      <button type="button" class="workspace-dropdown-add" data-action="add-workspace">
        ${icons.plus}<span>Add a workspace</span>
      </button>
    </div>
  `;
}

// ---- Modal ----
export function renderAddWorkspaceModal() {
  return `
    <div class="modal-backdrop" data-add-workspace-backdrop>
      <div class="modal" role="dialog" aria-modal="true" aria-label="Add workspace">
        <header class="modal-header">
          <h2>Add a workspace</h2>
          <button type="button" class="icon-button" data-action="close-add-workspace" title="Close" aria-label="Close">${icons.x}</button>
        </header>

        <div class="modal-body">
          <label class="field">
            <span>Workspace name</span>
            <input type="text" data-field="name" placeholder="My workspace" autocomplete="off" />
          </label>

          <div class="field">
            <span>Icon</span>
            <div class="icon-upload">
              <div class="icon-upload-preview" data-icon-preview>${icons.image}</div>
              <div class="icon-upload-controls">
                <button type="button" class="secondary-button compact-button" data-action="pick-icon">${icons.upload}<span>Choose image</span></button>
                <span class="icon-upload-name" data-icon-name>No image selected</span>
              </div>
              <input type="file" accept="image/png,image/gif,image/jpeg,image/webp,image/bmp,image/svg+xml,image/x-icon" data-icon-input hidden />
            </div>
          </div>

          <div class="field">
            <span>Folders</span>
            <div class="folder-list" data-folder-list></div>
            <button type="button" class="secondary-button compact-button folder-add" data-action="add-folder">${icons.plus}<span>Add folder</span></button>
          </div>
        </div>

        <footer class="modal-footer">
          <span class="modal-error" data-modal-error></span>
          <button type="button" class="secondary-button" data-action="cancel-add-workspace">Cancel</button>
          <button type="button" class="primary-button" data-action="create-workspace">${icons.check}<span>Create</span></button>
        </footer>
      </div>
    </div>
  `;
}

function folderRow(index, isMain) {
  return `
    <div class="folder-row" data-folder-row="${index}">
      <input type="text" data-folder-path="${index}" placeholder="C:\\path\\to\\folder" autocomplete="off" />
      ${isMain ? `<span class="folder-main-tag" title="Primary folder that owns the .echo directory">main</span>` : ""}
      <button type="button" class="icon-button" data-action="remove-folder" data-index="${index}" title="Remove folder" aria-label="Remove folder">${icons.x}</button>
    </div>
  `;
}

// ---- Wiring ----
// openDropdown positions a dropdown anchored to the given trigger button and
// returns a cleanup function that removes it. onSelect is called with the
// workspace id when a workspace row is clicked; onAdd is called when "+ Add a
// workspace" is clicked.
export function openWorkspaceDropdown(trigger, { onSelect, onAdd }) {
  const dropdown = document.createElement("div");
  dropdown.className = "workspace-dropdown-anchor";
  dropdown.innerHTML = renderWorkspaceDropdown();
  document.body.appendChild(dropdown);

  const position = () => {
    const rect = trigger.getBoundingClientRect();
    dropdown.style.top = `${rect.bottom + 6}px`;
    dropdown.style.left = `${rect.left}px`;
    const width = dropdown.offsetWidth || 240;
    const overflow = rect.left + width - window.innerWidth + 8;
    if (overflow > 0) dropdown.style.left = `${Math.max(8, rect.left - overflow)}px`;
  };
  position();
  trigger.setAttribute("aria-expanded", "true");

  const onDocClick = (e) => {
    if (!dropdown.contains(e.target) && e.target !== trigger) {
      close();
    }
  };
  const onResize = () => position();

  const onDropdownClick = (e) => {
    const item = e.target.closest("[data-workspace-id]");
    if (item) {
      close();
      onSelect?.(item.dataset.workspaceId);
      return;
    }
    if (e.target.closest('[data-action="add-workspace"]')) {
      close();
      onAdd?.();
    }
  };

  function close() {
    document.removeEventListener("click", onDocClick);
    window.removeEventListener("resize", onResize);
    dropdown.remove();
    trigger.setAttribute("aria-expanded", "false");
  }

  document.addEventListener("click", onDocClick);
  window.addEventListener("resize", onResize);
  dropdown.addEventListener("click", onDropdownClick);
  return close;
}

// openAddWorkspaceModal renders and wires the add-workspace modal. Returns a
// cleanup function. onCreate is called with the created workspace after a
// successful POST.
export function openAddWorkspaceModal({ onCreate } = {}) {
  const root = document.createElement("div");
  root.innerHTML = renderAddWorkspaceModal();
  const modal = root.firstElementChild;
  document.body.appendChild(modal);

  const folders = [{ path: "", main: true }];
  const folderList = modal.querySelector("[data-folder-list]");
  const errorEl = modal.querySelector("[data-modal-error]");
  let iconData = null;

  const renderFolders = () => {
    folderList.innerHTML = folders.map((f, i) => folderRow(i, f.main)).join("");
    // Only allow removing non-main folders.
    folderList.querySelectorAll("[data-action='remove-folder']").forEach((btn) => {
      btn.hidden = btn.dataset.index === "0";
    });
  };
  renderFolders();

  const setError = (msg) => {
    errorEl.textContent = msg || "";
    errorEl.hidden = !msg;
  };

  const close = () => {
    document.removeEventListener("keydown", onKeydown);
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
      folders.push({ path: "", main: false });
      renderFolders();
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
  modal.querySelector('[data-action="pick-icon"]').addEventListener("click", () => iconInput.click());
  iconInput.addEventListener("change", () => {
    const file = iconInput.files?.[0];
    if (!file) return;
    const ext = (file.name.split(".").pop() || "").toLowerCase();
    const reader = new FileReader();
    reader.onload = () => {
      // Send the raw bytes as a base64 string, which Go's []byte decodes from.
      iconData = { data: arrayBufferToBase64(reader.result), ext };
      iconNameEl.textContent = file.name;
      const url = URL.createObjectURL(file);
      iconPreview.innerHTML = `<img src="${url}" alt="" />`;
      // Revoke the object URL on next change or close.
      iconPreview._url = url;
    };
    reader.readAsArrayBuffer(file);
  });

  // Close / cancel.
  modal.querySelector('[data-action="close-add-workspace"]').addEventListener("click", close);
  modal.querySelector('[data-action="cancel-add-workspace"]').addEventListener("click", close);

  // Create.
  modal.querySelector('[data-action="create-workspace"]').addEventListener("click", async () => {
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
    const createBtn = modal.querySelector('[data-action="create-workspace"]');
    createBtn.disabled = true;
    try {
      const data = await post("/api/workspaces", {
        name,
        mainPath,
        folders: rest,
        icon: iconData || undefined,
      });
      const ws = data.workspace;
      workspaces.push(ws);
      if (iconPreview._url) URL.revokeObjectURL(iconPreview._url);
      close();
      onCreate?.(ws);
    } catch (err) {
      setError(err.message || "Failed to create workspace.");
      createBtn.disabled = false;
    }
  });

  return close;
}

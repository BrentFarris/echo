// Package workspaces manages Echo workspaces. A workspace is a named set of
// folders (paths on the server machine) that Echo operates on. The first
// (main) folder owns a hidden .echo directory that stores the workspace
// settings (workspace.json) and an optional icon (icon.<ext>).
//
// The workspace list itself lives in the shared Echo app data file (echo.json)
// alongside the application settings; see internal/appdata.
package workspaces

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brent/echo/internal/appdata"
)

// EchoDirName is the name of the hidden directory Echo creates in a
// workspace's main folder.
const EchoDirName = ".echo"

// Workspace is the shape returned to the frontend. It mirrors appdata.Workspace.
type Workspace struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	MainPath string   `json:"mainPath"`
	IconExt  string   `json:"iconExt,omitempty"`
	Folders  []string `json:"folders,omitempty"`
}

// CreateRequest is the payload accepted by the create-workspace endpoint.
type CreateRequest struct {
	Name     string   `json:"name"`
	MainPath string   `json:"mainPath"`
	Folders  []string `json:"folders"`
	Icon     *Icon    `json:"icon,omitempty"`
}

// Icon carries an uploaded workspace icon image. Data is the raw file bytes and
// Ext is the detected extension (without a leading dot), e.g. "png".
type Icon struct {
	Data []byte `json:"data"`
	Ext  string `json:"ext"`
}

// workspaceFile is the on-disk shape of .echo/workspace.json.
type workspaceFile struct {
	Name     string   `json:"name"`
	MainPath string   `json:"mainPath"`
	Folders  []string `json:"folders"`
}

// Manager reads and writes the workspace list in the shared app data file.
type Manager struct {
	data *appdata.Store
}

// NewManager creates a Manager backed by the given app data store path.
func NewManager(path string) *Manager {
	return &Manager{data: appdata.NewStore(path)}
}

// List returns all registered workspaces.
func (m *Manager) List() ([]Workspace, error) {
	f, err := m.data.Load()
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(f.Workspaces))
	for _, w := range f.Workspaces {
		out = append(out, Workspace{
			ID:       w.ID,
			Name:     w.Name,
			MainPath: w.MainPath,
			IconExt:  w.IconExt,
			Folders:  append([]string(nil), w.Folders...),
		})
	}
	return out, nil
}

// Create validates and registers a new workspace. It verifies the name is
// unique, ensures every folder path exists on the server, creates the .echo
// directory in the main folder, writes .echo/workspace.json, copies any
// uploaded icon to .echo/icon.<ext>, and appends an entry to the shared app
// data file.
func (m *Manager) Create(req CreateRequest) (Workspace, error) {
	name := strings.TrimSpace(req.Name)
	mainPath := strings.TrimSpace(req.MainPath)
	if name == "" {
		return Workspace{}, fmt.Errorf("workspace name is required")
	}
	if mainPath == "" {
		return Workspace{}, fmt.Errorf("main folder path is required")
	}

	// Folders: the main folder is always first; additional folders follow.
	folders := append([]string{mainPath}, cleanFolders(req.Folders, mainPath)...)

	// 1. Verify the workspace name is unique.
	existing, err := m.List()
	if err != nil {
		return Workspace{}, err
	}
	for _, w := range existing {
		if strings.EqualFold(w.Name, name) {
			return Workspace{}, fmt.Errorf("a workspace named %q already exists", name)
		}
	}

	// 2. Ensure every path is a valid directory on the server.
	for _, folder := range folders {
		info, err := os.Stat(folder)
		if err != nil {
			return Workspace{}, fmt.Errorf("path %q is not accessible: %v", folder, err)
		}
		if !info.IsDir() {
			return Workspace{}, fmt.Errorf("path %q is not a folder", folder)
		}
	}

	// 3. Create the .echo folder in the main folder if it isn't already there.
	echoDir := filepath.Join(mainPath, EchoDirName)
	if err := os.MkdirAll(echoDir, 0o755); err != nil {
		return Workspace{}, fmt.Errorf("create .echo folder: %w", err)
	}

	// 5. Write .echo/workspace.json with the workspace settings (folders list).
	if err := writeWorkspaceFile(echoDir, workspaceFile{
		Name:     name,
		MainPath: mainPath,
		Folders:  folders,
	}); err != nil {
		return Workspace{}, err
	}

	// 4. Copy the uploaded image (if any) to .echo/icon.<ext>.
	iconExt := ""
	if req.Icon != nil && len(req.Icon.Data) > 0 {
		ext := sanitizeExt(req.Icon.Ext)
		if ext == "" {
			return Workspace{}, fmt.Errorf("icon has an unsupported file extension")
		}
		if err := os.WriteFile(filepath.Join(echoDir, "icon."+ext), req.Icon.Data, 0o644); err != nil {
			return Workspace{}, fmt.Errorf("write icon: %w", err)
		}
		iconExt = ext
	}

	// 6. Append an entry to the shared app data file.
	ws := Workspace{
		ID:       newID(),
		Name:     name,
		MainPath: mainPath,
		IconExt:  iconExt,
		Folders:  folders,
	}
	if err := m.append(ws); err != nil {
		return Workspace{}, err
	}
	return ws, nil
}

// append adds a workspace to the shared app data file, preserving settings.
func (m *Manager) append(ws Workspace) error {
	f, err := m.data.Load()
	if err != nil {
		return err
	}
	f.Workspaces = append(f.Workspaces, appdata.Workspace{
		ID:       ws.ID,
		Name:     ws.Name,
		MainPath: ws.MainPath,
		IconExt:  ws.IconExt,
		Folders:  append([]string(nil), ws.Folders...),
	})
	return m.data.Save(f)
}

// IconPath returns the path to a workspace's icon file, or "" when the
// workspace has no icon. The extension is auto-detected from the stored
// IconExt; if that is empty it scans the .echo directory for an icon.* file.
func (m *Manager) IconPath(id string) (string, error) {
	ws, ok, err := m.find(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("workspace %q not found", id)
	}
	if ws.IconExt != "" {
		return filepath.Join(ws.MainPath, EchoDirName, "icon."+ws.IconExt), nil
	}
	// Fall back to scanning for any icon.* file.
	matches, err := filepath.Glob(filepath.Join(ws.MainPath, EchoDirName, "icon.*"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", nil
	}
	return matches[0], nil
}

// Active returns the currently active workspace, or ok=false when none is set
// or the stored id no longer matches a registered workspace.
func (m *Manager) Active() (Workspace, bool, error) {
	f, err := m.data.Load()
	if err != nil {
		return Workspace{}, false, err
	}
	if f.ActiveWorkspaceID == "" {
		return Workspace{}, false, nil
	}
	for _, w := range f.Workspaces {
		if w.ID == f.ActiveWorkspaceID {
			return Workspace{
				ID:       w.ID,
				Name:     w.Name,
				MainPath: w.MainPath,
				IconExt:  w.IconExt,
				Folders:  append([]string(nil), w.Folders...),
			}, true, nil
		}
	}
	return Workspace{}, false, nil
}

// SetActive records the given workspace id as the active (last opened)
// workspace, preserving settings and the workspace list.
func (m *Manager) SetActive(id string) error {
	f, err := m.data.Load()
	if err != nil {
		return err
	}
	// Only allow setting an id that exists.
	found := false
	for _, w := range f.Workspaces {
		if w.ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("workspace %q not found", id)
	}
	f.ActiveWorkspaceID = id
	return m.data.Save(f)
}

func (m *Manager) find(id string) (appdata.Workspace, bool, error) {
	f, err := m.data.Load()
	if err != nil {
		return appdata.Workspace{}, false, err
	}
	for _, w := range f.Workspaces {
		if w.ID == id {
			return w, true, nil
		}
	}
	return appdata.Workspace{}, false, nil
}

// writeWorkspaceFile writes the workspace settings JSON atomically.
func writeWorkspaceFile(echoDir string, wf workspaceFile) error {
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspace file: %w", err)
	}
	path := filepath.Join(echoDir, "workspace.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write workspace file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename workspace file: %w", err)
	}
	return nil
}

// cleanFolders trims whitespace and drops empty/duplicate entries, excluding
// the main path.
func cleanFolders(folders []string, mainPath string) []string {
	seen := map[string]bool{mainPath: true}
	var out []string
	for _, f := range folders {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// sanitizeExt normalizes an icon extension to a safe lowercase value without a
// leading dot, or returns "" if it is unsupported.
func sanitizeExt(ext string) string {
	ext = strings.TrimSpace(ext)
	ext = strings.TrimPrefix(ext, ".")
	ext = strings.ToLower(ext)
	switch ext {
	case "png", "gif", "jpeg", "jpg", "webp", "bmp", "svg", "ico":
		return ext
	}
	return ""
}

// newID returns a short unique identifier for a workspace.
func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ws-%d", os.Getpid())
	}
	return "ws-" + hex.EncodeToString(b[:])
}

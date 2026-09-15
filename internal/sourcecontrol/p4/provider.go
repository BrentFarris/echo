package p4

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/workspacefs"
)

const ID = "p4"

var capabilities = []sourcecontrol.Capability{sourcecontrol.CapabilityStatus, sourcecontrol.CapabilityDiff, sourcecontrol.CapabilityChangelists, sourcecontrol.CapabilityOpenForEdit, sourcecontrol.CapabilityReconcile, sourcecontrol.CapabilityRevert}

type Settings struct {
	Roots        map[string]Override           `json:"roots"`
	Repositories map[string]RepositorySettings `json:"repositories"`
}
type Override struct {
	Server string `json:"server"`
	User   string `json:"user"`
	Client string `json:"client"`
}
type RepositorySettings struct {
	Connection *connection         `json:"connection,omitempty"`
	ClientRoot string              `json:"clientRoot,omitempty"`
	RootIDs    []string            `json:"rootIds,omitempty"`
	Tracking   bool                `json:"tracking"`
	Active     string              `json:"active"`
	Overrides  map[string]Override `json:"overrides,omitempty"`
}
type config struct {
	Workspaces map[string]Settings `json:"workspaces"`
}

type repository struct {
	ownedStamps         map[string]string
	detached            bool
	mu                  *sync.Mutex // shared by connection/client, including across Echo workspaces
	id, workspace, root string
	connection          connection
	connections         map[string]connection
	roots               []workspacefs.Root
	lineEnd             string
	revision            uint64
	last                sourcecontrol.StatusSnapshot
	candidates          map[string]string
	suppressed          map[string]time.Time
	incomplete          bool
	previews            map[string]preview
	baselines           map[string][]byte
}
type discovery struct {
	configuration string
	at            time.Time
	repos         []*repository
	diagnostic    string
}
type Provider struct {
	fs                 *workspacefs.Service
	sandbox            *sandbox.Manager
	binary, dataDir    string
	timeout            time.Duration
	env                []string             // isolated integration fixtures only; production inherits P4 configuration
	commandHook        func([]string) error // isolated native-command fault injection
	mu                 sync.Mutex
	discoveryMu        sync.Mutex
	config             config
	discovery          map[string]discovery
	journalDiagnostics map[string]string
	clients            map[string]*sync.Mutex
	subscriptions      map[string]context.CancelFunc
	notify             func(sourcecontrol.Event)
	watch              func(string, bool) error
	watching           map[string]bool
}

func New(fs *workspacefs.Service, sandbox *sandbox.Manager, dataDir string) *Provider {
	p := &Provider{fs: fs, sandbox: sandbox, dataDir: dataDir, binary: "p4", timeout: 15 * time.Second, discovery: map[string]discovery{}, clients: map[string]*sync.Mutex{}, subscriptions: map[string]context.CancelFunc{}, watching: map[string]bool{}}
	p.config.Workspaces = map[string]Settings{}
	if data, err := os.ReadFile(filepath.Join(dataDir, "settings.json")); err == nil {
		_ = json.Unmarshal(data, &p.config)
	}
	if p.config.Workspaces == nil {
		p.config.Workspaces = map[string]Settings{}
	}
	return p
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}
func (p *Provider) disabled(workspace string) bool {
	return p.sandbox != nil && p.sandbox.IsEnabled(workspace)
}
func (p *Provider) Descriptor(ctx context.Context, workspace string) sourcecontrol.ProviderDescriptor {
	d := sourcecontrol.ProviderDescriptor{ID: ID, Label: "Perforce", Capabilities: capabilities}
	if p.disabled(workspace) {
		d.Diagnostic = "P4 requires host execution; disable the workspace sandbox to use it"
		return d
	}
	if _, err := exec.LookPath(p.binary); err != nil {
		d.Diagnostic = "Install the P4 2024.1 command-line client and add it to PATH"
		return d
	}
	d.Available = true
	p.mu.Lock()
	d.Diagnostic = p.discovery[workspace].diagnostic
	p.mu.Unlock()
	return d
}

func (p *Provider) Settings(workspace string) Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Return a copy so request handlers cannot race configuration readers.
	data, _ := json.Marshal(p.config.Workspaces[workspace])
	var settings Settings
	_ = json.Unmarshal(data, &settings)
	if settings.Roots == nil {
		settings.Roots = map[string]Override{}
	}
	if settings.Repositories == nil {
		settings.Repositories = map[string]RepositorySettings{}
	}
	return settings
}
func (p *Provider) Configure(workspace string, settings Settings) error {
	for _, override := range settings.Roots {
		if strings.ContainsAny(override.Server+override.Client+override.User, "\r\n\x00") {
			return errors.New("invalid P4 connection settings")
		}
	}
	p.mu.Lock()
	old := p.config.Workspaces[workspace]
	p.config.Workspaces[workspace] = settings
	err := writeJSON(filepath.Join(p.dataDir, "settings.json"), p.config)
	if err != nil {
		p.config.Workspaces[workspace] = old
	} else {
		cached := p.discovery[workspace]
		cached.at = time.Time{}
		p.discovery[workspace] = cached
	}
	p.mu.Unlock()
	if err == nil {
		p.updateWatch(workspace)
	}
	return err
}

func (p *Provider) Repositories(ctx context.Context, workspace string) ([]sourcecontrol.Repository, error) {
	states, err := p.discover(ctx, workspace)
	if err != nil {
		return nil, err
	}
	out := make([]sourcecontrol.Repository, 0, len(states))
	for _, r := range states {
		r.mu.Lock()
		item := sourcecontrol.Repository{ID: r.id, ProviderID: ID, ProviderLabel: "Perforce", Label: r.connection.Client + " · " + r.connection.Server, Available: !p.disabled(workspace), Capabilities: capabilities, Revision: r.revision}
		if r.detached {
			item.Label += " · recovery"
			item.Diagnostic = "Pending operations from a previous P4 connection; automatic tracking is disabled for this identity"
		}
		for _, root := range r.roots {
			rel := r.relative(root.HostPath)
			if rel == "." {
				rel = ""
			}
			item.Scopes = append(item.Scopes, sourcecontrol.Scope{RootID: root.ID, RootLabel: root.Label, RepoPrefix: filepath.ToSlash(rel)})
		}
		if len(r.roots) > 0 {
			item.RootRef = &workspacefs.FileRef{RootID: r.roots[0].ID, Path: ""}
		}
		if !item.Available {
			item.Diagnostic = "P4 is unavailable in sandboxed workspaces"
		}
		r.mu.Unlock()
		out = append(out, item)
	}
	return out, nil
}

func (p *Provider) discover(ctx context.Context, workspace string) ([]*repository, error) {
	p.discoveryMu.Lock()
	defer p.discoveryMu.Unlock()
	p.mu.Lock()
	cached, ok := p.discovery[workspace]
	p.mu.Unlock()
	if p.disabled(workspace) {
		return cached.repos, nil
	}
	if ok && time.Since(cached.at) < time.Minute {
		return cached.repos, nil
	}
	roots, err := p.fs.Roots(workspace)
	if err != nil {
		return nil, err
	}
	settings := p.Settings(workspace)
	found := map[string]*repository{}
	configuration, _ := json.Marshal(settings.Roots)
	d := discovery{at: time.Now(), configuration: digest(string(configuration))}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	for index, probed := range p.probeRoots(ctx, roots, settings) {
		if probed.err != nil {
			d.diagnostic = probed.err.Error()
		}
		if !probed.valid {
			continue
		}
		root, c, clientRoot := roots[index], probed.connection, probed.clientRoot
		identity := c.Server + "\x00" + c.User + "\x00" + c.Client
		id := "p4-" + digest(workspace+"\x00"+identity)
		if r := found[id]; r != nil {
			r.mu.Lock()
			r.roots = append(r.roots, root)
			r.connections[root.ID] = c
			r.mu.Unlock()
			continue
		}
		p.mu.Lock()
		clientKey := c.Server + "\x00" + c.Client
		lock := p.clients[clientKey]
		if lock == nil {
			lock = &sync.Mutex{}
			p.clients[clientKey] = lock
		}
		p.mu.Unlock()
		var r *repository
		for _, previous := range cached.repos {
			if previous.id == id {
				r = previous
				break
			}
		}
		if r == nil {
			r = &repository{mu: lock, id: id, workspace: workspace, candidates: map[string]string{}, suppressed: map[string]time.Time{}, incomplete: true, previews: map[string]preview{}, baselines: map[string][]byte{}}
		}
		r.mu.Lock()
		r.detached = false
		if clientRoot != "" {
			clientRoot = filepath.Clean(clientRoot)
		}
		r.connection, r.root, r.roots, r.lineEnd = c, clientRoot, []workspacefs.Root{root}, probed.lineEnd
		r.connections = map[string]connection{root.ID: c}
		clear(r.baselines) // native encoding/client options may have changed
		r.mu.Unlock()
		found[id] = r
	}
	for _, r := range found {
		d.repos = append(d.repos, r)
	}
	// Preserve enabled identities independently: one disconnected client must not
	// be forgotten merely because another root's connection succeeded.
	for _, previous := range cached.repos {
		if found[previous.id] == nil && (settings.Repositories[previous.id].Tracking || len(p.pending(previous)) > 0) {
			previous.mu.Lock()
			previous.detached = identityDetached(previous.id, previous.roots, settings, found, d.diagnostic != "")
			previous.mu.Unlock()
			d.repos = append(d.repos, previous)
		}
	}
	p.recoverIdentities(workspace, roots, &d)
	sort.Slice(d.repos, func(i, j int) bool { return d.repos[i].id < d.repos[j].id })
	p.mu.Lock()
	p.discovery[workspace] = d
	p.mu.Unlock()
	p.updateWatch(workspace)
	return d.repos, nil
}

type rootProbe struct {
	connection          connection
	clientRoot, lineEnd string
	valid               bool
	err                 error
}

// Probe independent root configurations together so an unavailable server in
// one folder cannot consume the entire discovery deadline for a healthy one.
func (p *Provider) probeRoots(ctx context.Context, roots []workspacefs.Root, settings Settings) []rootProbe {
	results := make([]rootProbe, len(roots))
	limit := make(chan struct{}, 8)
	var pending sync.WaitGroup
	for index, root := range roots {
		pending.Add(1)
		go func() {
			defer pending.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				results[index].err = ctx.Err()
				return
			}
			results[index] = p.probeRoot(ctx, root, settings.Roots[root.ID])
		}()
	}
	pending.Wait()
	return results
}

func (p *Provider) probeRoot(ctx context.Context, root workspacefs.Root, override Override) (result rootProbe) {
	if strings.Contains(root.HostPath, "...") {
		result.err = errors.New("P4 cannot safely address a workspace path containing an ellipsis wildcard")
		return result
	}
	c := connection{Directory: root.HostPath, Server: override.Server, User: override.User, Client: override.Client}
	if c.Server == "" {
		configured, err := p.command(ctx, c, nil, false, "set", "-q", "P4PORT")
		if err != nil {
			result.err = err
			return result
		}
		c.Server = settingValue(configured, "P4PORT")
		if c.Server == "" {
			return result
		}
	}
	if charset, charsetErr := p.command(ctx, c, nil, false, "set", "-q", "P4CHARSET"); charsetErr == nil {
		c.Charset = settingValue(charset, "P4CHARSET")
	}
	if c.Charset != "" && c.Charset != "none" && !strings.HasPrefix(c.Charset, "auto") {
		c.CommandCharset = "utf8"
	}
	info, err := p.run(ctx, c, "info")
	if err != nil || len(info) == 0 {
		result.err = err
		return result
	}
	c.User, c.Client = info[0]["userName"], info[0]["clientName"]
	c.Unicode = info[0]["unicode"] == "enabled" || info[0]["unicode"] == "1"
	if c.Unicode {
		// Metadata, forms, and -x filenames use UTF-8 independently of the
		// workspace's file-content charset. -Q is invalid on non-Unicode servers.
		c.CommandCharset = "utf8"
	}
	if c.Client == "" || c.Client == "*unknown*" {
		return result
	}
	client, err := p.run(ctx, c, "client", "-o", c.Client)
	if err != nil || len(client) == 0 || client[0]["Update"] == "" {
		result.err = err
		return result
	}
	clientRoot := client[0]["Root"]
	if info[0]["clientRoot"] != "" {
		clientRoot = info[0]["clientRoot"]
	}
	if clientRoot == "" {
		clientRoot = root.HostPath
	}
	if clientRoot == "null" {
		clientRoot = ""
	}
	if clientRoot != "" && !inside(clientRoot, root.HostPath) {
		return result
	}
	// View mappings, including exclusions, do not enumerate depot contents.
	mapped, err := p.runInput(ctx, c, []string{"where"}, []string{fileArg(root.HostPath) + string(filepath.Separator) + "..."})
	if err != nil {
		result.err = err
		return result
	}
	for _, mapping := range mapped {
		_, excluded := mapping["unmap"]
		result.valid = result.valid || (!excluded && mapping["depotFile"] != "")
	}
	result.connection, result.clientRoot, result.lineEnd = c, clientRoot, client[0]["LineEnd"]
	return result
}

func settingValue(data []byte, key string) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+"=") {
			value := strings.TrimPrefix(line, key+"=")
			if i := strings.Index(value, " ("); i >= 0 {
				value = value[:i]
			}
			return value
		}
	}
	return ""
}

func (p *Provider) recoverIdentities(workspace string, roots []workspacefs.Root, d *discovery) {
	directories, _ := os.ReadDir(filepath.Join(p.dataDir, "journal"))
	known := map[string]bool{}
	configured := map[string]*repository{}
	for _, r := range d.repos {
		known[r.id] = true
		if !r.detached {
			configured[r.id] = r
		}
	}
	settings := p.Settings(workspace)
	for id, saved := range settings.Repositories {
		if known[id] || !saved.Tracking || saved.Connection == nil {
			continue
		}
		c := *saved.Connection
		identity := c.Server + "\x00" + c.User + "\x00" + c.Client
		if id != "p4-"+digest(workspace+"\x00"+identity) {
			continue
		}
		p.mu.Lock()
		clientKey := c.Server + "\x00" + c.Client
		lock := p.clients[clientKey]
		if lock == nil {
			lock = &sync.Mutex{}
			p.clients[clientKey] = lock
		}
		p.mu.Unlock()
		scopes := []workspacefs.Root{}
		for _, root := range roots {
			for _, rootID := range saved.RootIDs {
				if root.ID == rootID {
					scopes = append(scopes, root)
				}
			}
		}
		if len(scopes) == 0 {
			continue
		}
		r := &repository{mu: lock, id: id, workspace: workspace, root: saved.ClientRoot, connection: c, roots: scopes, candidates: map[string]string{}, suppressed: map[string]time.Time{}, previews: map[string]preview{}, baselines: map[string][]byte{}, incomplete: true, detached: identityDetached(id, scopes, settings, configured, d.diagnostic != "")}
		d.repos = append(d.repos, r)
		known[id] = true
	}
	for _, directory := range directories {
		if !directory.IsDir() || known[directory.Name()] {
			continue
		}
		probe := &repository{id: directory.Name(), workspace: workspace}
		records := p.pending(probe)
		if len(records) == 0 {
			continue
		}
		record := records[0]
		if r := p.repositoryFromRecord(record, roots); r != nil {
			d.repos = append(d.repos, r)
		}
	}
}

func identityDetached(id string, scopes []workspacefs.Root, settings Settings, found map[string]*repository, failed bool) bool {
	saved := settings.Repositories[id]
	if !failed {
		return true
	}
	for _, root := range scopes {
		if saved.Overrides != nil && saved.Overrides[root.ID] != settings.Roots[root.ID] {
			return true
		}
		for otherID, other := range found {
			if otherID == id {
				continue
			}
			for _, otherRoot := range other.roots {
				if root.ID == otherRoot.ID {
					return true
				}
			}
		}
	}
	return false
}

func (p *Provider) repositoryFromRecord(record operationRecord, roots []workspacefs.Root) *repository {
	identity := record.Connection.Server + "\x00" + record.Connection.User + "\x00" + record.Connection.Client
	if record.Repository != "p4-"+digest(record.Workspace+"\x00"+identity) {
		return nil
	}
	scopes := []workspacefs.Root{}
	for _, root := range roots {
		for _, saved := range record.Scopes {
			if root.ID == saved.ID && filepath.Clean(root.HostPath) == filepath.Clean(saved.HostPath) {
				scopes = append(scopes, root)
			}
		}
	}
	if len(scopes) == 0 {
		return nil
	}
	p.mu.Lock()
	clientKey := record.Connection.Server + "\x00" + record.Connection.Client
	lock := p.clients[clientKey]
	if lock == nil {
		lock = &sync.Mutex{}
		p.clients[clientKey] = lock
	}
	p.mu.Unlock()
	return &repository{mu: lock, id: record.Repository, workspace: record.Workspace, root: record.ClientRoot, connection: record.Connection, roots: scopes, candidates: map[string]string{}, suppressed: map[string]time.Time{}, previews: map[string]preview{}, baselines: map[string][]byte{}, incomplete: true, detached: true}
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (p *Provider) repo(ctx context.Context, workspace, id string) (*repository, error) {
	if p.disabled(workspace) {
		return nil, errors.New("P4 is unavailable in sandboxed workspaces")
	}
	states, err := p.discover(ctx, workspace)
	if err != nil {
		return nil, err
	}
	for _, r := range states {
		if r.id == id {
			return r, nil
		}
	}
	return nil, sourcecontrol.ErrNotFound
}
func (r *repository) ref(path string) *workspacefs.FileRef {
	for _, root := range r.roots {
		if inside(root.HostPath, path) {
			rel, _ := filepath.Rel(root.HostPath, path)
			return &workspacefs.FileRef{RootID: root.ID, Path: filepath.ToSlash(rel)}
		}
	}
	return nil
}

// P4CONFIG/P4IGNORE and tickets are resolved from the registered folder used by
// the operation, even when several folders share one native client identity.
func (r *repository) fileConnection(path string) connection {
	c := r.connection
	if ref := r.ref(path); ref != nil {
		if configured, ok := r.connections[ref.RootID]; ok {
			return configured
		}
		for _, root := range r.roots {
			if root.ID == ref.RootID {
				c.Directory = root.HostPath
				break
			}
		}
	}
	return c
}

func (p *Provider) filesInScopes(ctx context.Context, r *repository, args, paths []string) ([]record, error) {
	groups := map[string][]string{}
	connections := map[string]connection{}
	for _, path := range paths {
		c := r.fileConnection(path)
		key := c.Directory + "\x00" + c.Charset
		groups[key] = append(groups[key], path)
		connections[key] = c
	}
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var rows []record
	for _, key := range keys {
		batch, err := p.files(ctx, connections[key], args, groups[key])
		rows = append(rows, batch...)
		if err != nil {
			return rows, err
		}
	}
	return rows, nil
}
func (r *repository) relative(path string) string {
	// Root=null clients can span drives. Give each registered folder a stable
	// logical prefix instead of inventing an invalid filesystem-relative path.
	if r.root == "" {
		if ref := r.ref(path); ref != nil {
			prefix := "roots/" + ref.RootID
			if ref.Path != "." && ref.Path != "" {
				prefix += "/" + ref.Path
			}
			return prefix
		}
		return ""
	}
	rel, _ := filepath.Rel(r.root, path)
	return filepath.ToSlash(rel)
}

func (r *repository) absolute(path string) string {
	if r.root != "" {
		return filepath.Join(r.root, filepath.FromSlash(path))
	}
	for _, root := range r.roots {
		prefix := "roots/" + root.ID
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			abs := filepath.Join(root.HostPath, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(path, prefix), "/")))
			if inside(root.HostPath, abs) {
				return abs
			}
		}
	}
	return ""
}
func (p *Provider) resolve(r *repository, path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") || strings.Contains(path, "...") {
		return "", sourcecontrol.ErrInvalidPath
	}
	abs := r.absolute(path)
	if abs == "" {
		return "", sourcecontrol.ErrInvalidPath
	}
	ref := r.ref(abs)
	if ref == nil || workspacefs.IsProtectedWorkspaceMetadataPath(ref.Path) {
		return "", sourcecontrol.ErrInvalidPath
	}
	resolved, err := p.fs.ResolveProspectiveEntryHostPath(r.workspace, *ref)
	if ref.Path == "." || ref.Path == "" {
		ref.Path = ""
		return p.fs.ResolveExistingHostPath(r.workspace, *ref, true)
	}
	if err != nil {
		return "", err
	}
	return resolved.HostPath, nil
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".p4-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("persist P4 state: %w", err)
	}
	return nil
}

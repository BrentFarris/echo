// Package plugins implements Echo's optional extension system. Core Echo
// features remain statically owned by the application; this package only owns
// explicitly installed plugin contributions.
package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const (
	ManifestFileName = "echo-plugin.json"
	ManifestVersion  = 1
	HostAPIMajor     = 1
	RPCProtocol      = "echo-jsonrpc-1"
	MaxManifestBytes = 256 << 10
)

var (
	pluginIDPattern  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	viewIDPattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	toolNamePattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	rpcMethodPattern = regexp.MustCompile(
		`^[A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z][A-Za-z0-9_-]*)*$`,
	)
	semverPattern   = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	apiRangePattern = regexp.MustCompile(`^(?:1|1\.x|\^1(?:\.(?:0|[1-9][0-9]*)){0,2})$`)
)

var allowedPermissionNames = map[string]bool{
	"network": true, "filesystem": true, "process": true, "secrets": true,
	"ui.notifications": true, "ui.clipboard-write": true, "ui.external-links": true,
}

// Manifest is the reviewable, static contract at the root of every package.
// Contributions are intentionally declarative so Echo can validate and show
// them to the owner before any plugin process starts.
type Manifest struct {
	ManifestVersion int           `json:"manifestVersion"`
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Version         string        `json:"version"`
	Description     string        `json:"description,omitempty"`
	Author          string        `json:"author,omitempty"`
	License         string        `json:"license,omitempty"`
	Homepage        string        `json:"homepage,omitempty"`
	Echo            Compatibility `json:"echo"`
	Permissions     []Permission  `json:"permissions,omitempty"`
	Runtime         *Runtime      `json:"runtime,omitempty"`
	Contributes     Contributions `json:"contributes,omitempty"`
}

type Compatibility struct {
	API string `json:"api"`
}

// Permission records an owner-facing disclosure. UI bridge permissions are
// enforced by the host. Permissions claimed by native processes are disclosed
// for review but cannot form an OS sandbox.
type Permission struct {
	Name   string   `json:"name"`
	Reason string   `json:"reason"`
	Hosts  []string `json:"hosts,omitempty"`
}

type Runtime struct {
	Protocol string                   `json:"protocol"`
	Targets  map[string]RuntimeTarget `json:"targets"`
}

type RuntimeTarget struct {
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
}

type Contributions struct {
	Views    []ViewContribution    `json:"views,omitempty"`
	Tools    []ToolContribution    `json:"tools,omitempty"`
	Settings []SettingContribution `json:"settings,omitempty"`
	RPC      []RPCContribution     `json:"rpc,omitempty"`
}

type ViewContribution struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	Icon        string     `json:"icon,omitempty"`
	Entry       string     `json:"entry"`
	Singleton   *bool      `json:"singleton,omitempty"`
	DefaultSize Dimensions `json:"defaultSize,omitempty"`
	MinimumSize Dimensions `json:"minimumSize,omitempty"`
}

func (v ViewContribution) IsSingleton() bool {
	return v.Singleton == nil || *v.Singleton
}

type Dimensions struct {
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

type ToolContribution struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	InputSchema    map[string]any `json:"inputSchema"`
	OutputSchema   map[string]any `json:"outputSchema,omitempty"`
	Method         string         `json:"method"`
	TimeoutSeconds int            `json:"timeoutSeconds,omitempty"`
	ReadOnly       bool           `json:"readOnly,omitempty"`
	Mutating       bool           `json:"mutating,omitempty"`
}

type SettingContribution struct {
	Key         string          `json:"key"`
	Type        string          `json:"type"`
	Scope       string          `json:"scope"`
	Label       string          `json:"label"`
	Help        string          `json:"help,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
	Environment string          `json:"environment,omitempty"`
	Minimum     *float64        `json:"minimum,omitempty"`
	Maximum     *float64        `json:"maximum,omitempty"`
	Pattern     string          `json:"pattern,omitempty"`
	Options     []SettingOption `json:"options,omitempty"`
}

func (s SettingContribution) Secret() bool { return s.Type == "secret" }

type SettingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type RPCContribution struct {
	Method         string `json:"method"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

// Validation describes a package after static inspection.
type Validation struct {
	Manifest   Manifest `json:"manifest"`
	Compatible bool     `json:"compatible"`
	Target     string   `json:"target"`
	Digest     string   `json:"digest,omitempty"`
}

func ReadManifest(root string) (Manifest, error) {
	path, err := packagePath(root, ManifestFileName)
	if err != nil {
		return Manifest{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open plugin manifest: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Manifest{}, fmt.Errorf("stat plugin manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("plugin manifest must be a regular file no larger than %d bytes", MaxManifestBytes)
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse plugin manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("plugin manifest contains trailing JSON values")
		}
		return Manifest{}, fmt.Errorf("parse trailing plugin manifest data: %w", err)
	}
	return manifest, nil
}

// ValidatePackage rejects unsafe package layouts and validates every declared
// contribution without executing package code. coreToolNames is the immutable
// set that a plugin may not shadow.
func ValidatePackage(root string, coreToolNames map[string]bool) (Validation, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Validation{}, fmt.Errorf("resolve package root: %w", err)
	}
	if err := rejectUnsafeTree(root); err != nil {
		return Validation{}, err
	}
	manifest, err := ReadManifest(root)
	if err != nil {
		return Validation{}, err
	}
	if err := validateManifest(root, manifest, coreToolNames); err != nil {
		return Validation{}, err
	}
	target := runtime.GOOS + "-" + runtime.GOARCH
	compatible := manifest.Runtime == nil
	if manifest.Runtime != nil {
		_, compatible = manifest.Runtime.Targets[target]
	}
	digest, err := HashPackage(root)
	if err != nil {
		return Validation{}, err
	}
	return Validation{Manifest: manifest, Compatible: compatible, Target: target, Digest: digest}, nil
}

func validateManifest(root string, manifest Manifest, coreToolNames map[string]bool) error {
	if manifest.ManifestVersion != ManifestVersion {
		return fmt.Errorf("unsupported manifestVersion %d; expected %d", manifest.ManifestVersion, ManifestVersion)
	}
	if !pluginIDPattern.MatchString(manifest.ID) || len(manifest.ID) > 64 {
		return fmt.Errorf("plugin id must be a lowercase kebab-case slug no longer than 64 characters")
	}
	manifest.Name = strings.TrimSpace(manifest.Name)
	if manifest.Name == "" || len(manifest.Name) > 100 {
		return fmt.Errorf("plugin name is required and must be 100 characters or fewer")
	}
	if !semverPattern.MatchString(strings.TrimSpace(manifest.Version)) {
		return fmt.Errorf("plugin version must be semantic version x.y.z")
	}
	if !compatibleAPI(manifest.Echo.API) {
		return fmt.Errorf("plugin requires unsupported Echo API %q", manifest.Echo.API)
	}
	permissionNames := map[string]bool{}
	for _, permission := range manifest.Permissions {
		name := strings.TrimSpace(permission.Name)
		if !allowedPermissionNames[name] || permissionNames[name] {
			return fmt.Errorf("plugin permissions must have unique non-empty names")
		}
		if strings.TrimSpace(permission.Reason) == "" {
			return fmt.Errorf("permission %q must explain why it is needed", name)
		}
		permissionNames[name] = true
		if len(permission.Hosts) > 64 {
			return fmt.Errorf("permission %q declares too many hosts", name)
		}
		for _, host := range permission.Hosts {
			if strings.TrimSpace(host) == "" || len(host) > 512 || strings.ContainsAny(host, "\r\n\x00") {
				return fmt.Errorf("permission %q contains an invalid host", name)
			}
		}
	}
	if len(manifest.Contributes.Views) > 128 || len(manifest.Contributes.Tools) > 128 || len(manifest.Contributes.Settings) > 256 || len(manifest.Contributes.RPC) > 256 {
		return fmt.Errorf("plugin declares too many contributions")
	}
	if err := validateRuntime(root, manifest.Runtime); err != nil {
		return err
	}
	if err := validateViews(root, manifest.Contributes.Views); err != nil {
		return err
	}
	if err := validateTools(manifest, coreToolNames); err != nil {
		return err
	}
	if err := validateSettings(manifest.Contributes.Settings); err != nil {
		return err
	}
	if err := validateRPC(manifest); err != nil {
		return err
	}
	return nil
}

func compatibleAPI(value string) bool {
	return apiRangePattern.MatchString(strings.TrimSpace(value))
}

func validateRuntime(root string, pluginRuntime *Runtime) error {
	if pluginRuntime == nil {
		return nil
	}
	if pluginRuntime.Protocol != RPCProtocol {
		return fmt.Errorf("runtime protocol must be %q", RPCProtocol)
	}
	if len(pluginRuntime.Targets) == 0 {
		return fmt.Errorf("runtime must declare at least one target")
	}
	keys := make([]string, 0, len(pluginRuntime.Targets))
	currentTarget := runtime.GOOS + "-" + runtime.GOARCH
	for key := range pluginRuntime.Targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		target := pluginRuntime.Targets[key]
		if !validRuntimeTarget(key) || strings.TrimSpace(target.Path) == "" {
			return fmt.Errorf("runtime target %q is invalid", key)
		}
		path, err := packagePath(root, target.Path)
		if err != nil {
			return fmt.Errorf("runtime target %q: %w", key, err)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("runtime target %q executable is unavailable", key)
		}
		if key == currentTarget && runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("runtime target %q is not executable", key)
		}
		for _, argument := range target.Args {
			if strings.IndexByte(argument, 0) >= 0 {
				return fmt.Errorf("runtime target %q contains an invalid argument", key)
			}
		}
	}
	return nil
}

func validateViews(root string, views []ViewContribution) error {
	seen := map[string]bool{}
	for _, view := range views {
		if !viewIDPattern.MatchString(view.ID) || len(view.ID) > 64 || seen[view.ID] {
			return fmt.Errorf("plugin views must have unique lowercase kebab-case ids")
		}
		seen[view.ID] = true
		if view.Kind != "page" && view.Kind != "floating" {
			return fmt.Errorf("view %q kind must be page or floating", view.ID)
		}
		if strings.TrimSpace(view.Title) == "" || len(view.Title) > 100 {
			return fmt.Errorf("view %q title is required and must be 100 characters or fewer", view.ID)
		}
		if !view.IsSingleton() {
			return fmt.Errorf("view %q must be singleton in plugin API v1", view.ID)
		}
		entry, err := packagePath(root, view.Entry)
		if err != nil {
			return fmt.Errorf("view %q entry: %w", view.ID, err)
		}
		if strings.ToLower(filepath.Ext(entry)) != ".html" {
			return fmt.Errorf("view %q entry must be an HTML file", view.ID)
		}
		if info, err := os.Stat(entry); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("view %q entry is unavailable", view.ID)
		}
		if view.Icon != "" {
			icon, err := packagePath(root, view.Icon)
			if err != nil {
				return fmt.Errorf("view %q icon: %w", view.ID, err)
			}
			extension := strings.ToLower(filepath.Ext(icon))
			if extension != ".svg" && extension != ".png" && extension != ".webp" {
				return fmt.Errorf("view %q icon must be SVG, PNG, or WebP", view.ID)
			}
			if info, err := os.Stat(icon); err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("view %q icon is unavailable", view.ID)
			}
		}
		if err := validateDimensions(view); err != nil {
			return err
		}
	}
	return nil
}

func validateDimensions(view ViewContribution) error {
	for label, dimensions := range map[string]Dimensions{"default": view.DefaultSize, "minimum": view.MinimumSize} {
		if dimensions.Width < 0 || dimensions.Height < 0 || dimensions.Width > 4096 || dimensions.Height > 4096 {
			return fmt.Errorf("view %q %s dimensions are invalid", view.ID, label)
		}
	}
	return nil
}

func validateTools(manifest Manifest, coreToolNames map[string]bool) error {
	seen := map[string]bool{}
	prefix := strings.ReplaceAll(manifest.ID, "-", "_") + "_"
	for _, tool := range manifest.Contributes.Tools {
		if !toolNamePattern.MatchString(tool.Name) || !strings.HasPrefix(tool.Name, prefix) || seen[tool.Name] {
			return fmt.Errorf("plugin tools must have unique names beginning with %q", prefix)
		}
		if coreToolNames[tool.Name] {
			return fmt.Errorf("plugin tool %q conflicts with a core tool", tool.Name)
		}
		seen[tool.Name] = true
		if strings.TrimSpace(tool.Description) == "" {
			return fmt.Errorf("plugin tool %q description is required", tool.Name)
		}
		if len(tool.InputSchema) == 0 {
			return fmt.Errorf("plugin tool %q inputSchema is required", tool.Name)
		}
		if schemaType, _ := tool.InputSchema["type"].(string); schemaType != "object" {
			return fmt.Errorf("plugin tool %q inputSchema root type must be object", tool.Name)
		}
		if err := ValidateJSONSchemaDefinition(tool.InputSchema); err != nil {
			return fmt.Errorf("plugin tool %q inputSchema: %w", tool.Name, err)
		}
		if len(tool.OutputSchema) > 0 {
			if err := ValidateJSONSchemaDefinition(tool.OutputSchema); err != nil {
				return fmt.Errorf("plugin tool %q outputSchema: %w", tool.Name, err)
			}
		}
		if !rpcMethodPattern.MatchString(tool.Method) {
			return fmt.Errorf("plugin tool %q method is invalid", tool.Name)
		}
		if strings.HasPrefix(tool.Method, "echo.") {
			return fmt.Errorf("plugin tool %q uses the reserved echo RPC namespace", tool.Name)
		}
		if tool.TimeoutSeconds < 0 || tool.TimeoutSeconds > 600 {
			return fmt.Errorf("plugin tool %q timeoutSeconds must be between 1 and 600 when set", tool.Name)
		}
		if tool.ReadOnly == tool.Mutating {
			return fmt.Errorf("plugin tool %q must be classified as exactly one of readOnly or mutating", tool.Name)
		}
	}
	if len(manifest.Contributes.Tools) > 0 && manifest.Runtime == nil {
		return fmt.Errorf("plugin tools require a runtime")
	}
	return nil
}

func validateSettings(settings []SettingContribution) error {
	seen := map[string]bool{}
	for _, setting := range settings {
		if !viewIDPattern.MatchString(setting.Key) || len(setting.Key) > 64 || seen[setting.Key] {
			return fmt.Errorf("plugin settings must have unique lowercase kebab-case keys")
		}
		seen[setting.Key] = true
		switch setting.Type {
		case "string", "url", "number", "boolean", "select", "secret":
		default:
			return fmt.Errorf("setting %q has unsupported type %q", setting.Key, setting.Type)
		}
		if setting.Scope != "global" && setting.Scope != "workspace" {
			return fmt.Errorf("setting %q scope must be global or workspace", setting.Key)
		}
		if strings.TrimSpace(setting.Label) == "" {
			return fmt.Errorf("setting %q label is required", setting.Key)
		}
		if setting.Pattern != "" {
			if _, err := regexp.Compile(setting.Pattern); err != nil {
				return fmt.Errorf("setting %q pattern is invalid", setting.Key)
			}
		}
		if setting.Type == "select" && len(setting.Options) == 0 {
			return fmt.Errorf("setting %q select options are required", setting.Key)
		}
		if setting.Environment != "" && (!setting.Secret() || !validEnvironmentName(setting.Environment)) {
			return fmt.Errorf("setting %q has an invalid environment fallback", setting.Key)
		}
		optionValues := map[string]bool{}
		for _, option := range setting.Options {
			if option.Value == "" || optionValues[option.Value] || option.Label == "" {
				return fmt.Errorf("setting %q select options must have unique values and labels", setting.Key)
			}
			optionValues[option.Value] = true
		}
		if len(setting.Default) > 0 {
			var value any
			if err := json.Unmarshal(setting.Default, &value); err != nil {
				return fmt.Errorf("setting %q default is invalid JSON", setting.Key)
			}
			if setting.Secret() {
				return fmt.Errorf("setting %q secret defaults are not allowed", setting.Key)
			}
			if err := validateSettingValue(setting, value); err != nil {
				return fmt.Errorf("setting %q default: %w", setting.Key, err)
			}
		}
	}
	return nil
}

func validateRPC(manifest Manifest) error {
	seen := map[string]bool{}
	for _, contribution := range manifest.Contributes.RPC {
		if !rpcMethodPattern.MatchString(contribution.Method) || strings.HasPrefix(contribution.Method, "echo.") || seen[contribution.Method] {
			return fmt.Errorf("plugin RPC methods must be unique valid dotted identifiers")
		}
		seen[contribution.Method] = true
		if contribution.TimeoutSeconds < 0 || contribution.TimeoutSeconds > 600 {
			return fmt.Errorf("RPC method %q timeoutSeconds must be between 1 and 600 when set", contribution.Method)
		}
	}
	if len(manifest.Contributes.RPC) > 0 && manifest.Runtime == nil {
		return fmt.Errorf("plugin RPC methods require a runtime")
	}
	return nil
}

func packagePath(root, relative string) (string, error) {
	relative = filepath.FromSlash(strings.TrimSpace(relative))
	if relative == "" || filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" || strings.ContainsRune(relative, 0) {
		return "", fmt.Errorf("path must be package-relative")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the plugin package")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the plugin package")
	}
	return full, nil
}

func validRuntimeTarget(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return false
	}
	validOS := map[string]bool{"windows": true, "linux": true, "darwin": true}
	validArch := map[string]bool{"amd64": true, "arm64": true}
	return validOS[parts[0]] && validArch[parts[1]]
}

func rejectUnsafeTree(root string) error {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("plugin package root is unavailable")
	}
	entries := 0
	var total int64
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root {
			entries++
			if entries > MaxPackageFiles {
				return fmt.Errorf("plugin package exceeds entry-count limits")
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin packages may not contain symlinks: %s", path)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("plugin packages may contain only directories and regular files: %s", path)
		}
		if info.Mode().IsRegular() {
			total += info.Size()
			if info.Size() > MaxFileBytes || total > MaxPackageBytes {
				return fmt.Errorf("plugin package exceeds size limits")
			}
		}
		return nil
	})
}

// RPCMethods returns the set that sandboxed UI may invoke.
func (m Manifest) RPCMethods() map[string]RPCContribution {
	methods := make(map[string]RPCContribution, len(m.Contributes.RPC))
	for _, contribution := range m.Contributes.RPC {
		methods[contribution.Method] = contribution
	}
	return methods
}

func (m Manifest) Tool(name string) (ToolContribution, bool) {
	for _, contribution := range m.Contributes.Tools {
		if contribution.Name == name {
			return contribution, true
		}
	}
	return ToolContribution{}, false
}

func (m Manifest) View(id string) (ViewContribution, bool) {
	for _, contribution := range m.Contributes.Views {
		if contribution.ID == id {
			return contribution, true
		}
	}
	return ViewContribution{}, false
}

func isNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

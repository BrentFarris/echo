//go:build windows

package broker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var userenv = windows.NewLazySystemDLL("userenv.dll")
var createProfile = userenv.NewProc("CreateAppContainerProfile")
var deriveProfile = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
var deleteProfile = userenv.NewProc("DeleteAppContainerProfile")
var freeSID = windows.NewLazySystemDLL("advapi32.dll").NewProc("FreeSid")

type securityCapabilities struct {
	SID          *windows.SID
	Capabilities *windows.SIDAndAttributes
	Count        uint32
	Reserved     uint32
}

func profile(name string) (*windows.SID, error) {
	p, _ := windows.UTF16PtrFromString(name)
	var sid *windows.SID
	hr, _, _ := createProfile.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(p)), 0, 0, uintptr(unsafe.Pointer(&sid)))
	if uint32(hr) == 0x800700b7 {
		hr, _, _ = deriveProfile.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&sid)))
	}
	if hr != 0 {
		return nil, fmt.Errorf("create AppContainer: HRESULT %#x", hr)
	}
	return sid, nil
}
func changeAccess(path string, sid *windows.SID, remove bool, readOnly bool) error {
	sd, e := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if e != nil {
		return e
	}
	acl, _, e := sd.DACL()
	if e != nil {
		return e
	}
	// Installed tools often already grant read/execute to AppContainers. Do not
	// try to rewrite administrator-owned ACLs or journal a grant we don't need.
	ownedEntry, publicRead := false, false
	if acl != nil {
		for i := uint32(0); i < uint32(acl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if windows.GetAce(acl, i, &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
				continue
			}
			entrySID := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
			if entrySID == sid.String() {
				ownedEntry = true
			}
			mask := uint32(ace.Mask)
			required := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE)
			if entrySID == "S-1-15-2-1" && ace.Header.AceFlags&0x08 == 0 && mask&required == required {
				publicRead = true
			}
		}
	}
	if remove && !ownedEntry {
		return nil
	}
	if !remove && readOnly && publicRead {
		return nil
	}
	access := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE)
	if !readOnly {
		access |= windows.FILE_GENERIC_WRITE | windows.DELETE | 0x40
	}
	mode := windows.GRANT_ACCESS
	if remove {
		mode = windows.REVOKE_ACCESS
	}
	entries := []windows.EXPLICIT_ACCESS{{AccessPermissions: windows.ACCESS_MASK(access), AccessMode: windows.ACCESS_MODE(mode), Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)}}}
	merged, e := windows.ACLFromEntries(entries, acl)
	if e != nil {
		return e
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil)
}
func (b *ProcessBroker) cleanup(j aclJournal) error {
	sid, e := windows.StringToSid(j.SID)
	if e != nil {
		return e
	}
	var failures []string
	for _, path := range j.Paths {
		if e = changeAccess(path, sid, true, false); e != nil && !os.IsNotExist(e) {
			failures = append(failures, path+": "+e.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("sandbox ACL cleanup pending: %s", strings.Join(failures, "; "))
	}
	p, _ := windows.UTF16PtrFromString(j.Name)
	hr, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(p)))
	if hr != 0 && uint32(hr) != 0x80070002 {
		return fmt.Errorf("remove AppContainer: HRESULT %#x", hr)
	}
	return os.Remove(filepath.Join(b.Dir, j.Name+".json"))
}

// A read-only mirror supports installations whose administrator-owned ACLs do
// not admit AppContainers. No installer or executable is run while copying.
func (b *ProcessBroker) mirrorToolchain(ctx context.Context, source string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sum := sha256.Sum256([]byte(strings.ToLower(source)))
	dest := filepath.Join(b.Dir, "toolchains", hex.EncodeToString(sum[:12]))
	var size int64
	e := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("toolchain mirror does not follow links: %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported toolchain file: %s", path)
		}
		size += info.Size()
		if size > 512<<20 {
			return fmt.Errorf("toolchain exceeds the 512 MiB portable-copy limit; install an AppContainer-readable toolchain")
		}
		if old, err := os.Stat(target); err == nil && old.Size() == info.Size() && old.ModTime().Equal(info.ModTime()) {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = os.Rename(target+".tmp", target); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
	return dest, e
}
func (b *ProcessBroker) Recover() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	files, e := filepath.Glob(filepath.Join(b.Dir, "*.json"))
	if e != nil {
		return e
	}
	for _, path := range files {
		data, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var j aclJournal
		if e = json.Unmarshal(data, &j); e != nil {
			return e
		}
		if e = b.cleanup(j); e != nil {
			return e
		}
	}
	return nil
}

func (b *ProcessBroker) run(ctx context.Context, r ProcessRequest, emit func(string)) (result ProcessResult, err error) {
	// Per invocation identities avoid concurrent ACL cleanup invalidating another run.
	program := r.Executable
	if program == "" {
		powershell, lookupErr := exec.LookPath("pwsh.exe")
		if lookupErr != nil {
			powershell = filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		}
		program = powershell
		if len(r.Roots) > 0 {
			r.Toolchains = append(r.Toolchains, filepath.Dir(program))
		}
	}
	h := sha256.Sum256([]byte(r.BotID + time.Now().String()))
	name := "Echo2." + hex.EncodeToString(h[:12])
	scratch := filepath.Join(b.Dir, name)
	if err = os.MkdirAll(scratch, 0700); err != nil {
		return result, err
	}
	// Direct executables can ask Windows for the AppContainer known temp folder
	// before creating any files. PowerShell normally creates this implicitly.
	if err = os.MkdirAll(filepath.Join(scratch, "Packages", name, "AC", "Temp"), 0700); err != nil {
		return result, err
	}
	var caps securityCapabilities
	var networkCaps []windows.SIDAndAttributes
	var sid *windows.SID
	toolAliases := map[string]string{}
	if len(r.Roots) > 0 {
		sid, err = profile(name)
		if err != nil {
			return result, err
		}
		defer freeSID.Call(uintptr(unsafe.Pointer(sid)))
		j := aclJournal{Name: name, SID: sid.String()}
		defer func() {
			if e := b.cleanup(j); e != nil && err == nil {
				err = e
			}
		}()
		for _, p := range append(append(append([]string{}, r.Roots...), scratch), r.Toolchains...) {
			j.Paths = append(j.Paths, p)
			if err = b.journal(j); err != nil {
				return result, err
			}
			readOnly := false
			for _, t := range r.Toolchains {
				if p == t {
					readOnly = true
				}
			}
			if err = changeAccess(p, sid, false, readOnly); err != nil {
				if !readOnly || err != windows.ERROR_ACCESS_DENIED {
					return result, fmt.Errorf("grant AppContainer access to %s: %w", p, err)
				}
				mirror, e := b.mirrorToolchain(ctx, p)
				if e != nil {
					return result, e
				}
				j.Paths = append(j.Paths, mirror)
				if err = b.journal(j); err != nil {
					return result, err
				}
				if err = changeAccess(mirror, sid, false, true); err != nil {
					return result, err
				}
				toolAliases[p] = mirror
				if Within(p, program) {
					rel, _ := filepath.Rel(p, program)
					program = filepath.Join(mirror, rel)
				}
			}
		}
		caps.SID = sid
		for _, grant := range []struct {
			enabled bool
			kind    windows.WELL_KNOWN_SID_TYPE
		}{{r.Internet, windows.WinCapabilityInternetClientSid}, {r.PrivateNetwork, windows.WinCapabilityPrivateNetworkClientServerSid}} {
			if !grant.enabled {
				continue
			}
			capSID, e := windows.CreateWellKnownSid(grant.kind)
			if e != nil {
				return result, e
			}
			networkCaps = append(networkCaps, windows.SIDAndAttributes{Sid: capSID, Attributes: windows.SE_GROUP_ENABLED})
		}
		if len(networkCaps) > 0 {
			caps.Capabilities = &networkCaps[0]
			caps.Count = uint32(len(networkCaps))
		}
	}
	defer os.RemoveAll(scratch)
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return result, err
	}
	defer attrs.Delete()
	if sid != nil {
		if err = attrs.Update(0x20009, unsafe.Pointer(&caps), unsafe.Sizeof(caps)); err != nil {
			return result, err
		}
	}
	read, write, err := os.Pipe()
	if err != nil {
		return result, err
	}
	defer read.Close()
	defer write.Close()
	nul, err := os.Open(os.DevNull)
	if err != nil {
		return result, err
	}
	defer nul.Close()
	if r.Input != "" {
		inputPath := filepath.Join(scratch, "stdin.txt")
		if err = os.WriteFile(inputPath, []byte(r.Input), 0600); err != nil {
			return result, err
		}
		nul.Close()
		nul, err = os.Open(inputPath)
		if err != nil {
			return result, err
		}
		defer nul.Close()
	}
	handles := []windows.Handle{windows.Handle(write.Fd()), windows.Handle(nul.Fd())}
	for _, h := range handles {
		if err = windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return result, err
		}
	}
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return result, err
	}
	si := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES, StdInput: handles[1], StdOutput: handles[0], StdErr: handles[0]}, ProcThreadAttributeList: attrs.List()}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return result, err
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return result, err
	}

	arguments := r.Args
	if r.Executable == "" {
		r.Command = "New-PSDrive -Name EchoWorkspace -PSProvider FileSystem -Root '" + strings.ReplaceAll(r.Directory, "'", "''") + "' -Scope Global | Out-Null; Set-Location EchoWorkspace:; " + r.Command
		u := utf16.Encode([]rune("$ProgressPreference='SilentlyContinue'; $ErrorActionPreference='Stop'; " + r.Command))
		encoded := make([]byte, len(u)*2)
		for i, v := range u {
			binary.LittleEndian.PutUint16(encoded[i*2:], v)
		}
		arguments = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-OutputFormat", "Text", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded)}
	}
	app, _ := windows.UTF16PtrFromString(program)
	cmd, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{program}, arguments...)))
	cwd, _ := windows.UTF16PtrFromString(r.Directory)
	pathParts := filepath.SplitList(os.Getenv("PATH"))
	for i, p := range pathParts {
		for source, target := range toolAliases {
			if Within(source, p) {
				rel, _ := filepath.Rel(source, p)
				pathParts[i] = filepath.Join(target, rel)
			}
		}
	}
	env := []string{"SystemDrive=" + os.Getenv("SystemDrive"), "SystemRoot=" + os.Getenv("SystemRoot"), "WINDIR=" + os.Getenv("WINDIR"), "PATH=" + os.Getenv("PATH"), "PATHEXT=.COM;.EXE;.BAT;.CMD", "TEMP=" + scratch, "TMP=" + scratch, "USERPROFILE=" + scratch, "HOME=" + scratch, "APPDATA=" + scratch, "LOCALAPPDATA=" + scratch, "GOCACHE=" + filepath.Join(scratch, "go-build"), "GOMODCACHE=" + filepath.Join(scratch, "go-mod"), "DOTNET_CLI_HOME=" + scratch, "DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1", "NUGET_PACKAGES=" + filepath.Join(scratch, "nuget"), "npm_config_cache=" + filepath.Join(scratch, "npm")}
	for i, v := range env {
		if strings.HasPrefix(v, "PATH=") {
			env[i] = "PATH=" + strings.Join(pathParts, string(os.PathListSeparator))
		}
	}
	env = append(env, "ECHO_WORKSPACE="+r.Directory)
	env = append(env, "GOTMPDIR="+scratch)
	// Native clients must not fall back to virtualized known-folder paths or
	// machine-wide credential/configuration files.
	if r.Executable != "" {
		env = append(env, "FOSSIL_HOME="+scratch, "P4ENVIRO="+filepath.Join(scratch, "p4enviro.txt"), "P4TICKETS="+filepath.Join(scratch, "p4tickets.txt"), "P4TRUST="+filepath.Join(scratch, "p4trust.txt"))
	}
	if sid != nil {
		// These modes avoid ancestor inspection by the module/ref loaders. The
		// kernel still checks the resolved target, including every junction.
		env = append(env, "GIT_DEFAULT_REF_FORMAT=reftable", "NODE_OPTIONS=--preserve-symlinks --preserve-symlinks-main")
	}
	for _, key := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "CommonProgramFiles", "CommonProgramFiles(x86)", "CommonProgramW6432", "PROCESSOR_ARCHITECTURE", "NUMBER_OF_PROCESSORS"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "ProgramData="+scratch, "ALLUSERSPROFILE="+scratch, "DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_GENERATE_ASPNET_CERTIFICATE=false", "DOTNET_ADD_GLOBAL_TOOLS_TO_PATH=false", "MSBuildEnableWorkloadResolver=false")
	// Native plugin callers can add only host-constructed values. Replace keys
	// case-insensitively so Windows never chooses between duplicate entries.
	for key, value := range r.Environment {
		prefix := strings.ToUpper(key) + "="
		filtered := env[:0]
		for _, entry := range env {
			if !strings.HasPrefix(strings.ToUpper(entry), prefix) {
				filtered = append(filtered, entry)
			}
		}
		env = append(filtered, key+"="+value)
	}
	sort.Strings(env)
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	var pi windows.ProcessInformation
	err = windows.CreateProcess(app, cmd, nil, nil, true, windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_SUSPENDED|windows.CREATE_NO_WINDOW, &block[0], cwd, &si.StartupInfo, &pi)
	runtime.KeepAlive(caps)
	runtime.KeepAlive(networkCaps)
	runtime.KeepAlive(block)
	if err != nil {
		return result, fmt.Errorf("start native sandbox (no host fallback): %w", err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	if err = windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		return result, err
	}
	if _, err = windows.ResumeThread(pi.Thread); err != nil {
		windows.TerminateJobObject(job, 1)
		return result, err
	}
	write.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, e := read.Read(buf)
			if n > 0 {
				s := string(buf[:n])
				if len(result.Output) < 1<<20 {
					left := (1 << 20) - len(result.Output)
					if len(s) > left {
						s = s[:left]
						result.Truncated = true
					}
					result.Output += s
					if emit != nil {
						emit(s)
					}
				} else {
					result.Truncated = true
				}
			}
			if e != nil {
				if e != io.EOF {
				}
				return
			}
		}
	}()
	processDone := make(chan struct{})
	go func() { windows.WaitForSingleObject(pi.Process, windows.INFINITE); close(processDone) }()
	select {
	case <-ctx.Done():
		windows.TerminateJobObject(job, 1)
		<-processDone
		err = ctx.Err()
	case <-processDone:
	}
	windows.GetExitCodeProcess(pi.Process, &result.ExitCode)
	// Terminate background descendants before awaiting their inherited pipe handles.
	windows.TerminateJobObject(job, 0)
	<-done
	return result, err
}

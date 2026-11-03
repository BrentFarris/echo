package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func unixSandbox(ctx context.Context, r ProcessRequest, scratch, private string, proxy *egressProxy, executable string, args []string) (*exec.Cmd, string, error) {
	bwrap := ""
	for _, candidate := range []string{"/usr/bin/bwrap", "/bin/bwrap"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			bwrap = candidate
			break
		}
	}
	if bwrap == "" {
		return nil, "", errors.New("Linux native execution requires bubblewrap (bwrap); install it and enable unprivileged user namespaces. Echo will not run this command without containment")
	}
	runner, err := os.Executable()
	if err != nil {
		return nil, "", err
	}
	argv := []string{"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL"}
	if len(r.Roots) == 0 {
		argv = append(argv, "--bind", "/", "/")
	} else {
		for _, path := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc/ssl", "/etc/pki", "/etc/ld.so.cache", "/etc/ld.so.conf", "/etc/ld.so.conf.d", "/etc/passwd", "/etc/group", "/etc/nsswitch.conf", "/etc/hosts", "/etc/resolv.conf", "/etc/localtime"} {
			if _, err := os.Stat(path); err == nil {
				argv = append(argv, "--ro-bind", path, path)
			}
		}
		argv = append(argv, "--tmpfs", "/tmp", "--tmpfs", "/run")
	}
	for _, path := range r.Toolchains {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, "", err
		}
		argv = append(argv, "--ro-bind", real, real)
	}
	for _, root := range r.Roots {
		argv = append(argv, "--bind", root, root)
	}
	argv = append(argv, "--proc", "/proc", "--dev", "/dev", "--tmpfs", private, "--bind", scratch, scratch, "--ro-bind", runner, "/.echo/runner", "--chdir", r.Directory, "--", "/.echo/runner", "--echo-sandbox-relay", proxy.socket, "--", executable)
	argv = append(argv, args...)
	return exec.CommandContext(ctx, bwrap, argv...), "http://127.0.0.1:17439", nil
}

// A trusted trampoline inside the network namespace bridges only to the
// filtered host proxy. The untrusted child inherits a seccomp policy denying
// Unix sockets, ptrace, and namespace changes, so workspace socket files cannot
// bypass network grants or compromise the proxy process.
func init() {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[1] {
	case "--echo-sandbox-child":
		if len(os.Args) < 3 {
			os.Exit(125)
		}
		runtime.LockOSThread()
		if err := childFilter(); err != nil {
			fmt.Fprintln(os.Stderr, "install native syscall containment:", err)
			os.Exit(125)
		}
		if err := syscall.Exec(os.Args[2], os.Args[2:], os.Environ()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
	case "--echo-sandbox-relay":
		if len(os.Args) < 5 || os.Args[3] != "--" {
			os.Exit(125)
		}
		// The child must not inspect or modify the trusted relay through /proc.
		if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:17439")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go func() {
					defer conn.Close()
					upstream, err := net.Dial("unix", os.Args[2])
					if err != nil {
						return
					}
					defer upstream.Close()
					done := make(chan struct{})
					go func() { io.Copy(conn, upstream); conn.Close(); close(done) }()
					io.Copy(upstream, conn)
					upstream.Close()
					<-done
				}()
			}
		}()
		command := exec.Command(os.Args[0], append([]string{"--echo-sandbox-child"}, os.Args[4:]...)...)
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		command.Env = os.Environ()
		err = command.Run()
		listener.Close()
		if err == nil {
			os.Exit(0)
		}
		if command.ProcessState != nil {
			os.Exit(command.ProcessState.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
}

func childFilter() error {
	arch := uint32(0)
	switch runtime.GOARCH {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
	default:
		return errors.New("native syscall containment supports Linux amd64 and arm64")
	}
	load := func(offset uint32) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: offset}
	}
	eq := func(value uint32, yes, no uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: value, Jt: yes, Jf: no}
	}
	ret := func(value uint32) unix.SockFilter { return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: value} }
	deny := ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	filter := []unix.SockFilter{load(4), eq(arch, 1, 0), ret(unix.SECCOMP_RET_KILL_PROCESS), load(0)}
	// Reject the x32 ABI, which otherwise aliases x86-64 socket syscalls.
	if runtime.GOARCH == "amd64" {
		filter = append(filter, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, K: 0x40000000, Jt: 0, Jf: 1}, deny)
	}
	for _, call := range []uint32{unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_BPF, unix.SYS_KEYCTL, unix.SYS_PERF_EVENT_OPEN} {
		filter = append(filter, eq(call, 0, 1), deny)
	}
	// socket(domain,...) only; AF_INET/AF_INET6 see an isolated network namespace.
	filter = append(filter, eq(unix.SYS_SOCKET, 0, 5), load(16), eq(unix.AF_INET, 2, 0), eq(unix.AF_INET6, 1, 0), deny, ret(unix.SECCOMP_RET_ALLOW), ret(unix.SECCOMP_RET_ALLOW))
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	return unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0)
}

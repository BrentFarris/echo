package broker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func unixSandbox(ctx context.Context, r ProcessRequest, scratch, private string, proxy *egressProxy, executable string, args []string) (*exec.Cmd, string, error) {
	const sandbox = "/usr/bin/sandbox-exec"
	if _, err := os.Stat(sandbox); err != nil {
		return nil, "", errors.New("macOS native execution requires /usr/bin/sandbox-exec (Seatbelt); Echo will not run this command without containment")
	}
	quote := strconv.Quote
	var policy strings.Builder
	policy.WriteString("(version 1)\n(deny default)\n(allow process-exec process-fork)\n(allow signal (target same-sandbox))\n(allow process-info* (target same-sandbox))\n(allow sysctl-read)\n(allow file-read-metadata)\n")
	for _, path := range []string{"/usr", "/bin", "/sbin", "/System", "/Library/Apple", "/Library/Developer", "/private/etc/ssl", "/private/etc/hosts", "/private/etc/localtime", "/dev/null", "/dev/zero", "/dev/random", "/dev/urandom"} {
		fmt.Fprintf(&policy, "(allow file-read* (subpath %s))\n", quote(path))
	}
	policy.WriteString("(allow file-write-data (literal \"/dev/null\"))\n(allow mach-lookup (global-name \"com.apple.system.logger\") (global-name \"com.apple.trustd.agent\") (global-name \"com.apple.TrustEvaluationAgent\"))\n")
	for _, path := range r.Toolchains {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, "", err
		}
		fmt.Fprintf(&policy, "(allow file-read* (subpath %s))\n", quote(real))
	}
	roots := append([]string{}, r.Roots...)
	if len(roots) == 0 {
		roots = []string{"/"}
	}
	roots = append(roots, scratch)
	for _, path := range roots {
		fmt.Fprintf(&policy, "(allow file-read* file-write* (subpath %s))\n", quote(path))
	}
	fmt.Fprintf(&policy, "(deny file-read* file-write* (subpath %s))\n", quote(private))
	_, port, err := net.SplitHostPort(proxy.listener.Addr().String())
	if err != nil {
		return nil, "", err
	}
	fmt.Fprintf(&policy, "(allow network-outbound (remote ip %s))\n", quote("localhost:"+port))
	argv := []string{"-p", policy.String(), executable}
	argv = append(argv, args...)
	return exec.CommandContext(ctx, sandbox, argv...), "http://" + proxy.listener.Addr().String(), nil
}

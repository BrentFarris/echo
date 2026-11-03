package fossil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"github.com/brent/echo/internal/scmexec"
	"net/http"
	"strings"
)

// WebUI renders one request through the same confined runner as all VCS work.
// No persistent unauthenticated listener or host browser process is launched.
func (p *Provider) WebUI(ctx context.Context, workspace, id, base, path string) (*http.Response, error) {
	state, err := p.repository(ctx, workspace, id)
	if err != nil {
		return nil, err
	}
	if state.parent {
		return nil, errors.New("open the full checkout folder to browse Fossil UI")
	}
	info, err := p.checkoutInfo(ctx, workspace, state.root)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(path, "\r\n\x00") {
		return nil, errors.New("invalid Fossil UI path")
	}
	request := []byte("GET /" + strings.TrimPrefix(path, "/") + " HTTP/1.0\r\nHost: localhost\r\n\r\n")
	ctx, cancel := p.executionContext(ctx, workspace)
	defer cancel()
	result, err := p.runner.Execute(ctx, workspace, scmexec.Request{Command: []string{"fossil", "http", info.Repository, "--baseurl", base, "--nocompress", "--nodelay", "--nossl"}, WorkingDirectory: state.root, Input: request, OutputLimit: 16 << 20})
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 || result.StdoutTruncated {
		return nil, errors.New("Fossil UI response unavailable")
	}
	return http.ReadResponse(bufio.NewReader(bytes.NewReader(result.Stdout)), nil)
}

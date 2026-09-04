package fossil

import (
	"context"
	"strings"

	"github.com/brent/echo/internal/sourcecontrol"
)

const maxAnnotationLines = 500

func (p *Provider) Annotate(ctx context.Context, workspaceID, repositoryID, pathValue, revision string, startLine, endLine int) (sourcecontrol.Annotation, error) {
	state, err := p.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.Annotation{}, err
	}
	pathValue, err = cleanPath(pathValue)
	if err != nil || !state.pathAllowed(pathValue) {
		return sourcecontrol.Annotation{}, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "source control path is outside this workspace", Cause: sourcecontrol.ErrInvalidPath}
	}
	if strings.TrimSpace(revision) == "" {
		revision = "current"
	}
	if err := requireRef(revision); err != nil {
		return sourcecontrol.Annotation{}, err
	}
	if startLine <= 0 {
		startLine = 1
	}
	truncated := false
	if endLine <= 0 {
		endLine = startLine + 199
	} else if endLine < startLine {
		return sourcecontrol.Annotation{}, &sourcecontrol.Error{Code: "invalid_line_range", Message: "endLine must be greater than or equal to startLine"}
	} else if endLine-startLine+1 > maxAnnotationLines {
		endLine = startLine + maxAnnotationLines - 1
		truncated = true
	}
	output, err := p.run(ctx, state.workspaceID, state.root, false, "blame", "-n", "none", "-r", revision, "./"+pathValue)
	if err != nil {
		return sourcecontrol.Annotation{}, err
	}
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	from := startLine - 1
	if from > len(lines) {
		from = len(lines)
	}
	to := endLine
	if to > len(lines) {
		to = len(lines)
	}
	if to < from {
		to = from
	}
	return sourcecontrol.Annotation{
		RepositoryID: repositoryID, ProviderID: ID, Revision: revision, Path: pathValue,
		StartLine: startLine, EndLine: startLine + (to - from) - 1,
		Text: strings.Join(lines[from:to], "\n"), Truncated: truncated || to < len(lines),
	}, nil
}

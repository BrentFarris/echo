package gitservice

import (
	"context"
	"fmt"
	"strings"
)

const maxAnnotationLines = 500

func (s *Service) Annotate(ctx context.Context, workspaceID, repositoryID, path, revision string, startLine, endLine int) (Annotation, error) {
	ctx = s.executionContext(ctx, workspaceID)
	state, err := s.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return Annotation{}, err
	}
	path, err = cleanGitPath(path)
	if err != nil || !state.pathAllowed(path) {
		return Annotation{}, &Error{Code: "path_outside_workspace", Message: "Git path is outside this workspace", Cause: ErrInvalidPath}
	}
	if strings.TrimSpace(revision) == "" {
		revision = "HEAD"
	}
	revision, err = validateCommitRef(ctx, state, revision)
	if err != nil {
		return Annotation{}, err
	}
	if startLine <= 0 {
		startLine = 1
	}
	truncated := false
	if endLine <= 0 {
		endLine = startLine + 199
	} else if endLine < startLine {
		return Annotation{}, &Error{Code: "invalid_line_range", Message: "endLine must be greater than or equal to startLine"}
	} else if endLine-startLine+1 > maxAnnotationLines {
		endLine = startLine + maxAnnotationLines - 1
		truncated = true
	}
	output, err := runGit(ctx, state.root, nil, true, "blame", "--line-porcelain", "-L", fmt.Sprintf("%d,%d", startLine, endLine), revision, "--", path)
	if err != nil {
		return Annotation{}, err
	}
	return Annotation{Revision: revision, Path: path, StartLine: startLine, EndLine: endLine, Text: string(output), Truncated: truncated}, nil
}

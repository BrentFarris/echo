package fossil

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/brent/echo/internal/sourcecontrol"
)

type revisionEntry struct {
	hash, permissions, oldPath string
}

// Check-in manifests provide exact filenames, rename origins, and object IDs.
// The human-readable --brief diff does not reliably classify additions or
// deletions and cannot represent rename endpoints without ambiguity.
func (p *Provider) revisionEntries(ctx context.Context, state *repositoryState, ref string, depth int) (map[string]revisionEntry, error) {
	if depth > 1 {
		return nil, fmt.Errorf("Fossil manifest has an invalid baseline chain")
	}
	artifact, truncated, err := p.runLimited(ctx, state.workspaceID, state.root, false, maximumCommandOutput, "artifact", ref)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("Fossil check-in manifest exceeds the inspection limit")
	}
	entries := make(map[string]revisionEntry)
	lines := strings.Split(string(artifact), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "B" {
			entries, err = p.revisionEntries(ctx, state, fields[1], depth+1)
			if err != nil {
				return nil, err
			}
			// A baseline's rename annotations belong to its own parent.
			for path, entry := range entries {
				entry.oldPath = ""
				entries[path] = entry
			}
			break
		}
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "F" {
			continue
		}
		path := decodeManifestName(fields[1])
		if len(fields) == 2 {
			delete(entries, path)
			continue
		}
		entry := revisionEntry{hash: fields[2]}
		if len(fields) > 3 && fields[3] != "w" {
			entry.permissions = fields[3]
		}
		if len(fields) > 4 {
			entry.oldPath = decodeManifestName(fields[4])
		}
		entries[path] = entry
	}
	return entries, nil
}

func decodeManifestName(value string) string {
	return strings.NewReplacer(`\\`, `\`, `\s`, " ", `\n`, "\n", `\r`, "\r", `\t`, "\t", `\v`, "\v", `\f`, "\f").Replace(value)
}

func changedRevisionFiles(before, after map[string]revisionEntry) []sourcecontrol.RevisionFile {
	files := []sourcecontrol.RevisionFile{}
	renamed := make(map[string]bool)
	for path, entry := range after {
		oldPath := path
		if entry.oldPath != "" && entry.oldPath != path {
			if _, exists := before[entry.oldPath]; exists {
				oldPath = entry.oldPath
			}
		}
		previous, exists := before[oldPath]
		file := sourcecontrol.RevisionFile{Path: path}
		switch {
		case oldPath != path:
			file.Status, file.OldPath = "R", oldPath
			renamed[oldPath] = true
		case !exists:
			file.Status = "A"
		case previous.hash != entry.hash || previous.permissions != entry.permissions:
			file.Status = "M"
		default:
			continue
		}
		files = append(files, file)
	}
	for path := range before {
		if _, exists := after[path]; !exists && !renamed[path] {
			files = append(files, sourcecontrol.RevisionFile{Path: path, Status: "D"})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

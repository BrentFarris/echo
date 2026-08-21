package workspaceskills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"

	"github.com/brent/echo/internal/tools"
)

//go:embed builtin/echo-plugins/SKILL.md
var builtinSkillFiles embed.FS

func builtinEchoPluginsSkill() (tools.WorkspaceSkill, error) {
	data, err := builtinSkillFiles.ReadFile("builtin/echo-plugins/SKILL.md")
	if err != nil {
		return tools.WorkspaceSkill{}, err
	}
	header, body, err := parse(data)
	if err != nil {
		return tools.WorkspaceSkill{}, fmt.Errorf("parse built-in skill: %w", err)
	}
	digest := sha256.Sum256(data)
	return tools.WorkspaceSkill{
		WorkspaceSkillSummary: tools.WorkspaceSkillSummary{
			ID: "builtin/echo-plugins", Folder: "builtin", Name: header.Name,
			Description: header.Description, Triggers: append([]string(nil), header.Triggers...),
		},
		Body: body, Revision: hex.EncodeToString(digest[:]), ModifiedAt: "built-in",
	}, nil
}

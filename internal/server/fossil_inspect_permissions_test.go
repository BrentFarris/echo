package server

import (
	"testing"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/tools"
)

func TestFossilInspectModeAvailabilityIsAdditiveAndExplicit(t *testing.T) {
	for _, mode := range agentmodes.Defaults() {
		allowed := toolScopesForAgentMode(mode).HasTool(tools.FossilInspectToolName)
		switch mode.ID {
		case agentmodes.GeneralID, agentmodes.PlanID:
			if !allowed {
				t.Fatalf("%s mode must expose fossil_inspect", mode.ID)
			}
		case agentmodes.GoalID:
			if allowed {
				t.Fatal("Goal mode gained fossil_inspect without an explicit grant")
			}
		}
	}
	legacyCustom := agentmodes.Mode{ID: "custom", Name: "Existing Custom"}
	if toolScopesForAgentMode(legacyCustom).HasTool(tools.FossilInspectToolName) {
		t.Fatal("an existing unrestricted custom mode gained fossil_inspect automatically")
	}
	explicitCustom := agentmodes.Mode{ID: "custom", Name: "Custom", Permissions: map[string]tools.ToolPermission{
		tools.FossilInspectToolName: {Name: tools.FossilInspectToolName},
	}}
	if !toolScopesForAgentMode(explicitCustom).HasTool(tools.FossilInspectToolName) {
		t.Fatal("an explicit custom-mode fossil_inspect grant was ignored")
	}
}

func TestFossilInspectResearchWorkerAvailabilityRespectsCustomModeOptIn(t *testing.T) {
	general, err := agentmodes.NewManager().Resolve(t.TempDir(), agentmodes.GeneralID)
	if err != nil {
		t.Fatal(err)
	}
	if !researchToolScopes(general).HasTool(tools.FossilInspectToolName) {
		t.Fatal("built-in research workers must expose fossil_inspect")
	}
	legacyCustom := agentmodes.Mode{ID: "custom", Name: "Existing Custom"}
	if researchToolScopes(legacyCustom).HasTool(tools.FossilInspectToolName) {
		t.Fatal("existing custom-mode research workers gained fossil_inspect automatically")
	}
}

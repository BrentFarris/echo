package domain

// Swarm permissions describe direct access. Shared bots retain their configured
// tools, but their administrative operations use the same grants as the caller.
const (
	AccessBots          = "runBots"
	AccessRead          = "readFiles"
	AccessWrite         = "writeFiles"
	AccessExecute       = "execute"
	AccessManageBots    = "manageBots"
	AccessManagePlugins = "managePlugins"
	AccessModels        = "manageModels"
	AccessSchedules     = "manageSchedules"
	AccessChats         = "changeChats"
	AccessMachine       = "manageMachine"
)

var SwarmPermissions = []string{AccessBots, AccessRead, AccessWrite, AccessExecute, AccessManageBots, AccessManagePlugins, AccessModels, AccessSchedules, AccessChats, AccessMachine}

// Missing entries inherit (or allow, for machine defaults). Resource entries
// can only narrow sharing; a globally private bot/plugin is never overridden.
type SwarmPolicy struct {
	Permissions map[string]bool `json:"permissions"`
	Bots        map[string]bool `json:"bots,omitempty"`
	Plugins     map[string]bool `json:"plugins,omitempty"`
}

type SwarmAccess struct {
	Permissions map[string]bool `json:"permissions"`
	Bots        map[string]bool `json:"bots"`
	Plugins     map[string]bool `json:"plugins"`
	Full        bool            `json:"full"`
	Owner       bool            `json:"owner"`
}

// ExecutionAccess is host-established provenance, never accepted from browser
// JSON. Worker transport authenticates it separately from the message permit.
type ExecutionAccess struct {
	Origin     string         `json:"origin"`
	FullDenied bool           `json:"fullDenied,omitempty"`
	Via        []string       `json:"via,omitempty"`
	Denied     []string       `json:"denied,omitempty"`
	Parents    []AccessParent `json:"parents,omitempty"`
}

type AccessParent struct {
	Task  string `json:"task"`
	Node  string `json:"node"`
	ID    string `json:"id"`
	Token string `json:"token"`
}

func Shared(value *bool) bool { return value == nil || *value }

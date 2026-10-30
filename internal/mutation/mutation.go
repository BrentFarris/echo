// Package mutation defines the shared boundary between file writes and SCMs.
// Operations use already-resolved host paths; the workspace filesystem owns
// confinement and path locks, and providers own their native transactions.
package mutation

import "context"

type Operation struct {
	WorkspaceID string
	Kind        string // edit, create, move, delete, restore
	Path        string
	Destination string
	Origin      string
	// RecoveryID binds a restore to the exact Trash operation, even after
	// tracking is disabled or the same path has been deleted more than once.
	RecoveryID string
	// Validate runs under both the path and provider locks, before side effects.
	Validate func() error
}

type Result struct {
	Pending     bool   `json:"pending,omitempty"`
	Diagnostic  string `json:"diagnostic,omitempty"`
	OperationID string `json:"operationId,omitempty"`
}

type Coordinator interface {
	Run(context.Context, Operation, func() error) (Result, error)
	Tracks(string, string) bool
}

// RecoveryCoordinator can finish an explicitly requested recovery after normal
// automatic tracking has been disabled. It must retain the original identity.
type RecoveryCoordinator interface {
	Handles(Operation) bool
}

func Apply(op Operation, apply func() error) (Result, error) {
	if op.Validate != nil {
		if err := op.Validate(); err != nil {
			return Result{}, err
		}
	}
	return Result{}, apply()
}

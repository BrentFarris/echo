package sourcecontrol

import "context"

// Provider owns discovery and identity for one VCS. Feature interfaces are
// intentionally separate so providers are never forced into Git semantics.
type Provider interface {
	Descriptor(context.Context, string) ProviderDescriptor
	Repositories(context.Context, string) ([]Repository, error)
}

type StatusProvider interface {
	Status(context.Context, string, string) (StatusSnapshot, error)
}

type DiffProvider interface {
	Diff(context.Context, string, string, DiffTarget) (DiffDocument, error)
}

type MetadataProvider interface {
	Metadata(context.Context, string, string) (Metadata, error)
}

type HistoryProvider interface {
	History(context.Context, string, string, int, int) (History, error)
	RevisionDetail(context.Context, string, string, string, string) (RevisionDetail, error)
}

type AnnotateProvider interface {
	Annotate(context.Context, string, string, string, string, int, int) (Annotation, error)
}

type ActionProvider interface {
	Action(context.Context, string, string, ActionRequest) (ActionResult, error)
}

type WatchProvider interface {
	Subscribe(context.Context, string) error
	Unsubscribe(string)
	InvalidateWorkspace(string)
	ResetWorkspace(context.Context, string) error
	RemoveWorkspace(string)
	StopWorkspaceProcesses(string)
	Close()
}

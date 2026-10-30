package sourcecontrol

import (
	"context"
	"errors"
	"testing"
)

type lifecycleFixture struct {
	WatchProvider
	id    string
	err   error
	reset bool
}

func (p *lifecycleFixture) Descriptor(context.Context, string) ProviderDescriptor {
	return ProviderDescriptor{ID: p.id}
}
func (p *lifecycleFixture) Repositories(context.Context, string) ([]Repository, error) {
	return []Repository{{ID: p.id + ":repo", ProviderID: p.id}}, p.err
}
func (p *lifecycleFixture) ResetWorkspace(context.Context, string) error {
	p.reset = true
	return p.err
}

func TestUnavailableProviderDoesNotHideRepositoriesOrPreventScopeReset(t *testing.T) {
	s := New()
	failure := errors.New("repository metadata unavailable")
	unavailable := &lifecycleFixture{id: "unavailable", err: failure}
	healthy := &lifecycleFixture{id: "healthy"}
	for _, provider := range []*lifecycleFixture{unavailable, healthy} {
		if err := s.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	repositories, err := s.Repositories(context.Background(), "bot")
	if err != nil || len(repositories) != 2 {
		t.Fatalf("partial discovery lost repositories: %v, %v", repositories, err)
	}
	if diagnostic := s.Providers(context.Background(), "bot")[0].Diagnostic; diagnostic == "" {
		t.Fatal("incomplete discovery must remain visible")
	}
	if err := s.ResetWorkspace(context.Background(), "bot"); !errors.Is(err, failure) {
		t.Fatalf("reset must report its failure: %v", err)
	}
	if !unavailable.reset || !healthy.reset {
		t.Fatal("one provider failure prevented another provider's scope reset")
	}
	if _, ok := s.repositories["bot"]; ok {
		t.Fatal("scope reset retained repository routing")
	}
}

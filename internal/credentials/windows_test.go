//go:build windows

package credentials

import (
	"context"
	"github.com/brent/echo/internal/domain"
	"os"
	"testing"
)

func TestNativeCredentialRoundTrip(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("native Windows credential integration")
	}
	vault := New()
	ctx := context.Background()
	key := "Echo2/test/" + domain.ID()
	secret := domain.ID()
	if e := vault.Set(ctx, key, secret); e != nil {
		t.Fatal(e)
	}
	defer vault.Delete(ctx, key)
	actual, e := vault.Get(ctx, key)
	if e != nil || actual != secret {
		t.Fatal("protected credential round trip failed")
	}
	if _, e = vault.Get(ctx, key+"-another-plugin"); e == nil {
		t.Fatal("different reference resolved the credential")
	}
	if e = vault.Delete(ctx, key); e != nil {
		t.Fatal(e)
	}
	if _, e = vault.Get(ctx, key); e == nil {
		t.Fatal("deleted credential remained readable")
	}
}

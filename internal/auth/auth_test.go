package auth

import (
	"context"
	"errors"
	"testing"
)

func TestScopesDoNotImplyEachOther(t *testing.T) {
	admin := Principal{Kind: KindAPIKey, KeyID: "k", Scopes: []Scope{ScopeAdmin}}
	if admin.Has(ScopeMetricsRead) || admin.Has(ScopeSystemRead) {
		t.Error("admin must not imply read scopes")
	}
	reader := Principal{Kind: KindAPIKey, KeyID: "k", Scopes: []Scope{ScopeMetricsRead, ScopeSystemRead}}
	if reader.Has(ScopeAdmin) {
		t.Error("read scopes must not imply admin")
	}
}

func TestLocalAdminHasAllScopes(t *testing.T) {
	p := LocalAdmin(1000, 42)
	for _, s := range KnownScopes() {
		if !p.Has(s) {
			t.Errorf("local admin missing %s", s)
		}
	}
	if p.LogID() != "local-admin" {
		t.Errorf("LogID %q", p.LogID())
	}
}

func TestKnown(t *testing.T) {
	if !ScopeAdmin.Known() || Scope("processes:write").Known() {
		t.Error("scope registry wrong")
	}
}

func TestRejectAll(t *testing.T) {
	_, err := RejectAll{}.Verify(context.Background(), "anything")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("got %v", err)
	}
}

func TestPrincipalContext(t *testing.T) {
	if _, ok := PrincipalFrom(context.Background()); ok {
		t.Error("empty context has principal")
	}
	ctx := WithPrincipal(context.Background(), Principal{Kind: KindAPIKey, KeyID: "abc"})
	p, ok := PrincipalFrom(ctx)
	if !ok || p.LogID() != "key:abc" {
		t.Errorf("got %+v %v", p, ok)
	}
}

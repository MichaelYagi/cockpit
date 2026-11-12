package cli

import (
	"slices"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/auth"
)

// --scope all must stay in step with what the server can issue.
func TestReadScopesMatchIssuable(t *testing.T) {
	var want []string
	for _, s := range auth.IssuableScopes() {
		want = append(want, string(s))
	}
	if !slices.Equal(readScopes, want) {
		t.Errorf("readScopes %v, server issues %v", readScopes, want)
	}
}

func TestExpandScopes(t *testing.T) {
	got := expandScopes([]string{"metrics:read", "all", "system:read"})
	want := []string{"metrics:read", "system:read", "network:read", "processes:read"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v", got)
	}
}

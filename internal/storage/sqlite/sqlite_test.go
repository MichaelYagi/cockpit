package sqlite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/auth"
)

func open(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cockpit.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func TestOpenMigratesAndRestrictsPermissions(t *testing.T) {
	db, path := open(t)
	v, err := db.SchemaVersion(context.Background())
	if err != nil || v != migrationCount(t) {
		t.Fatalf("version %d %v", v, err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("db mode %04o", fi.Mode().Perm())
		}
	}
	db.Close()
	// Reopening does not reapply migrations.
	db2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if v, _ := db2.SchemaVersion(context.Background()); v != migrationCount(t) {
		t.Errorf("version after reopen %d", v)
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	db, path := open(t)
	if _, err := db.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("opened a database from a newer version")
	}
}

func TestRejectsURIPath(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "x?mode=memory")); err == nil {
		t.Fatal("accepted '?' in path")
	}
}

func TestKeyStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	exp := now.Add(time.Hour)
	k := auth.StoredKey{KeyMeta: auth.KeyMeta{ID: "aaaaaaaaaaaa", Name: "n", Scopes: []auth.Scope{auth.ScopeMetricsRead, auth.ScopeSystemRead},
		CreatedAt: now, ExpiresAt: &exp}, Hash: bytes.Repeat([]byte{7}, 32)}
	if err := db.InsertKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetKey(ctx, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "n" || len(got.Scopes) != 2 || !got.CreatedAt.Equal(now) || !got.ExpiresAt.Equal(exp) || !bytes.Equal(got.Hash, k.Hash) || got.RevokedAt != nil {
		t.Errorf("round trip %+v", got)
	}
	if _, err := db.GetKey(ctx, "nosuchkeyzzz"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("missing key: %v", err)
	}
	if n, _ := db.CountActiveKeys(ctx, now); n != 1 {
		t.Errorf("active %d", n)
	}
	if n, _ := db.CountActiveKeys(ctx, exp); n != 0 {
		t.Errorf("expired counted as active: %d", n)
	}

	later := now.Add(time.Minute)
	if err := db.UpdateLastUsed(ctx, map[string]time.Time{k.ID: later}); err != nil {
		t.Fatal(err)
	}
	// An older timestamp never moves last_used_at backwards.
	_ = db.UpdateLastUsed(ctx, map[string]time.Time{k.ID: now})
	if got, _ := db.GetKey(ctx, k.ID); got.LastUsedAt == nil || !got.LastUsedAt.Equal(later) {
		t.Errorf("last used %v", got.LastUsedAt)
	}

	next := auth.StoredKey{KeyMeta: auth.KeyMeta{ID: "bbbbbbbbbbbb", Name: "n", Scopes: k.Scopes, CreatedAt: later, RotatedFrom: k.ID}, Hash: bytes.Repeat([]byte{8}, 32)}
	if err := db.RotateKey(ctx, k.ID, later, next); err != nil {
		t.Fatal(err)
	}
	old, _ := db.GetKey(ctx, k.ID)
	nw, _ := db.GetKey(ctx, next.ID)
	if old.RevokedAt == nil || nw.RotatedFrom != k.ID {
		t.Errorf("rotate: old %+v new %+v", old, nw)
	}
	// Rotating an already revoked key fails and inserts nothing.
	again := next
	again.ID = "cccccccccccc"
	if err := db.RotateKey(ctx, k.ID, later, again); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("rotate revoked: %v", err)
	}
	if _, err := db.GetKey(ctx, again.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Error("failed rotation left a new key behind")
	}

	if err := db.RevokeKey(ctx, next.ID, later); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeKey(ctx, "nosuchkeyzzz", later); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("revoke missing: %v", err)
	}
	keys, _ := db.ListKeys(ctx)
	if len(keys) != 2 {
		t.Errorf("list %d", len(keys))
	}
}

// End to end with the real service: the database file never contains the
// plaintext key or its secret part.
func TestPlaintextNeverPersisted(t *testing.T) {
	ctx := context.Background()
	db, path := open(t)
	svc := auth.NewService(db, auth.ServiceOptions{})
	c, err := svc.Create(ctx, auth.CreateRequest{Name: "probe", Scopes: []auth.Scope{auth.ScopeSystemRead}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(ctx, c.Key); err != nil {
		t.Fatal(err)
	}
	if err := svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	db.Close()
	secret := c.Key[len("ckp_")+13:]
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("%s contains the key secret", filepath.Base(f))
		}
	}
}

// migrationCount is the number of embedded migrations, i.e. the expected
// schema version.
func migrationCount(t *testing.T) int {
	t.Helper()
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

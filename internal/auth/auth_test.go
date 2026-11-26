package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/storage"
)

func testParams() Argon2Params {
	return Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
}

func newTestService(t *testing.T, cfg Config) (*Service, storage.Storage) {
	t.Helper()
	store, err := storage.Open(t.TempDir(), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	def := &collections.CollectionDef{
		Name: "users",
		Type: collections.TypeAuth,
		Indexes: []collections.Index{
			{Name: "email_idx", Field: "email", Unique: true, State: "ready"},
		},
	}
	if err := store.Update(func(tx storage.WriteTx) error { return tx.SaveCollection(def) }); err != nil {
		t.Fatal(err)
	}
	cfg.Params = testParams()
	s, err := New(store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, store
}

func TestPasswordHashing(t *testing.T) {
	p := testParams()
	hash, err := HashPassword("correct horse battery staple", p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("unexpected hash: %s", hash)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify: %v %v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong")
	if err != nil || ok {
		t.Fatalf("wrong password accepted: %v %v", ok, err)
	}
	if _, err := VerifyPassword("not-a-hash", "x"); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("expected invalid hash, got %v", err)
	}
	for _, pw := range []string{"short", "", "       "} {
		if err := ValidatePassword(pw); err == nil {
			t.Errorf("password %q should be rejected", pw)
		}
	}
	if err := ValidatePassword("long-enough"); err != nil {
		t.Fatal(err)
	}
}

func TestAdminLoginIdentifyAndRevoke(t *testing.T) {
	svc, _ := newTestService(t, Config{})
	if _, err := svc.CreateFirstAdmin("admin@example.com", "super-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateFirstAdmin("other@example.com", "super-secret"); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("expected setup complete, got %v", err)
	}
	sess, err := svc.AuthenticateAdmin("admin@example.com", "super-secret")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Token == "" || sess.RefreshToken == "" || sess.ExpiresIn <= 0 {
		t.Fatalf("incomplete session: %+v", sess)
	}
	if !sess.Identity.IsAdmin() {
		t.Fatal("expected admin identity")
	}
	id, err := svc.Identify(sess.Token)
	if err != nil {
		t.Fatal(err)
	}
	if id.ID != sess.Identity.ID || !id.IsAdmin() {
		t.Fatalf("identity = %+v", id)
	}
	if _, err := svc.Identify(sess.Token + "x"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("tampered token accepted: %v", err)
	}
	if _, err := svc.AuthenticateAdmin("admin@example.com", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := svc.RevokeAllSessions(sess.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Identify(sess.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("token still valid after revocation: %v", err)
	}
}

func TestRegisterLoginRefreshRotation(t *testing.T) {
	svc, _ := newTestService(t, Config{})
	sess, err := svc.Register("users", "Ana@Example.com", "password-123", collections.Document{"role": "author"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Identity.Email != "ana@example.com" {
		t.Fatalf("email not normalized: %s", sess.Identity.Email)
	}
	if sess.Record["passwordHash"] != nil || sess.Record["tokenKey"] != nil {
		t.Fatal("secrets leaked in session record")
	}
	if sess.Identity.AuthMap()["role"] != "author" {
		t.Fatal("custom field missing from auth map")
	}

	if _, err := svc.Register("users", "ana@example.com", "password-123", nil); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("expected email conflict, got %v", err)
	}
	if _, err := svc.Register("users", "bad", "password-123", nil); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("expected invalid email, got %v", err)
	}
	if _, err := svc.Register("users", "b@example.com", "short", nil); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("expected weak password, got %v", err)
	}

	login, err := svc.Authenticate("users", "ana@example.com", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate("users", "ana@example.com", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password accepted: %v", err)
	}
	if _, err := svc.Authenticate("users", "nobody@example.com", "password-123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user accepted: %v", err)
	}
	// Login by id also works.
	if _, err := svc.Authenticate("users", login.Identity.ID, "password-123"); err != nil {
		t.Fatalf("login by id: %v", err)
	}

	rotated, err := svc.Refresh(login.RefreshToken, "")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken == login.RefreshToken || rotated.Token == login.Token {
		t.Fatal("refresh did not rotate tokens")
	}
	if _, err := svc.Identify(rotated.Token); err != nil {
		t.Fatal(err)
	}

	// Reusing the old token revokes the whole family.
	if _, err := svc.Refresh(login.RefreshToken, ""); !errors.Is(err, ErrRefreshReuse) {
		t.Fatalf("expected reuse detection, got %v", err)
	}
	if _, err := svc.Refresh(rotated.RefreshToken, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("family should be revoked, got %v", err)
	}

	// Logout revokes the current family.
	login2, err := svc.Authenticate("users", "ana@example.com", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(login2.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(login2.RefreshToken, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("logout did not revoke token: %v", err)
	}
}

func TestRegistrationRequiresAuthCollection(t *testing.T) {
	svc, store := newTestService(t, Config{})
	if err := store.Update(func(tx storage.WriteTx) error {
		return tx.SaveCollection(&collections.CollectionDef{Name: "posts"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register("posts", "a@example.com", "password-123", nil); !errors.Is(err, ErrNotAuthCollection) {
		t.Fatalf("expected not-auth error, got %v", err)
	}
	if _, err := svc.Authenticate("missing", "a@example.com", "password-123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("missing collection login should be generic: %v", err)
	}
}

func TestAccessAndRefreshExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, _ := newTestService(t, Config{
		AccessTTL:  time.Minute,
		RefreshTTL: time.Hour,
		Now:        func() time.Time { return now },
	})
	if _, err := svc.CreateAdmin("admin@example.com", "super-secret"); err != nil {
		t.Fatal(err)
	}
	sess, err := svc.AuthenticateAdmin("admin@example.com", "super-secret")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := svc.Identify(sess.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired access token accepted: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := svc.Refresh(sess.RefreshToken, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired refresh token accepted: %v", err)
	}
}

func TestEmailValidation(t *testing.T) {
	for _, ok := range []string{"a@b.co", "USER+tag@example.com"} {
		if _, err := NormalizeEmail(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a", "a@", "@b.co", "Name <a@b.co>", "a b@c.co"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

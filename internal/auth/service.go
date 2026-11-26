// Package auth implements Doketfire users, superusers, access tokens and
// refresh-token rotation.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/doketfire/doketfire/internal/codec"
	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/storage"
)

const (
	metaBucket    = "_meta"
	adminsBucket  = "_admins"
	refreshBucket = "_refresh_tokens"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrEmailTaken         = errors.New("email is already registered")
	ErrNotAuthCollection  = errors.New("collection is not an auth collection")
	ErrWeakPassword       = errors.New("weak password")
	ErrInvalidEmail       = errors.New("invalid email address")
	ErrRefreshReuse       = errors.New("refresh token reuse detected; session family revoked")
	ErrSetupComplete      = errors.New("setup was already completed")
	ErrAdminLast          = errors.New("cannot delete the last superuser")
	ErrAdminSelf          = errors.New("cannot delete the currently authenticated superuser")
	ErrAdminNotFound      = errors.New("superuser not found")
)

// Config configures authentication.
type Config struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	Params     Argon2Params
	Issuer     string
	// Now is injectable for tests.
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.AccessTTL <= 0 {
		c.AccessTTL = 15 * time.Minute
	}
	if c.RefreshTTL <= 0 {
		c.RefreshTTL = 30 * 24 * time.Hour
	}
	if c.Issuer == "" {
		c.Issuer = "doketfire"
	}
	c.Params = c.Params.withDefaults()
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Identity is an authenticated principal.
type Identity struct {
	Kind       string               `json:"kind"` // user | admin
	ID         string               `json:"id"`
	Collection string               `json:"collection,omitempty"`
	Email      string               `json:"email,omitempty"`
	Record     collections.Document `json:"record,omitempty"`
}

// IsAdmin reports whether the principal is a superuser.
func (i *Identity) IsAdmin() bool { return i != nil && i.Kind == "admin" }

// AuthMap converts the identity into the map exposed to filters and rules.
// The returned map is a fresh shallow copy and can be safely mutated by the
// caller.
func (i *Identity) AuthMap() map[string]any {
	if i == nil {
		return nil
	}
	m := i.Record.SanitizeShallow()
	if m == nil {
		m = map[string]any{}
	}
	m["id"] = i.ID
	if i.Email != "" {
		m["email"] = i.Email
	}
	if i.IsAdmin() {
		m["admin"] = true
	}
	return m
}

// Session is returned after a successful login or refresh.
type Session struct {
	Token        string               `json:"token"`
	RefreshToken string               `json:"refreshToken"`
	ExpiresIn    int64                `json:"expiresIn"`
	Identity     *Identity            `json:"-"`
	Record       collections.Document `json:"record,omitempty"`
}

// Claims are the JWT claims used by Doketfire.
type Claims struct {
	Kind       string `json:"kind"`
	Collection string `json:"collection,omitempty"`
	Email      string `json:"email,omitempty"`
	jwt.RegisteredClaims
}

type refreshRecord struct {
	UserID     string    `json:"userId"`
	Collection string    `json:"collection,omitempty"`
	Kind       string    `json:"kind"`
	Family     string    `json:"family"`
	ExpiresAt  time.Time `json:"expiresAt"`
	CreatedAt  time.Time `json:"createdAt"`
	Used       bool      `json:"used,omitempty"`
}

// AdminInfo is the sanitized superuser representation for the admin panel.
type AdminInfo struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

// SessionInfo is the sanitized refresh-session representation for the admin
// panel. TokenId is a short prefix of the stored hash, never a usable token.
type SessionInfo struct {
	TokenID    string `json:"tokenId"`
	Kind       string `json:"kind"`
	UserID     string `json:"userId"`
	Collection string `json:"collection"`
	Family     string `json:"family"`
	CreatedAt  string `json:"createdAt"`
	ExpiresAt  string `json:"expiresAt"`
	Used       bool   `json:"used"`
}

type subject struct {
	kind       string
	collection string
	id         string
	email      string
	tokenKey   string
	record     collections.Document
}

// Service implements authentication.
type Service struct {
	store     storage.Storage
	secret    []byte
	cfg       Config
	dummyHash string
	now       func() time.Time
}

// New creates the auth service and loads (or generates) the server secret.
func New(store storage.Storage, cfg Config) (*Service, error) {
	if store == nil {
		return nil, errors.New("auth: storage is required")
	}
	cfg = cfg.withDefaults()
	secret, err := loadOrCreateSecret(store)
	if err != nil {
		return nil, err
	}
	dummy, err := HashPassword("doketfire-timing-equalizer", cfg.Params)
	if err != nil {
		return nil, err
	}
	return &Service{store: store, secret: secret, cfg: cfg, dummyHash: dummy, now: cfg.Now}, nil
}

func loadOrCreateSecret(store storage.Storage) ([]byte, error) {
	var secret []byte
	if err := store.View(func(tx storage.ReadTx) error {
		raw, err := tx.RawGet(metaBucket, []byte("server_secret"))
		if err != nil {
			return err
		}
		secret = raw
		return nil
	}); err != nil {
		return nil, err
	}
	if len(secret) >= 32 {
		return secret, nil
	}
	secret = make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate server secret: %w", err)
	}
	if err := store.Update(func(tx storage.WriteTx) error {
		return tx.RawPut(metaBucket, []byte("server_secret"), secret)
	}); err != nil {
		return nil, fmt.Errorf("store server secret: %w", err)
	}
	// Re-read to win a potential race with another process (bbolt has a single
	// writer, so this is deterministic).
	if err := store.View(func(tx storage.ReadTx) error {
		raw, err := tx.RawGet(metaBucket, []byte("server_secret"))
		if err != nil {
			return err
		}
		secret = raw
		return nil
	}); err != nil {
		return nil, err
	}
	return secret, nil
}

// NormalizeEmail lowercases and validates an email address.
func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || len(email) > 254 {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || strings.ToLower(addr.Address) != email {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// Authenticate verifies a user password and starts a session.
func (s *Service) Authenticate(collection, identity, password string) (*Session, error) {
	if identity == "" || password == "" {
		// Still spend some time hashing to reduce the timing difference.
		_, _ = VerifyPassword(s.dummyHash, password)
		return nil, ErrInvalidCredentials
	}
	var rec collections.Document
	var found bool
	if err := s.store.View(func(tx storage.ReadTx) error {
		var err error
		rec, found, err = findUser(tx, collection, identity)
		return err
	}); err != nil {
		if errors.Is(err, storage.ErrCollectionNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	hash := s.dummyHash
	if found {
		hash = rec.GetString(collections.FieldPasswordHash)
	}
	ok, err := VerifyPassword(hash, password)
	if err != nil || !found || !ok {
		return nil, ErrInvalidCredentials
	}
	return s.startSession("user", collection, rec.ID(), "")
}

// AuthenticateAdmin verifies a superuser password and starts a session.
func (s *Service) AuthenticateAdmin(email, password string) (*Session, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	var admin collections.Document
	var found bool
	if err := s.store.View(func(tx storage.ReadTx) error {
		var err error
		admin, found, err = findAdmin(tx, email)
		return err
	}); err != nil {
		return nil, err
	}
	hash := s.dummyHash
	if found {
		hash = admin.GetString(collections.FieldPasswordHash)
	}
	ok, err := VerifyPassword(hash, password)
	if err != nil || !found || !ok {
		return nil, ErrInvalidCredentials
	}
	return s.startSession("admin", "", admin.ID(), "")
}

// startSession reloads the subject inside a write transaction, stores a
// refresh token and issues an access token after the commit.
func (s *Service) startSession(kind, collection, id, family string) (*Session, error) {
	var sub subject
	var refreshRaw string
	var expiresAt time.Time
	err := s.store.Update(func(tx storage.WriteTx) error {
		var err error
		sub, err = loadSubject(tx, kind, collection, id)
		if err != nil {
			return err
		}
		refreshRaw, err = randomToken()
		if err != nil {
			return err
		}
		if family == "" {
			family, err = randomHex(16)
			if err != nil {
				return err
			}
		}
		expiresAt = s.now().Add(s.cfg.RefreshTTL)
		rec := refreshRecord{
			UserID:     sub.id,
			Collection: sub.collection,
			Kind:       sub.kind,
			Family:     family,
			ExpiresAt:  expiresAt,
			CreatedAt:  s.now(),
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return tx.RawPut(refreshBucket, []byte(hashToken(refreshRaw)), raw)
	})
	if err != nil {
		return nil, err
	}
	token, err := s.signAccess(sub)
	if err != nil {
		return nil, err
	}
	identity := identityFromSubject(sub)
	return &Session{
		Token:        token,
		RefreshToken: refreshRaw,
		ExpiresIn:    int64(s.cfg.AccessTTL.Seconds()),
		Identity:     identity,
		Record:       identity.Record,
	}, nil
}

// Refresh rotates a refresh token. Reuse of an already-used token revokes the
// whole token family. expectedCollection, when non-empty, restricts the
// operation to refresh tokens issued for that collection; the check happens
// before any state is mutated so a mismatched request cannot burn the token.
func (s *Service) Refresh(refreshToken, expectedCollection string) (*Session, error) {
	if refreshToken == "" {
		return nil, ErrUnauthorized
	}
	key := hashToken(refreshToken)
	var sub subject
	var newRaw string
	var reused, expired bool
	err := s.store.Update(func(tx storage.WriteTx) error {
		raw, err := tx.RawGet(refreshBucket, []byte(key))
		if err != nil {
			return err
		}
		if raw == nil {
			return ErrUnauthorized
		}
		var rec refreshRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return ErrUnauthorized
		}
		if expectedCollection != "" && rec.Collection != expectedCollection {
			return ErrUnauthorized
		}
		if rec.Used {
			reused = true
			return revokeFamily(tx, rec.Family)
		}
		if s.now().After(rec.ExpiresAt) {
			expired = true
			return tx.RawDelete(refreshBucket, []byte(key))
		}
		sub, err = loadSubject(tx, rec.Kind, rec.Collection, rec.UserID)
		if err != nil {
			return ErrUnauthorized
		}
		rec.Used = true
		updated, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if err := tx.RawPut(refreshBucket, []byte(key), updated); err != nil {
			return err
		}
		newRaw, err = randomToken()
		if err != nil {
			return err
		}
		next := refreshRecord{
			UserID:     rec.UserID,
			Collection: rec.Collection,
			Kind:       rec.Kind,
			Family:     rec.Family,
			ExpiresAt:  s.now().Add(s.cfg.RefreshTTL),
			CreatedAt:  s.now(),
		}
		nextRaw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		return tx.RawPut(refreshBucket, []byte(hashToken(newRaw)), nextRaw)
	})
	if err != nil {
		return nil, err
	}
	if reused {
		return nil, ErrRefreshReuse
	}
	if expired {
		return nil, ErrUnauthorized
	}
	token, err := s.signAccess(sub)
	if err != nil {
		return nil, err
	}
	identity := identityFromSubject(sub)
	return &Session{
		Token:        token,
		RefreshToken: newRaw,
		ExpiresIn:    int64(s.cfg.AccessTTL.Seconds()),
		Identity:     identity,
		Record:       identity.Record,
	}, nil
}

// Logout revokes the family associated with a refresh token. It is idempotent.
func (s *Service) Logout(refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	key := hashToken(refreshToken)
	return s.store.Update(func(tx storage.WriteTx) error {
		raw, err := tx.RawGet(refreshBucket, []byte(key))
		if err != nil || raw == nil {
			return err
		}
		var rec refreshRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil
		}
		return revokeFamily(tx, rec.Family)
	})
}

// RevokeAllSessions rotates the user's token key and deletes refresh tokens.
// Existing access tokens become invalid immediately.
func (s *Service) RevokeAllSessions(identity *Identity) error {
	_, err := s.RevokeAllSessionsWithCount(identity)
	return err
}

// RevokeAllSessionsWithCount behaves like RevokeAllSessions and also reports
// how many refresh records were deleted in the same transaction as the token
// key rotation.
func (s *Service) RevokeAllSessionsWithCount(identity *Identity) (int, error) {
	if identity == nil {
		return 0, ErrUnauthorized
	}
	revoked := 0
	err := s.store.Update(func(tx storage.WriteTx) error {
		sub, err := loadSubject(tx, identity.Kind, identity.Collection, identity.ID)
		if err != nil {
			return err
		}
		tokenKey, err := randomHex(32)
		if err != nil {
			return err
		}
		sub.record[collections.FieldTokenKey] = tokenKey
		if sub.kind == "admin" {
			raw, err := sub.record.Encode()
			if err != nil {
				return err
			}
			if err := tx.RawPut(adminsBucket, []byte(sub.id), raw); err != nil {
				return err
			}
		} else {
			if err := tx.PutDoc(sub.collection, sub.record); err != nil {
				return err
			}
		}
		revoked, err = revokeSubjectTokensCount(tx, sub.kind, sub.collection, sub.id)
		return err
	})
	return revoked, err
}

// Register creates a user in an auth collection and starts a session.
func (s *Service) Register(collection, email, password string, extra collections.Document) (*Session, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWeakPassword, err)
	}
	hash, err := HashPassword(password, s.cfg.Params)
	if err != nil {
		return nil, err
	}
	tokenKey, err := randomHex(32)
	if err != nil {
		return nil, err
	}

	doc := collections.Document{
		collections.FieldEmail:        email,
		collections.FieldPasswordHash: hash,
		collections.FieldTokenKey:     tokenKey,
		collections.FieldVerified:     false,
	}
	for k, v := range extra {
		switch k {
		case collections.FieldID, collections.FieldEmail, collections.FieldPasswordHash,
			collections.FieldTokenKey, collections.FieldVerified,
			collections.FieldCreated, collections.FieldUpdated:
			continue
		}
		doc[k] = v
	}

	var sub subject
	var refreshRaw string
	err = s.store.Update(func(tx storage.WriteTx) error {
		def, err := tx.Collection(collection)
		if err != nil {
			return err
		}
		if def.Type != collections.TypeAuth {
			return ErrNotAuthCollection
		}
		existing, found, err := findUserByEmail(tx, collection, email)
		if err != nil {
			return err
		}
		_ = existing
		if found {
			return ErrEmailTaken
		}
		if err := tx.PutDoc(collection, doc); err != nil {
			if errors.Is(err, storage.ErrConflict) {
				return ErrEmailTaken
			}
			return err
		}
		stored, ok, err := tx.GetDoc(collection, doc.ID())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("auth: new user disappeared during write")
		}
		sub, err = loadSubjectFromRecord("user", collection, stored)
		if err != nil {
			return err
		}
		refreshRaw, err = randomToken()
		if err != nil {
			return err
		}
		family, err := randomHex(16)
		if err != nil {
			return err
		}
		rec := refreshRecord{
			UserID:     sub.id,
			Collection: collection,
			Kind:       "user",
			Family:     family,
			ExpiresAt:  s.now().Add(s.cfg.RefreshTTL),
			CreatedAt:  s.now(),
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return tx.RawPut(refreshBucket, []byte(hashToken(refreshRaw)), raw)
	})
	if err != nil {
		return nil, err
	}
	token, err := s.signAccess(sub)
	if err != nil {
		return nil, err
	}
	identity := identityFromSubject(sub)
	return &Session{
		Token:        token,
		RefreshToken: refreshRaw,
		ExpiresIn:    int64(s.cfg.AccessTTL.Seconds()),
		Identity:     identity,
		Record:       identity.Record,
	}, nil
}

// CreateAdmin creates a superuser. It is used by the CLI and initial setup.
// ChangePassword updates the password hash of a user record. When
// currentPassword is not empty it must match the stored hash; superusers may
// change any password without providing the current one. The token key is
// rotated so previously issued sessions stop validating.
func (s *Service) ChangePassword(collection, id, currentPassword, newPassword string) error {
	if err := ValidatePassword(newPassword); err != nil {
		return fmt.Errorf("%w: %v", ErrWeakPassword, err)
	}
	hash, err := HashPassword(newPassword, s.cfg.Params)
	if err != nil {
		return err
	}
	tokenKey, err := randomHex(32)
	if err != nil {
		return err
	}
	return s.store.Update(func(tx storage.WriteTx) error {
		def, err := tx.Collection(collection)
		if err != nil {
			return err
		}
		if def.Type != collections.TypeAuth {
			return ErrNotAuthCollection
		}
		doc, ok, err := tx.GetDoc(collection, id)
		if err != nil {
			return err
		}
		if !ok {
			return storage.ErrNotFound
		}
		if currentPassword != "" {
			ok, err := VerifyPassword(doc.GetString(collections.FieldPasswordHash), currentPassword)
			if err != nil {
				return err
			}
			if !ok {
				return ErrInvalidCredentials
			}
		}
		doc[collections.FieldPasswordHash] = hash
		doc[collections.FieldTokenKey] = tokenKey
		return tx.PutDoc(collection, doc)
	})
}

func (s *Service) CreateAdmin(email, password string) (*Identity, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWeakPassword, err)
	}
	hash, err := HashPassword(password, s.cfg.Params)
	if err != nil {
		return nil, err
	}
	tokenKey, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	doc := collections.Document{
		collections.FieldID:           collections.NewID(),
		collections.FieldEmail:        email,
		collections.FieldPasswordHash: hash,
		collections.FieldTokenKey:     tokenKey,
		collections.FieldCreated:      collections.Now(),
		collections.FieldUpdated:      collections.Now(),
	}
	if err := s.store.Update(func(tx storage.WriteTx) error {
		if _, found, err := findAdmin(tx, email); err != nil {
			return err
		} else if found {
			return ErrEmailTaken
		}
		raw, err := doc.Encode()
		if err != nil {
			return err
		}
		return tx.RawPut(adminsBucket, []byte(doc.ID()), raw)
	}); err != nil {
		return nil, err
	}
	return &Identity{Kind: "admin", ID: doc.ID(), Email: email, Record: doc.SanitizeShallow()}, nil
}

// CreateFirstAdmin creates a superuser only when none exists. The existence
// check runs inside the same write transaction as the insert so two concurrent
// callers (or a CLI invocation racing the HTTP setup endpoint) cannot both
// succeed.
func (s *Service) CreateFirstAdmin(email, password string) (*Identity, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWeakPassword, err)
	}
	hash, err := HashPassword(password, s.cfg.Params)
	if err != nil {
		return nil, err
	}
	tokenKey, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	doc := collections.Document{
		collections.FieldID:           collections.NewID(),
		collections.FieldEmail:        email,
		collections.FieldPasswordHash: hash,
		collections.FieldTokenKey:     tokenKey,
		collections.FieldCreated:      collections.Now(),
		collections.FieldUpdated:      collections.Now(),
	}
	if err := s.store.Update(func(tx storage.WriteTx) error {
		entries, err := tx.RawScan(adminsBucket, nil)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return ErrSetupComplete
		}
		raw, err := doc.Encode()
		if err != nil {
			return err
		}
		return tx.RawPut(adminsBucket, []byte(doc.ID()), raw)
	}); err != nil {
		return nil, err
	}
	return &Identity{Kind: "admin", ID: doc.ID(), Email: email, Record: doc.SanitizeShallow()}, nil
}

// AdminCount returns the number of superusers.
func (s *Service) AdminCount() (int, error) {
	var count int
	err := s.store.View(func(tx storage.ReadTx) error {
		entries, err := tx.RawScan(adminsBucket, nil)
		count = len(entries)
		return err
	})
	return count, err
}

// ListAdmins returns superusers without password hashes or token keys.
func (s *Service) ListAdmins() ([]AdminInfo, error) {
	var admins []AdminInfo
	err := s.store.View(func(tx storage.ReadTx) error {
		entries, err := tx.RawScan(adminsBucket, nil)
		if err != nil {
			return err
		}
		admins = make([]AdminInfo, 0, len(entries))
		for _, e := range entries {
			doc, err := collections.Decode(e.Value)
			if err != nil {
				continue
			}
			admins = append(admins, AdminInfo{
				ID:      doc.ID(),
				Email:   doc.GetString(collections.FieldEmail),
				Created: doc.GetString(collections.FieldCreated),
				Updated: doc.GetString(collections.FieldUpdated),
			})
		}
		return nil
	})
	sort.Slice(admins, func(i, j int) bool { return admins[i].Created < admins[j].Created })
	return admins, err
}

// DeleteAdmin removes a superuser and its sessions. It refuses to remove the
// last superuser or the principal making the request.
func (s *Service) DeleteAdmin(id, currentID string) error {
	return s.store.Update(func(tx storage.WriteTx) error {
		entries, err := tx.RawScan(adminsBucket, nil)
		if err != nil {
			return err
		}
		found := false
		for _, e := range entries {
			doc, err := collections.Decode(e.Value)
			if err == nil && doc.ID() == id {
				found = true
				break
			}
		}
		if !found {
			return ErrAdminNotFound
		}
		if len(entries) <= 1 {
			return ErrAdminLast
		}
		if currentID != "" && currentID == id {
			return ErrAdminSelf
		}
		if err := tx.RawDelete(adminsBucket, []byte(id)); err != nil {
			return err
		}
		return revokeSubjectTokens(tx, "admin", "", id)
	})
}

// ListSessions returns sanitized refresh sessions, newest first. The optional
// kind filters by "admin" or "user"; limit <= 0 means no application-side cap.
func (s *Service) ListSessions(kind string, limit int) ([]SessionInfo, error) {
	var sessions []SessionInfo
	err := s.store.View(func(tx storage.ReadTx) error {
		entries, err := tx.RawScan(refreshBucket, nil)
		if err != nil {
			return err
		}
		sessions = make([]SessionInfo, 0, len(entries))
		for _, e := range entries {
			var rec refreshRecord
			if err := json.Unmarshal(e.Value, &rec); err != nil {
				continue
			}
			if kind != "" && rec.Kind != kind {
				continue
			}
			tokenID := string(e.Key)
			if len(tokenID) > 12 {
				tokenID = tokenID[:12]
			}
			sessions = append(sessions, SessionInfo{
				TokenID:    tokenID,
				Kind:       rec.Kind,
				UserID:     rec.UserID,
				Collection: rec.Collection,
				Family:     rec.Family,
				CreatedAt:  rec.CreatedAt.UTC().Format(time.RFC3339),
				ExpiresAt:  rec.ExpiresAt.UTC().Format(time.RFC3339),
				Used:       rec.Used,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].CreatedAt == sessions[j].CreatedAt {
			return sessions[i].TokenID < sessions[j].TokenID
		}
		return sessions[i].CreatedAt > sessions[j].CreatedAt
	})
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

// RevokeFamily deletes every refresh record in a family in one transaction.
func (s *Service) RevokeFamily(family string) (int, error) {
	if family == "" {
		return 0, nil
	}
	revoked := 0
	err := s.store.Update(func(tx storage.WriteTx) error {
		entries, err := tx.RawScan(refreshBucket, nil)
		if err != nil {
			return err
		}
		for _, e := range entries {
			var rec refreshRecord
			if err := json.Unmarshal(e.Value, &rec); err != nil || rec.Family != family {
				continue
			}
			if err := tx.RawDelete(refreshBucket, e.Key); err != nil {
				return err
			}
			revoked++
		}
		return nil
	})
	return revoked, err
}

// RevokeAllRefreshTokens deletes every refresh record in one transaction.
func (s *Service) RevokeAllRefreshTokens() (int, error) {
	revoked := 0
	err := s.store.Update(func(tx storage.WriteTx) error {
		entries, err := tx.RawScan(refreshBucket, nil)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := tx.RawDelete(refreshBucket, e.Key); err != nil {
				return err
			}
			revoked++
		}
		return nil
	})
	return revoked, err
}

// Identify validates an access token and loads the current principal.
func (s *Service) Identify(token string) (*Identity, error) {
	if token == "" {
		return nil, ErrUnauthorized
	}
	unverified := &Claims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, unverified); err != nil {
		return nil, ErrUnauthorized
	}
	if unverified.Kind != "user" && unverified.Kind != "admin" {
		return nil, ErrUnauthorized
	}
	var sub subject
	if err := s.store.View(func(tx storage.ReadTx) error {
		var err error
		sub, err = loadSubject(tx, unverified.Kind, unverified.Collection, unverified.Subject)
		return err
	}); err != nil {
		return nil, ErrUnauthorized
	}
	key := deriveAccessKey(s.secret, sub.kind, sub.collection, sub.tokenKey)
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, ErrUnauthorized
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(s.cfg.Issuer), jwt.WithExpirationRequired(), jwt.WithTimeFunc(s.now))
	if err != nil || !parsed.Valid {
		return nil, ErrUnauthorized
	}
	if claims.Subject != sub.id || claims.Kind != sub.kind || claims.Collection != sub.collection {
		return nil, ErrUnauthorized
	}
	return identityFromSubject(sub), nil
}

func (s *Service) signAccess(sub subject) (string, error) {
	now := s.now()
	jti, err := randomHex(8)
	if err != nil {
		return "", err
	}
	claims := Claims{
		Kind:       sub.kind,
		Collection: sub.collection,
		Email:      sub.email,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Issuer:    s.cfg.Issuer,
			Subject:   sub.id,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.AccessTTL)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	key := deriveAccessKey(s.secret, sub.kind, sub.collection, sub.tokenKey)
	return tok.SignedString(key)
}

func deriveAccessKey(secret []byte, kind, collection, tokenKey string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("doketfire/access/v1\x00"))
	mac.Write([]byte(kind))
	mac.Write([]byte{0})
	mac.Write([]byte(collection))
	mac.Write([]byte{0})
	mac.Write([]byte(tokenKey))
	return mac.Sum(nil)
}

func identityFromSubject(sub subject) *Identity {
	return &Identity{
		Kind:       sub.kind,
		ID:         sub.id,
		Collection: sub.collection,
		Email:      sub.email,
		Record:     sub.record.SanitizeShallow(),
	}
}

func loadSubject(tx storage.ReadTx, kind, collection, id string) (subject, error) {
	switch kind {
	case "admin":
		raw, err := tx.RawGet(adminsBucket, []byte(id))
		if err != nil {
			return subject{}, err
		}
		if raw == nil {
			return subject{}, ErrUnauthorized
		}
		doc, err := collections.Decode(raw)
		if err != nil {
			return subject{}, ErrUnauthorized
		}
		return loadSubjectFromRecord("admin", "", doc)
	case "user":
		doc, ok, err := tx.GetDoc(collection, id)
		if err != nil {
			return subject{}, ErrUnauthorized
		}
		if !ok {
			return subject{}, ErrUnauthorized
		}
		return loadSubjectFromRecord("user", collection, doc)
	default:
		return subject{}, ErrUnauthorized
	}
}

func loadSubjectFromRecord(kind, collection string, doc collections.Document) (subject, error) {
	tokenKey := doc.GetString(collections.FieldTokenKey)
	if tokenKey == "" {
		return subject{}, ErrUnauthorized
	}
	return subject{
		kind:       kind,
		collection: collection,
		id:         doc.ID(),
		email:      doc.GetString(collections.FieldEmail),
		tokenKey:   tokenKey,
		record:     doc,
	}, nil
}

func findUser(tx storage.ReadTx, collection, identity string) (collections.Document, bool, error) {
	def, err := tx.Collection(collection)
	if err != nil {
		return nil, false, err
	}
	if def.Type != collections.TypeAuth {
		return nil, false, ErrNotAuthCollection
	}
	if collections.ValidID(identity) {
		if doc, ok, err := tx.GetDoc(collection, identity); err != nil {
			return nil, false, err
		} else if ok {
			return doc, true, nil
		}
	}
	email, err := NormalizeEmail(identity)
	if err != nil {
		return nil, false, nil
	}
	return findUserByEmail(tx, collection, email)
}

func findUserByEmail(tx storage.ReadTx, collection, email string) (collections.Document, bool, error) {
	def, err := tx.Collection(collection)
	if err != nil {
		return nil, false, err
	}
	if idx, ok := def.IndexForField(collections.FieldEmail, true); ok {
		enc, err := codec.EncodeIndexValue(email)
		if err != nil {
			return nil, false, err
		}
		start, end := codec.PrefixRange(enc)
		it := tx.ScanIndex(collection, idx.Name, storage.KeyRange{Start: start, End: end}, storage.ScanOpts{})
		defer it.Close()
		for it.Next() {
			doc := it.Doc()
			if strings.EqualFold(doc.GetString(collections.FieldEmail), email) {
				return doc, true, nil
			}
		}
		if err := it.Err(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	it := tx.ScanDocs(collection, storage.ScanOpts{})
	defer it.Close()
	for it.Next() {
		doc := it.Doc()
		if strings.EqualFold(doc.GetString(collections.FieldEmail), email) {
			return doc, true, nil
		}
	}
	if err := it.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

func findAdmin(tx storage.ReadTx, email string) (collections.Document, bool, error) {
	entries, err := tx.RawScan(adminsBucket, nil)
	if err != nil {
		return nil, false, err
	}
	for _, e := range entries {
		doc, err := collections.Decode(e.Value)
		if err != nil {
			continue
		}
		if strings.EqualFold(doc.GetString(collections.FieldEmail), email) {
			return doc, true, nil
		}
	}
	return nil, false, nil
}

func revokeFamily(tx storage.WriteTx, family string) error {
	entries, err := tx.RawScan(refreshBucket, nil)
	if err != nil {
		return err
	}
	for _, e := range entries {
		var rec refreshRecord
		if err := json.Unmarshal(e.Value, &rec); err != nil {
			continue
		}
		if rec.Family == family {
			if err := tx.RawDelete(refreshBucket, e.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

func revokeSubjectTokens(tx storage.WriteTx, kind, collection, id string) error {
	_, err := revokeSubjectTokensCount(tx, kind, collection, id)
	return err
}

func revokeSubjectTokensCount(tx storage.WriteTx, kind, collection, id string) (int, error) {
	entries, err := tx.RawScan(refreshBucket, nil)
	if err != nil {
		return 0, err
	}
	revoked := 0
	for _, e := range entries {
		var rec refreshRecord
		if err := json.Unmarshal(e.Value, &rec); err != nil {
			continue
		}
		if rec.Kind == kind && rec.Collection == collection && rec.UserID == id {
			if err := tx.RawDelete(refreshBucket, e.Key); err != nil {
				return revoked, err
			}
			revoked++
		}
	}
	return revoked, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

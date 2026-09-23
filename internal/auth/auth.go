// Package auth handles console users: passwords, sessions, API tokens,
// TOTP two-factor authentication and login throttling.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookie = "nh_session"
	SessionTTL    = 12 * time.Hour
	bcryptCost    = 12
)

var (
	ErrInvalid      = errors.New("invalid username or password")
	ErrTOTPRequired = errors.New("two-factor code required")
	ErrTOTPInvalid  = errors.New("invalid two-factor code")
	ErrLocked       = errors.New("too many failed attempts; try again in a few minutes")
	ErrDisabled     = errors.New("this account is disabled")
	ErrTOTPBroken   = errors.New("the two-factor secret cannot be read on this server; reset the account with `nodehoster reset-password`")
)

// totpValid checks a code against a sealed secret, failing closed when the
// secret cannot be decrypted: an empty key would make codes predictable. A
// code is accepted in the previous, current or next 30-second step, and at
// most once: a step at or before the user's last accepted one is refused
// (RFC 6238 section 5.2), so an observed code cannot be replayed.
func (s *Service) totpValid(userID, sealed, code string) (bool, error) {
	secret, err := s.box.Unseal(sealed)
	if err != nil || secret == "" {
		return false, ErrTOTPBroken
	}
	code = strings.ReplaceAll(code, " ", "")
	now := time.Now()
	for _, skew := range []time.Duration{-1, 0, 1} {
		at := now.Add(skew * totpPeriod)
		want, err := totp.GenerateCode(secret, at)
		if err != nil {
			return false, nil
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) != 1 {
			continue
		}
		step := at.Unix() / int64(totpPeriod/time.Second)
		s.mu.Lock()
		defer s.mu.Unlock()
		if step <= s.totpLastStep[userID] {
			return false, nil
		}
		s.totpLastStep[userID] = step
		return true, nil
	}
	return false, nil
}

const totpPeriod = 30 * time.Second

type Service struct {
	store *store.Store
	box   *secrets.Box

	mu           sync.Mutex
	failures     map[string][]time.Time // ip|username -> recent failures
	totpLastStep map[string]int64       // user ID -> last accepted TOTP time step
}

func New(st *store.Store, box *secrets.Box) *Service {
	return &Service{store: st, box: box, failures: map[string][]time.Time{}, totpLastStep: map[string]int64{}}
}

func HashPassword(pw string) (string, error) {
	if err := CheckPasswordPolicy(pw); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(h), err
}

func CheckPasswordPolicy(pw string) error {
	if utf8.RuneCountInString(pw) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	if len(pw) > 72 {
		return errors.New("password must be at most 72 bytes")
	}
	return nil
}

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// EnsureAdmin creates the first administrator when there are no users and
// returns its generated password ("" if users already exist).
func (s *Service) EnsureAdmin(ctx context.Context) (string, error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil || n > 0 {
		return "", err
	}
	pw := randomString(15)
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	u := &store.UserRecord{User: model.User{ID: uuid.NewString(), Username: "admin", Role: model.RoleAdmin, PasswordHash: string(hash), CreatedAt: time.Now()}, MustChange: true}
	return pw, s.store.PutUser(ctx, u)
}

// ResetPassword sets a new random password for a user (CLI recovery).
func (s *Service) ResetPassword(ctx context.Context, username string) (string, error) {
	u, err := s.store.GetUserByName(ctx, username)
	if err != nil {
		return "", fmt.Errorf("user %q not found", username)
	}
	pw := randomString(15)
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	u.PasswordHash, u.MustChange, u.Disabled = string(hash), true, false
	u.TOTPEnabled, u.TOTPSecret = false, ""
	if err := s.store.PutUser(ctx, u); err != nil {
		return "", err
	}
	s.store.DeleteUserSessions(ctx, u.ID, "")
	return pw, nil
}

func (s *Service) throttled(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-15 * time.Minute)
	list := s.failures[key][:0]
	for _, t := range s.failures[key] {
		if t.After(cutoff) {
			list = append(list, t)
		}
	}
	s.failures[key] = list
	return len(list) >= 5
}

func (s *Service) fail(key string) {
	s.mu.Lock()
	s.failures[key] = append(s.failures[key], time.Now())
	s.mu.Unlock()
}

// Login checks credentials and creates a session. It returns the raw
// session token for the cookie.
func (s *Service) Login(ctx context.Context, username, password, code, ip, ua string) (*store.UserRecord, string, error) {
	key := ip + "|" + strings.ToLower(username)
	if s.throttled(key) {
		return nil, "", ErrLocked
	}
	u, err := s.store.GetUserByName(ctx, username)
	if err != nil {
		bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		s.fail(key)
		return nil, "", ErrInvalid
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		s.fail(key)
		return nil, "", ErrInvalid
	}
	if u.Disabled {
		return nil, "", ErrDisabled
	}
	if u.TOTPEnabled {
		if code == "" {
			return nil, "", ErrTOTPRequired
		}
		ok, err := s.totpValid(u.ID, u.TOTPSecret, code)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			s.fail(key)
			return nil, "", ErrTOTPInvalid
		}
	}
	s.mu.Lock()
	delete(s.failures, key)
	s.mu.Unlock()

	token := randomString(32)
	if err := s.store.CreateSession(ctx, sha(token), u.ID, ip, ua, time.Now().Add(SessionTTL)); err != nil {
		return nil, "", err
	}
	now := time.Now()
	u.LastLogin = &now
	s.store.PutUser(ctx, u)
	return u, token, nil
}

var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("nodehoster"), bcryptCost)
	return h
})

// Session resolves a session cookie, sliding its expiry forward.
func (s *Service) Session(ctx context.Context, token string) (*store.UserRecord, error) {
	if token == "" {
		return nil, store.ErrNotFound
	}
	h := sha(token)
	u, exp, err := s.store.SessionUser(ctx, h)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, ErrDisabled
	}
	if time.Until(exp) < SessionTTL/2 {
		s.store.ExtendSession(ctx, h, time.Now().Add(SessionTTL))
	}
	return u, nil
}

func (s *Service) Logout(ctx context.Context, token string) {
	s.store.DeleteSession(ctx, sha(token))
}

// ChangePassword verifies the current password and ends other sessions.
func (s *Service) ChangePassword(ctx context.Context, u *store.UserRecord, current, next, keepToken string) error {
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)) != nil {
		return errors.New("current password is incorrect")
	}
	if current == next {
		return errors.New("choose a password different from the current one")
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	u.PasswordHash, u.MustChange = hash, false
	if err := s.store.PutUser(ctx, u); err != nil {
		return err
	}
	return s.store.DeleteUserSessions(ctx, u.ID, sha(keepToken))
}

// ---- TOTP

func (s *Service) TOTPSetup(ctx context.Context, u *store.UserRecord) (secret, url string, err error) {
	// Starting over would replace (and so switch off) an active second
	// factor without proving possession of it.
	if u.TOTPEnabled {
		return "", "", errors.New("two-factor authentication is already on; turn it off with a current code first")
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "NodeHoster", AccountName: u.Username})
	if err != nil {
		return "", "", err
	}
	sealed, err := s.box.Seal(key.Secret())
	if err != nil {
		return "", "", err
	}
	u.TOTPSecret, u.TOTPEnabled = sealed, false
	if err := s.store.PutUser(ctx, u); err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

func (s *Service) TOTPEnable(ctx context.Context, u *store.UserRecord, code string) error {
	if u.TOTPSecret == "" {
		return errors.New("start two-factor setup first")
	}
	if ok, err := s.totpValid(u.ID, u.TOTPSecret, code); err != nil {
		return err
	} else if !ok {
		return ErrTOTPInvalid
	}
	u.TOTPEnabled = true
	return s.store.PutUser(ctx, u)
}

func (s *Service) TOTPDisable(ctx context.Context, u *store.UserRecord, code string) error {
	if !u.TOTPEnabled {
		return nil
	}
	if ok, err := s.totpValid(u.ID, u.TOTPSecret, code); err != nil {
		return err
	} else if !ok {
		return ErrTOTPInvalid
	}
	u.TOTPEnabled, u.TOTPSecret = false, ""
	return s.store.PutUser(ctx, u)
}

// ---- API tokens

func (s *Service) CreateToken(ctx context.Context, userID, name string, expiresDays int) (string, *model.APIToken, error) {
	return s.CreateRestrictedToken(ctx, userID, name, expiresDays, "", nil)
}

// CreateRestrictedToken creates a token limited to a maximum role and/or
// to some sites (nil: every site the owner can access). The caller checks
// that the restriction is within the owner's access; enforcement always
// intersects it with the owner's access at the time of each request.
func (s *Service) CreateRestrictedToken(ctx context.Context, userID, name string, expiresDays int, role model.Role, siteIDs []string) (string, *model.APIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, errors.New("give the token a name")
	}
	raw := "nh_" + randomString(32)
	t := &model.APIToken{ID: uuid.NewString(), UserID: userID, Name: name, Prefix: raw[:10], Hash: sha(raw), CreatedAt: time.Now(), Role: role, SiteIDs: siteIDs}
	if expiresDays > 0 {
		exp := time.Now().AddDate(0, 0, expiresDays)
		t.ExpiresAt = &exp
	}
	return raw, t, s.store.CreateToken(ctx, t)
}

// TokenUser resolves a bearer token.
func (s *Service) TokenUser(ctx context.Context, raw string) (*store.UserRecord, error) {
	u, _, err := s.Token(ctx, raw)
	return u, err
}

// Token resolves a bearer token to its owner and the token itself, whose
// restriction applies on top of the owner's access (Access.Restrict).
func (s *Service) Token(ctx context.Context, raw string) (*store.UserRecord, *model.APIToken, error) {
	if !strings.HasPrefix(raw, "nh_") {
		return nil, nil, store.ErrNotFound
	}
	t, err := s.store.TokenByHash(ctx, sha(raw))
	if err != nil {
		return nil, nil, err
	}
	if t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt) {
		return nil, nil, errors.New("token expired")
	}
	u, err := s.store.GetUser(ctx, t.UserID)
	if err != nil {
		return nil, nil, err
	}
	if u.Disabled {
		return nil, nil, ErrDisabled
	}
	go s.store.TouchToken(context.Background(), t.ID)
	return u, t, nil
}

// Allowed reports whether a server-wide role may perform an action class.
// The site-scoped role "sites" is allowed nothing: its rights are in the
// grants (see Access).
func Allowed(role model.Role, need model.Role) bool {
	return rank(need) > 0 && rank(role) >= rank(need)
}

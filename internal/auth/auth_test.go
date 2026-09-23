package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const testPassword = "correct horse battery"

type env struct {
	svc *Service
	st  *store.Store
	box *secrets.Box
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "nodehoster.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Registered after TempDir, so it runs first: the DB file must be
	// closed before the directory can be removed on Windows.
	t.Cleanup(func() { st.Close() })
	box, err := secrets.Open(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: New(st, box), st: st, box: box}
}

// addUser stores a user whose password hash uses bcrypt.MinCost so tests
// stay fast; CompareHashAndPassword honours the cost embedded in the hash.
func (e *env) addUser(t *testing.T, name string, mods ...func(*store.UserRecord)) *store.UserRecord {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := &store.UserRecord{User: model.User{ID: "id-" + name, Username: name, Role: model.RoleOperator, PasswordHash: string(h), CreatedAt: time.Now()}}
	for _, m := range mods {
		m(u)
	}
	if err := e.st.PutUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

// enableTOTP runs the real setup/enable flow and returns the plain secret.
func (e *env) enableTOTP(t *testing.T, u *store.UserRecord) string {
	t.Helper()
	ctx := context.Background()
	secret, _, err := e.svc.TOTPSetup(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	// Enable with the previous step's code (still accepted): codes can be
	// used only once, and the tests log in with the current and next ones.
	if err := e.svc.TOTPEnable(ctx, u, code(t, secret, time.Now().Add(-30*time.Second))); err != nil {
		t.Fatal(err)
	}
	return secret
}

func (e *env) reload(t *testing.T, id string) *store.UserRecord {
	t.Helper()
	u, err := e.st.GetUser(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func code(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	c, err := totp.GenerateCode(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// wrongCode returns a well-formed code that is not valid at any time step
// accepted by the validator (current +/- one period).
func wrongCode(t *testing.T, secret string) string {
	t.Helper()
	now := time.Now()
	valid := map[string]bool{}
	for _, d := range []time.Duration{-60 * time.Second, -30 * time.Second, 0, 30 * time.Second, 60 * time.Second} {
		valid[code(t, secret, now.Add(d))] = true
	}
	for i := 0; ; i++ {
		c := fmt.Sprintf("%06d", i)
		if !valid[c] {
			return c
		}
	}
}

// ---- password policy & hashing

func TestCheckPasswordPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		pw   string
		ok   bool
	}{
		{"empty", "", false},
		{"9 chars", "123456789", false},
		{"10 chars", "1234567890", true},
		{"72 bytes", strings.Repeat("a", 72), true},
		{"73 bytes", strings.Repeat("a", 73), false},
		// Length is counted in characters, the upper bound in bytes (bcrypt).
		{"10 multibyte runes", strings.Repeat("é", 10), true},
		{"9 four-byte runes", strings.Repeat("🔑", 9), false},
		{"24 three-byte runes (72 bytes)", strings.Repeat("密", 24), true},
		{"25 three-byte runes (75 bytes)", strings.Repeat("密", 25), false},
	}
	for _, c := range cases {
		err := CheckPasswordPolicy(c.pw)
		if (err == nil) != c.ok {
			t.Errorf("%s: CheckPasswordPolicy err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestHashPassword(t *testing.T) {
	t.Parallel()
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("HashPassword accepted a password violating the policy")
	}
	if _, err := HashPassword(strings.Repeat("x", 73)); err == nil {
		t.Fatal("HashPassword accepted a >72 byte password (bcrypt would truncate/reject it)")
	}

	h, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if h == testPassword || strings.Contains(h, testPassword) {
		t.Fatal("hash contains the plaintext")
	}
	cost, err := bcrypt.Cost([]byte(h))
	if err != nil {
		t.Fatalf("not a bcrypt hash: %v", err)
	}
	if cost != bcryptCost {
		t.Fatalf("bcrypt cost = %d, want %d", cost, bcryptCost)
	}
	if bcrypt.CompareHashAndPassword([]byte(h), []byte(testPassword)) != nil {
		t.Fatal("hash does not verify the password")
	}
}

// The unknown-user path compares against a dummy hash to equalise timing;
// that hash must be real bcrypt at production cost or the timing leaks.
func TestDummyHashMatchesProductionCost(t *testing.T) {
	t.Parallel()
	h := dummyHash()
	cost, err := bcrypt.Cost(h)
	if err != nil {
		t.Fatalf("dummy hash invalid: %v", err)
	}
	if cost != bcryptCost {
		t.Fatalf("dummy hash cost = %d, want %d", cost, bcryptCost)
	}
}

func TestShaAndRandomString(t *testing.T) {
	t.Parallel()
	if got, want := sha("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; got != want {
		t.Fatalf("sha(abc) = %s, want %s", got, want)
	}
	seen := map[string]bool{}
	for range 100 {
		s := randomString(32)
		if len(s) != 43 {
			t.Fatalf("randomString(32) length = %d, want 43", len(s))
		}
		if strings.ContainsAny(s, "+/=") {
			t.Fatalf("randomString not URL-safe: %q", s)
		}
		if seen[s] {
			t.Fatal("randomString repeated a value")
		}
		seen[s] = true
	}
}

// ---- login

func TestLoginSuccessCreatesHashedSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")

	before := time.Now().Add(-time.Second)
	u, token, err := e.svc.Login(ctx, "alice", testPassword, "", "10.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if u.ID != "id-alice" {
		t.Fatalf("Login user = %+v", u)
	}
	if len(token) < 40 {
		t.Fatalf("session token too short: %d chars", len(token))
	}

	// Only the SHA-256 of the token is stored.
	if _, _, err := e.st.SessionUser(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("raw token found in session table: %v", err)
	}
	su, exp, err := e.st.SessionUser(ctx, sha(token))
	if err != nil || su.ID != u.ID {
		t.Fatalf("hashed session lookup = %v, %v", su, err)
	}
	if d := time.Until(exp); d < SessionTTL-time.Minute || d > SessionTTL+time.Minute {
		t.Fatalf("session expires in %v, want ~%v", d, SessionTTL)
	}

	stored := e.reload(t, u.ID)
	if stored.LastLogin == nil || stored.LastLogin.Before(before) {
		t.Fatalf("LastLogin not recorded: %v", stored.LastLogin)
	}

	got, err := e.svc.Session(ctx, token)
	if err != nil || got.ID != u.ID {
		t.Fatalf("Session(token) = %v, %v", got, err)
	}

	_, token2, err := e.svc.Login(ctx, "alice", testPassword, "", "10.0.0.1", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if token2 == token {
		t.Fatal("two logins returned the same session token")
	}
}

func TestLoginUsernameCaseInsensitive(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.addUser(t, "alice")
	if _, _, err := e.svc.Login(context.Background(), "ALICE", testPassword, "", "ip", ""); err != nil {
		t.Fatalf("Login with different case: %v", err)
	}
}

func TestLoginFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")
	e.addUser(t, "disabled", func(u *store.UserRecord) { u.Disabled = true })

	cases := []struct {
		name, user, pw string
		want           error
	}{
		{"wrong password", "alice", testPassword + "!", ErrInvalid},
		{"empty password", "alice", "", ErrInvalid},
		{"password prefix", "alice", testPassword[:len(testPassword)-1], ErrInvalid},
		// Same error as a wrong password: no username enumeration.
		{"unknown user", "mallory", testPassword, ErrInvalid},
		// A disabled account only reveals itself to the right password.
		{"disabled, wrong password", "disabled", "nope-nope-nope", ErrInvalid},
		{"disabled, right password", "disabled", testPassword, ErrDisabled},
	}
	for i, c := range cases {
		// Distinct IPs so the throttle does not interfere.
		u, tok, err := e.svc.Login(ctx, c.user, c.pw, "", fmt.Sprintf("ip%d", i), "")
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if u != nil || tok != "" {
			t.Errorf("%s: failed login returned user=%v token=%q", c.name, u, tok)
		}
	}
	for _, id := range []string{"id-alice", "id-disabled"} {
		if u := e.reload(t, id); u.LastLogin != nil {
			t.Errorf("failed login recorded LastLogin for %s", id)
		}
	}
}

func TestLoginTOTP(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	u := e.addUser(t, "alice")
	secret := e.enableTOTP(t, u)

	if _, tok, err := e.svc.Login(ctx, "alice", testPassword, "", "ip1", ""); !errors.Is(err, ErrTOTPRequired) || tok != "" {
		t.Fatalf("no code: err = %v tok=%q, want ErrTOTPRequired", err, tok)
	}
	if _, tok, err := e.svc.Login(ctx, "alice", testPassword, wrongCode(t, secret), "ip2", ""); !errors.Is(err, ErrTOTPInvalid) || tok != "" {
		t.Fatalf("wrong code: err = %v, want ErrTOTPInvalid", err)
	}
	stale := code(t, secret, time.Now().Add(-10*time.Minute))
	if stale != code(t, secret, time.Now()) {
		if _, _, err := e.svc.Login(ctx, "alice", testPassword, stale, "ip3", ""); !errors.Is(err, ErrTOTPInvalid) {
			t.Fatalf("10-minute-old code: err = %v, want ErrTOTPInvalid", err)
		}
	}
	// Correct code but wrong password: the password check comes first.
	if _, _, err := e.svc.Login(ctx, "alice", "wrong-password!", code(t, secret, time.Now()), "ip4", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong password + valid code: err = %v, want ErrInvalid", err)
	}
	if _, tok, err := e.svc.Login(ctx, "alice", testPassword, code(t, secret, time.Now()), "ip5", ""); err != nil || tok == "" {
		t.Fatalf("valid code: err = %v", err)
	}
	// Users often type the code with a space in the middle. Use the next
	// time step (accepted via skew) so the code differs from the one above.
	c := code(t, secret, time.Now().Add(30*time.Second))
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, c[:3]+" "+c[3:], "ip6", ""); err != nil {
		t.Fatalf("code with space: %v", err)
	}
}

// RFC 6238 section 5.2: a code must not be accepted a second time after a
// successful validation.
func TestLoginTOTPReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	u := e.addUser(t, "alice")
	secret := e.enableTOTP(t, u)

	c := code(t, secret, time.Now())
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, c, "ip", ""); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, c, "ip", ""); err == nil {
		t.Fatal("the same TOTP code was accepted twice")
	}
}

func TestLoginTOTPFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)

	// Secret sealed under a different master key (e.g. DB copied to another
	// machine): login must be refused, not treated as "no second factor".
	other, err := secrets.Open(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.Seal("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	e.addUser(t, "foreign", func(u *store.UserRecord) { u.TOTPEnabled, u.TOTPSecret = true, foreign })
	// Enabled with no secret at all: an empty key would make codes predictable.
	e.addUser(t, "empty", func(u *store.UserRecord) { u.TOTPEnabled, u.TOTPSecret = true, "" })

	for _, name := range []string{"foreign", "empty"} {
		if _, tok, err := e.svc.Login(ctx, name, testPassword, "123456", "ip-"+name, ""); !errors.Is(err, ErrTOTPBroken) || tok != "" {
			t.Errorf("%s: err = %v tok=%q, want ErrTOTPBroken", name, err, tok)
		}
	}
	// Even a code computed from the real secret must not get through.
	if _, _, err := e.svc.Login(ctx, "foreign", testPassword, code(t, "JBSWY3DPEHPK3PXP", time.Now()), "ip-x", ""); !errors.Is(err, ErrTOTPBroken) {
		t.Errorf("foreign with valid code: err = %v, want ErrTOTPBroken", err)
	}
}

// ---- throttling

func TestLoginThrottle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")
	e.addUser(t, "bob")

	for i := range 5 {
		// Mixed case must share one counter.
		name := "alice"
		if i%2 == 1 {
			name = "ALICE"
		}
		if _, _, err := e.svc.Login(ctx, name, "bad-password", "", "1.1.1.1", ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("attempt %d: err = %v, want ErrInvalid", i, err)
		}
	}
	// Now locked, even with the right password.
	if _, tok, err := e.svc.Login(ctx, "alice", testPassword, "", "1.1.1.1", ""); !errors.Is(err, ErrLocked) || tok != "" {
		t.Fatalf("after 5 failures: err = %v, want ErrLocked", err)
	}
	// The lock is per ip+username.
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, "", "2.2.2.2", ""); err != nil {
		t.Fatalf("other IP locked out: %v", err)
	}
	if _, _, err := e.svc.Login(ctx, "bob", testPassword, "", "1.1.1.1", ""); err != nil {
		t.Fatalf("other user on same IP locked out: %v", err)
	}

	// Failures older than the 15 minute window no longer count.
	key := "1.1.1.1|alice"
	e.svc.mu.Lock()
	for i := range e.svc.failures[key] {
		e.svc.failures[key][i] = time.Now().Add(-16 * time.Minute)
	}
	e.svc.mu.Unlock()
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, "", "1.1.1.1", ""); err != nil {
		t.Fatalf("lock did not expire: %v", err)
	}
}

func TestLoginThrottleResetOnSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")

	fail := func(n int) {
		for range n {
			if _, _, err := e.svc.Login(ctx, "alice", "bad-password", "", "ip", ""); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		}
	}
	fail(4)
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, "", "ip", ""); err != nil {
		t.Fatal(err)
	}
	fail(4)
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, "", "ip", ""); err != nil {
		t.Fatalf("counter not reset by successful login: %v", err)
	}
}

func TestLoginThrottleCountsUnknownUsersAndBadCodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	u := e.addUser(t, "alice")
	secret := e.enableTOTP(t, u)

	// Bad TOTP codes count toward the lock (brute-forcing 6 digits).
	bad := wrongCode(t, secret)
	for range 5 {
		if _, _, err := e.svc.Login(ctx, "alice", testPassword, bad, "ip", ""); !errors.Is(err, ErrTOTPInvalid) {
			t.Fatalf("err = %v, want ErrTOTPInvalid", err)
		}
	}
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, code(t, secret, time.Now()), "ip", ""); !errors.Is(err, ErrLocked) {
		t.Fatalf("after 5 bad codes: err = %v, want ErrLocked", err)
	}

	// Failures against a non-existent account are recorded too, so it
	// cannot be used to probe without limit.
	e.svc.mu.Lock()
	e.svc.failures["ip|ghost"] = []time.Time{time.Now(), time.Now(), time.Now(), time.Now()}
	e.svc.mu.Unlock()
	if _, _, err := e.svc.Login(ctx, "ghost", "whatever-pw", "", "ip", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown user err = %v", err)
	}
	if _, _, err := e.svc.Login(ctx, "ghost", "whatever-pw", "", "ip", ""); !errors.Is(err, ErrLocked) {
		t.Fatalf("unknown user not throttled: %v", err)
	}
}

// Missing a code with the right password is not a failed guess.
func TestLoginTOTPRequiredDoesNotLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	u := e.addUser(t, "alice")
	secret := e.enableTOTP(t, u)
	for range 8 {
		if _, _, err := e.svc.Login(ctx, "alice", testPassword, "", "ip", ""); !errors.Is(err, ErrTOTPRequired) {
			t.Fatalf("err = %v, want ErrTOTPRequired", err)
		}
	}
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, code(t, secret, time.Now()), "ip", ""); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentLogins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			e.svc.Login(ctx, "alice", "bad-password", "", "attacker", "")
			e.svc.Login(ctx, "alice", testPassword, "", fmt.Sprintf("user-%d", i), "")
		})
	}
	wg.Wait()
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, "", "attacker", ""); !errors.Is(err, ErrLocked) {
		t.Fatalf("after 20 concurrent failures: err = %v, want ErrLocked", err)
	}
}

// ---- sessions

func TestSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")

	if _, err := e.svc.Session(ctx, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty token err = %v", err)
	}
	if _, err := e.svc.Session(ctx, "made-up-token"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown token err = %v", err)
	}

	_, tok, err := e.svc.Login(ctx, "alice", testPassword, "", "ip", "")
	if err != nil {
		t.Fatal(err)
	}
	e.svc.Logout(ctx, tok)
	if _, err := e.svc.Session(ctx, tok); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session valid after logout: %v", err)
	}
	e.svc.Logout(ctx, tok) // idempotent
}

func TestSessionExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")

	tok := "expired-token"
	if err := e.st.CreateSession(ctx, sha(tok), "id-alice", "", "", time.Now().Add(-time.Millisecond*10)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Session(ctx, tok); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session err = %v, want ErrNotFound", err)
	}
}

func TestSessionSlidingExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")

	// Less than half the TTL left: extended to a full TTL.
	short := "short-token"
	if err := e.st.CreateSession(ctx, sha(short), "id-alice", "", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Session(ctx, short); err != nil {
		t.Fatal(err)
	}
	_, exp, err := e.st.SessionUser(ctx, sha(short))
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < SessionTTL-time.Minute {
		t.Fatalf("session not extended: %v left, want ~%v", d, SessionTTL)
	}

	// More than half left: left alone.
	long := "long-token"
	orig := time.UnixMilli(time.Now().Add(SessionTTL - time.Hour).UnixMilli())
	if err := e.st.CreateSession(ctx, sha(long), "id-alice", "", "", orig); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Session(ctx, long); err != nil {
		t.Fatal(err)
	}
	if _, exp, _ := e.st.SessionUser(ctx, sha(long)); !exp.Equal(orig) {
		t.Fatalf("fresh session expiry changed from %v to %v", orig, exp)
	}
}

func TestSessionDisabledUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")
	_, tok, err := e.svc.Login(ctx, "alice", testPassword, "", "ip", "")
	if err != nil {
		t.Fatal(err)
	}
	u := e.reload(t, "id-alice")
	u.Disabled = true
	if err := e.st.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.Session(ctx, tok); !errors.Is(err, ErrDisabled) || got != nil {
		t.Fatalf("disabled user session = %v, %v; want ErrDisabled", got, err)
	}
}

func TestSessionDeletedUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")
	_, tok, err := e.svc.Login(ctx, "alice", testPassword, "", "ip", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteUser(ctx, "id-alice"); err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.Session(ctx, tok); err == nil {
		t.Fatalf("session of deleted user resolved to %+v", got)
	}
}

// ---- password change & reset

func TestChangePassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice", func(u *store.UserRecord) { u.MustChange = true })

	_, keep, err := e.svc.Login(ctx, "alice", testPassword, "", "ip", "")
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := e.svc.Login(ctx, "alice", testPassword, "", "ip", "")
	if err != nil {
		t.Fatal(err)
	}
	u := e.reload(t, "id-alice")
	origHash := u.PasswordHash

	if err := e.svc.ChangePassword(ctx, u, "not-the-password", "brand new password", keep); err == nil {
		t.Fatal("wrong current password accepted")
	}
	if err := e.svc.ChangePassword(ctx, u, testPassword, testPassword, keep); err == nil {
		t.Fatal("unchanged password accepted")
	}
	if err := e.svc.ChangePassword(ctx, u, testPassword, "short", keep); err == nil {
		t.Fatal("policy-violating password accepted")
	}
	if got := e.reload(t, "id-alice"); got.PasswordHash != origHash || !got.MustChange {
		t.Fatal("failed ChangePassword modified the stored user")
	}
	if _, err := e.svc.Session(ctx, other); err != nil {
		t.Fatal("failed ChangePassword ended sessions")
	}

	const next = "brand new password"
	if err := e.svc.ChangePassword(ctx, u, testPassword, next, keep); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	got := e.reload(t, "id-alice")
	if got.MustChange {
		t.Fatal("MustChange still set")
	}
	if got.PasswordHash == origHash {
		t.Fatal("password hash unchanged")
	}
	if cost, _ := bcrypt.Cost([]byte(got.PasswordHash)); cost != bcryptCost {
		t.Fatalf("new hash cost = %d, want %d", cost, bcryptCost)
	}
	if bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte(next)) != nil {
		t.Fatal("new password does not verify")
	}
	if _, err := e.svc.Session(ctx, keep); err != nil {
		t.Fatalf("current session ended: %v", err)
	}
	if _, err := e.svc.Session(ctx, other); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other session survived password change: %v", err)
	}
}

func TestResetPassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	u := e.addUser(t, "alice")
	e.enableTOTP(t, u)
	u = e.reload(t, "id-alice")
	u.Disabled = true
	if err := e.st.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"s1", "s2"} {
		if err := e.st.CreateSession(ctx, sha(h), u.ID, "", "", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := e.svc.ResetPassword(ctx, "nobody"); err == nil {
		t.Fatal("reset of unknown user succeeded")
	}

	pw, err := e.svc.ResetPassword(ctx, "ALICE")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPasswordPolicy(pw); err != nil {
		t.Fatalf("generated password %q violates policy: %v", pw, err)
	}
	got := e.reload(t, "id-alice")
	if !got.MustChange || got.Disabled || got.TOTPEnabled || got.TOTPSecret != "" {
		t.Fatalf("after reset: %+v", got)
	}
	if got.PasswordHash == u.PasswordHash {
		t.Fatal("password hash unchanged")
	}
	for _, h := range []string{"s1", "s2"} {
		if _, err := e.svc.Session(ctx, h); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("session %s survived reset: %v", h, err)
		}
	}
	if _, _, err := e.svc.Login(ctx, "alice", pw, "", "ip", ""); err != nil {
		t.Fatalf("login with reset password (no TOTP): %v", err)
	}
}

func TestEnsureAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)

	pw, err := e.svc.EnsureAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPasswordPolicy(pw); err != nil {
		t.Fatalf("generated admin password %q violates policy: %v", pw, err)
	}
	u, err := e.st.GetUserByName(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != model.RoleAdmin || !u.MustChange || u.Disabled || u.TOTPEnabled {
		t.Fatalf("bootstrap admin = %+v", u)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pw)) != nil {
		t.Fatal("returned password does not match stored hash")
	}

	again, err := e.svc.EnsureAdmin(ctx)
	if err != nil || again != "" {
		t.Fatalf("second EnsureAdmin = %q, %v; want \"\", nil", again, err)
	}
	if n, _ := e.st.CountUsers(ctx); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
	if got, _ := e.st.GetUserByName(ctx, "admin"); got.PasswordHash != u.PasswordHash {
		t.Fatal("second EnsureAdmin changed the admin password")
	}
}

func TestEnsureAdminSkipsWhenUsersExist(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "viewer", func(u *store.UserRecord) { u.Role = model.RoleViewer })
	pw, err := e.svc.EnsureAdmin(ctx)
	if err != nil || pw != "" {
		t.Fatalf("EnsureAdmin = %q, %v", pw, err)
	}
	if _, err := e.st.GetUserByName(ctx, "admin"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("admin created although users exist: %v", err)
	}
}

// ---- TOTP management

func TestTOTPSetupAndEnable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	u := e.addUser(t, "alice")

	if err := e.svc.TOTPEnable(ctx, u, "123456"); err == nil {
		t.Fatal("enable before setup succeeded")
	}

	secret, url, err := e.svc.TOTPSetup(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || !strings.HasPrefix(url, "otpauth://totp/") || !strings.Contains(url, "issuer=NodeHoster") || !strings.Contains(url, "alice") {
		t.Fatalf("setup returned secret=%q url=%q", secret, url)
	}
	stored := e.reload(t, u.ID)
	if stored.TOTPEnabled {
		t.Fatal("setup alone enabled TOTP")
	}
	if stored.TOTPSecret == secret || !strings.HasPrefix(stored.TOTPSecret, "enc:v1:") {
		t.Fatalf("TOTP secret stored unencrypted: %q", stored.TOTPSecret)
	}
	if plain, err := e.box.Unseal(stored.TOTPSecret); err != nil || plain != secret {
		t.Fatalf("stored secret unseals to %q, %v", plain, err)
	}

	// Setup can be restarted while not yet enabled, replacing the secret.
	secret2, _, err := e.svc.TOTPSetup(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if secret2 == secret {
		t.Fatal("restarted setup reused the secret")
	}

	if err := e.svc.TOTPEnable(ctx, u, wrongCode(t, secret2)); !errors.Is(err, ErrTOTPInvalid) {
		t.Fatalf("enable with wrong code: %v", err)
	}
	if e.reload(t, u.ID).TOTPEnabled {
		t.Fatal("wrong code enabled TOTP")
	}
	// A code for the abandoned first secret must not work.
	if c := code(t, secret, time.Now()); c != code(t, secret2, time.Now()) {
		if err := e.svc.TOTPEnable(ctx, u, c); !errors.Is(err, ErrTOTPInvalid) {
			t.Fatalf("code for replaced secret: %v", err)
		}
	}
	if err := e.svc.TOTPEnable(ctx, u, code(t, secret2, time.Now())); err != nil {
		t.Fatal(err)
	}
	if !e.reload(t, u.ID).TOTPEnabled {
		t.Fatal("TOTP not enabled in store")
	}

	// Re-running setup while enabled would silently swap out the second
	// factor without proving possession of it.
	before := e.reload(t, u.ID).TOTPSecret
	if _, _, err := e.svc.TOTPSetup(ctx, u); err == nil {
		t.Fatal("setup while enabled succeeded")
	}
	if after := e.reload(t, u.ID); !after.TOTPEnabled || after.TOTPSecret != before {
		t.Fatal("refused setup changed stored TOTP state")
	}
}

func TestTOTPDisable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	plainUser := e.addUser(t, "bob")
	if err := e.svc.TOTPDisable(ctx, plainUser, ""); err != nil {
		t.Fatalf("disable when not enabled: %v", err)
	}

	u := e.addUser(t, "alice")
	secret := e.enableTOTP(t, u)

	for _, c := range []string{"", wrongCode(t, secret)} {
		if err := e.svc.TOTPDisable(ctx, u, c); !errors.Is(err, ErrTOTPInvalid) {
			t.Fatalf("disable with code %q: err = %v, want ErrTOTPInvalid", c, err)
		}
	}
	if got := e.reload(t, u.ID); !got.TOTPEnabled || got.TOTPSecret == "" {
		t.Fatal("failed disable changed TOTP state")
	}
	if err := e.svc.TOTPDisable(ctx, u, code(t, secret, time.Now())); err != nil {
		t.Fatal(err)
	}
	got := e.reload(t, u.ID)
	if got.TOTPEnabled || got.TOTPSecret != "" {
		t.Fatalf("after disable: enabled=%v secret=%q", got.TOTPEnabled, got.TOTPSecret)
	}
	if _, _, err := e.svc.Login(ctx, "alice", testPassword, "", "ip", ""); err != nil {
		t.Fatalf("login after disabling TOTP: %v", err)
	}
}

func TestTOTPManagementWithUnreadableSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	other, err := secrets.Open(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.Seal("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	valid := code(t, "JBSWY3DPEHPK3PXP", time.Now())

	pending := e.addUser(t, "pending", func(u *store.UserRecord) { u.TOTPSecret = foreign })
	if err := e.svc.TOTPEnable(ctx, pending, valid); !errors.Is(err, ErrTOTPBroken) {
		t.Fatalf("enable: err = %v, want ErrTOTPBroken", err)
	}
	enabled := e.addUser(t, "enabled", func(u *store.UserRecord) { u.TOTPSecret, u.TOTPEnabled = foreign, true })
	if err := e.svc.TOTPDisable(ctx, enabled, valid); !errors.Is(err, ErrTOTPBroken) {
		t.Fatalf("disable: err = %v, want ErrTOTPBroken", err)
	}
	if !e.reload(t, enabled.ID).TOTPEnabled {
		t.Fatal("TOTP disabled despite unreadable secret")
	}
}

// ---- API tokens

// waitTouched waits for the asynchronous TouchToken started by TokenUser,
// so it neither races the assertions nor outlives the store.
func waitTouched(t *testing.T, st *store.Store, userID, tokenID string) *model.APIToken {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err := st.ListTokens(context.Background(), userID)
		if err != nil {
			t.Fatal(err)
		}
		for _, tk := range list {
			if tk.ID == tokenID && tk.LastUsed != nil {
				return tk
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("token LastUsed never recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCreateToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")

	for _, name := range []string{"", "   ", "\t\n"} {
		if _, _, err := e.svc.CreateToken(ctx, "id-alice", name, 0); err == nil {
			t.Fatalf("CreateToken with blank name %q succeeded", name)
		}
	}

	raw, tk, err := e.svc.CreateToken(ctx, "id-alice", "  deploy bot  ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "nh_") || len(raw) != 3+43 {
		t.Fatalf("raw token %q has wrong shape", raw)
	}
	if tk.Name != "deploy bot" {
		t.Fatalf("name not trimmed: %q", tk.Name)
	}
	if tk.Prefix != raw[:10] {
		t.Fatalf("prefix = %q, want %q", tk.Prefix, raw[:10])
	}
	if tk.Hash != sha(raw) || strings.Contains(tk.Hash, raw) {
		t.Fatal("token hash is not SHA-256 of the raw token")
	}
	if tk.ExpiresAt != nil {
		t.Fatal("expiresDays=0 produced an expiry")
	}
	stored, err := e.st.TokenByHash(ctx, sha(raw))
	if err != nil || stored.ID != tk.ID || stored.UserID != "id-alice" {
		t.Fatalf("stored token = %+v, %v", stored, err)
	}
	if _, err := e.st.TokenByHash(ctx, raw); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("raw token stored in plaintext")
	}

	_, tk30, err := e.svc.CreateToken(ctx, "id-alice", "monthly", 30)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Now().AddDate(0, 0, 30)
	if tk30.ExpiresAt == nil || tk30.ExpiresAt.Sub(want).Abs() > time.Minute {
		t.Fatalf("ExpiresAt = %v, want ~%v", tk30.ExpiresAt, want)
	}
	if _, tkNeg, err := e.svc.CreateToken(ctx, "id-alice", "neg", -5); err != nil || tkNeg.ExpiresAt != nil {
		t.Fatalf("negative expiry = %+v, %v; want no expiry", tkNeg, err)
	}

	if _, _, err := e.svc.CreateToken(ctx, "no-such-user", "x", 0); err == nil {
		t.Fatal("token created for a non-existent user")
	}
}

func TestTokenUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")

	raw, tk, err := e.svc.CreateToken(ctx, "id-alice", "ci", 1)
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.svc.TokenUser(ctx, raw)
	if err != nil || u.ID != "id-alice" {
		t.Fatalf("TokenUser = %v, %v", u, err)
	}
	waitTouched(t, e.st, "id-alice", tk.ID)

	flipped := []byte(raw)
	flipped[len(flipped)-1] ^= 1
	for name, bad := range map[string]string{
		"empty":            "",
		"no prefix":        strings.TrimPrefix(raw, "nh_"),
		"wrong prefix":     "xx_" + strings.TrimPrefix(raw, "nh_"),
		"prefix only":      "nh_",
		"last char change": string(flipped),
		"truncated":        raw[:len(raw)-1],
		"hash as token":    tk.Hash,
		"session hash":     "nh_" + tk.Hash,
	} {
		if got, err := e.svc.TokenUser(ctx, bad); err == nil {
			t.Errorf("%s: TokenUser accepted %q -> %v", name, bad, got)
		}
	}
}

func TestTokenUserExpired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")

	raw := "nh_" + randomString(32)
	past := time.Now().Add(-time.Minute)
	if err := e.st.CreateToken(ctx, &model.APIToken{ID: "t-old", UserID: "id-alice", Name: "old", Prefix: raw[:10], Hash: sha(raw), ExpiresAt: &past, CreatedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if u, err := e.svc.TokenUser(ctx, raw); err == nil {
		t.Fatalf("expired token accepted for %v", u)
	}
	tk, err := e.st.TokenByHash(ctx, sha(raw))
	if err != nil {
		t.Fatal(err)
	}
	if tk.LastUsed != nil {
		t.Fatal("expired token marked as used")
	}
}

func TestTokenUserDisabledOrDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addUser(t, "alice")
	e.addUser(t, "bob")

	rawA, _, err := e.svc.CreateToken(ctx, "id-alice", "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	rawB, _, err := e.svc.CreateToken(ctx, "id-bob", "b", 0)
	if err != nil {
		t.Fatal(err)
	}

	a := e.reload(t, "id-alice")
	a.Disabled = true
	if err := e.st.PutUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	if u, err := e.svc.TokenUser(ctx, rawA); !errors.Is(err, ErrDisabled) || u != nil {
		t.Fatalf("disabled user token = %v, %v; want ErrDisabled", u, err)
	}

	if err := e.st.DeleteUser(ctx, "id-bob"); err != nil {
		t.Fatal(err)
	}
	if u, err := e.svc.TokenUser(ctx, rawB); err == nil {
		t.Fatalf("deleted user's token resolved to %v", u)
	}
}

// ---- roles

func TestAllowed(t *testing.T) {
	t.Parallel()
	roles := []model.Role{model.RoleViewer, model.RoleOperator, model.RoleAdmin}
	for i, have := range roles {
		for j, need := range roles {
			if got, want := Allowed(have, need), i >= j; got != want {
				t.Errorf("Allowed(%s, %s) = %v, want %v", have, need, got, want)
			}
		}
	}
	// Unknown or empty roles get no access to anything real.
	for _, bogus := range []model.Role{"", "root", "Admin", "ADMIN", " admin"} {
		for _, need := range roles {
			if Allowed(bogus, need) {
				t.Errorf("Allowed(%q, %s) = true, want false", bogus, need)
			}
		}
	}
}

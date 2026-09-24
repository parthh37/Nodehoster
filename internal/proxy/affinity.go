package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"strings"
	"time"
)

// Session affinity works like ARR's client affinity: the response that
// first reaches a client through one backend carries a cookie naming that
// backend, and later requests presenting the cookie go back to it for as
// long as it can take them. The cookie names the backend by an opaque ID
// (never an address or port) and is HMAC-signed with a key only this server
// knows, so a client can neither learn the topology nor steer itself to a
// backend of its choosing.
//
// A node site's instances are identified by their slot, not their process,
// so a zero-downtime recycle keeps every client on "its" instance.

// affinityID names a backend in the cookie: the first bytes of an HMAC of
// the site and the backend (an upstream URL, or "local" for this server's
// own instances).
type affinityID [8]byte

// noSlot is the slot of a cookie that does not pin an instance.
const noSlot = -1

// hopCookieSuffix is appended to the cookie name on requests another
// NodeHoster forwarded (the hop header). That server keeps its own cookie
// for which server answers; ours, for which instance here, must not
// overwrite it.
const hopCookieSuffix = "-hop"

type affinityCookie struct {
	member affinityID
	slot   int   // instance slot when the member is local, noSlot otherwise
	issued int64 // unix seconds, for sliding renewal of a lifetime
}

const (
	affPayloadLen = 8 + 2 + 4
	affTagLen     = 12
)

// affinityRuntime is a site's compiled affinity setting.
type affinityRuntime struct {
	key      []byte
	siteID   string
	name     string
	lifetime time.Duration
	localID  affinityID
}

func newAffinity(key []byte, siteID, name string, lifetimeSec int) *affinityRuntime {
	a := &affinityRuntime{key: key, siteID: siteID, name: name, lifetime: time.Duration(lifetimeSec) * time.Second}
	a.localID = a.memberID("local")
	return a
}

func (a *affinityRuntime) memberID(name string) affinityID {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte("member\x00" + a.siteID + "\x00" + name))
	var id affinityID
	copy(id[:], m.Sum(nil))
	return id
}

func (a *affinityRuntime) tag(payload []byte) []byte {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte("nhaff1\x00" + a.siteID + "\x00"))
	m.Write(payload)
	return m.Sum(nil)[:affTagLen]
}

func (a *affinityRuntime) encode(c affinityCookie) string {
	b := make([]byte, affPayloadLen, affPayloadLen+affTagLen)
	copy(b, c.member[:])
	slot := uint16(0xFFFF)
	if c.slot >= 0 {
		slot = uint16(c.slot)
	}
	binary.BigEndian.PutUint16(b[8:], slot)
	binary.BigEndian.PutUint32(b[10:], uint32(c.issued))
	b = append(b, a.tag(b)...)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (a *affinityRuntime) decode(v string) (affinityCookie, bool) {
	// Strict: unused trailing bits must be zero, so no two strings decode
	// to the same cookie.
	b, err := base64.RawURLEncoding.Strict().DecodeString(v)
	if err != nil || len(b) != affPayloadLen+affTagLen {
		return affinityCookie{}, false
	}
	if !hmac.Equal(b[affPayloadLen:], a.tag(b[:affPayloadLen])) {
		return affinityCookie{}, false
	}
	var c affinityCookie
	copy(c.member[:], b[:8])
	c.slot = noSlot
	if s := binary.BigEndian.Uint16(b[8:]); s != 0xFFFF {
		c.slot = int(s)
	}
	c.issued = int64(binary.BigEndian.Uint32(b[10:]))
	return c, true
}

// affinityState follows one request through the balancing handlers: the
// pool picks the member, the node handler the instance, and whichever
// proxies the request writes the cookie on the response.
type affinityState struct {
	a      *affinityRuntime
	name   string
	secure bool
	in     affinityCookie
	valid  bool // the request presented a genuine cookie

	chosen bool
	member affinityID
	slot   int
	multi  bool // more than one backend could have answered
}

type ctxAffinityKey struct{}

// affinityState returns the request's affinity state for this site, adding
// it to the request's context the first time. It is nil when the site does
// not use affinity.
func (rt *siteRuntime) affinityState(r *http.Request) (*affinityState, *http.Request) {
	a := rt.affinity
	if a == nil {
		return nil, r
	}
	if st, ok := r.Context().Value(ctxAffinityKey{}).(*affinityState); ok && st.a == a {
		return st, r
	}
	st := &affinityState{a: a, name: a.name, slot: noSlot}
	if r.Header.Get(hopHeader) != "" {
		st.name += hopCookieSuffix
	}
	st.secure = r.TLS != nil || (rt.srv.trustsForwarded(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
	if ck, err := r.Cookie(st.name); err == nil {
		st.in, st.valid = a.decode(ck.Value)
	}
	return st, r.WithContext(context.WithValue(r.Context(), ctxAffinityKey{}, st))
}

func affinityFrom(ctx context.Context) *affinityState {
	st, _ := ctx.Value(ctxAffinityKey{}).(*affinityState)
	return st
}

// pinned is the member the client's cookie names, if it has a valid one.
func (st *affinityState) pinned() (affinityID, bool) {
	if st == nil || !st.valid {
		return affinityID{}, false
	}
	return st.in.member, true
}

// pinnedSlot is the local instance slot the cookie names, or noSlot.
func (st *affinityState) pinnedSlot() int {
	if st == nil || !st.valid || st.in.member != st.a.localID {
		return noSlot
	}
	return st.in.slot
}

// setCookie adds the affinity cookie to a response when the client needs a
// new one: it had none (or a forged or stale one), its backend went away,
// or half of a configured lifetime has passed (sliding expiry). A site with
// a single backend gets no cookie; one sent anyway is simply ignored.
func (st *affinityState) setCookie(h http.Header) {
	if st == nil || !st.chosen || !st.multi {
		return
	}
	now := time.Now()
	if st.valid && st.in.member == st.member && st.in.slot == st.slot &&
		(st.a.lifetime == 0 || now.Sub(time.Unix(st.in.issued, 0)) < st.a.lifetime/2) {
		return
	}
	c := &http.Cookie{
		Name:     st.name,
		Value:    st.a.encode(affinityCookie{member: st.member, slot: st.slot, issued: now.Unix()}),
		Path:     "/",
		HttpOnly: true,
		Secure:   st.secure,
		SameSite: http.SameSiteLaxMode,
	}
	if st.a.lifetime > 0 {
		c.MaxAge = int(st.a.lifetime / time.Second)
		c.Expires = now.Add(st.a.lifetime)
	}
	h.Add("Set-Cookie", c.String())
}

// modifyAffinity is the reverse proxies' ModifyResponse hook: it runs only
// for a response the backend actually produced (upgrades included), so a
// client is never pinned to a backend that failed.
func modifyAffinity(res *http.Response) error {
	affinityFrom(res.Request.Context()).setCookie(res.Header)
	return nil
}

// affinityKey signs affinity cookies. The server's persistent key is used
// when one was provided, so cookies stay valid across service restarts.
func (s *Server) affinityKey() []byte {
	s.affKeyOnce.Do(func() {
		if len(s.deps.AffinityKey) >= 16 {
			s.affKey = s.deps.AffinityKey
			return
		}
		s.affKey = make([]byte, 32)
		rand.Read(s.affKey)
	})
	return s.affKey
}

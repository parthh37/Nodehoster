package certs

// OCSP stapling: for certificates that name an OCSP responder, the
// responder's signed answer ("good until ...") is fetched ahead of time and
// sent in the TLS handshake, so browsers need not ask the CA themselves.
// Responses are kept next to the certificate (data/certs/<id>/ocsp.der) so
// that a restart staples at once, whether or not the responder answers.
//
// Let's Encrypt stopped running OCSP in 2025: its certificates name no
// responder, and there is nothing to staple ("none"), which is fine.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"golang.org/x/crypto/ocsp"
)

const (
	ocspFile        = "ocsp.der"
	ocspTimeout     = 15 * time.Second
	ocspMaxResponse = 64 << 10
	ocspTick        = 30 * time.Second
	// A staple is dropped this long before its NextUpdate, so that a
	// handshake never carries an expired response even between passes.
	ocspStapleMargin = 2 * time.Minute
	// Responses without a NextUpdate ("newer information is always
	// available") are used for this long after their ThisUpdate.
	ocspNoNextUpdate = time.Hour
	ocspMaxBackoff   = time.Hour
	// A response may be dated slightly ahead of this server's clock.
	ocspClockSkew = 5 * time.Minute
)

// oidTLSFeature is the TLS Feature extension (RFC 7633); a certificate
// listing status_request (5) in it is "OCSP Must-Staple".
var oidTLSFeature = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 1, 24}

// ocspState is the Manager's stapling state; its zero value is ready.
type ocspState struct {
	mu      sync.Mutex
	entries map[string]*ocspEntry // certificate id ->
	kick    chan struct{}

	fetch sync.Mutex // one request to a responder at a time
	// Client fetches responses; nil = a client with ocspTimeout.
	Client *http.Client
}

// ocspEntry is one loaded certificate's stapling state.
type ocspEntry struct {
	leaf, issuer *x509.Certificate
	status       model.OCSPStatus
	raw          []byte    // the newest valid response (DER), stapled if good
	until        time.Time // raw is valid until then
	nextCheck    time.Time // zero = never (nothing to ask)
	failures     int

	revokedNoticed bool // cert.revoked sent for this certificate
	stapleWarned   bool // Must-Staple without a staple reported
}

func (o *ocspState) kickCh() chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.kick == nil {
		o.kick = make(chan struct{}, 1)
	}
	return o.kick
}

func (o *ocspState) wake() {
	select {
	case o.kickCh() <- struct{}{}:
	default:
	}
}

func (m *Manager) ocspPath(id string) string { return filepath.Join(m.dir, id, ocspFile) }

// ocspLoadAll starts tracking every loaded certificate, stapling the
// responses saved on disk that are still valid.
func (m *Manager) ocspLoadAll() {
	m.mu.RLock()
	pairs := make(map[string]*tls.Certificate, len(m.cache))
	for id, c := range m.cache {
		pairs[id] = c
	}
	m.mu.RUnlock()
	for id, c := range pairs {
		m.ocspTrack(id, c, true)
	}
}

// ocspTrack (re)starts stapling for a certificate's key pair. fromDisk
// reuses the saved response; otherwise the pair is new material and a
// saved response (for the previous certificate) is discarded.
func (m *Manager) ocspTrack(id string, pair *tls.Certificate, fromDisk bool) {
	if pair == nil || len(pair.Certificate) == 0 {
		return
	}
	leaf := pair.Leaf
	if leaf == nil {
		var err error
		if leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return
		}
	}
	now := time.Now()
	e := &ocspEntry{leaf: leaf, status: model.OCSPStatus{State: model.OCSPNone, Responder: responderURL(leaf), MustStaple: mustStaple(leaf)}}
	if !fromDisk {
		os.Remove(m.ocspPath(id))
	}
	if e.status.Responder != "" {
		e.issuer = findIssuer(leaf, pair.Certificate[1:])
		if e.issuer == nil {
			e.status.State = model.OCSPError
			e.status.LastError = "the certificate file does not include its issuer's certificate, which OCSP needs; import the full chain"
		} else {
			e.status.State = model.OCSPPending
			e.nextCheck = now
		}
	}
	var staple []byte
	if e.issuer != nil && fromDisk {
		if der, err := os.ReadFile(m.ocspPath(id)); err == nil {
			if resp, until, err := parseOCSP(der, leaf, e.issuer, now); err == nil {
				e.record(resp, der, until, now)
				e.revokedNoticed = resp.Status == ocsp.Revoked // reported before the restart
				if resp.Status == ocsp.Good {
					staple = der
				}
			} else {
				m.log.Info("saved OCSP response not used", "cert", id, "err", err)
			}
		}
	}
	e.status.Stapled = staple != nil && m.setStaple(id, leaf.Raw, staple)
	m.ocsp.mu.Lock()
	if m.ocsp.entries == nil {
		m.ocsp.entries = map[string]*ocspEntry{}
	}
	m.ocsp.entries[id] = e
	m.ocsp.mu.Unlock()
	if !e.nextCheck.IsZero() {
		m.ocsp.wake()
	}
}

// ocspForget stops stapling for a deleted certificate.
func (m *Manager) ocspForget(id string) {
	m.ocsp.mu.Lock()
	delete(m.ocsp.entries, id)
	m.ocsp.mu.Unlock()
}

// record takes a valid response into the entry (the caller holds the
// state's lock or owns the entry).
func (e *ocspEntry) record(resp *ocsp.Response, der []byte, until, now time.Time) {
	e.raw, e.until = der, until
	e.failures = 0
	st := &e.status
	st.LastError = ""
	st.ThisUpdate, st.NextUpdate = timePtr(resp.ThisUpdate), timePtr(until)
	st.RevokedAt, st.RevocationReason = nil, ""
	switch resp.Status {
	case ocsp.Good:
		st.State = model.OCSPGood
	case ocsp.Revoked:
		st.State = model.OCSPRevoked
		st.RevokedAt = timePtr(resp.RevokedAt)
		st.RevocationReason = revocationReason(resp.RevocationReason)
	default:
		st.State = model.OCSPUnknown
	}
	e.nextCheck = refreshAt(resp.ThisUpdate, until, now)
	if resp.Status == ocsp.Unknown {
		// Often a certificate the responder has not heard of yet.
		if soon := now.Add(time.Hour); soon.Before(e.nextCheck) {
			e.nextCheck = soon
		}
	}
}

// setStaple puts a response into the handshakes of a certificate (nil
// removes it). The cached pair is replaced, never changed: handshakes in
// progress keep the one they have. It reports false if the certificate
// was replaced (renewed) meanwhile.
func (m *Manager) setStaple(id string, leafDER, staple []byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.cache[id]
	if cur == nil || len(cur.Certificate) == 0 || !bytes.Equal(cur.Certificate[0], leafDER) {
		return false
	}
	if bytes.Equal(cur.OCSPStaple, staple) {
		return true
	}
	cp := *cur
	cp.OCSPStaple = staple
	m.cache[id] = &cp
	return true
}

// runOCSP keeps the staples fresh until ctx ends.
func (m *Manager) runOCSP(ctx context.Context) {
	t := time.NewTicker(ocspTick)
	defer t.Stop()
	kick := m.ocsp.kickCh()
	for {
		m.ocspPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
	}
}

// ocspPass drops staples about to expire and asks the responders of the
// certificates that are due.
func (m *Manager) ocspPass(ctx context.Context) {
	m.ocspExpire()
	now := time.Now()
	var due []string
	m.ocsp.mu.Lock()
	for id, e := range m.ocsp.entries {
		if !e.nextCheck.IsZero() && !now.Before(e.nextCheck) {
			due = append(due, id)
		}
	}
	m.ocsp.mu.Unlock()
	slices.Sort(due)
	for _, id := range due {
		if ctx.Err() != nil {
			return
		}
		m.checkOCSP(ctx, id)
		m.ocspExpire() // a long pass must not leave an expired staple behind
	}
}

// ocspExpire removes staples that are (nearly) out of date.
func (m *Manager) ocspExpire() {
	now := time.Now()
	type drop struct {
		id   string
		leaf []byte
	}
	var drops []drop
	var warn []string
	m.ocsp.mu.Lock()
	for id, e := range m.ocsp.entries {
		if e.raw == nil || now.Before(e.until.Add(-ocspStapleMargin)) {
			continue
		}
		e.raw, e.until = nil, time.Time{}
		e.status.Stapled = false
		if e.status.State == model.OCSPGood {
			e.status.State = model.OCSPError
			if e.status.LastError == "" {
				e.status.LastError = "the last OCSP response expired before a new one could be obtained"
			}
		}
		drops = append(drops, drop{id, e.leaf.Raw})
		if e.status.MustStaple && !e.stapleWarned {
			e.stapleWarned = true
			warn = append(warn, id)
		}
	}
	m.ocsp.mu.Unlock()
	for _, d := range drops {
		m.setStaple(d.id, d.leaf, nil)
	}
	for _, id := range warn {
		m.warnMustStaple(id, "its OCSP response expired")
	}
}

// CheckOCSP asks a certificate's responder now and returns the result.
func (m *Manager) CheckOCSP(ctx context.Context, id string) (*model.OCSPStatus, error) {
	m.ocsp.mu.Lock()
	e := m.ocsp.entries[id]
	m.ocsp.mu.Unlock()
	if e == nil {
		return nil, errors.New("the certificate has not been issued or loaded yet")
	}
	if e.issuer != nil {
		m.checkOCSP(ctx, id)
	}
	return m.OCSPStatus(id), nil
}

// OCSPStatus is a certificate's stapling state; nil for certificates that
// are not loaded (pending, or their files are missing).
func (m *Manager) OCSPStatus(id string) *model.OCSPStatus {
	m.ocsp.mu.Lock()
	defer m.ocsp.mu.Unlock()
	e := m.ocsp.entries[id]
	if e == nil {
		return nil
	}
	st := e.status
	st.NextCheck = nil
	if !e.nextCheck.IsZero() {
		st.NextCheck = timePtr(e.nextCheck)
	}
	return &st
}

// checkOCSP fetches, verifies and applies one certificate's response.
func (m *Manager) checkOCSP(ctx context.Context, id string) {
	m.ocsp.fetch.Lock()
	defer m.ocsp.fetch.Unlock()
	m.ocsp.mu.Lock()
	e := m.ocsp.entries[id]
	var responder string
	if e != nil {
		responder = e.status.Responder
	}
	m.ocsp.mu.Unlock()
	if e == nil || e.issuer == nil {
		return
	}
	der, err := m.fetchOCSP(ctx, responder, e.leaf, e.issuer)
	now := time.Now()
	var resp *ocsp.Response
	var until time.Time
	if err == nil {
		resp, until, err = parseOCSP(der, e.leaf, e.issuer, now)
	}
	if err != nil && ctx.Err() != nil {
		return // shutting down: not the responder's fault
	}
	if err == nil && resp.Status != ocsp.Unknown {
		// Kept whatever it says: a revocation is remembered across
		// restarts too.
		if werr := config.WriteFileAtomic(m.ocspPath(id), der, 0o644); werr != nil {
			m.log.Warn("save OCSP response", "cert", id, "err", werr)
		}
	}
	var staple []byte
	if err == nil && resp.Status == ocsp.Good {
		staple = der
	}
	// Staple first, then publish the state: Stapled never claims more
	// than handshakes carry. A failed check keeps a still-valid staple.
	stapled := false
	if err == nil {
		stapled = m.setStaple(id, e.leaf.Raw, staple) && staple != nil
	}

	var revoked, warnStaple bool
	m.ocsp.mu.Lock()
	if m.ocsp.entries[id] != e { // renewed or deleted meanwhile
		m.ocsp.mu.Unlock()
		return
	}
	e.status.LastCheck = timePtr(now)
	if err != nil {
		e.failures++
		e.status.LastError = err.Error()
		e.nextCheck = now.Add(min(time.Minute<<min(e.failures-1, 10), ocspMaxBackoff))
		if e.raw == nil && e.status.State != model.OCSPRevoked {
			e.status.State = model.OCSPError
		}
	} else {
		e.record(resp, der, until, now)
		if resp.Status != ocsp.Good {
			e.raw = nil // only good answers are stapled (as nginx does)
		}
		e.status.Stapled = stapled
		if resp.Status == ocsp.Revoked && !e.revokedNoticed {
			e.revokedNoticed, revoked = true, true
		}
		if stapled {
			e.stapleWarned = false
		}
	}
	if e.status.MustStaple && !e.status.Stapled && e.raw == nil && !e.stapleWarned {
		e.stapleWarned, warnStaple = true, true
	}
	st := e.status
	m.ocsp.mu.Unlock()

	if err != nil {
		m.log.Warn("OCSP check failed", "cert", id, "responder", st.Responder, "err", err)
	}
	if revoked {
		m.certRevoked(id, st)
	}
	if warnStaple {
		reason := "its OCSP responder gave no valid answer"
		switch {
		case st.State == model.OCSPRevoked:
			reason = "it has been revoked"
		case st.State == model.OCSPUnknown:
			reason = "its OCSP responder does not know it"
		case st.LastError != "":
			reason += " (" + st.LastError + ")"
		}
		m.warnMustStaple(id, reason)
	}
}

// certRevoked reports a revocation and, for an automatic certificate,
// obtains a new one (with a new key: lego generates one per order).
func (m *Manager) certRevoked(id string, st model.OCSPStatus) {
	name := id
	c, err := m.store.GetCertificate(context.Background(), id)
	if err == nil {
		name = c.Name
	}
	when := ""
	if st.RevokedAt != nil {
		when = " on " + st.RevokedAt.Format("2006-01-02")
	}
	reason := ""
	if st.RevocationReason != "" {
		reason = " (" + st.RevocationReason + ")"
	}
	renew := err == nil && c.Source == model.CertACME && c.AutoRenew
	next := "replace it"
	if renew {
		next = "a new certificate is being requested"
	}
	m.bus.Error(events.CertRevoked, "", "certificate %s was revoked by its CA%s%s; browsers will refuse it: %s", name, when, reason, next)
	if renew {
		go m.issue(id)
	}
}

func (m *Manager) warnMustStaple(id, reason string) {
	name := id
	if c, err := m.store.GetCertificate(context.Background(), id); err == nil {
		name = c.Name
	}
	m.bus.Warn(events.CertStapling, "", "certificate %s requires OCSP stapling (Must-Staple), but there is no valid response to staple: %s; browsers may refuse it", name, reason)
}

// fetchOCSP asks the responder about leaf (RFC 6960 over HTTP POST).
func (m *Manager) fetchOCSP(ctx context.Context, responder string, leaf, issuer *x509.Certificate) ([]byte, error) {
	reqDER, err := ocsp.CreateRequest(leaf, issuer, &ocsp.RequestOptions{Hash: crypto.SHA1})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, ocspTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responder, bytes.NewReader(reqDER))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/ocsp-request")
	req.Header.Set("Accept", "application/ocsp-response")
	client := m.ocsp.Client
	if client == nil {
		client = &http.Client{Timeout: ocspTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OCSP responder %s: %w", hostOf(responder), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OCSP responder %s answered HTTP %d", hostOf(responder), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ocspMaxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("OCSP responder %s: %w", hostOf(responder), err)
	}
	if len(body) > ocspMaxResponse {
		return nil, fmt.Errorf("OCSP responder %s sent more than %d KB", hostOf(responder), ocspMaxResponse>>10)
	}
	return body, nil
}

// parseOCSP verifies a response for leaf: signed by its issuer (or a
// responder the issuer delegated to), about this certificate, and current.
// It returns the time the response is good until.
func parseOCSP(der []byte, leaf, issuer *x509.Certificate, now time.Time) (*ocsp.Response, time.Time, error) {
	resp, err := ocsp.ParseResponseForCert(der, leaf, issuer)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("invalid OCSP response: %w", err)
	}
	if resp.SerialNumber == nil || resp.SerialNumber.Cmp(leaf.SerialNumber) != 0 {
		return nil, time.Time{}, errors.New("the OCSP response is about another certificate")
	}
	if rc := resp.Certificate; rc != nil && !bytes.Equal(rc.Raw, issuer.Raw) {
		// A delegated responder (RFC 6960 4.2.2.2) must be allowed to sign
		// OCSP responses and be valid itself.
		if !slices.Contains(rc.ExtKeyUsage, x509.ExtKeyUsageOCSPSigning) {
			return nil, time.Time{}, errors.New("the OCSP response is signed by a certificate not authorized for OCSP")
		}
		if now.Before(rc.NotBefore) || now.After(rc.NotAfter) {
			return nil, time.Time{}, errors.New("the OCSP responder's certificate is not valid now")
		}
	}
	if resp.ThisUpdate.After(now.Add(ocspClockSkew)) {
		return nil, time.Time{}, fmt.Errorf("the OCSP response is dated %s, in the future (is this server's clock right?)", resp.ThisUpdate.UTC().Format(time.RFC3339))
	}
	until := resp.NextUpdate
	if until.IsZero() {
		until = resp.ThisUpdate.Add(ocspNoNextUpdate)
	}
	if !now.Before(until) {
		return nil, time.Time{}, fmt.Errorf("the OCSP response expired at %s", until.UTC().Format(time.RFC3339))
	}
	return resp, until, nil
}

// refreshAt is halfway through a response's validity, or halfway through
// what is left of it for a response that was already old when fetched.
func refreshAt(thisUpdate, until, now time.Time) time.Time {
	at := thisUpdate.Add(until.Sub(thisUpdate) / 2)
	if at.Before(now) {
		at = now.Add(until.Sub(now) / 2)
	}
	if soon := now.Add(time.Minute); at.Before(soon) {
		at = soon
	}
	return at
}

// responderURL is the first http(s) OCSP responder the certificate names.
func responderURL(leaf *x509.Certificate) string {
	for _, s := range leaf.OCSPServer {
		if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			return s
		}
	}
	return ""
}

// findIssuer is the certificate of the chain that signed leaf.
func findIssuer(leaf *x509.Certificate, chain [][]byte) *x509.Certificate {
	for _, der := range chain {
		c, err := x509.ParseCertificate(der)
		if err != nil || !bytes.Equal(c.RawSubject, leaf.RawIssuer) {
			continue
		}
		if leaf.CheckSignatureFrom(c) == nil {
			return c
		}
	}
	return nil
}

// mustStaple reports the TLS Feature extension with status_request.
func mustStaple(leaf *x509.Certificate) bool {
	for _, ext := range leaf.Extensions {
		if !ext.Id.Equal(oidTLSFeature) {
			continue
		}
		var features []int
		if _, err := asn1.Unmarshal(ext.Value, &features); err == nil && slices.Contains(features, 5) {
			return true
		}
	}
	return false
}

func revocationReason(code int) string {
	switch code {
	case ocsp.KeyCompromise:
		return "key compromise"
	case ocsp.CACompromise:
		return "CA compromise"
	case ocsp.AffiliationChanged:
		return "affiliation changed"
	case ocsp.Superseded:
		return "superseded"
	case ocsp.CessationOfOperation:
		return "cessation of operation"
	case ocsp.CertificateHold:
		return "certificate hold"
	case ocsp.PrivilegeWithdrawn:
		return "privilege withdrawn"
	case ocsp.AACompromise:
		return "AA compromise"
	}
	return ""
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimSpace(raw)
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

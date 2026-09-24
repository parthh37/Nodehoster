package desktop

import (
	"fmt"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// ClientCertText describes a binding's client certificate policy for the
// bindings list: "", "Accepted (1 CA)", "Required (2 CAs, 3 allowed)".
func ClientCertText(p *model.ClientCertPolicy) string {
	if !p.Active() {
		return ""
	}
	s := "Accepted"
	if p.Mode == model.ClientCertRequire {
		s = "Required"
	}
	var parts []string
	if cas, err := model.ParseCABundle(p.CAPEM); err == nil {
		parts = append(parts, countNoun(len(cas), "CA"))
	} else {
		parts = append(parts, "CA bundle invalid")
	}
	if n := len(p.AllowedSubjects) + len(p.AllowedFingerprints); n > 0 {
		parts = append(parts, fmt.Sprintf("%d allowed", n))
	}
	if n := len(p.RequirePaths); n > 0 && p.Mode == model.ClientCertAccept {
		parts = append(parts, "required on "+strings.Join(p.RequirePaths, ", "))
	}
	return s + " (" + strings.Join(parts, ", ") + ")"
}

// OCSPLevel is how urgent a certificate's OCSP state is: revoked, or a
// Must-Staple certificate without a staple, is down; an OCSP error is a
// warning; none, pending and good are fine.
func OCSPLevel(s *model.OCSPStatus) Level {
	switch {
	case s == nil:
		return LevelOK
	case s.State == model.OCSPRevoked, s.MustStaple && !s.Stapled && s.State != model.OCSPPending:
		return LevelDown
	case s.State == model.OCSPError, s.State == model.OCSPUnknown:
		return LevelWarning
	}
	return LevelOK
}

func countNoun(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

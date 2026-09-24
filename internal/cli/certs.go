package cli

import (
	"flag"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "cert list", MaxArgs: 0, Summary: "List the certificates and when they expire",
			Setup: func(*flag.FlagSet) Runner { return certList }},
		&Command{Name: "cert renew", Args: "<id|name|domain>", MinArgs: 1, MaxArgs: 1,
			Summary: "Renew a certificate now",
			Setup:   func(*flag.FlagSet) Runner { return certRenew }},
	)
}

// certView is a certificate as the API lists it.
type certView struct {
	model.Certificate
	UsedBy []struct {
		SiteName string `json:"siteName"`
		Binding  string `json:"binding"`
	} `json:"usedBy"`
	OCSP *model.OCSPStatus `json:"ocsp"`
}

func certList(e *Env, _ []string) error {
	var list []certView
	raw, err := e.get("/api/certificates", &list)
	if err != nil || e.JSON {
		if err == nil {
			err = e.printJSON(raw)
		}
		return err
	}
	if len(list) == 0 {
		e.printf("The certificate store is empty.\n")
		return nil
	}
	rows := make([][]string, 0, len(list))
	for _, c := range list {
		expires, days := "-", "-"
		if c.NotAfter != nil {
			expires = c.NotAfter.Local().Format("2006-01-02")
			days = fmt.Sprint(int(time.Until(*c.NotAfter).Hours() / 24))
		}
		status := c.Status
		if c.LastError != "" {
			status += ": " + truncate(c.LastError, 50)
		}
		var used []string
		for _, u := range c.UsedBy {
			used = append(used, u.SiteName)
		}
		rows = append(rows, []string{c.Name, truncate(strings.Join(c.Domains, ", "), 50), status, expires, days, yesNo(c.AutoRenew),
			truncate(c.OCSP.Summary(), 40), orDash(strings.Join(used, ", ")), c.ID})
	}
	e.table([]string{"NAME", "DOMAINS", "STATUS", "EXPIRES", "DAYS LEFT", "AUTO-RENEW", "OCSP", "USED BY", "ID"}, rows)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// findCert matches an ID, then a name, then a domain (ignoring case) that
// exactly one certificate covers.
func findCert(list []certView, ref string) (*certView, error) {
	for i := range list {
		if list[i].ID == ref {
			return &list[i], nil
		}
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, ref) {
			return &list[i], nil
		}
	}
	var found []*certView
	for i := range list {
		for _, d := range list[i].Domains {
			if strings.EqualFold(d, ref) {
				found = append(found, &list[i])
				break
			}
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no certificate has the ID, name or domain %q (nodehoster cert list shows them)", ref)
	case 1:
		return found[0], nil
	}
	var names []string
	for _, c := range found {
		names = append(names, c.Name+" ("+c.ID+")")
	}
	return nil, fmt.Errorf("several certificates cover %s: %s; use the ID", ref, strings.Join(names, ", "))
}

func certRenew(e *Env, args []string) error {
	var list []certView
	if _, err := e.get("/api/certificates", &list); err != nil {
		return err
	}
	c, err := findCert(list, args[0])
	if err != nil {
		return err
	}
	var out certView
	raw, err := e.post("/api/certificates/"+url.PathEscape(c.ID)+"/renew", &out)
	if err != nil {
		return err
	}
	if e.JSON {
		return e.printJSON(raw)
	}
	e.printf("Renewal of %s started (status: %s). nodehoster cert list shows the result.\n", out.Name, out.Status)
	return nil
}

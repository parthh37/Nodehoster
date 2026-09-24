package cli

import (
	"flag"
	"net/url"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "tls", MaxArgs: 0, Summary: "Show the TLS settings (minimum version, HTTP/2, HTTP/3) and the HTTP/3 listeners",
			Setup: func(*flag.FlagSet) Runner { return tlsShow }},
		&Command{Name: "tls set", MaxArgs: 0,
			Summary: "Change the TLS settings: --http3 on|off, --http2 on|off, --min-version 1.2|1.3",
			Setup:   tlsSetCmd},
		&Command{Name: "cert ocsp", Args: "<id|name|domain>", MinArgs: 1, MaxArgs: 1,
			Summary: "Ask a certificate's OCSP responder now and show the stapling status",
			Setup:   func(*flag.FlagSet) Runner { return certOCSP }},
	)
}

func tlsShow(e *Env, _ []string) error {
	var v model.TLSView
	raw, err := e.get("/api/tls", &v)
	if err != nil || e.JSON {
		if err == nil {
			err = e.printJSON(raw)
		}
		return err
	}
	printTLS(e, v)
	return nil
}

func printTLS(e *Env, v model.TLSView) {
	listeners := "-"
	if len(v.HTTP3Listeners) > 0 {
		listeners = strings.Join(v.HTTP3Listeners, ", ")
	}
	e.table([]string{"SETTING", "VALUE"}, [][]string{
		{"Minimum TLS version", v.MinVersion},
		{"HTTP/2", onOffText(v.HTTP2)},
		{"HTTP/3 (QUIC over UDP)", onOffText(v.HTTP3)},
		{"HTTP/3 listeners", listeners},
	})
}

func tlsSetCmd(fs *flag.FlagSet) Runner {
	h3 := fs.String("http3", "", "on or off: a UDP (QUIC) listener next to every HTTPS listener")
	h2 := fs.String("http2", "", "on or off")
	minVersion := fs.String("min-version", "", "1.2 or 1.3")
	return func(e *Env, _ []string) error {
		if *h3 == "" && *h2 == "" && *minVersion == "" {
			return usagef("set at least one of --http3, --http2, --min-version")
		}
		var cur model.TLSView
		if _, err := e.get("/api/tls", &cur); err != nil {
			return err
		}
		in := cur.TLSSettings
		for _, f := range []struct {
			name, val string
			dst       *bool
		}{{"--http3", *h3, &in.HTTP3}, {"--http2", *h2, &in.HTTP2}} {
			switch strings.ToLower(f.val) {
			case "":
			case "on":
				*f.dst = true
			case "off":
				*f.dst = false
			default:
				return usagef("%s: expected on or off, not %q", f.name, f.val)
			}
		}
		if *minVersion != "" {
			in.MinVersion = *minVersion
		}
		var v model.TLSView
		if err := e.Client.Put(e.Ctx, "/api/tls", in, &v); err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(v)
		}
		printTLS(e, v)
		if v.HTTP3 {
			e.printf("\nHTTP/3 uses UDP on the HTTPS ports: allow UDP through firewalls in front of this server too.\n")
		}
		return nil
	}
}

func certOCSP(e *Env, args []string) error {
	var list []certView
	if _, err := e.get("/api/certificates", &list); err != nil {
		return err
	}
	c, err := findCert(list, args[0])
	if err != nil {
		return err
	}
	var out certView
	raw, err := e.post("/api/certificates/"+url.PathEscape(c.ID)+"/ocsp", &out)
	if err != nil {
		return err
	}
	if e.JSON {
		return e.printJSON(raw)
	}
	st := out.OCSP
	rows := [][]string{{"OCSP", st.Summary()}}
	if st != nil {
		rows = append(rows, []string{"Responder", orDash(st.Responder)}, []string{"Must-Staple", yesNo(st.MustStaple)})
		if st.NextCheck != nil {
			rows = append(rows, []string{"Next check", st.NextCheck.Local().Format("2006-01-02 15:04")})
		}
		if st.LastError != "" && st.State != model.OCSPError {
			rows = append(rows, []string{"Last error", st.LastError})
		}
	}
	e.table([]string{"CERTIFICATE", out.Name}, rows)
	return nil
}

func onOffText(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

package mail

import (
	"bytes"
	"fmt"
	"mime"
	"mime/quotedprintable"

	"github.com/parthh37/nodehoster/internal/model"
)

// Notify queues a plain-text notification from NodeHoster itself (resource
// alerts), delivered like any other message: directly or through the smart
// host, whether or not the SMTP listener is enabled. The sender is
// nodehoster@<the server's mail host name>.
func (s *Server) Notify(to []string, subject, body string) (*model.MailMessage, error) {
	var rcpt []string
	for _, a := range to {
		if validAddress(a) {
			rcpt = append(rcpt, a)
		}
	}
	if len(rcpt) == 0 {
		return nil, &model.ValidationError{Field: "to", Message: "no valid recipient"}
	}
	from := "nodehoster@" + s.hostname()
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: NodeHoster <%s>\r\n", from)
	for i, a := range rcpt {
		if i == 0 {
			fmt.Fprintf(&b, "To: <%s>", a)
		} else {
			fmt.Fprintf(&b, ",\r\n <%s>", a)
		}
	}
	fmt.Fprintf(&b, "\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n"+
		"Content-Transfer-Encoding: quoted-printable\r\nAuto-Submitted: auto-generated\r\n\r\n", mime.QEncoding.Encode("utf-8", subject))
	qp := quotedprintable.NewWriter(&b)
	qp.Write(bytes.ReplaceAll([]byte(body), []byte("\n"), []byte("\r\n")))
	qp.Close()
	return s.submit(submission{from: from, to: rcpt, data: b.Bytes(), source: "alert"})
}

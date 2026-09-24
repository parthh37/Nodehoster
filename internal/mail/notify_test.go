package mail

import (
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
)

func TestNotify(t *testing.T) {
	rem := newRemote(t)
	h := newHarness(t, rem)
	if _, err := h.s.Notify([]string{"not an address"}, "x", "y"); err == nil {
		t.Fatal("no valid recipient was accepted")
	}
	m, err := h.s.Notify([]string{"ops@x.test", "bad", "oncall@x.test"}, "[NodeHoster] Critical: api: 5xx error rate 12% …", "api.example.com\nCPU 93% for 5 min (limit 85%) — see the console\n")
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != "alert" || len(m.Recipients) != 2 || m.From != "nodehoster@mail.nodehoster.test" || !strings.HasSuffix(m.Subject, "12% …") {
		t.Fatalf("queued: %+v", m)
	}
	waitFor(t, "the notification to be delivered", func() bool { return len(rem.messages()) == 1 })
	got := rem.messages()[0]
	if strings.Join(got.to, ",") != "ops@x.test,oncall@x.test" {
		t.Fatalf("recipients: %v", got.to)
	}
	msg, err := mail.ReadMessage(strings.NewReader(got.data))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if subject != "[NodeHoster] Critical: api: 5xx error rate 12% …" || msg.Header.Get("Auto-Submitted") != "auto-generated" {
		t.Fatalf("headers: %v (subject %q)", msg.Header, subject)
	}
	if string(body) != "api.example.com\r\nCPU 93% for 5 min (limit 85%) — see the console\r\n" {
		t.Fatalf("body: %q", body)
	}
}

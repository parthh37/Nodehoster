package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- SMTP E-mail: the page of NodeHoster's own, built-in SMTP server (it
// does not use the Windows SMTP service)

type mailPage struct {
	page
	props, queue table
	status       *model.MailStatus
	msgs         []model.MailMessage
	filter       string // "" (all) | queued | failed
	loading      bool
	queueLabel   *walk.Label

	showAll, showQueued, showFailed, retry, retryAll, del, test *walk.LinkLabel
}

func (s *mailPage) init(m *manager) *page {
	s.title = func() string { return "SMTP E-mail" }
	// The queue changes by itself, so it is read on every refresh while
	// the page is shown, not only when the page is opened.
	s.update = func() { s.reload(m) }
	s.load = func() { s.reload(m) }
	s.queue.onSelect = s.enable
	s.props.color = func(row, col int) (walk.Color, bool) {
		if row != 0 || col != 1 || s.status == nil {
			return 0, false
		}
		switch {
		case s.status.Listening:
			return colorOK, true
		case s.status.Error != "":
			return colorError, true
		}
		return colorMuted, true
	}
	s.queue.color = func(row, col int) (walk.Color, bool) {
		if row >= len(s.msgs) || col != 5 {
			return 0, false
		}
		switch s.msgs[row].State {
		case model.MailFailed:
			return colorError, true
		case model.MailSending:
			return colorOK, true
		}
		return 0, false
	}
	return &s.page
}

func (s *mailPage) content(m *manager) []Widget {
	props := properties(&s.props)
	props.MaxSize = Size{Height: 170}
	props.MinSize = Size{Height: 150}
	queue := s.queue.view(nil, col("Received", 130), col("From", 170), col("Recipients", 240), col("Subject", 180),
		col("Attempts", 60), col("State", 70), col("Next attempt / last error", 260))
	queue.StretchFactor = 3
	return []Widget{
		props,
		Label{AssignTo: &s.queueLabel, Text: "Queue", Font: Font{Bold: true}},
		queue,
		Label{Text: "Applications send through this server over SMTP, or by dropping .eml files in the pickup folder. It relays only; it does not accept mail for mailboxes.", TextColor: colorMuted},
	}
}

func (s *mailPage) actionsPane(m *manager) []Widget {
	return []Widget{
		heading("SMTP E-mail"),
		link(nil, "Properties…", func() { mailPropertiesDialog(m) }),
		link(&s.test, "Send test e-mail…", func() { s.sendTest(m) }),
		link(nil, "Check deliverability…", func() { mailHealthDialog(m) }),
		link(nil, "Refresh", func() { m.refresh(true) }),
		heading("Queue"),
		link(&s.showAll, "Show all messages", func() { s.setFilter(m, "") }),
		link(&s.showQueued, "Show queued only", func() { s.setFilter(m, model.MailQueued) }),
		link(&s.showFailed, "Show failed only", func() { s.setFilter(m, model.MailFailed) }),
		link(&s.retryAll, "Retry all", func() { s.retryEverything(m) }),
		heading("Selected message"),
		link(&s.retry, "Retry now", func() { s.retrySelected(m) }),
		link(&s.del, "Delete…", func() { s.deleteSelected(m) }),
	}
}

func (s *mailPage) setFilter(m *manager, f string) {
	s.filter = f
	s.enable()
	s.reload(m)
}

// reload reads the status and the queue off the UI thread. One read at a
// time: the page asks on every refresh tick.
func (s *mailPage) reload(m *manager) {
	s.enable()
	if s.loading || !m.connected() {
		return
	}
	s.loading = true
	filter := s.filter
	path := "/api/mail/queue"
	if filter != "" {
		path += "?state=" + url.QueryEscape(filter)
	}
	go func() {
		var st model.MailStatus
		var list []model.MailMessage
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := m.cl.Get(ctx, "/api/mail/status", &st)
		if err == nil {
			err = m.cl.Get(ctx, path, &list)
		}
		cancel()
		m.mw.Synchronize(func() {
			s.loading = false
			if err != nil {
				s.status = nil
				s.props.setProperties([2]string{"State", "Unknown: " + err.Error()})
				return
			}
			if filter != s.filter { // the filter changed meanwhile
				s.reload(m)
				return
			}
			s.status, s.msgs = &st, list
			s.redraw()
		})
	}()
}

func (s *mailPage) redraw() {
	st := s.status
	state := "Stopped"
	switch {
	case !st.Enabled:
		state = "Disabled (turn it on in Properties)"
	case st.Listening:
		state = "Listening on " + st.Addr
	case st.Error != "":
		state = "Error: " + st.Error
	}
	since := "the service started"
	if !st.Since.IsZero() {
		since = st.Since.Local().Format("2006-01-02 15:04")
	}
	s.props.setProperties(
		[2]string{"State", state},
		[2]string{"Messages queued", strconv.Itoa(st.Queued)},
		[2]string{"Undeliverable (kept)", strconv.Itoa(st.Failed)},
		[2]string{"Messages received", fmt.Sprintf("%d since %s", st.Accepted, since)},
		[2]string{"Recipients delivered", fmt.Sprintf("%d since %s", st.Delivered, since)},
		[2]string{"Recipients bounced", fmt.Sprintf("%d since %s", st.Bounced, since)},
	)

	keys := make([]string, len(s.msgs))
	rows := make([][]string, len(s.msgs))
	for i, msg := range s.msgs {
		var rcpts []string
		for _, r := range msg.Recipients {
			rcpts = append(rcpts, r.Address+" ("+r.State+")")
		}
		next := ""
		switch {
		case msg.State == model.MailFailed:
			next = msg.LastError
		case msg.State == model.MailSending:
			next = "sending now"
		case msg.NextAttempt != nil:
			next = msg.NextAttempt.Local().Format("2006-01-02 15:04:05")
			if msg.LastError != "" {
				next += " — " + msg.LastError
			}
		default:
			next = msg.LastError
		}
		keys[i] = msg.ID
		rows[i] = []string{msg.ReceivedAt.Local().Format("2006-01-02 15:04:05"), msg.From, strings.Join(rcpts, ", "),
			msg.Subject, strconv.Itoa(msg.Attempts), msg.State, next}
	}
	s.queue.set(keys, rows)
	s.queueLabel.SetText(map[string]string{"": "Queue: all messages", model.MailQueued: "Queue: queued messages",
		model.MailFailed: "Queue: undeliverable messages"}[s.filter] + fmt.Sprintf(" (%d)", len(s.msgs)))
	s.enable()
}

func (s *mailPage) enable() {
	if s.showAll == nil {
		return
	}
	setEnabled(s.filter != "", s.showAll)
	setEnabled(s.filter != model.MailQueued, s.showQueued)
	setEnabled(s.filter != model.MailFailed, s.showFailed)
	setEnabled(s.queue.selected() != "", s.retry, s.del)
	setEnabled(s.status != nil && s.status.Queued > 0, s.retryAll) // failed mail is retried one message at a time
	setEnabled(s.status != nil && s.status.Enabled, s.test)
}

func (s *mailPage) selected() *model.MailMessage {
	id := s.queue.selected()
	for i := range s.msgs {
		if s.msgs[i].ID == id {
			return &s.msgs[i]
		}
	}
	return nil
}

func (s *mailPage) retrySelected(m *manager) {
	msg := s.selected()
	if msg == nil {
		return
	}
	id := msg.ID
	m.do("Retrying the message", func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/mail/queue/"+url.PathEscape(id)+"/retry", nil, nil)
	})
}

func (s *mailPage) retryEverything(m *manager) {
	m.do("Retrying the queue", func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/mail/queue/retry", nil, nil)
	})
}

func (s *mailPage) deleteSelected(m *manager) {
	msg := s.selected()
	if msg == nil {
		return
	}
	var to []string
	for _, r := range msg.Recipients {
		to = append(to, r.Address)
	}
	if !m.confirm("Delete message", fmt.Sprintf("Delete the message from %s to %s? It is not delivered to the recipients who have not received it yet.",
		msg.From, strings.Join(to, ", "))) {
		return
	}
	id := msg.ID
	m.do("Deleting the message", func(ctx context.Context) error {
		return m.cl.Delete(ctx, "/api/mail/queue/"+url.PathEscape(id))
	})
}

func (s *mailPage) sendTest(m *manager) {
	to, ok := inputDialog(m.mw, "Send test e-mail", "Send a test message to (e-mail address):", "", false)
	if to = strings.TrimSpace(to); !ok || to == "" {
		return
	}
	m.do("Sending a test e-mail", func(ctx context.Context) error {
		var msg model.MailMessage
		err := m.cl.Post(ctx, "/api/mail/test", model.MailTest{To: to}, &msg)
		if err == nil {
			m.mw.Synchronize(func() {
				walk.MsgBox(m.mw, "Send test e-mail", "The test message to "+to+" is in the queue. Its delivery, or the reason it fails, shows in the queue.",
					walk.MsgBoxIconInformation)
			})
		}
		return err
	})
}

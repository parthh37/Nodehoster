package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- SMTP E-mail: the page of NodeHoster's own, built-in SMTP server (it
// does not use the Windows SMTP service)

var (
	mailFilters     = []string{"", model.MailQueued, model.MailFailed}
	mailFilterNames = []string{"All messages", "Queued", "Undeliverable"}
)

type mailPage struct {
	page
	queue   table
	status  *model.MailStatus
	msgs    []model.MailMessage
	filter  string // "" (all) | queued | failed
	loading bool
	bar     infoBar
	show    *walk.ComboBox
	find    *walk.LineEdit
	count   *walk.Label

	state, queued, failed, received, delivered, bounced statCard

	props, test, health, refresh, retryAll, retry, save, del *command
}

func (s *mailPage) init(m *manager) *page {
	s.icon = desktop.IconMail
	s.title = func() string { return "SMTP E-mail" }
	s.subtitle = func() string {
		return "NodeHoster's own send-only SMTP server: applications send through it, it delivers"
	}
	s.search = func() *walk.LineEdit { return s.find }
	// The queue changes by itself, so it is read on every refresh while
	// the page is shown, not only when the page is opened.
	s.update = func() { s.reload(m) }
	s.load = func() { s.reload(m) }
	s.props = newCommand("Properties…", desktop.IconSettings, func() { mailPropertiesDialog(m) })
	s.test = newCommand("Send a test e-mail…", desktop.IconSend, func() { s.sendTest(m) })
	s.health = newCommand("Check deliverability…", desktop.IconCheckList, func() { mailHealthDialog(m) })
	s.refresh = newCommand("Refresh", desktop.IconRefresh, func() { m.refresh(true) })
	s.retryAll = newCommand("Retry all queued", desktop.IconRestart, func() { s.retryEverything(m) })
	s.retry = newCommand("Retry now", desktop.IconRestart, func() { s.retrySelected(m) })
	s.save = newCommand("Save as .eml…", desktop.IconSave, func() { s.saveSelected(m) })
	s.del = newCommand("Delete…", desktop.IconRemove, func() { s.deleteSelected(m) })
	s.queue.onSelect = s.enable
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
	s.queue.icon = func(row, col int) walk.Image {
		if row >= len(s.msgs) {
			return nil
		}
		switch {
		case col == 0:
			return img(desktop.IconMail)
		case col == 5 && s.msgs[row].State == model.MailFailed:
			return img(desktop.IconError)
		case col == 5 && s.msgs[row].State == model.MailSending:
			return img(desktop.IconSend)
		case col == 5:
			return img(desktop.IconClock)
		}
		return nil
	}
	return &s.page
}

func (s *mailPage) content(m *manager) []Widget {
	return []Widget{
		s.bar.widget(),
		cards(s.state.widget(desktop.IconPower, "Server", false), s.queued.widget(desktop.IconMailQueue, "Queued", false),
			s.failed.widget(desktop.IconError, "Undeliverable", false)),
		cards(s.received.widget(desktop.IconDownload, "Received", false), s.delivered.widget(desktop.IconSend, "Delivered", false),
			s.bounced.widget(desktop.IconBan, "Bounced", false)),
		searchRow(&s.find, &s.count, "Search senders, recipients, subjects", &s.queue,
			Label{Text: "Show:"},
			ComboBox{AssignTo: &s.show, Model: mailFilterNames, CurrentIndex: 0, OnCurrentIndexChanged: func() {
				s.setFilter(m, mailFilters[max(s.show.CurrentIndex(), 0)])
			}}),
		s.queue.viewWith(tableOpts{name: "mailQueue", sortable: true, onDelete: s.del.trigger,
			menu: menu(s.retry, s.save, nil, s.del)},
			col("Received", 140), col("From", 170), col("Recipients", 250), col("Subject", 190),
			colR("Attempts", 70), col("State", 90), col("Next attempt / last error", 280)),
		hint("Applications send through this server over SMTP, or by dropping .eml files in the pickup folder. It relays only; it does not accept mail for mailboxes."),
	}
}

func (s *mailPage) actionsPane(m *manager) []Widget {
	return pane(
		"SMTP E-mail", s.props, s.test, s.health, s.refresh,
		"Queue", s.retryAll,
		"Selected message", s.retry, s.save, s.del,
	)
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
				s.bar.show(barError, "The mail server's state could not be read: "+err.Error(), "Retry", func() { s.reload(m) })
				s.enable()
				return
			}
			if filter != s.filter { // the filter changed meanwhile
				s.reload(m)
				return
			}
			s.status, s.msgs = &st, list
			s.redraw(m)
		})
	}()
}

func (s *mailPage) redraw(m *manager) {
	st := s.status
	switch {
	case !st.Enabled:
		s.bar.show(barInfo, "The SMTP server is turned off: applications cannot send mail through it.", "Turn it on in Properties…", s.props.trigger)
		s.state.set("Off", "turn it on in Properties", colorMuted)
	case st.Listening:
		s.bar.hide()
		s.state.set("Listening", st.Addr, colorOK)
	case st.Error != "":
		s.bar.show(barError, "The SMTP server is not listening: "+st.Error, "Properties…", s.props.trigger)
		s.state.set("Error", st.Error, colorError)
	default:
		s.bar.hide()
		s.state.set("Stopped", "", colorMuted)
	}
	since := "since the service started"
	if !st.Since.IsZero() {
		since = "since " + st.Since.Local().Format("2006-01-02 15:04")
	}
	s.queued.set(strconv.Itoa(st.Queued), "waiting to be delivered", 0)
	failedColor := walk.Color(0)
	if st.Failed > 0 {
		failedColor = colorError
	}
	s.failed.set(strconv.Itoa(st.Failed), "kept for inspection", failedColor)
	s.received.set(strconv.FormatInt(st.Accepted, 10), "messages "+since, 0)
	s.delivered.set(strconv.FormatInt(st.Delivered, 10), "recipients "+since, 0)
	bouncedColor := walk.Color(0)
	if st.Bounced > 0 {
		bouncedColor = colorWarning
	}
	s.bounced.set(strconv.FormatInt(st.Bounced, 10), "recipients "+since, bouncedColor)

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
	updateCount(s.count, &s.queue)
	s.enable()
}

func (s *mailPage) enable() {
	if s.props == nil {
		return
	}
	on := s.status != nil
	sel := s.selected() != nil
	setEnabled(sel, s.retry, s.save, s.del)
	setEnabled(on && s.status.Queued > 0, s.retryAll) // failed mail is retried one message at a time
	setEnabled(on && s.status.Enabled, s.test)
}

func (s *mailPage) selected() *model.MailMessage {
	if i := s.queue.current(); i >= 0 && i < len(s.msgs) {
		return &s.msgs[i]
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

// saveSelected downloads the selected message as an .eml file, which mail
// programs open.
func (s *mailPage) saveSelected(m *manager) {
	msg := s.selected()
	if msg == nil {
		return
	}
	id := msg.ID
	dlg := walk.FileDialog{Title: "Save the message", Filter: "E-mail messages (*.eml)|*.eml", FilePath: id + ".eml"}
	if ok, _ := dlg.ShowSave(m.mw); !ok {
		return
	}
	path := dlg.FilePath
	if filepath.Ext(path) == "" {
		path += ".eml"
	}
	m.do("Saving the message", func(ctx context.Context) error {
		tmp := path + ".partial"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		_, _, err = m.cl.Download(ctx, "/api/mail/queue/"+url.PathEscape(id)+"/eml", f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp, path)
		}
		if err != nil {
			os.Remove(tmp)
		}
		return err
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
	if ask(m.mw, "Delete message", "Delete the message from "+msg.From+"?",
		fmt.Sprintf("To %s. It is not delivered to the recipients who have not received it yet.", strings.Join(to, ", ")),
		walk.TaskDialogSystemIconWarning, [2]string{"Delete", ""}) != 0 {
		return
	}
	id := msg.ID
	m.do("Deleting the message", func(ctx context.Context) error {
		return m.cl.Delete(ctx, "/api/mail/queue/"+url.PathEscape(id))
	})
}

func (s *mailPage) sendTest(m *manager) {
	to, ok := inputDialog(m.mw, "Send a test e-mail", desktop.IconSend, "Send a test message to (e-mail address):", "", false)
	if to = strings.TrimSpace(to); !ok || to == "" {
		return
	}
	m.do("Sending a test e-mail", func(ctx context.Context) error {
		var msg model.MailMessage
		err := m.cl.Post(ctx, "/api/mail/test", model.MailTest{To: to}, &msg)
		if err == nil {
			m.mw.Synchronize(func() {
				notify(m.mw, "Send a test e-mail", "The test message to "+to+" is in the queue.",
					"Its delivery, or the reason it fails, shows in the queue.", "", walk.TaskDialogSystemIconInformation)
			})
		}
		return err
	})
}

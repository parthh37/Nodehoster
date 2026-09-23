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

// healthRow is one check of the deliverability report, flattened for the
// list: the server's checks and each domain's.
type healthRow struct {
	scope string // "This server" or the domain
	check model.MailCheck
}

// mailHealthDialog runs the deliverability checks of the SMTP server
// (GET /api/mail/health) and lists what receivers will judge: this server's
// address, reverse DNS, host name, port 25 and blacklists, and the SPF, DKIM
// and DMARC records of each sending domain, with the fix for each problem.
func mailHealthDialog(m *manager) {
	const title = "Check deliverability"
	var dlg *walk.Dialog
	var domain *walk.LineEdit
	var runBtn, closeBtn, copyName, copyValue *walk.PushButton
	var status *walk.Label
	var detail *walk.TextEdit
	var rows []healthRow
	var list table
	running := false

	// Cancelled when the dialog closes: a run still in progress is
	// abandoned and its result, if it arrives, is ignored.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	current := func() *healthRow {
		if list.tv == nil {
			return nil
		}
		if i := list.tv.CurrentIndex(); i >= 0 && i < len(rows) {
			return &rows[i]
		}
		return nil
	}
	showSelected := func() {
		if detail == nil {
			return
		}
		r := current()
		dns := r != nil && r.check.FixDNS != "" && r.check.Fix != ""
		copyName.SetEnabled(dns)
		copyValue.SetEnabled(dns)
		if r == nil {
			detail.SetText("")
			return
		}
		c := r.check
		parts := []string{r.scope + " — " + c.Name + ": " + checkResult(c.Status)}
		if c.Detail != "" {
			parts = append(parts, c.Detail)
		}
		if c.Record != "" {
			parts = append(parts, "Record found:\r\n"+c.Record)
		}
		switch {
		case dns:
			parts = append(parts, "Publish at "+c.FixDNS+":\r\n"+c.Fix)
		case c.Fix != "":
			parts = append(parts, "To fix:\r\n"+c.Fix)
		}
		detail.SetText(strings.Join(parts, "\r\n\r\n"))
	}
	list.onSelect = showSelected
	list.color = func(row, col int) (walk.Color, bool) {
		if row >= len(rows) || col != 2 {
			return 0, false
		}
		switch rows[row].check.Status {
		case model.CheckFail:
			return colorError, true
		case model.CheckWarn:
			return colorWarning, true
		case model.CheckPass:
			return colorOK, true
		case model.CheckInfo:
			return colorMuted, true
		}
		return 0, false
	}
	copyFix := func(name bool) {
		if r := current(); r != nil && r.check.FixDNS != "" {
			if name {
				walk.Clipboard().SetText(r.check.FixDNS)
			} else {
				walk.Clipboard().SetText(r.check.Fix)
			}
		}
	}

	show := func(h *model.MailHealth) {
		rows = rows[:0]
		for _, c := range h.Server {
			rows = append(rows, healthRow{"This server", c})
		}
		for _, d := range h.Domains {
			for _, c := range d.Checks {
				rows = append(rows, healthRow{d.Domain, c})
			}
		}
		keys := make([]string, len(rows))
		cells := make([][]string, len(rows))
		fails, warns := 0, 0
		for i, r := range rows {
			keys[i] = strconv.Itoa(i) + "\x00" + r.scope + "\x00" + r.check.Name
			cells[i] = []string{r.scope, r.check.Name, checkResult(r.check.Status), r.check.Detail}
			switch r.check.Status {
			case model.CheckFail:
				fails++
			case model.CheckWarn:
				warns++
			}
		}
		list.set(keys, cells)

		summary := "No problems found"
		switch {
		case fails > 0 && warns > 0:
			summary = plural(fails, "problem") + ", " + plural(warns, "warning")
		case fails > 0:
			summary = plural(fails, "problem")
		case warns > 0:
			summary = "No problems, " + plural(warns, "warning")
		}
		ip := h.PublicIP
		if ip == "" {
			ip = "unknown"
		}
		delivery := "direct delivery"
		if h.Delivery == "smarthost" {
			delivery = "through a smart host"
		}
		checked := time.Now()
		if !h.CheckedAt.IsZero() {
			checked = h.CheckedAt
		}
		status.SetText(fmt.Sprintf("%s. Public IP %s, host name %s, %s; checked at %s.",
			summary, ip, h.Hostname, delivery, checked.Local().Format("15:04:05")))
		showSelected()
	}

	run := func() {
		if running {
			return
		}
		q := url.Values{}
		for _, d := range strings.FieldsFunc(strings.ToLower(domain.Text()), func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t'
		}) {
			if i := strings.LastIndexByte(d, '@'); i >= 0 { // an address was typed
				d = d[i+1:]
			}
			d = strings.TrimSuffix(d, ".")
			if !strings.Contains(d, ".") || strings.ContainsAny(d, "/:\\") {
				invalid(dlg, "Enter the domain mail is sent from, such as example.com, or leave it empty to check the configured sending domains.")
				return
			}
			q.Add("domain", d)
		}
		path := "/api/mail/health"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}

		running = true
		runBtn.SetEnabled(false)
		status.SetText("Checking… this takes up to half a minute.")
		go func() {
			var h model.MailHealth
			rctx, rcancel := context.WithTimeout(ctx, 90*time.Second)
			err := m.cl.Get(rctx, path, &h)
			rcancel()
			m.mw.Synchronize(func() {
				if ctx.Err() != nil { // the dialog was closed
					return
				}
				running = false
				runBtn.SetEnabled(true)
				if err != nil {
					status.SetText("The checks did not run.")
					m.errorBoxFor(dlg, title, err)
					return
				}
				show(&h)
			})
		}()
	}

	err := Dialog{
		AssignTo:      &dlg,
		Title:         title,
		MinSize:       Size{Width: 600, Height: 420},
		Size:          Size{Width: 800, Height: 560},
		DefaultButton: &runBtn,
		CancelButton:  &closeBtn,
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: "Checks what receiving mail servers judge: this server's address, reverse DNS, host name, port 25 and blacklists, and the SPF, DKIM and DMARC records of each sending domain."},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				Label{Text: "Extra domain:"},
				LineEdit{AssignTo: &domain, CueBanner: "example.com (the configured sending domains are always checked)"},
				PushButton{AssignTo: &runBtn, Text: "Run checks", OnClicked: run},
			}},
			Label{AssignTo: &status, Text: " "},
			list.view(nil, col("Scope", 150), col("Check", 150), col("Result", 60), col("Detail", 360)),
			TextEdit{AssignTo: &detail, ReadOnly: true, VScroll: true, MinSize: Size{Height: 110}, MaxSize: Size{Height: 160},
				Font: Font{Family: "Consolas", PointSize: 9}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				PushButton{AssignTo: &copyName, Text: "Copy record name", Enabled: false, OnClicked: func() { copyFix(true) }},
				PushButton{AssignTo: &copyValue, Text: "Copy record value", Enabled: false, OnClicked: func() { copyFix(false) }},
				HSpacer{},
				PushButton{AssignTo: &closeBtn, Text: "Close", OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}.Create(m.mw)
	if err != nil {
		walk.MsgBox(m.mw, title, err.Error(), walk.MsgBoxIconError)
		return
	}
	run()
	dlg.Run()
}

// checkResult is a check's status as shown in the Result column.
func checkResult(status string) string {
	if status == "" {
		return "?"
	}
	return strings.ToUpper(status)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

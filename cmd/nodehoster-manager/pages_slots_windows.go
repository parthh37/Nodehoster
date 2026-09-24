package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
)

// ---- deployment slots: swapping a slot into production, like the Swap
// button of an Azure App Service. The slots themselves (their settings,
// auto-swap, sticky variables) are edited in the web console's Slots tab.

// swapSlot asks which slot to swap (when the site has several), shows what
// the swap will do, and runs it: the slot's instances restart with
// production's settings, are warmed up, then take production's traffic.
func (m *manager) swapSlot(id string) {
	s := m.siteByID(id)
	if s == nil || len(s.Slots) == 0 {
		return
	}
	name := s.Name
	slot := s.Slots[0].Name
	if len(s.Slots) > 1 {
		var choices [][2]string
		for _, sl := range s.Slots {
			choices = append(choices, [2]string{sl.Name, "Runs " + orNoRelease(sl.ActiveRelease)})
		}
		i := ask(m.mw, "Swap slots", "Which slot goes into production of "+name+"?", "", walk.TaskDialogSystemIconInformation, choices...)
		if i < 0 {
			return
		}
		slot = s.Slots[i].Name
	}
	base := "/api/sites/" + url.PathEscape(id) + "/slots/" + url.PathEscape(slot)
	go func() {
		var pv model.SwapPreview
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := m.cl.Get(ctx, base+"/swap", &pv)
		cancel()
		m.mw.Synchronize(func() {
			if err != nil {
				m.errorBox("Swap slots", err)
				return
			}
			if len(pv.Blockers) > 0 {
				m.errorBox("Swap "+slot+" into production of "+name, errors.New(strings.Join(pv.Blockers, " ")))
				return
			}
			var b strings.Builder
			for _, c := range pv.Changes {
				b.WriteString("• " + c + "\n")
			}
			for _, w := range pv.Warnings {
				b.WriteString("\n⚠ " + w + "\n")
			}
			if ask(m.mw, "Swap slots", "Swap "+slot+" ("+orNoRelease(pv.SlotRelease)+") into production of "+name+"?",
				strings.TrimSpace(b.String()), walk.TaskDialogSystemIconWarning, [2]string{"Swap", ""}) != 0 {
				return
			}
			m.long("Swapping "+slot+" into production of "+name, func(ctx context.Context) error {
				return m.runSwap(ctx, id, base)
			})
		})
	}()
}

// runSwap starts a swap and waits for its result: the swap runs in the
// service, so closing the Manager does not stop it.
func (m *manager) runSwap(ctx context.Context, id, base string) error {
	var started model.SwapProgress
	if err := m.cl.Post(ctx, base+"/swap", nil, &started); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		var v model.SlotsView
		if err := m.cl.Get(ctx, "/api/sites/"+url.PathEscape(id)+"/slots", &v); err != nil {
			return err
		}
		if v.Swap != nil {
			continue
		}
		r := v.LastSwap
		if r == nil || r.StartedAt.Before(started.StartedAt) {
			return errors.New("the swap ended without a result; see the site's events")
		}
		if !r.Succeeded {
			return errors.New(r.Message + " Production is unchanged.")
		}
		return nil
	}
}

// hostWithSlot is a binding's host name, with the deployment slot it
// routes to (the web console assigns bindings to slots).
func hostWithSlot(b model.Binding) string {
	if b.Slot == "" {
		return b.Host
	}
	return strings.TrimSpace(b.Host + " (" + b.Slot + " slot)")
}

func orNoRelease(r string) string {
	if r == "" {
		return "no release"
	}
	return r
}

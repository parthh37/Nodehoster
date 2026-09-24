package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "slot list", Args: "<site>", MinArgs: 1, MaxArgs: 1,
			Summary: "List a site's deployment slots: state, release, bindings, last swap",
			Setup:   func(*flag.FlagSet) Runner { return slotList }},
		&Command{Name: "slot swap", Args: "<site> [<slot>]", MinArgs: 1, MaxArgs: 2,
			Summary: "Warm up a slot and swap it into production (asks first unless --yes)",
			Setup:   slotSwapCmd},
	)
	for _, a := range []struct{ name, summary, done string }{
		{"start", "Start a deployment slot's instances", "Started"},
		{"stop", "Stop a deployment slot's instances", "Stopped"},
		{"recycle", "Recycle a deployment slot without downtime", "Recycled"},
	} {
		register(&Command{Name: "slot " + a.name, Args: "<site> <slot>", Summary: a.summary, MinArgs: 2, MaxArgs: 2,
			Setup: func(*flag.FlagSet) Runner { return slotAction(a.name, a.done) }})
	}
}

// slotFlag declares --slot for the deployment commands.
func slotFlag(fs *flag.FlagSet) *string {
	return fs.String("slot", "", "the deployment slot (default: production)")
}

// checkSlot validates a --slot against the site ("" = production).
func checkSlot(s *localapi.Site, slot string) (string, error) {
	slot = model.NormalizeSlot(slot)
	if slot != "" && s.FindSlot(slot) == nil {
		return "", fmt.Errorf("%s has no deployment slot %q (nodehoster slot list %s shows them)", s.Name, slot, s.Name)
	}
	return slot, nil
}

// slotQuery is the ?slot= of a request, "" for production.
func slotQuery(slot string) string {
	if slot == "" {
		return ""
	}
	return "?slot=" + url.QueryEscape(slot)
}

// siteLabel names a site, or one of its slots, in messages.
func siteLabel(s *localapi.Site, slot string) string {
	if slot == "" {
		return s.Name
	}
	return s.Name + " [" + slot + "]"
}

// defaultSlot is the slot a command means when none is named: the site's
// only one.
func defaultSlot(s *localapi.Site, args []string) (string, error) {
	if len(args) > 1 {
		slot, err := checkSlot(s, args[1])
		if err == nil && slot == "" {
			err = usagef("name the slot to swap into production, not production itself")
		}
		return slot, err
	}
	switch len(s.Slots) {
	case 0:
		return "", fmt.Errorf("%s has no deployment slots; add one in the site's Slots tab or its configuration", s.Name)
	case 1:
		return s.Slots[0].Name, nil
	}
	return "", usagef("%s has several deployment slots: name the one to swap", s.Name)
}

func slotList(e *Env, args []string) error {
	s, err := e.resolveSite(args[0])
	if err != nil {
		return err
	}
	var v model.SlotsView
	raw, err := e.get(sitePath(s)+"/slots", &v)
	if err != nil || e.JSON {
		if err == nil {
			err = e.printJSON(raw)
		}
		return err
	}
	rows := make([][]string, 0, len(v.Slots))
	for _, sl := range v.Slots {
		ready := 0
		for _, in := range sl.Status.Instances {
			if in.State == "ready" {
				ready++
			}
		}
		var hosts []string
		for _, b := range sl.Bindings {
			hosts = append(hosts, b.String())
		}
		auto := ""
		if sl.AutoSwap {
			auto = "auto-swap"
		}
		rows = append(rows, []string{sl.Name, string(sl.Status.State), fmt.Sprintf("%d/%d", ready, len(sl.Status.Instances)),
			orDash(sl.Release), truncate(strings.Join(hosts, ", "), 60), auto})
	}
	e.table([]string{"SLOT", "STATE", "READY", "RELEASE", "BINDINGS", ""}, rows)
	if v.Swap != nil {
		e.printf("\nSwapping %s since %s: %s (%s).\n", v.Swap.Slot, localTime(v.Swap.StartedAt), v.Swap.Phase, v.Swap.Message)
	} else if r := v.LastSwap; r != nil {
		e.printf("\nLast swap of %s, %s: %s — %s.\n", r.Slot, localTime(r.FinishedAt), map[bool]string{true: "succeeded", false: "failed"}[r.Succeeded], r.Message)
	}
	return nil
}

func slotSwapCmd(fs *flag.FlagSet) Runner {
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	noWait := fs.Bool("no-wait", false, "return once the swap has started")
	return func(e *Env, args []string) error {
		s, err := e.resolveSite(args[0])
		if err != nil {
			return err
		}
		slot, err := defaultSlot(s, args)
		if err != nil {
			return err
		}
		base := sitePath(s) + "/slots/" + url.PathEscape(slot)
		var pv model.SwapPreview
		if _, err := e.get(base+"/swap", &pv); err != nil {
			return err
		}
		if len(pv.Blockers) > 0 {
			return fmt.Errorf("%s cannot be swapped: %s", slot, strings.Join(pv.Blockers, " "))
		}
		if !*yes {
			if !e.Interactive {
				return usagef("a swap puts %s's release into production; add --yes to confirm", slot)
			}
			fmt.Fprintf(e.Stdout, "Swap %s (%s) into production (%s) of %s:\n", slot, orDash(pv.SlotRelease), orDash(pv.ProductionRelease), s.Name)
			for _, c := range pv.Changes {
				fmt.Fprintf(e.Stdout, "  - %s\n", c)
			}
			for _, w := range pv.Warnings {
				fmt.Fprintf(e.Stdout, "  ! %s\n", w)
			}
			fmt.Fprint(e.Stdout, "Swap now? [y/N] ")
			answer, _ := bufio.NewReader(e.Stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				return errors.New("cancelled; nothing was swapped")
			}
		}
		var started model.SwapProgress
		raw, err := e.post(base+"/swap", &started)
		if err != nil {
			return err
		}
		if *noWait {
			if e.JSON {
				return e.printJSON(raw)
			}
			e.printf("Swap of %s into production of %s started. Follow it with: nodehoster slot list %s\n", slot, s.Name, s.Name)
			return nil
		}
		res, err := e.followSwap(s, started.StartedAt)
		if err != nil {
			return err
		}
		if e.JSON {
			if err := e.printJSON(res); err != nil {
				return err
			}
		}
		if !res.Succeeded {
			return fmt.Errorf("the swap of %s failed, production is unchanged: %s", slot, res.Message)
		}
		e.printf("Swapped: %s. Swap again to roll back.\n", res.Message)
		return nil
	}
}

// followSwap polls the site's slots, printing each phase of the swap (to
// stderr with --json), until the swap that started at since has ended.
func (e *Env) followSwap(s *localapi.Site, since time.Time) (*model.SwapResult, error) {
	out := e.Stdout
	if e.JSON {
		out = e.Stderr
	}
	last := ""
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		var v model.SlotsView
		if _, err := e.get(sitePath(s)+"/slots", &v); err != nil {
			return nil, err
		}
		if v.Swap != nil {
			if msg := v.Swap.Message; msg != last {
				fmt.Fprintf(out, "[%s] %s\n", time.Now().Format("15:04:05"), msg)
				last = msg
			}
		} else if r := v.LastSwap; r != nil && !r.StartedAt.Before(since) {
			return r, nil
		} else {
			return nil, errors.New("the swap ended without a result; see nodehoster events")
		}
		select {
		case <-e.Ctx.Done():
			return nil, e.Ctx.Err()
		case <-t.C:
		}
	}
}

func slotAction(action, done string) Runner {
	return func(e *Env, args []string) error {
		s, err := e.resolveSite(args[0])
		if err != nil {
			return err
		}
		slot, err := checkSlot(s, args[1])
		if err != nil {
			return err
		}
		raw, err := e.post(sitePath(s)+"/slots/"+url.PathEscape(slotOrProduction(slot))+"/"+action, nil)
		if err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(raw)
		}
		e.printf("%s %s.\n", done, siteLabel(s, slot))
		return nil
	}
}

func slotOrProduction(slot string) string {
	if slot == "" {
		return model.ProductionSlot
	}
	return slot
}

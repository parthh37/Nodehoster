package deploy

import (
	"context"
	"errors"

	"github.com/parthh37/nodehoster/internal/model"
)

// Deployment slots. A deployment is made to a slot by passing the slot's
// configuration (model.SlotSite, which sets Site.Slot): it is built with
// the slot's variables, recorded with the slot's name and activated in
// the slot. A site has one deployment at a time whatever the slot, and a
// swap reserves the site the same way, so the two never overlap.

// ErrSwapping is returned while a slot swap holds the site. It matches
// ErrBusy with errors.Is.
var ErrSwapping error = busyErr("a slot swap is in progress for this site")

// swapHolder is what a swap's reservation records in place of a
// deployment ID.
const swapHolder = "swap"

type busyErr string

func (e busyErr) Error() string        { return string(e) }
func (e busyErr) Is(target error) bool { return target == ErrBusy }

// busyError is why a site cannot take a deployment: what holds it.
func busyError(holder string) error {
	if holder == swapHolder {
		return ErrSwapping
	}
	return ErrBusy
}

// Reserve holds a site for a slot swap: deployments are refused until
// release is called. It fails with ErrBusy while a deployment runs.
func (d *Deployer) Reserve(siteID string) (release func(), err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if holder, busy := d.running[siteID]; busy {
		if holder == swapHolder {
			return nil, ErrSwapping
		}
		return nil, busyErr("a deployment is running for this site; swap when it has finished")
	}
	d.running[siteID] = swapHolder
	return func() {
		d.mu.Lock()
		if d.running[siteID] == swapHolder {
			delete(d.running, siteID)
		}
		d.mu.Unlock()
	}, nil
}

// activate points the site, or the slot its configuration is for, at a
// release.
func (d *Deployer) activate(ctx context.Context, site *model.Site, release string) error {
	if site.Slot == "" {
		return d.opts.Activate(ctx, site.ID, release)
	}
	if d.opts.ActivateSlot == nil {
		return errors.New("deployment slots are not available")
	}
	return d.opts.ActivateSlot(ctx, site.ID, site.Slot, release)
}

func (d *Deployer) finished(site *model.Site, dep *model.Deployment) {
	if d.opts.OnFinish != nil {
		snapshot := *dep
		d.opts.OnFinish(site, &snapshot)
	}
}

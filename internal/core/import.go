package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/parthh37/nodehoster/internal/importer"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
)

// ImportPreview reads a configuration to migrate from (see package
// importer). For model.ImportLocalIIS data is ignored: this server's IIS
// configuration is read.
func (c *Core) ImportPreview(source string, data []byte, filename, name, appRoot string) (model.ImportPreview, error) {
	if source == model.ImportLocalIIS {
		var err error
		if data, err = readLocalIIS(); err != nil {
			return model.ImportPreview{}, err
		}
	}
	pv, err := importer.Preview(source, data, filename, importer.Options{
		Existing: c.Sites(), Name: name, AppRoot: appRoot,
		NodeInstalled: func(v string) bool { _, err := c.Nodes.Resolve(v); return err == nil },
	})
	if err == nil && source == model.ImportLocalIIS {
		pv.Warnings = append(pv.Warnings, "IIS keeps listening on the ports of its sites: stop them in IIS Manager (or run `iisreset /stop`) before starting the imported sites, or they cannot bind.")
	}
	return pv, err
}

// readLocalIIS reads this server's applicationHost.config.
func readLocalIIS() ([]byte, error) {
	if runtime.GOOS != "windows" {
		return nil, &model.ValidationError{Field: "source", Message: "reading this server's IIS configuration is only possible on Windows; upload an applicationHost.config instead"}
	}
	windir := os.Getenv("windir")
	if windir == "" {
		windir = `C:\Windows`
	}
	path := filepath.Join(windir, "System32", "inetsrv", "config", "applicationHost.config")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &model.ValidationError{Field: "source", Message: "IIS is not installed on this server (" + path + " was not found)"}
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// ImportApply creates reviewed import drafts. It never changes an existing
// site's configuration other than adding a scheduled task to it, and never
// overwrites one: names must be free. Sites are created stopped unless
// start is set, and mounted applications before the sites mounting them.
func (c *Core) ImportApply(ctx context.Context, req model.ImportApplyRequest) model.ImportApplyResult {
	res := model.ImportApplyResult{Created: []model.ImportCreated{}, Failed: []model.ImportFailed{}}
	ids := map[string]string{} // item key -> created site id
	fail := func(it model.ImportApplyItem, name string, err error) {
		f := model.ImportFailed{Key: it.Key, Name: name, Error: err.Error()}
		var ve *model.ValidationError
		if errors.As(err, &ve) {
			f.Error, f.Field = ve.Message, ve.Field
		}
		res.Failed = append(res.Failed, f)
	}

	// Sites: those that mount others after them, as long as that makes
	// progress.
	pending := []model.ImportApplyItem{}
	for _, it := range req.Items {
		switch it.Kind {
		case model.ImportKindSite:
			if it.Site == nil {
				fail(it, it.Key, errors.New("no site in the item"))
				continue
			}
			pending = append(pending, it)
		case model.ImportKindTask:
		default:
			fail(it, it.Key, fmt.Errorf("unknown kind %q", it.Kind))
		}
	}
	var created []*model.Site
	for len(pending) > 0 {
		var next []model.ImportApplyItem
		progress := false
		for _, it := range pending {
			s := *it.Site
			s.Routing.Locations = append([]model.Location(nil), s.Routing.Locations...)
			waiting := false
			for i, l := range s.Routing.Locations {
				ref, ok := strings.CutPrefix(l.SiteID, model.ImportRef)
				if l.Kind != "site" || !ok {
					continue
				}
				if id, done := ids[ref]; done {
					s.Routing.Locations[i].SiteID = id
				} else {
					waiting = true
				}
			}
			if waiting {
				next = append(next, it)
				continue
			}
			progress = true
			site, err := c.importSite(ctx, &s)
			if err != nil {
				fail(it, s.Name, err)
				continue
			}
			ids[it.Key] = site.ID
			created = append(created, site)
			ic := model.ImportCreated{Key: it.Key, Kind: model.ImportKindSite, SiteID: site.ID, Name: site.Name}
			if site.RunsNode() && !site.Node.RunAs.Enabled {
				ic.Warning = importer.ServiceAccountWarning
			}
			res.Created = append(res.Created, ic)
		}
		if !progress {
			for _, it := range next {
				fail(it, it.Site.Name, errors.New("it mounts an application that was not imported; import that one too, or remove the location"))
			}
			break
		}
		pending = next
	}

	// Tasks, added to a site of this import or to an existing one.
	for _, it := range req.Items {
		if it.Kind != model.ImportKindTask {
			continue
		}
		if it.Task == nil {
			fail(it, it.Key, errors.New("no task in the item"))
			continue
		}
		target := it.TaskSite
		if ref, ok := strings.CutPrefix(target, model.ImportRef); ok {
			if target, ok = ids[ref]; !ok {
				fail(it, it.Task.Name, errors.New("the site that should run this task was not imported"))
				continue
			}
		}
		site, err := c.addTask(ctx, target, *it.Task)
		if err != nil {
			fail(it, it.Task.Name, err)
			continue
		}
		res.Created = append(res.Created, model.ImportCreated{Key: it.Key, Kind: model.ImportKindTask, SiteID: site.ID, Name: it.Task.Name})
	}

	if req.Start {
		for _, s := range created {
			if err := c.StartSite(s.ID); err != nil {
				for i := range res.Created {
					if res.Created[i].SiteID == s.ID && res.Created[i].Kind == model.ImportKindSite {
						w := "created, but it could not be started: " + err.Error()
						if prev := res.Created[i].Warning; prev != "" {
							w += ". " + prev
						}
						res.Created[i].Warning = w
					}
				}
			}
		}
	}
	return res
}

// importSite creates a site from an import draft: always new, never
// started here.
func (c *Core) importSite(ctx context.Context, in *model.Site) (*model.Site, error) {
	in.ID, in.ActiveRelease = "", ""
	for i := range in.Bindings {
		in.Bindings[i].ID = ""
	}
	// A draft carries no secret values of NodeHoster's: a masked value
	// here would be stored as the mask itself.
	for _, e := range nodeEnv(in) {
		if e.Value == secrets.Mask {
			return nil, &model.ValidationError{Field: "node.env", Message: fmt.Sprintf("%s has no value", e.Name)}
		}
	}
	for _, s := range c.Sites() {
		if strings.EqualFold(s.Name, strings.TrimSpace(in.Name)) {
			return nil, store.ErrDuplicateName
		}
	}
	return c.createSite(ctx, in, false)
}

func nodeEnv(s *model.Site) []model.EnvVar {
	if s.Node == nil {
		return nil
	}
	return s.Node.Env
}

// addTask adds a scheduled task to a node or worker site. The site is read
// and written under the lock, so an edit saved meanwhile is not lost.
func (c *Core) addTask(ctx context.Context, siteID string, t model.ScheduledTask) (*model.Site, error) {
	if siteID == "" {
		return nil, &model.ValidationError{Field: "taskSite", Message: "choose the site that runs this task"}
	}
	c.sitesMu.Lock()
	defer c.sitesMu.Unlock()
	existing, err := c.Site(siteID)
	if err != nil {
		return nil, &model.ValidationError{Field: "taskSite", Message: "the site that should run this task does not exist"}
	}
	if !existing.RunsNode() {
		return nil, &model.ValidationError{Field: "taskSite", Message: fmt.Sprintf("%q is not a Node.js or background worker site", existing.Name)}
	}
	s := Masked(existing) // masked secrets keep their stored values
	t.ID = ""
	s.Tasks = append(s.Tasks, t)
	return c.updateSite(ctx, siteID, s)
}

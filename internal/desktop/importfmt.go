package desktop

import (
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// ImportDetail summarizes a proposed import for a list column: where the
// site's files are and how it is reached, or when a task runs.
func ImportDetail(o model.ImportOption) string {
	if o.Kind == model.ImportKindTask && o.Task != nil {
		when := o.Task.Schedule
		if when == "" {
			when = "on demand"
		}
		return "Runs " + taskCommand(*o.Task) + ", " + when
	}
	s := o.Site
	if s == nil {
		return ""
	}
	var parts []string
	switch {
	case s.Node != nil:
		entry := s.Node.Script
		if s.Node.NpmScript != "" {
			entry = "npm run " + s.Node.NpmScript
		}
		parts = append(parts, strings.TrimSpace(s.Node.AppRoot+" "+entry))
	case s.Static != nil:
		parts = append(parts, s.Static.Root)
	case s.Proxy != nil && len(s.Proxy.Upstreams) > 0:
		parts = append(parts, "→ "+s.Proxy.Upstreams[0].URL)
	case s.Redirect != nil:
		parts = append(parts, "→ "+s.Redirect.TargetURL)
	}
	if len(s.Bindings) > 0 {
		parts = append(parts, BindingsText(s.Bindings))
	}
	return strings.Join(parts, " — ")
}

func taskCommand(t model.ScheduledTask) string {
	if t.NpmScript != "" {
		return "npm run " + t.NpmScript
	}
	return t.Script
}

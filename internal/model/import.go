package model

// Importing sites from another server: IIS (applicationHost.config, with
// iisnode applications), a single iisnode web.config, or PM2. A preview
// proposes complete site drafts and explains what was and was not
// converted; nothing is created until the reviewed drafts are applied.

// Import sources.
const (
	ImportIIS       = "iis"       // an uploaded applicationHost.config
	ImportLocalIIS  = "local-iis" // this server's applicationHost.config (Windows)
	ImportWebConfig = "webconfig" // an iisnode application's web.config
	ImportPM2       = "pm2"       // ecosystem.config.js/.cjs/.json, `pm2 jlist` or `pm2 prettylist`
)

// ImportNote explains one aspect of a conversion.
type ImportNote struct {
	Level string `json:"level"` // converted | approximated | skipped
	Text  string `json:"text"`
}

const (
	NoteConverted    = "converted"
	NoteApproximated = "approximated"
	NoteSkipped      = "skipped"
)

// ImportOption is one way of importing an item: a site, or a scheduled
// task added to a site.
type ImportOption struct {
	Label string         `json:"label"`          // "Node.js application", "Background worker", "Scheduled task"
	Kind  string         `json:"kind"`           // site | task
	Site  *Site          `json:"site,omitempty"` // kind site: the complete draft
	Task  *ScheduledTask `json:"task,omitempty"` // kind task
	// TaskSite is the site that gets the task: "import:<key>" for a site
	// of the same import, or the id of an existing node or worker site.
	// Empty when there was no obvious candidate: the user picks one.
	TaskSite string `json:"taskSite,omitempty"`
}

const (
	ImportKindSite = "site"
	ImportKindTask = "task"
	// ImportRef prefixes references to other items of the same import, in
	// ImportOption.TaskSite and in Location.SiteID of a draft.
	ImportRef = "import:"
)

// ImportItem is one thing found in the source (an IIS site or application,
// a PM2 app) with the ways it can be imported.
type ImportItem struct {
	Key       string         `json:"key"`       // unique within the preview
	Source    string         `json:"source"`    // what it was, for display: `IIS site "Shop"`, `PM2 app "api"`
	Options   []ImportOption `json:"options"`   // at least one
	Choice    int            `json:"choice"`    // the proposed option
	Selected  bool           `json:"selected"`  // proposed for import
	Notes     []ImportNote   `json:"notes"`     // converted / approximated / not converted
	Conflicts []string       `json:"conflicts"` // what stops the proposed option from being created as is
}

type ImportPreview struct {
	Source   string       `json:"source"`
	Items    []ImportItem `json:"items"`
	Warnings []string     `json:"warnings"` // about the source as a whole
}

// ImportApplyItem is a reviewed (possibly edited) option to create.
type ImportApplyItem struct {
	Key      string         `json:"key"`
	Kind     string         `json:"kind"`
	Site     *Site          `json:"site,omitempty"`
	Task     *ScheduledTask `json:"task,omitempty"`
	TaskSite string         `json:"taskSite,omitempty"`
}

type ImportApplyRequest struct {
	Source string            `json:"source,omitempty"` // for the audit log
	Items  []ImportApplyItem `json:"items"`
	Start  bool              `json:"start"` // start the created sites (otherwise they are left stopped)
}

type ImportCreated struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	SiteID  string `json:"siteId"` // the created site, or the site that got the task
	Name    string `json:"name"`   // of the site or the task
	Warning string `json:"warning,omitempty"`
}

type ImportFailed struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Error string `json:"error"`
	Field string `json:"field,omitempty"`
}

type ImportApplyResult struct {
	Created []ImportCreated `json:"created"`
	Failed  []ImportFailed  `json:"failed"`
}

// Package importer turns the configuration of another hosting setup into
// NodeHoster site drafts: IIS (applicationHost.config, with iisnode, URL
// Rewrite and ARR), a single iisnode web.config, or PM2. It only reads
// text: it never runs anything from what it is given, and it never
// imports passwords. Every draft comes with notes saying what was
// converted, what was approximated and what was left out.
package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

type Options struct {
	Existing []*model.Site // the sites on this server, for conflicts
	CPUs     int           // for "one process per CPU"; default runtime.NumCPU()
	// ReadFile reads a web.config next to an IIS site. Default: the local
	// file system, files of at most 1 MB.
	ReadFile func(path string) ([]byte, error)
	// ReadDir lists the names in an IIS site's folder, to recognize
	// ASP.NET (bin\*.dll, App_Data) and pages of other languages. Default:
	// the local file system.
	ReadDir func(path string) ([]string, error)
	// Getenv expands %VARIABLES% in IIS physical paths. Default: this
	// machine's environment for the local IIS, Windows defaults otherwise.
	Getenv func(string) string
	// NodeInstalled reports whether a Node.js version is installed here.
	NodeInstalled func(version string) bool

	// A single web.config says nothing about where the application is or
	// what to call it; the upload form may.
	Name, AppRoot string
}

func (o *Options) defaults(source string) {
	if o.CPUs <= 0 {
		o.CPUs = runtime.NumCPU()
	}
	if o.ReadFile == nil {
		o.ReadFile = readSmallFile
	}
	if o.ReadDir == nil {
		o.ReadDir = readDirNames
	}
	if o.Getenv == nil {
		if source == model.ImportLocalIIS {
			o.Getenv = os.Getenv
		} else {
			o.Getenv = windowsDefaults
		}
	}
	if o.NodeInstalled == nil {
		o.NodeInstalled = func(string) bool { return true }
	}
}

func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.IsDir() || st.Size() > 1<<20 {
		return nil, errors.New("not a readable configuration file")
	}
	return os.ReadFile(path)
}

// readDirNames lists a folder, up to a few thousand names: enough to see
// what kind of site it is.
func readDirNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	names, err := f.Readdirnames(5000)
	if len(names) > 0 {
		return names, nil
	}
	return nil, err
}

// windowsDefaults resolves the variables an applicationHost.config
// usually contains, for a file uploaded from another machine.
func windowsDefaults(name string) string {
	switch strings.ToLower(name) {
	case "systemdrive":
		return "C:"
	case "systemroot", "windir":
		return `C:\Windows`
	case "programfiles":
		return `C:\Program Files`
	case "programfiles(x86)":
		return `C:\Program Files (x86)`
	case "programdata", "allusersprofile":
		return `C:\ProgramData`
	}
	return ""
}

// Preview reads a configuration and proposes what to create.
func Preview(source string, data []byte, filename string, opts Options) (model.ImportPreview, error) {
	opts.defaults(source)
	pv := model.ImportPreview{Source: source}
	var err error
	switch source {
	case model.ImportIIS, model.ImportLocalIIS:
		err = previewIIS(&pv, data, opts)
	case model.ImportWebConfig:
		err = previewWebConfig(&pv, data, opts)
	case model.ImportPM2:
		err = previewPM2(&pv, data, filename, opts)
	default:
		return pv, &model.ValidationError{Field: "source", Message: "must be iis, local-iis, webconfig or pm2"}
	}
	if err != nil {
		var ve *model.ValidationError
		if errors.As(err, &ve) {
			return pv, err
		}
		return pv, &model.ValidationError{Field: "file", Message: err.Error()}
	}
	serviceAccountNotes(&pv)
	checkConflicts(&pv, opts.Existing)
	if pv.Items == nil {
		pv.Items = []model.ImportItem{}
	}
	if pv.Warnings == nil {
		pv.Warnings = []string{}
	}
	return pv, nil
}

// ServiceAccountWarning is said of every imported Node.js site that has no
// Run as. IIS ran it as a low-privilege application pool identity and PM2
// as the user who started it; NodeHoster runs it as its own service
// account unless told otherwise, which an import must not hide.
const ServiceAccountWarning = "It runs as the NodeHoster service account (LocalSystem), which has full control of this server: set Run as on the site's Settings tab to a low-privilege account before starting it."

func serviceAccountNotes(pv *model.ImportPreview) {
	for i := range pv.Items {
		it := &pv.Items[i]
		for _, o := range it.Options {
			if o.Kind == model.ImportKindSite && o.Site != nil && o.Site.RunsNode() && (o.Site.Node == nil || !o.Site.Node.RunAs.Enabled) {
				it.Notes = append(it.Notes, model.ImportNote{Level: model.NoteApproximated, Text: ServiceAccountWarning})
				break
			}
		}
	}
}

// ---- building items

// item collects the notes of one thing being converted.
type item struct {
	model.ImportItem
}

func newItem(key, source string) *item {
	return &item{model.ImportItem{Key: key, Source: source, Selected: true, Notes: []model.ImportNote{}, Conflicts: []string{}}}
}

func (it *item) note(level, format string, a ...any) {
	it.Notes = append(it.Notes, model.ImportNote{Level: level, Text: fmt.Sprintf(format, a...)})
}
func (it *item) converted(f string, a ...any)    { it.note(model.NoteConverted, f, a...) }
func (it *item) approximated(f string, a ...any) { it.note(model.NoteApproximated, f, a...) }
func (it *item) skipped(f string, a ...any)      { it.note(model.NoteSkipped, f, a...) }

func (it *item) addSite(label string, s *model.Site) {
	it.Options = append(it.Options, model.ImportOption{Label: label, Kind: model.ImportKindSite, Site: s})
}

// ---- names and variables

var nameCleanRe = regexp.MustCompile(`[^A-Za-z0-9 ._-]+`)

// siteName makes a valid site name out of anything.
func siteName(s string) string {
	s = strings.TrimSpace(nameCleanRe.ReplaceAllString(s, "-"))
	s = strings.TrimLeft(s, " ._-")
	if len(s) > 64 {
		s = strings.TrimSpace(s[:64])
	}
	if s == "" {
		s = "imported"
	}
	return s
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// secretName guesses which variables hold secrets, so they are stored
// encrypted and masked. Better one masked too many than a password shown
// in the console.
var secretName = regexp.MustCompile(`(?i)(key|secret|passw|pwd|token|connectionstring|conn_?str|credential|private|auth|dsn|salt)`)

// envVar converts one variable; ok is false for names Node.js programs
// cannot read as process.env.NAME the usual way.
func envVar(name, value string) (model.EnvVar, bool) {
	if !envNameRe.MatchString(name) {
		return model.EnvVar{}, false
	}
	secret := secretName.MatchString(name) || credentialsInURL.MatchString(value)
	return model.EnvVar{Name: name, Value: value, Secret: secret && value != ""}, true
}

// credentialsInURL finds a password in a connection URL
// (postgres://user:password@host/db).
var credentialsInURL = regexp.MustCompile(`://[^/@\s:]+:[^/@\s]+@`)

var versionRe = regexp.MustCompile(`(?i)(?:^|[\\/v@-])v?(\d{1,2}\.\d{1,3}\.\d{1,3})(?:[\\/]|$)`)

// nodeVersionIn finds a Node.js version in a path such as
// C:\nvm\v18.17.0\node.exe or /home/u/.nvm/versions/node/v20.11.1/bin/node.
func nodeVersionIn(path string) string {
	if m := versionRe.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return ""
}

func (it *item) nodeVersion(n *model.NodeConfig, v, from string, opts Options) {
	if v == "" {
		return
	}
	n.NodeVersion = v
	if opts.NodeInstalled(v) {
		it.converted("Node.js %s (from %s).", v, from)
	} else {
		it.approximated("Node.js %s (from %s) is not installed on this server: install it on the Node.js page before starting the site, or pick another version.", v, from)
	}
}

// splitArgs splits a command line: spaces separate, double or single
// quotes group.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quote := rune(0)
	has := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, has = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// ---- conflicts

// checkConflicts validates each item's proposed option as it would be
// created, against the existing sites and the items before it. An item
// with a conflict is not selected by default: the user fixes it first.
func checkConflicts(pv *model.ImportPreview, existing []*model.Site) {
	names := map[string]bool{}
	var others []*model.Site
	for _, s := range existing {
		names[strings.ToLower(s.Name)] = true
		others = append(others, s)
	}
	for i := range pv.Items {
		it := &pv.Items[i]
		if it.Conflicts == nil {
			it.Conflicts = []string{}
		}
		if it.Choice < 0 || it.Choice >= len(it.Options) {
			it.Choice = 0
		}
		opt := it.Options[it.Choice]
		switch opt.Kind {
		case model.ImportKindSite:
			s := cloneSite(opt.Site)
			s.ID = "import-" + it.Key
			s.ApplyDefaults()
			if names[strings.ToLower(s.Name)] {
				it.Conflicts = append(it.Conflicts, fmt.Sprintf("a site named %q already exists", s.Name))
			}
			if err := s.Validate(); err != nil {
				it.Conflicts = append(it.Conflicts, validationText(err))
			}
			if err := model.ValidateBindings(s, others); err != nil {
				it.Conflicts = append(it.Conflicts, validationText(err))
			}
			if it.Selected {
				names[strings.ToLower(s.Name)] = true
				others = append(others, s)
			}
		case model.ImportKindTask:
			if opt.TaskSite == "" {
				it.Conflicts = append(it.Conflicts, "choose the site that runs this task")
			}
			host := &model.Site{Name: "x", Type: model.SiteWorker, Node: &model.NodeConfig{AppRoot: "x", Script: "x"}, Tasks: []model.ScheduledTask{*opt.Task}}
			host.ApplyDefaults()
			if err := host.Validate(); err != nil {
				it.Conflicts = append(it.Conflicts, validationText(err))
			}
		}
		if len(it.Conflicts) > 0 {
			it.Selected = false
		}
	}
}

func validationText(err error) string {
	var ve *model.ValidationError
	if errors.As(err, &ve) {
		return ve.Field + ": " + ve.Message
	}
	return err.Error()
}

func cloneSite(s *model.Site) *model.Site {
	b, _ := json.Marshal(s)
	var out model.Site
	json.Unmarshal(b, &out)
	return &out
}

// nodeSite is a node site draft with NodeHoster's defaults.
func nodeSite(name, appRoot, script string) *model.Site {
	s := &model.Site{Name: siteName(name), Type: model.SiteNode, AutoStart: true,
		Node: &model.NodeConfig{AppRoot: appRoot, Script: script, AgentEnabled: true, Instances: 1}}
	s.Routing.Compression, s.Routing.WebSockets, s.Routing.AccessLog = true, true, true
	return s
}

func uniqueKey(seen map[string]bool, key string) string {
	k := key
	for i := 2; seen[k]; i++ {
		k = fmt.Sprintf("%s-%d", key, i)
	}
	seen[k] = true
	return k
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

package desktop

import "github.com/parthh37/nodehoster/internal/model"

// SiteKindText names a site's type with its runtime, for lists: "Python
// application", "Background worker (Bun)"; Node.js sites and the other
// types read as SiteTypeText.
func SiteKindText(site *model.Site) string {
	if !site.RunsNode() || site.Node == nil {
		return SiteTypeText(site.Type)
	}
	rt := site.Node.RuntimeName()
	switch {
	case rt == model.RuntimeNode:
		return SiteTypeText(site.Type)
	case site.Type == model.SiteWorker:
		return "Background worker (" + model.RuntimeLabel(rt) + ")"
	case rt == model.RuntimeCustom:
		return "Custom command"
	}
	return model.RuntimeLabel(rt) + " application"
}

// RuntimeText is a node or worker site's runtime with the version it
// pins: "Node.js 22.11.0", "Python 3.12", "Bun (server default)",
// "Custom command"; "–" for other sites.
func RuntimeText(site *model.Site) string {
	if !site.RunsNode() || site.Node == nil {
		return "–"
	}
	n := site.Node
	rt := n.RuntimeName()
	label := model.RuntimeLabel(rt)
	v := n.RuntimeVersion
	if rt == model.RuntimeNode {
		v = n.NodeVersion
	}
	switch {
	case rt == model.RuntimeCustom:
		return label
	case v != "":
		return label + " " + v
	}
	return label + " (server default)"
}

// RuntimeOptions are the runtimes the site dialogs offer, in model.Runtimes
// order, as labels.
func RuntimeOptions() []string {
	out := make([]string, len(model.Runtimes))
	for i, rt := range model.Runtimes {
		out[i] = model.RuntimeLabel(rt)
	}
	return out
}

// EntryHint is what the entry field of a site's dialog asks for, per
// runtime: its label and an example.
func EntryHint(rt string) (label, example string) {
	switch rt {
	case model.RuntimeBun:
		return "Entry script:", "index.ts"
	case model.RuntimeDeno:
		return "Entry script:", "main.ts"
	case model.RuntimePython:
		return "Python script:", "app.py"
	case model.RuntimeDotnet:
		return "Application (.dll or .exe):", `publish\MyApp.dll`
	case model.RuntimeCustom:
		return "Program:", `C:\tools\server.exe`
	}
	return "Entry script:", "server.js"
}

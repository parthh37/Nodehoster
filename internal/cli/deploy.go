package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "deploy", Args: "<site>", MinArgs: 1, MaxArgs: 1,
			Summary: "Deploy --zip <file> or --git [--branch b], showing the log",
			Setup:   deployCmd},
		&Command{Name: "rollback", Args: "<site> [<release-id>]", MinArgs: 1, MaxArgs: 2,
			Summary: "Activate an earlier release (default: the previous good one)",
			Setup:   func(*flag.FlagSet) Runner { return rollback }},
		&Command{Name: "releases", Args: "<site>", MinArgs: 1, MaxArgs: 1,
			Summary: "List a site's deployments (* = active)",
			Setup:   func(*flag.FlagSet) Runner { return releases }},
	)
}

func deployCmd(fs *flag.FlagSet) Runner {
	zipFile := fs.String("zip", "", "the .zip archive to deploy")
	git := fs.Bool("git", false, "deploy from the site's git repository")
	branch := fs.String("branch", "", "git branch (default: the site's)")
	noWait := fs.Bool("no-wait", false, "return once the deployment has started")
	return func(e *Env, args []string) error {
		if (*zipFile == "") == !*git {
			return usagef("use either --zip <file> or --git")
		}
		if *branch != "" && !*git {
			return usagef("--branch goes with --git")
		}
		s, err := e.resolveSite(args[0])
		if err != nil {
			return err
		}
		var dep model.Deployment
		if *git {
			var body any
			if *branch != "" {
				body = map[string]string{"branch": *branch}
			}
			err = e.Client.Post(e.Ctx, sitePath(s)+"/deploy/git", body, &dep)
		} else {
			f, ferr := os.Open(*zipFile)
			if ferr != nil {
				return ferr
			}
			defer f.Close()
			err = e.Client.UploadFile(e.Ctx, sitePath(s)+"/deploy/zip", "file", filepath.Base(*zipFile), f, &dep)
		}
		if err != nil {
			return err
		}
		if *noWait {
			if e.JSON {
				return e.printJSON(dep)
			}
			e.printf("Deployment %s of %s started. Follow it with: nodehoster releases %s\n", dep.ID, s.Name, s.Name)
			return nil
		}
		final, err := e.followDeployment(s, dep.ID)
		if err != nil {
			return err
		}
		if e.JSON {
			if err := e.printJSON(final); err != nil {
				return err
			}
		}
		if final.Status != "succeeded" {
			return fmt.Errorf("deployment %s of %s %s: %s", final.ID, s.Name, final.Status, final.Message)
		}
		e.printf("Deployment %s of %s succeeded.\n", final.ID, s.Name)
		return nil
	}
}

// followDeployment prints a deployment's log as it is written (to stderr
// with --json, which keeps stdout for the result) and returns the
// deployment once it has finished.
func (e *Env) followDeployment(s *localapi.Site, depID string) (*model.Deployment, error) {
	out := e.Stdout
	if e.JSON {
		out = e.Stderr
	}
	var final *model.Deployment
	err := e.Client.Stream(e.Ctx, sitePath(s)+"/deployments/"+url.PathEscape(depID)+"/log/stream", func(event string, data []byte) {
		switch event {
		case "log":
			var chunk string
			if json.Unmarshal(data, &chunk) == nil {
				io.WriteString(out, chunk)
			}
		case "done":
			var d model.Deployment
			if json.Unmarshal(data, &d) == nil {
				final = &d
			}
		}
	})
	if final != nil {
		return final, nil
	}
	if err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
		err = errors.New("the log stream ended before the deployment finished")
	}
	return nil, fmt.Errorf("following deployment %s: %w", depID, err)
}

func (e *Env) deployments(s *localapi.Site) (json.RawMessage, []model.Deployment, error) {
	var list []model.Deployment
	raw, err := e.get(sitePath(s)+"/deployments", &list)
	return raw, list, err
}

// previousRelease is the newest successful deployment older than the
// active one whose files are still there: what "roll back" means.
func previousRelease(active string, list []model.Deployment) (*model.Deployment, error) {
	if active == "" {
		return nil, errors.New("the site runs from its configured folder, not a release; name the release to activate (nodehoster releases <site> lists them)")
	}
	seen := false
	for i := range list { // newest first, as the API lists them
		d := &list[i]
		switch {
		case d.ID == active:
			seen = true
		case seen && d.Status == "succeeded" && d.ReleaseDir != "":
			return d, nil
		}
	}
	if !seen {
		return nil, fmt.Errorf("the active release %s is not among the site's deployments; name the release to activate", active)
	}
	return nil, errors.New("there is no earlier successful release to roll back to")
}

func rollback(e *Env, args []string) error {
	s, err := e.resolveSite(args[0])
	if err != nil {
		return err
	}
	var target string
	if len(args) > 1 {
		target = args[1]
	} else {
		_, list, err := e.deployments(s)
		if err != nil {
			return err
		}
		prev, err := previousRelease(s.ActiveRelease, list)
		if err != nil {
			return err
		}
		target = prev.ID
	}
	var dep model.Deployment
	raw, err := e.post(sitePath(s)+"/deployments/"+url.PathEscape(target)+"/activate", &dep)
	if err != nil {
		return err
	}
	if e.JSON {
		return e.printJSON(raw)
	}
	e.printf("%s now runs release %s (deployed %s from %s).\n", s.Name, dep.ID, localTime(dep.StartedAt), dep.Source)
	return nil
}

func releases(e *Env, args []string) error {
	s, err := e.resolveSite(args[0])
	if err != nil {
		return err
	}
	raw, list, err := e.deployments(s)
	if err != nil || e.JSON {
		if err == nil {
			err = e.printJSON(raw)
		}
		return err
	}
	if len(list) == 0 {
		e.printf("%s has no deployments.\n", s.Name)
		return nil
	}
	rows := make([][]string, 0, len(list))
	for _, d := range list {
		mark := ""
		if d.ID == s.ActiveRelease {
			mark = "*"
		}
		took := "-"
		if d.FinishedAt != nil {
			took = d.FinishedAt.Sub(d.StartedAt).Round(time.Second).String()
		}
		detail := strings.TrimSpace(shortCommit(d.Commit) + " " + firstLine(d.Message))
		rows = append(rows, []string{mark, d.ID, d.Source, d.Status, localTime(d.StartedAt), took, d.User, truncate(detail, 60)})
	}
	e.table([]string{"", "RELEASE", "SOURCE", "STATUS", "STARTED", "TOOK", "USER", "DETAILS"}, rows)
	return nil
}

func shortCommit(c string) string {
	if len(c) > 8 {
		return c[:8]
	}
	return c
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

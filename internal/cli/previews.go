package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "preview list", Args: "<site>", MinArgs: 1, MaxArgs: 1,
			Summary: "List a site's preview deployments (one per pull request or branch)",
			Setup:   func(*flag.FlagSet) Runner { return previewList }},
		&Command{Name: "preview deploy", Args: "<site> <branch>", MinArgs: 2, MaxArgs: 2,
			Summary: "Deploy a branch as a preview of the site, or deploy its preview again",
			Setup:   func(*flag.FlagSet) Runner { return previewDeploy }},
		&Command{Name: "preview redeploy", Args: "<site> <preview>", MinArgs: 2, MaxArgs: 2,
			Summary: "Deploy a preview's head again",
			Setup:   func(*flag.FlagSet) Runner { return previewRedeploy }},
		&Command{Name: "preview delete", Args: "<site> <preview>", MinArgs: 2, MaxArgs: 2,
			Summary: "Delete a preview with its releases, logs and certificate (--yes)",
			Setup:   previewDeleteCmd},
	)
}

func (e *Env) previewList(s *localapi.Site) (raw json.RawMessage, list []model.PreviewView, err error) {
	raw, err = e.get(sitePath(s)+"/previews", &list)
	return raw, list, err
}

// resolvePreview finds a preview of a site by ID, key ("pr:42"), pull
// request number ("42", "#42"), host or its first label ("pr-42"), site
// name or branch.
func (e *Env) resolvePreview(s *localapi.Site, ref string) (*model.PreviewView, error) {
	_, list, err := e.previewList(s)
	if err != nil {
		return nil, err
	}
	n, numErr := strconv.Atoi(strings.TrimPrefix(ref, "#"))
	match := []func(p *model.PreviewView) bool{
		func(p *model.PreviewView) bool { return p.ID == ref || p.Preview.Key == ref },
		func(p *model.PreviewView) bool {
			return numErr == nil && p.Preview.Kind == model.PreviewPR && p.Preview.Number == n
		},
		func(p *model.PreviewView) bool {
			label, _, _ := strings.Cut(p.Preview.Host, ".")
			return strings.EqualFold(p.Preview.Host, ref) || strings.EqualFold(label, ref) || strings.EqualFold(p.Name, ref)
		},
		func(p *model.PreviewView) bool {
			return p.Preview.Kind == model.PreviewBranch && p.Preview.Branch == ref
		},
	}
	for _, m := range match {
		for i := range list {
			if m(&list[i]) {
				return &list[i], nil
			}
		}
	}
	return nil, fmt.Errorf("%s has no preview %q (nodehoster preview list %s shows them)", s.Name, ref, s.Name)
}

func previewList(e *Env, args []string) error {
	s, err := e.resolveSite(args[0])
	if err != nil {
		return err
	}
	raw, list, err := e.previewList(s)
	if err != nil || e.JSON {
		if err == nil {
			err = e.printJSON(raw)
		}
		return err
	}
	if len(list) == 0 {
		if s.Deploy.Previews.Enabled {
			e.printf("%s has no previews. They are created by its push webhook for pull requests and previewed branches.\n", s.Name)
		} else {
			e.printf("%s has no previews (previews are not enabled for it).\n", s.Name)
		}
		return nil
	}
	rows := make([][]string, 0, len(list))
	for _, p := range list {
		what := "branch " + p.Preview.Branch
		if p.Preview.Kind == model.PreviewPR {
			what = fmt.Sprintf("#%d %s", p.Preview.Number, p.Preview.Branch)
		}
		if p.Preview.Fork {
			what += " (fork)"
		}
		rows = append(rows, []string{truncate(what, 40), p.State, shortCommit(p.Preview.Commit), localTime(p.Preview.LastPush), p.Preview.URL, p.ID})
	}
	e.table([]string{"PREVIEW", "STATE", "COMMIT", "LAST PUSH", "URL", "ID"}, rows)
	return nil
}

func previewDeploy(e *Env, args []string) error {
	s, err := e.resolveSite(args[0])
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := e.Client.Post(e.Ctx, sitePath(s)+"/previews", map[string]string{"branch": args[1]}, &raw); err != nil {
		return err
	}
	if e.JSON {
		return e.printJSON(raw)
	}
	e.printf("The preview of branch %s of %s is being deployed. Follow it with: nodehoster preview list %s\n", args[1], s.Name, s.Name)
	return nil
}

func previewRedeploy(e *Env, args []string) error {
	s, err := e.resolveSite(args[0])
	if err != nil {
		return err
	}
	p, err := e.resolvePreview(s, args[1])
	if err != nil {
		return err
	}
	raw, err := e.post(sitePath(s)+"/previews/"+url.PathEscape(p.ID)+"/redeploy", nil)
	if err != nil {
		return err
	}
	if e.JSON {
		return e.printJSON(raw)
	}
	e.printf("%s is being deployed again. Follow it with: nodehoster preview list %s\n", p.Preview.URL, s.Name)
	return nil
}

func previewDeleteCmd(fs *flag.FlagSet) Runner {
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	return func(e *Env, args []string) error {
		s, err := e.resolveSite(args[0])
		if err != nil {
			return err
		}
		p, err := e.resolvePreview(s, args[1])
		if err != nil {
			return err
		}
		if !*yes {
			if !e.Interactive {
				return usagef("deleting a preview removes its site, releases and logs; add --yes to confirm")
			}
			fmt.Fprintf(e.Stdout, "Delete the preview %s (%s)? [y/N] ", p.Preview.URL, p.Name)
			answer, _ := bufio.NewReader(e.Stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				return errors.New("cancelled; nothing was deleted")
			}
		}
		var raw json.RawMessage
		if err := e.Client.Do(e.Ctx, http.MethodDelete, sitePath(s)+"/previews/"+url.PathEscape(p.ID), nil, &raw); err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(raw)
		}
		e.printf("The preview %s of %s is being deleted.\n", p.Preview.URL, s.Name)
		return nil
	}
}

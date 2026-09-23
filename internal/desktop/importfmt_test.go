package desktop

import (
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestImportDetail(t *testing.T) {
	for _, tc := range []struct {
		o    model.ImportOption
		want string
	}{
		{model.ImportOption{Kind: model.ImportKindSite, Site: &model.Site{Node: &model.NodeConfig{AppRoot: `C:\sites\shop`, Script: "server.js"},
			Bindings: []model.Binding{{Protocol: "http", Port: 80, Host: "shop.example.com"}}}},
			`C:\sites\shop server.js — http *:80 shop.example.com`},
		{model.ImportOption{Kind: model.ImportKindSite, Site: &model.Site{Proxy: &model.ProxyConfig{Upstreams: []model.Upstream{{URL: "http://localhost:3000"}}}}},
			"→ http://localhost:3000"},
		{model.ImportOption{Kind: model.ImportKindTask, Task: &model.ScheduledTask{NpmScript: "cleanup", Schedule: "0 3 * * *"}},
			"Runs npm run cleanup, 0 3 * * *"},
		{model.ImportOption{Kind: model.ImportKindTask, Task: &model.ScheduledTask{Script: "job.js"}}, "Runs job.js, on demand"},
	} {
		if got := ImportDetail(tc.o); got != tc.want {
			t.Errorf("ImportDetail = %q, want %q", got, tc.want)
		}
	}
}

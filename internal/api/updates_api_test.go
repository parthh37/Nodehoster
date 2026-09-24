package api

import (
	"net/http"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestUpdateEndpoints(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	e.user("operator", model.RoleOperator, false)
	operator := session(e.login("operator"))

	on := map[string]any{"auto": true, "time": "04:00", "weekdays": []int{0}}
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   any
		who    []opt
		want   int
	}{
		// Installing runs setup as SYSTEM: administrators only.
		{"operator reads", "GET", "/api/updates", nil, operator, http.StatusForbidden},
		{"operator installs", "POST", "/api/updates/install", nil, operator, http.StatusForbidden},
		{"operator turns on", "PUT", "/api/updates", on, operator, http.StatusForbidden},
		{"admin reads", "GET", "/api/updates", nil, admin, http.StatusOK},
		{"admin turns on", "PUT", "/api/updates", on, admin, http.StatusOK},
		{"bad time", "PUT", "/api/updates", map[string]any{"auto": true, "time": "4pm"}, admin, http.StatusUnprocessableEntity},
		{"bad weekday", "PUT", "/api/updates", map[string]any{"time": "04:00", "weekdays": []int{7}}, admin, http.StatusUnprocessableEntity},
		// Test builds are development builds, which never update.
		{"check in a dev build", "POST", "/api/updates/check", nil, admin, http.StatusConflict},
		{"install in a dev build", "POST", "/api/updates/install", nil, admin, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, e.do(tc.method, tc.path, tc.body, tc.who...), tc.want)
		})
	}

	st := decodeJSON[model.UpdateStatus](t, e.do("GET", "/api/updates", nil, admin...))
	if !st.Auto || st.Time != "04:00" || st.Supported || st.Reason == "" || st.Current == "" {
		t.Fatalf("status = %+v", st)
	}
	// The whole settings document carries the section too (the web console).
	s := decodeJSON[model.Settings](t, e.do("GET", "/api/settings", nil, admin...))
	if !s.Updates.Auto || s.Updates.Time != "04:00" {
		t.Fatalf("settings.updates = %+v", s.Updates)
	}
}

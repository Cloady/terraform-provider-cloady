package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var testFactories = map[string]func() (tfprotov6.ProviderServer, error){"cloady": providerserver.NewProtocol6WithError(New("test")())}

func TestProviderSchema(t *testing.T) {
	srv := providerserver.NewProtocol6(New("test")())()
	resp, err := srv.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("%s: %s", d.Summary, d.Detail)
		}
	}
	if len(resp.ResourceSchemas) != 4 || len(resp.DataSourceSchemas) != 2 {
		t.Fatalf("unexpected schemas: %d resources, %d data sources", len(resp.ResourceSchemas), len(resp.DataSourceSchemas))
	}
}

// Terraform drives the provider protocol against an isolated API. These tests
// never use a user's token or create billable resources.
func TestAccWorkspace(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 and install Terraform to run protocol tests")
	}
	mock := newMockAPI(t)
	defer mock.server.Close()
	config := func(name string) string {
		return fmt.Sprintf(`
provider "cloady" {
  token    = "test-token"
  base_url = %q
}
resource "cloady_workspace" "test" {
  slug = "demo"
  name = %q
}
data "cloady_workspace" "test" {
  slug = cloady_workspace.test.slug
}

data "cloady_regions" "test" {}
`, mock.server.URL, name)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: testFactories, CheckDestroy: func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if mock.workspace != nil {
			return fmt.Errorf("workspace remains")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config("Demo"), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("cloady_workspace.test", "id", "demo"), resource.TestCheckResourceAttr("cloady_workspace.test", "hues.0", "6"), resource.TestCheckResourceAttr("cloady_workspace.test", "hues.1", "211"), resource.TestCheckResourceAttr("data.cloady_workspace.test", "name", "Demo"), resource.TestCheckResourceAttr("data.cloady_regions.test", "regions.0.id", "eu1"))},
		{ResourceName: "cloady_workspace.test", ImportState: true, ImportStateVerify: true},
		{Config: config("Renamed"), Check: resource.TestCheckResourceAttr("cloady_workspace.test", "name", "Renamed")},
		{PreConfig: func() { mock.mu.Lock(); mock.workspace["name"] = "Drift"; mock.mu.Unlock() }, Config: config("Renamed"), Check: resource.TestCheckResourceAttr("cloady_workspace.test", "name", "Renamed")},
		{PreConfig: func() { mock.mu.Lock(); mock.workspace = nil; mock.mu.Unlock() }, Config: config("Renamed"), Check: resource.TestCheckResourceAttr("cloady_workspace.test", "id", "demo")},
	}})
}

// TestAccApp drives the app resource over the real Terraform protocol. Step two
// changes the branch and auto_deploy together in a single apply: the control
// plane used to overwrite the whole source object on a branch change and drop
// autoDeploy with it, which is why the provider once sent two PATCH requests.
func TestAccApp(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 and install Terraform to run protocol tests")
	}
	mock := newMockAPI(t)
	defer mock.server.Close()
	config := func(branch string, autoDeploy bool) string {
		return fmt.Sprintf(`
provider "cloady" {
  token    = "test-token"
  base_url = %q
}
resource "cloady_workspace" "test" {
  slug = "demo"
  name = "Demo"
}
resource "cloady_app" "api" {
  workspace = cloady_workspace.test.slug
  name      = "API"
  region    = "eu1"
  git = {
    repository_url = "https://github.com/acme/api"
    branch         = %q
    auto_deploy    = %t
  }
}
`, mock.server.URL, branch, autoDeploy)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: testFactories, CheckDestroy: func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if mock.app != nil {
			return fmt.Errorf("app remains")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config("main", true), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("cloady_app.api", "id", "demo/api/production/eu1"),
			resource.TestCheckResourceAttr("cloady_app.api", "slug", "api"),
			resource.TestCheckResourceAttr("cloady_app.api", "git.branch", "main"),
			resource.TestCheckResourceAttr("cloady_app.api", "git.auto_deploy", "true"),
			resource.TestCheckResourceAttr("cloady_app.api", "cpu_scale", "1"),
			// The create response carries the public URL, so endpoints[0] is
			// usable in an output on the very first apply.
			resource.TestCheckResourceAttr("cloady_app.api", "endpoints.0", "https://api-demo.cloady.io"),
		)},
		{Config: config("release", false), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("cloady_app.api", "git.branch", "release"),
			resource.TestCheckResourceAttr("cloady_app.api", "git.auto_deploy", "false"),
		)},
		{ResourceName: "cloady_app.api", ImportState: true, ImportStateId: "demo/api/production/eu1", ImportStateVerify: true},
	}})
}

type mockAPI struct {
	mu               sync.Mutex
	server           *httptest.Server
	workspace        map[string]any
	app              map[string]any
	variable         map[string]any
	domain           map[string]any
	appValues        map[string]variableRow
	deployments      []map[string]string
	failDeleteKey    string
	failRedeploy     bool
	redeployAttempts int
}

func newMockAPI(t *testing.T) *mockAPI {
	t.Helper()
	m := &mockAPI{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing test authentication")
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("invalid request JSON: %v", err)
			}
		}
		write := func(v any) {
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Error(err)
			}
		}
		notFound := func() {
			w.WriteHeader(404)
			write(map[string]any{"error": map[string]string{"code": "not_found", "message": "not found"}})
		}
		switch r.URL.Path {
		case "/api/regions":
			write(map[string]any{"regions": []any{map[string]any{"id": "eu1", "code": "eu1", "country": "DE", "status": "available", "freeEligible": true, "ipv4": "192.0.2.1", "ipv6": nil}}})
		case "/api/workspaces":
			if r.Method != "POST" {
				t.Errorf("unexpected %s", r.Method)
				w.WriteHeader(405)
				return
			}
			if _, sent := body["tier"]; sent {
				t.Error("workspace create must not send a billing tier")
			}
			m.workspace = map[string]any{"slug": body["slug"], "name": body["name"], "hues": body["hues"], "services": []any{}}
			w.WriteHeader(201)
			write(map[string]any{"workspace": m.workspace})
		case "/api/workspaces/demo":
			if m.workspace == nil {
				notFound()
				return
			}
			switch r.Method {
			case "GET":
				m.workspace["services"] = []any{}
				if m.app != nil {
					m.workspace["services"] = []any{m.app}
				}
				write(map[string]any{"workspace": m.workspace})
			case "PATCH":
				if body["slug"] != "demo" {
					t.Error("workspace PATCH must preserve slug")
				}
				for k, v := range body {
					m.workspace[k] = v
				}
				write(map[string]any{"workspace": m.workspace})
			case "DELETE":
				m.workspace = nil
				write(map[string]bool{"ok": true})
			default:
				t.Errorf("unexpected method %s", r.Method)
				w.WriteHeader(405)
			}
		default:
			if strings.HasPrefix(r.URL.Path, "/api/workspaces/demo/apps/") || r.URL.Path == "/api/workspaces/demo/deploy" {
				m.handleApp(t, w, r, body)
				return
			}
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			notFound()
		}
	}))
	return m
}

// handleApp serves the one create path (POST /deploy) plus the app's own
// PATCH/DELETE, mirroring the fields the provider actually sends.
func (m *mockAPI) handleApp(t *testing.T, w http.ResponseWriter, r *http.Request, body map[string]any) {
	t.Helper()
	write := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Error(err)
		}
	}
	if r.URL.Path == "/api/workspaces/demo/deploy" {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected %s on deploy", r.Method)
			w.WriteHeader(405)
			return
		}
		source, _ := body["source"].(map[string]any)
		if source["type"] == "git" {
			if _, ok := source["autoDeploy"]; ok {
				t.Error("autoDeploy belongs at the top level of the deploy body, not inside source")
			}
			if autoDeploy, ok := body["autoDeploy"].(bool); ok {
				source["autoDeploy"] = autoDeploy
			}
		}
		m.appValues = map[string]variableRow{}
		if values, ok := body["values"].(map[string]any); ok {
			for key, value := range values {
				m.appValues[key] = variableRow{ID: key, Key: key, Value: value.(string), IsSecret: strings.Contains(key, "SECRET")}
			}
		}
		m.recordDeployment()
		m.app = map[string]any{
			"slug": "api", "env": body["env"], "name": body["name"], "region": body["region"],
			"status": "deploying", "source": source, "scale": body["scale"],
			"endpoints":   []any{map[string]any{"component": "web", "url": "https://api-demo.cloady.io"}},
			"subServices": []any{}, "uptime": "", "limits": map[string]any{},
		}
		w.WriteHeader(201)
		write(map[string]any{"app": m.app})
		return
	}
	if m.app == nil {
		w.WriteHeader(404)
		write(map[string]any{"error": map[string]string{"code": "not_found", "message": "not found"}})
		return
	}
	if strings.Contains(r.URL.Path, "/vars") {
		m.handleAppValues(t, w, r, body)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/redeploy") {
		m.redeployAttempts++
		if m.failRedeploy {
			m.failRedeploy = false
			w.WriteHeader(502)
			write(map[string]string{"error": "deployment unavailable"})
			return
		}
		m.recordDeployment()
		write(map[string]any{"app": m.app})
		return
	}
	switch r.Method {
	case http.MethodPatch:
		// One request must carry every field: the API used to drop autoDeploy
		// whenever a branch change rode along with it.
		source, _ := m.app["source"].(map[string]any)
		if branch, ok := body["branch"]; ok && source != nil {
			if source["branch"] != branch {
				m.recordDeployment()
			}
			source["branch"] = branch
		}
		if autoDeploy, ok := body["autoDeploy"]; ok && source != nil {
			source["autoDeploy"] = autoDeploy
		}
		for _, key := range []string{"name", "slug", "scale"} {
			if value, ok := body[key]; ok {
				m.app[key] = value
			}
		}
		m.app["endpoints"] = []any{map[string]any{"component": "web", "url": "https://api-demo.cloady.io"}}
		write(map[string]any{"app": m.app})
	case http.MethodDelete:
		m.app = nil
		write(map[string]bool{"ok": true})
	default:
		t.Errorf("unexpected method %s on %s", r.Method, r.URL.Path)
		w.WriteHeader(405)
	}
}

func (m *mockAPI) recordDeployment() {
	values := map[string]string{}
	for key, row := range m.appValues {
		values[key] = row.Value
	}
	m.deployments = append(m.deployments, values)
}

func (m *mockAPI) handleAppValues(t *testing.T, w http.ResponseWriter, r *http.Request, body map[string]any) {
	t.Helper()
	write := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			t.Error(err)
		}
	}
	path := strings.SplitN(r.URL.Path, "/vars", 2)[1]
	if path == "" {
		if r.Method == http.MethodGet {
			rows := []variableRow{}
			for _, row := range m.appValues {
				if row.IsSecret {
					row.Value = "•••••"
				}
				rows = append(rows, row)
			}
			write(map[string]any{"vars": rows})
			return
		}
		key := body["key"].(string)
		if _, exists := m.appValues[key]; exists {
			w.WriteHeader(409)
			write(map[string]string{"error": "key taken"})
			return
		}
		secret, _ := body["isSecret"].(bool)
		m.appValues[key] = variableRow{ID: key, Key: key, Value: body["value"].(string), IsSecret: secret}
		write(map[string]any{"var": m.appValues[key]})
		return
	}
	key := strings.Split(strings.TrimPrefix(path, "/"), "/")[0]
	row, exists := m.appValues[key]
	if !exists {
		w.WriteHeader(404)
		write(map[string]string{"error": "not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !strings.HasSuffix(path, "/reveal") {
			t.Errorf("unexpected GET %s", path)
		}
		write(map[string]string{"value": row.Value})
	case http.MethodPatch:
		row.Value = body["value"].(string)
		m.appValues[key] = row
		write(map[string]any{"var": row})
	case http.MethodDelete:
		if key == m.failDeleteKey {
			m.failDeleteKey = ""
			w.WriteHeader(500)
			write(map[string]string{"error": "variable delete unavailable"})
			return
		}
		delete(m.appValues, key)
		write(map[string]bool{"ok": true})
	default:
		t.Errorf("unexpected %s %s", r.Method, path)
		w.WriteHeader(405)
	}
}

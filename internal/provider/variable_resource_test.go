package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cloady/terraform-provider-cloady/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const testChildUUID = "e25ffcac-b57d-46a0-9614-bbe3a1d18eaa"

func TestVariableSecretLifecycle(t *testing.T) {
	ctx := context.Background()
	calls := 0
	r := &variableResource{client: childTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		assertChildScope(t, req)
		switch {
		case req.Method == http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["key"] != "API_KEY" || body["value"] != "initial-secret" || body["isSecret"] != true {
				t.Errorf("unexpected create body: %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"var": variableRow{ID: testChildUUID, Key: "API_KEY", Value: "•••••", IsSecret: true}})
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/reveal"):
			_ = json.NewEncoder(w).Encode(map[string]string{"value": "rotated-secret"})
		case req.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"vars": []variableRow{{ID: testChildUUID, Key: "API_KEY", Value: "•••••", IsSecret: true}}})
		case req.Method == http.MethodPatch:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["value"] != "plaintext" || body["isSecret"] != false || len(body) != 2 {
				t.Errorf("secret-flag update must include value: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"var": variableRow{ID: testChildUUID, Key: "API_KEY", Value: "plaintext", IsSecret: false}})
		case req.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"Var not found"}}`))
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	})}
	model := testVariableModel()
	state := childTestState(t, r, &model)
	created := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	if calls != 1 {
		t.Fatalf("create made %d calls; only the write is required", calls)
	}
	if diags := created.State.Get(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	if model.Value.ValueString() != "initial-secret" || model.VariableID.ValueString() != testChildUUID {
		t.Fatalf("create lost plaintext or identity: %#v", model)
	}

	read := resource.ReadResponse{State: created.State}
	r.Read(ctx, resource.ReadRequest{State: created.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	if diags := read.State.Get(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	if model.Value.ValueString() != "rotated-secret" {
		t.Fatal("secret drift was not read through the reveal endpoint")
	}

	model.Value = types.StringValue("plaintext")
	model.IsSecret = types.BoolValue(false)
	planned := childTestState(t, r, &model)
	updated := resource.UpdateResponse{State: read.State}
	r.Update(ctx, resource.UpdateRequest{State: read.State, Plan: tfsdk.Plan{Schema: planned.Schema, Raw: planned.Raw}}, &updated)
	if updated.Diagnostics.HasError() {
		t.Fatal(updated.Diagnostics)
	}
	if diags := updated.State.Get(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	if model.Value.ValueString() != "plaintext" || model.IsSecret.ValueBool() {
		t.Fatal("secret flag was not cleared")
	}
	deleted := resource.DeleteResponse{State: updated.State}
	r.Delete(ctx, resource.DeleteRequest{State: updated.State}, &deleted)
	if deleted.Diagnostics.HasError() {
		t.Fatal(deleted.Diagnostics)
	}
}

func TestVariableImportRevealsSecret(t *testing.T) {
	ctx := context.Background()
	const remoteID = "opaque:id?version=2"
	r := &variableResource{client: childTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		assertChildScope(t, req)
		if strings.HasSuffix(req.URL.Path, "/reveal") {
			_ = json.NewEncoder(w).Encode(map[string]string{"value": "imported-secret"})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"vars": []variableRow{{ID: remoteID, Key: "API_KEY", Value: "•••••", IsSecret: true}}})
		}
	})}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	// Terraform hands ImportState a null object of the schema type; without a Raw
	// value SetAttribute cannot build one and every write fails.
	imported := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema,
		Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "team/api/production/us-east/" + remoteID}, &imported)
	if imported.Diagnostics.HasError() {
		t.Fatal(imported.Diagnostics)
	}
	read := resource.ReadResponse{State: imported.State}
	r.Read(ctx, resource.ReadRequest{State: imported.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	var model variableResourceModel
	if diags := read.State.Get(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	if model.VariableID.ValueString() != remoteID || model.Value.ValueString() != "imported-secret" || !model.IsSecret.ValueBool() {
		t.Fatal("import did not populate plaintext secret from API")
	}
}

func TestVariableRevealFailurePreservesState(t *testing.T) {
	ctx := context.Background()
	r := &variableResource{client: childTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/reveal") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"vars": []variableRow{{ID: testChildUUID, Key: "API_KEY", Value: "•••••", IsSecret: true}}})
	})}
	model := testVariableModel()
	model.setRow(variableRow{ID: testChildUUID, Key: "API_KEY", IsSecret: true}, true)
	state := childTestState(t, r, &model)
	read := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &read)
	if !read.Diagnostics.HasError() {
		t.Fatal("expected reveal error")
	}
	if !read.State.Raw.Equal(state.Raw) {
		t.Fatal("failed reveal must preserve state")
	}
}

func TestParseChildID(t *testing.T) {
	valid := "team/api/production/us-east/" + testChildUUID
	for _, id := range []string{valid, "team/api/preview/us-east/opaque:id?version=2"} {
		if parts, err := parseChildID(id); err != nil || len(parts) != 5 {
			t.Fatalf("valid import %q rejected: %v", id, err)
		}
	}
	for _, id := range []string{"", testChildUUID, "team/api/us-east/" + testChildUUID, "team/api/staging/us-east/" + testChildUUID, "team/api/production//" + testChildUUID, "team/api/production/us-east/", "team/api/production/ us-east/" + testChildUUID, "team/api/production/us-east/has space", valid + "/extra"} {
		t.Run(id, func(t *testing.T) {
			if _, err := parseChildID(id); err == nil {
				t.Fatal("invalid import accepted")
			}
		})
	}
}

func testVariableModel() variableResourceModel {
	return variableResourceModel{ID: types.StringUnknown(), VariableID: types.StringUnknown(), Workspace: types.StringValue("team"), App: types.StringValue("api"), Environment: types.StringValue("production"), Region: types.StringValue("us-east"), Key: types.StringValue("API_KEY"), Value: types.StringValue("initial-secret"), IsSecret: types.BoolValue(true)}
}

func childTestClient(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL, "test-token", "test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func childTestState(t *testing.T, r resource.Resource, model any) tfsdk.State {
	t.Helper()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	state := tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(context.Background(), model); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}

func assertChildScope(t *testing.T, req *http.Request) {
	t.Helper()
	if !strings.HasPrefix(req.URL.Path, "/api/workspaces/team/apps/api/") || req.URL.Query().Get("env") != "production" || req.URL.Query().Get("region") != "us-east" {
		t.Errorf("request lost app scope: %s", req.URL)
	}
}

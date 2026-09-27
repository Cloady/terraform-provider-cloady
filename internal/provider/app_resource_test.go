package provider

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	testresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAppVolumeCapacity(t *testing.T) {
	ctx := context.Background()
	r := &appResource{}
	base := appResourceModel{
		ID: types.StringValue("demo/db/production/eu1"), Workspace: types.StringValue("demo"),
		Name: types.StringValue("DB"), Slug: types.StringValue("db"), Environment: types.StringValue("production"), Region: types.StringValue("eu1"),
		Catalog: types.StringValue("postgres"), Git: types.ObjectNull(appGitTypes),
		CPUScale: types.Float64Value(1), MemoryScale: types.Float64Value(1),
		VolumeSizes:          types.MapValueMust(types.Int64Type, map[string]attr.Value{}),
		EffectiveVolumeSizes: types.MapValueMust(types.Int64Type, map[string]attr.Value{"data": types.Int64Value(10)}),
		Values:               types.MapValueMust(types.StringType, map[string]attr.Value{}),
		Status:               types.StringValue("running"), Endpoints: types.ListValueMust(types.StringType, nil),
	}
	for _, tc := range []struct {
		name   string
		change func(*appResourceModel)
		shrink bool
	}{
		{"newly managed server volume", func(*appResourceModel) {}, true},
		{"larger volume", func(m *appResourceModel) {
			m.VolumeSizes = types.MapValueMust(types.Int64Type, map[string]attr.Value{"data": types.Int64Value(20)})
		}, false},
		{"new workspace", func(m *appResourceModel) { m.Workspace = types.StringValue("other") }, false},
		{"new region", func(m *appResourceModel) { m.Region = types.StringValue("us1") }, false},
		{"new slug", func(m *appResourceModel) { m.Slug = types.StringValue("other") }, false},
		{"new environment", func(m *appResourceModel) { m.Environment = types.StringValue("preview") }, false},
		{"new catalog", func(m *appResourceModel) { m.Catalog = types.StringValue("mysql") }, false},
		{"new source type", func(m *appResourceModel) {
			m.Catalog = types.StringNull()
			m.Git = types.ObjectValueMust(appGitTypes, map[string]attr.Value{
				"repository_url": types.StringValue("https://github.com/acme/api"), "branch": types.StringValue("main"),
				"subdirectory": types.StringValue(""), "auto_deploy": types.BoolValue(true),
			})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := base
			plan.VolumeSizes = types.MapValueMust(types.Int64Type, map[string]attr.Value{"data": types.Int64Value(5)})
			tc.change(&plan)
			state, planned := childTestState(t, r, base), childTestState(t, r, plan)
			req := resource.ModifyPlanRequest{State: state, Plan: tfsdk.Plan{Schema: planned.Schema, Raw: planned.Raw}}
			resp := resource.ModifyPlanResponse{Plan: req.Plan}
			r.ModifyPlan(ctx, req, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("capacity check must not block replacement plans: %v", resp.Diagnostics)
			}
			if tc.shrink {
				updated := resource.UpdateResponse{State: state}
				// No HTTP client is configured: rejection must precede all I/O.
				r.Update(ctx, resource.UpdateRequest{State: state, Plan: req.Plan}, &updated)
				if !updated.Diagnostics.HasError() {
					t.Fatal("in-place shrinking update was not rejected")
				}
			}
		})
	}
}

func TestAccAppValues(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 and install Terraform to run protocol tests")
	}
	mock := newMockAPI(t)
	defer mock.server.Close()
	mock.workspace = map[string]any{"slug": "demo"}
	config := func(branch, values string) string {
		return fmt.Sprintf(`
provider "cloady" {
  token = "test-token"
  base_url = %q
}
resource "cloady_app" "test" {
  workspace = "demo"
  name = "API"
  region = "eu1"
  git = {
    repository_url = "https://github.com/acme/api"
    branch = %q
  }
  values = {%s}
}
`, mock.server.URL, branch, values)
	}
	check := func(count int, values map[string]string) testresource.TestCheckFunc {
		return func(state *terraform.State) error {
			mock.mu.Lock()
			defer mock.mu.Unlock()
			if len(mock.deployments) != count {
				return fmt.Errorf("got %d deployments, want %d", len(mock.deployments), count)
			}
			if !reflect.DeepEqual(mock.deployments[count-1], values) {
				return fmt.Errorf("deployment %d did not receive the configured values", count)
			}
			return testresource.TestCheckResourceAttr("cloady_app.test", "id", "demo/api/production/eu1")(state)
		}
	}
	first := `TOKEN_SECRET = "one", REMOVE = "old"`
	second := `TOKEN_SECRET = "two", NEW = "added"`
	third := `TOKEN_SECRET = "three", AFTER_FAIL = "added"`
	testresource.Test(t, testresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []testresource.TestStep{
		{Config: config("main", first), Check: check(1, map[string]string{"TOKEN_SECRET": "one", "REMOVE": "old"})},
		{PreConfig: func() {
			mock.mu.Lock()
			defer mock.mu.Unlock()
			mock.appValues["OUTSIDE"] = variableRow{ID: "OUTSIDE", Key: "OUTSIDE", Value: "untouched"}
		}, Config: config("release", second), Check: check(2, map[string]string{"TOKEN_SECRET": "two", "NEW": "added", "OUTSIDE": "untouched"})},
		{PreConfig: func() {
			mock.mu.Lock()
			defer mock.mu.Unlock()
			row := mock.appValues["TOKEN_SECRET"]
			row.Value = "external drift"
			mock.appValues[row.Key] = row
		}, Config: config("release", second), Check: check(3, map[string]string{"TOKEN_SECRET": "two", "NEW": "added", "OUTSIDE": "untouched"})},
		{PreConfig: func() {
			mock.mu.Lock()
			defer mock.mu.Unlock()
			mock.failRedeploy = true
		}, Config: config("release", third), ExpectError: regexp.MustCompile("deployment failed")},
		// Read now sees the desired stored values. The private pending marker
		// must still force Update through Terraform and retry the deployment.
		{Config: config("release", third), Check: testresource.ComposeAggregateTestCheckFunc(
			check(4, map[string]string{"TOKEN_SECRET": "three", "AFTER_FAIL": "added", "OUTSIDE": "untouched"}),
			func(*terraform.State) error {
				mock.mu.Lock()
				defer mock.mu.Unlock()
				if mock.redeployAttempts != 3 {
					return fmt.Errorf("redeploy attempts = %d, want 3", mock.redeployAttempts)
				}
				return nil
			},
		)},
		{PreConfig: func() {
			mock.mu.Lock()
			defer mock.mu.Unlock()
			mock.failDeleteKey = "AFTER_FAIL"
		}, Config: config("release", `TOKEN_SECRET = "four", PARTIAL = "added"`), ExpectError: regexp.MustCompile("Unable to update application values")},
		// Reverting configuration after a partial update must remove the newly
		// created key, which remains owned even though its apply failed.
		{Config: config("release", third), Check: check(5, map[string]string{"TOKEN_SECRET": "three", "AFTER_FAIL": "added", "OUTSIDE": "untouched"})},
		{Config: config("release", ""), Check: check(6, map[string]string{"OUTSIDE": "untouched"})},
		{Config: config("release", ""), Check: check(6, map[string]string{"OUTSIDE": "untouched"})},
	}})
}

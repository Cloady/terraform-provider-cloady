package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Cloady/terraform-provider-cloady/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/float64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

var (
	_           resource.Resource                   = &appResource{}
	_           resource.ResourceWithConfigure      = &appResource{}
	_           resource.ResourceWithImportState    = &appResource{}
	_           resource.ResourceWithValidateConfig = &appResource{}
	_           resource.ResourceWithModifyPlan     = &appResource{}
	appGitTypes                                     = map[string]attr.Type{
		"repository_url": types.StringType,
		"branch":         types.StringType,
		"subdirectory":   types.StringType,
		"auto_deploy":    types.BoolType,
	}
)

type appResource struct{ client *client.Client }

type appResourceModel struct {
	ID                   types.String  `tfsdk:"id"`
	Workspace            types.String  `tfsdk:"workspace"`
	Name                 types.String  `tfsdk:"name"`
	Slug                 types.String  `tfsdk:"slug"`
	Environment          types.String  `tfsdk:"environment"`
	Region               types.String  `tfsdk:"region"`
	Git                  types.Object  `tfsdk:"git"`
	Catalog              types.String  `tfsdk:"catalog"`
	CPUScale             types.Float64 `tfsdk:"cpu_scale"`
	MemoryScale          types.Float64 `tfsdk:"memory_scale"`
	VolumeSizes          types.Map     `tfsdk:"volume_sizes"`
	EffectiveVolumeSizes types.Map     `tfsdk:"effective_volume_sizes"`
	Values               types.Map     `tfsdk:"values"`
	Status               types.String  `tfsdk:"status"`
	Endpoints            types.List    `tfsdk:"endpoints"`
}

type appGitModel struct {
	RepositoryURL types.String `tfsdk:"repository_url"`
	Branch        types.String `tfsdk:"branch"`
	Subdirectory  types.String `tfsdk:"subdirectory"`
	AutoDeploy    types.Bool   `tfsdk:"auto_deploy"`
}

// These small wire types intentionally cover only the app fields Terraform owns.
type appWire struct {
	Slug      string         `json:"slug"`
	Env       string         `json:"env"`
	Name      string         `json:"name"`
	Region    string         `json:"region"`
	Status    string         `json:"status"`
	Source    *appSourceWire `json:"source"`
	Scale     appScaleWire   `json:"scale"`
	Endpoints []struct {
		URL string `json:"url"`
	} `json:"endpoints"`
}

type appSourceWire struct {
	Type          string `json:"type"`
	RepositoryURL string `json:"repoUrl,omitempty"`
	Name          string `json:"name,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Subdirectory  string `json:"subdir,omitempty"`
	AutoDeploy    *bool  `json:"autoDeploy,omitempty"`
}

type appScaleWire struct {
	CPU     float64          `json:"cpu"`
	Memory  float64          `json:"memory"`
	Volumes map[string]int64 `json:"volumes"`
}

type appEnvelope struct {
	App appWire `json:"app"`
}

func NewAppResource() resource.Resource { return &appResource{} }

func (r *appResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app"
}

func (r *appResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Deploy a Git repository or catalog application. Creation records the application and starts deployment; status reports its observed state. Git source identity, region, environment, and workspace changes replace the application.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, Description: "Import identifier: workspace/app/environment/region."},
			"workspace":   schema.StringAttribute{Required: true, Description: "Workspace slug.", PlanModifiers: replace, Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`), "must be a workspace slug")}},
			"name":        schema.StringAttribute{Required: true, Description: "Application display name. Changing it preserves the application slug."},
			"slug":        schema.StringAttribute{Optional: true, Computed: true, Description: "Application slug. Generated from name when omitted. Changing a configured slug replaces the application.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}, Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`), "must be a lowercase slug of at most 24 characters")}},
			"environment": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("production"), Description: "Application environment: production, preview, or development.", PlanModifiers: replace, Validators: []validator.String{stringvalidator.OneOf("production", "preview", "development")}},
			"region":      schema.StringAttribute{Required: true, Description: "Deployment region ID.", PlanModifiers: replace},
			"catalog":     schema.StringAttribute{Optional: true, Description: "Catalog application name, such as postgres. Set exactly one of catalog or git.", PlanModifiers: replace},
			"git": schema.SingleNestedAttribute{Optional: true, Description: "Git source. Set exactly one of git or catalog.", Attributes: map[string]schema.Attribute{
				"repository_url": schema.StringAttribute{Required: true, Description: "Repository URL, including its scheme, or git@ SSH address.", PlanModifiers: replace},
				"branch":         schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("main"), Description: "Git branch to deploy. Defaults to main, matching Cloady deployment behavior.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
				"subdirectory":   schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "Repository subdirectory without leading or trailing slashes.", PlanModifiers: replace},
				"auto_deploy":    schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Automatically deploy Git updates. Defaults to true; set false for manual deployments."},
			}},
			"cpu_scale":              schema.Float64Attribute{Optional: true, Computed: true, Default: float64default.StaticFloat64(1), Description: "CPU multiplier, from 0.5 through 4 in increments of 0.25. This is not a core count."},
			"memory_scale":           schema.Float64Attribute{Optional: true, Computed: true, Default: float64default.StaticFloat64(1), Description: "Memory multiplier, from 0.5 through 4 in increments of 0.25. This is not a GiB amount."},
			"volume_sizes":           schema.MapAttribute{Optional: true, Computed: true, ElementType: types.Int64Type, Default: mapdefault.StaticValue(types.MapValueMust(types.Int64Type, map[string]attr.Value{})), Description: "Explicit volume size overrides in GiB, keyed by template volume name (1–4096 GiB). In-place updates cannot shrink existing volumes. Unspecified volumes retain their server-managed size."},
			"effective_volume_sizes": schema.MapAttribute{Computed: true, ElementType: types.Int64Type, Description: "Effective volume capacities in GiB, including template defaults and server-managed volumes."},
			"values":                 schema.MapAttribute{Optional: true, Computed: true, Sensitive: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Environment values supplied before the first deployment. Changes synchronize these keys and start one deployment; other variables are preserved. Do not manage the same keys with cloady_variable. Plaintext values are stored in Terraform state."},
			"status":                 schema.StringAttribute{Computed: true, Description: "Observed deployment state; successful apply does not imply readiness."},
			"endpoints":              schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Public endpoint URLs returned by Cloady."},
		},
	}
}

func (r *appResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider configuration", fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *appResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data appResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !data.Git.IsUnknown() && !data.Catalog.IsUnknown() && data.Git.IsNull() == data.Catalog.IsNull() {
		resp.Diagnostics.AddError("Invalid application source", "Set exactly one of git or catalog.")
	}
	for name, value := range map[string]types.String{"name": data.Name, "region": data.Region, "catalog": data.Catalog} {
		if !value.IsNull() && !value.IsUnknown() && strings.TrimSpace(value.ValueString()) != value.ValueString() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Invalid whitespace", "Remove surrounding whitespace.")
		}
	}
	for key, value := range data.Values.Elements() {
		if value.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("values").AtMapKey(key), "Invalid application value", "Use a string value; remove the key to delete the variable.")
		}
	}
	if !data.Git.IsNull() && !data.Git.IsUnknown() {
		var git appGitModel
		resp.Diagnostics.Append(data.Git.As(ctx, &git, basetypes.ObjectAsOptions{})...)
		if !git.RepositoryURL.IsUnknown() {
			u := git.RepositoryURL.ValueString()
			if strings.TrimSpace(u) != u || (!strings.Contains(u, "://") && !strings.HasPrefix(u, "git@")) {
				resp.Diagnostics.AddAttributeError(path.Root("git").AtName("repository_url"), "Invalid repository URL", "Include the repository URL scheme (for example https://) or use a git@ SSH address, without surrounding whitespace.")
			}
		}
		if !git.Branch.IsNull() && !git.Branch.IsUnknown() && strings.TrimSpace(git.Branch.ValueString()) != git.Branch.ValueString() {
			resp.Diagnostics.AddAttributeError(path.Root("git").AtName("branch"), "Invalid branch", "Remove surrounding whitespace from the branch.")
		}
		if !git.Subdirectory.IsUnknown() {
			dir := git.Subdirectory.ValueString()
			if strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, "./") || strings.HasSuffix(dir, "/") {
				resp.Diagnostics.AddAttributeError(path.Root("git").AtName("subdirectory"), "Invalid subdirectory", "Use a relative subdirectory without ./ or leading or trailing slashes.")
			}
		}
	}
}

func (r *appResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state appResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Switching source type also replaces when a nested attribute disappears.
	if !plan.Git.IsUnknown() && plan.Git.IsNull() != state.Git.IsNull() {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("git"))
	}
	pending, diags := req.Private.GetKey(ctx, "deployment_pending")
	resp.Diagnostics.Append(diags...)
	if len(pending) > 0 {
		// A failed deployment still needs an apply even if stored values now match.
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
	}
}

func (r *appResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data appResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	requestedSlug := data.Slug.ValueString()
	source, autoDeploy, diags := data.source(ctx)
	resp.Diagnostics.Append(diags...)
	scale, diags := data.scale(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	values := map[string]string{}
	resp.Diagnostics.Append(data.Values.ElementsAs(ctx, &values, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"values": values, "name": data.Name.ValueString(), "region": data.Region.ValueString(), "env": data.Environment.ValueString(), "source": source, "scale": scale}
	if autoDeploy != nil {
		body["autoDeploy"] = *autoDeploy
	}
	var result appEnvelope
	if err := r.client.Do(ctx, http.MethodPost, client.WorkspacePath(data.Workspace.ValueString())+"/deploy", body, &result); err != nil {
		resp.Diagnostics.AddError("Unable to create application", err.Error())
		return
	}
	resp.Diagnostics.Append(data.refresh(ctx, result.App)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if requestedSlug != "" && requestedSlug != result.App.Slug {
		if err := r.client.Do(ctx, http.MethodPatch, data.apiPath(""), map[string]any{"name": data.Name.ValueString(), "slug": requestedSlug}, &result); err != nil {
			resp.Diagnostics.AddError("Application created but slug update failed", fmt.Sprintf("Application %s exists and is saved in state. %s", data.ID.ValueString(), err))
			return
		}
		resp.Diagnostics.Append(data.refresh(ctx, result.App)...)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		if data.Slug.ValueString() != requestedSlug {
			resp.Diagnostics.AddError("Application slug is unavailable", fmt.Sprintf("Cloady assigned slug %q instead of %q. The created application is saved in state; choose an available slug.", data.Slug.ValueString(), requestedSlug))
		}
	}
}

func (r *appResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data appResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var result struct {
		Workspace struct {
			Services []appWire `json:"services"`
		} `json:"workspace"`
	}
	if err := r.client.Do(ctx, http.MethodGet, client.WorkspacePath(data.Workspace.ValueString()), nil, &result); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read application", err.Error())
		return
	}
	for _, app := range result.Workspace.Services {
		if app.Slug == data.Slug.ValueString() && app.Env == data.Environment.ValueString() && app.Region == data.Region.ValueString() {
			resp.Diagnostics.Append(data.refresh(ctx, app)...)
			resp.Diagnostics.Append(r.readValues(ctx, &data)...)
			if resp.Diagnostics.HasError() {
				return
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *appResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, previous appResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	// Only an in-place update has existing volumes; replacement creates may
	// freely choose smaller sizes, including Terraform's explicit -replace.
	for name, value := range data.VolumeSizes.Elements() {
		old, exists := previous.EffectiveVolumeSizes.Elements()[name]
		if !exists {
			old = previous.VolumeSizes.Elements()[name]
		}
		size, ok := value.(types.Int64)
		prior, oldOK := old.(types.Int64)
		if ok && oldOK && !size.IsNull() && !prior.IsNull() && size.ValueInt64() < prior.ValueInt64() {
			resp.Diagnostics.AddAttributeError(path.Root("volume_sizes").AtMapKey(name), "Volume cannot shrink", "Cloady preserves existing volume capacity. Choose a size at least as large as the current size.")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	source, autoDeploy, diags := data.source(ctx)
	resp.Diagnostics.Append(diags...)
	scale, diags := data.scale(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	oldSource, oldAutoDeploy, diags := previous.source(ctx)
	resp.Diagnostics.Append(diags...)
	pending, diags := req.Private.GetKey(ctx, "deployment_pending")
	resp.Diagnostics.Append(diags...)
	valuesChanged := !data.Values.Equal(previous.Values)
	branchChanged := source.Type == "git" && source.Branch != oldSource.Branch
	deploy := valuesChanged || branchChanged || len(pending) > 0
	if deploy {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, "deployment_pending", []byte("true"))...)
		// Keep ownership of every touched key if a later request fails. Read
		// refreshes their values; the private marker keeps deployment retryable.
		owned := previous.Values.Elements()
		for key, value := range data.Values.Elements() {
			owned[key] = value
		}
		previous.Values = types.MapValueMust(types.StringType, owned)
		resp.Diagnostics.Append(resp.State.Set(ctx, &previous)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if valuesChanged || len(pending) > 0 {
		if err := r.syncValues(ctx, data, previous); err != nil {
			resp.Diagnostics.AddError("Unable to update application values", err.Error())
			return
		}
	}
	// Sending the slug pins it, so a rename preserves Terraform's address.
	body := map[string]any{"name": data.Name.ValueString(), "slug": previous.Slug.ValueString()}
	if branchChanged {
		body["branch"] = source.Branch
	}
	if autoDeploy != nil && (oldAutoDeploy == nil || *autoDeploy != *oldAutoDeploy) {
		body["autoDeploy"] = *autoDeploy
	}
	if !data.CPUScale.Equal(previous.CPUScale) || !data.MemoryScale.Equal(previous.MemoryScale) || !data.VolumeSizes.Equal(previous.VolumeSizes) {
		body["scale"] = scale
	}
	// Carry observed outputs into a values-only update that needs no PATCH.
	data.ID, data.Slug, data.Status, data.Endpoints = previous.ID, previous.Slug, previous.Status, previous.Endpoints
	data.EffectiveVolumeSizes = previous.EffectiveVolumeSizes
	var result appEnvelope
	if !data.Name.Equal(previous.Name) || len(body) > 2 {
		if err := r.client.Do(ctx, http.MethodPatch, previous.apiPath(""), body, &result); err != nil {
			resp.Diagnostics.AddError("Unable to update application", err.Error())
			return
		}
		resp.Diagnostics.Append(data.refresh(ctx, result.App)...)
	}
	// A branch PATCH already deploys using the values synchronized above.
	if deploy && !branchChanged {
		if err := r.client.Do(ctx, http.MethodPost, data.apiPath("/redeploy"), nil, &result); err != nil {
			resp.Diagnostics.AddError("Application values saved but deployment failed", err.Error())
			return
		}
		resp.Diagnostics.Append(data.refresh(ctx, result.App)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, "deployment_pending", nil)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *appResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data appResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Do(ctx, http.MethodDelete, data.apiPath(""), nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete application", err.Error())
	}
}

func (r *appResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[3] == "" || (parts[2] != "production" && parts[2] != "preview" && parts[2] != "development") {
		resp.Diagnostics.AddError("Invalid application import ID", "Use workspace/app/environment/region, with a valid environment (production, preview, or development).")
		return
	}
	for key, value := range map[string]string{"id": req.ID, "workspace": parts[0], "slug": parts[1], "environment": parts[2], "region": parts[3]} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(key), value)...)
	}
}

func (data appResourceModel) apiPath(suffix string) string {
	return client.AppPath(data.Workspace.ValueString(), data.Slug.ValueString(), data.Environment.ValueString(), data.Region.ValueString(), suffix)
}

func (data appResourceModel) source(ctx context.Context) (appSourceWire, *bool, diag.Diagnostics) {
	if !data.Catalog.IsNull() {
		return appSourceWire{Type: "catalog", Name: data.Catalog.ValueString()}, nil, nil
	}
	var git appGitModel
	diags := data.Git.As(ctx, &git, basetypes.ObjectAsOptions{})
	autoDeploy := git.AutoDeploy.ValueBool()
	return appSourceWire{Type: "git", RepositoryURL: git.RepositoryURL.ValueString(), Branch: git.Branch.ValueString(), Subdirectory: git.Subdirectory.ValueString()}, &autoDeploy, diags
}

func (data appResourceModel) scale(ctx context.Context) (appScaleWire, diag.Diagnostics) {
	volumes := map[string]int64{}
	diags := data.VolumeSizes.ElementsAs(ctx, &volumes, false)
	return appScaleWire{CPU: data.CPUScale.ValueFloat64(), Memory: data.MemoryScale.ValueFloat64(), Volumes: volumes}, diags
}

func (data *appResourceModel) refresh(ctx context.Context, app appWire) diag.Diagnostics {
	var diags diag.Diagnostics
	if app.Slug == "" || app.Env == "" || app.Region == "" {
		diags.AddError("Invalid application response", "Cloady returned an application without its slug, environment, or region.")
		return diags
	}
	data.Slug = types.StringValue(app.Slug)
	data.Environment = types.StringValue(app.Env)
	data.Region = types.StringValue(app.Region)
	data.Name = types.StringValue(app.Name)
	data.ID = types.StringValue(strings.Join([]string{data.Workspace.ValueString(), app.Slug, app.Env, app.Region}, "/"))
	data.Status = types.StringValue(app.Status)
	data.CPUScale = types.Float64Value(app.Scale.CPU)
	data.MemoryScale = types.Float64Value(app.Scale.Memory)
	// The API inserts template defaults during scale updates. Only track the
	// caller's explicit overrides so those extra keys do not create perpetual diffs.
	volumes := map[string]attr.Value{}
	for name := range data.VolumeSizes.Elements() {
		if size, ok := app.Scale.Volumes[name]; ok {
			volumes[name] = types.Int64Value(size)
		}
	}
	data.VolumeSizes = types.MapValueMust(types.Int64Type, volumes)
	capacities, capacityDiags := types.MapValueFrom(ctx, types.Int64Type, app.Scale.Volumes)
	diags.Append(capacityDiags...)
	data.EffectiveVolumeSizes = capacities
	urls := make([]string, 0, len(app.Endpoints))
	for _, endpoint := range app.Endpoints {
		urls = append(urls, endpoint.URL)
	}
	value, valueDiags := types.ListValueFrom(ctx, types.StringType, urls)
	diags.Append(valueDiags...)
	data.Endpoints = value
	if app.Source == nil {
		diags.AddError("Unsupported application source", "This application has no Git or catalog source.")
		return diags
	}
	switch app.Source.Type {
	case "catalog":
		data.Catalog = types.StringValue(app.Source.Name)
		data.Git = types.ObjectNull(appGitTypes)
	case "git":
		branch := app.Source.Branch
		if branch == "" {
			branch = "main"
		}
		autoDeploy := app.Env != "development"
		if app.Source.AutoDeploy != nil {
			autoDeploy = *app.Source.AutoDeploy
		}
		git := appGitModel{RepositoryURL: types.StringValue(app.Source.RepositoryURL), Branch: types.StringValue(branch), Subdirectory: types.StringValue(app.Source.Subdirectory), AutoDeploy: types.BoolValue(autoDeploy)}
		value, valueDiags := types.ObjectValueFrom(ctx, appGitTypes, git)
		diags.Append(valueDiags...)
		data.Git = value
		data.Catalog = types.StringNull()
	default:
		diags.AddError("Unsupported application source", fmt.Sprintf("Source type %q is not supported; this resource supports Git and catalog applications.", app.Source.Type))
	}
	return diags
}

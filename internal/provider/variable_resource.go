package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Cloady/terraform-provider-cloady/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_                   resource.ResourceWithConfigure   = &variableResource{}
	_                   resource.ResourceWithImportState = &variableResource{}
	childSegmentPattern                                  = regexp.MustCompile(`^[^\s/]+$`)
)

type variableResource struct{ client *client.Client }

type variableResourceModel struct {
	ID          types.String `tfsdk:"id"`
	VariableID  types.String `tfsdk:"variable_id"`
	Workspace   types.String `tfsdk:"workspace"`
	App         types.String `tfsdk:"app"`
	Environment types.String `tfsdk:"environment"`
	Region      types.String `tfsdk:"region"`
	Key         types.String `tfsdk:"key"`
	Value       types.String `tfsdk:"value"`
	IsSecret    types.Bool   `tfsdk:"is_secret"`
}

type variableRow struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret"`
}

func NewVariableResource() resource.Resource { return &variableResource{} }

func (r *variableResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_variable"
}

func (r *variableResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages one app environment variable. Values are marked sensitive but are stored in Terraform state. Changes take effect on the next app deployment.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Import identity: workspace/app/environment/region/variable_id.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"variable_id": schema.StringAttribute{Computed: true, MarkdownDescription: "Variable ID returned by the API.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"workspace":   childScopeAttribute("Workspace slug."),
			"app":         childScopeAttribute("App slug."),
			"environment": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("production"),
				MarkdownDescription: "App environment: production, preview, or development. Defaults to production.",
				Validators:          []validator.String{stringvalidator.OneOf("production", "preview", "development")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"region": childScopeAttribute("Region ID of the app instance."),
			"key": schema.StringAttribute{
				Required: true, MarkdownDescription: "Environment variable name, in SCREAMING_SNAKE_CASE (at most 80 characters).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"value": schema.StringAttribute{
				Required: true, Sensitive: true, MarkdownDescription: "Variable value, at most 4096 characters. Stored in Terraform state even when is_secret is true.",
			},
			"is_secret": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Encrypt the value at rest in Cloady. Defaults to false. Terraform state still contains the plaintext value."},
		},
	}
}

func (r *variableResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T.", req.ProviderData))
		return
	}
	r.client = c
}

func (r *variableResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data variableResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"key": data.Key.ValueString(), "value": data.Value.ValueString(), "isSecret": data.IsSecret.ValueBool()}
	var result struct {
		Var variableRow `json:"var"`
	}
	if err := r.client.Do(ctx, http.MethodPost, data.apiPath("/vars"), body, &result); err != nil {
		resp.Diagnostics.AddError("Unable to create variable", err.Error())
		return
	}
	if result.Var.ID == "" {
		resp.Diagnostics.AddError("Invalid API response", "The create response did not contain a variable ID.")
		return
	}
	data.setRow(result.Var, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *variableResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data variableResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var result struct {
		Vars []variableRow `json:"vars"`
	}
	if err := r.client.Do(ctx, http.MethodGet, data.apiPath("/vars"), nil, &result); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read variable", err.Error())
		return
	}
	for _, row := range result.Vars {
		if row.ID != data.VariableID.ValueString() {
			continue
		}
		if row.IsSecret {
			var revealed struct {
				Value string `json:"value"`
			}
			if err := r.client.Do(ctx, http.MethodGet, data.apiPath("/vars/"+url.PathEscape(row.ID)+"/reveal"), nil, &revealed); err != nil {
				if client.IsNotFound(err) {
					resp.State.RemoveResource(ctx)
					return
				}
				resp.Diagnostics.AddError("Unable to read secret variable", err.Error())
				return
			}
			row.Value = revealed.Value
		}
		data.setRow(row, false)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *variableResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data variableResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The API requires the plaintext value when changing the secret flag.
	body := map[string]any{"value": data.Value.ValueString(), "isSecret": data.IsSecret.ValueBool()}
	var result struct {
		Var variableRow `json:"var"`
	}
	if err := r.client.Do(ctx, http.MethodPatch, data.apiPath("/vars/"+url.PathEscape(data.VariableID.ValueString())), body, &result); err != nil {
		resp.Diagnostics.AddError("Unable to update variable", err.Error())
		return
	}
	data.setRow(result.Var, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *variableResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data variableResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Do(ctx, http.MethodDelete, data.apiPath("/vars/"+url.PathEscape(data.VariableID.ValueString())), nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete variable", err.Error())
	}
}

func (r *variableResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	childImportState(ctx, req, resp, "variable_id")
}

func (m variableResourceModel) apiPath(suffix string) string {
	return client.AppPath(m.Workspace.ValueString(), m.App.ValueString(), m.Environment.ValueString(), m.Region.ValueString(), suffix)
}

func (m *variableResourceModel) setRow(row variableRow, preserveSecret bool) {
	m.VariableID = types.StringValue(row.ID)
	m.ID = types.StringValue(strings.Join([]string{m.Workspace.ValueString(), m.App.ValueString(), m.Environment.ValueString(), m.Region.ValueString(), row.ID}, "/"))
	m.Key = types.StringValue(row.Key)
	m.IsSecret = types.BoolValue(row.IsSecret)
	// POST/PATCH return a mask for secrets. The submitted plaintext is authoritative;
	// Read uses the reveal endpoint to detect external changes, including on import.
	if !preserveSecret || !row.IsSecret {
		m.Value = types.StringValue(row.Value)
	}
}

func childScopeAttribute(description string) schema.StringAttribute {
	return schema.StringAttribute{
		Required: true, MarkdownDescription: description,
		Validators:    []validator.String{stringvalidator.RegexMatches(childSegmentPattern, "must be nonempty and contain no whitespace or slashes")},
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
}

func parseChildID(id string) ([]string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 5 {
		return nil, fmt.Errorf("expected workspace/app/environment/region/resource_id")
	}
	for _, part := range parts {
		if !childSegmentPattern.MatchString(part) {
			return nil, fmt.Errorf("identity components must be nonempty and contain no whitespace or slashes")
		}
	}
	if parts[2] != "production" && parts[2] != "preview" && parts[2] != "development" {
		return nil, fmt.Errorf("environment must be production, preview, or development")
	}
	return parts, nil
}

func childImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse, remoteIDAttribute string) {
	parts, err := parseChildID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	for i, name := range []string{"workspace", "app", "environment", "region", remoteIDAttribute} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), parts[i])...)
	}
}

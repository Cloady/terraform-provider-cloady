package provider

import (
	"context"
	"fmt"
	"regexp"
	"unicode/utf16"

	"github.com/Cloady/terraform-provider-cloady/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type workspaceResource struct{ client *client.Client }
type workspaceModel struct {
	ID   types.String `tfsdk:"id"`
	Slug types.String `tfsdk:"slug"`
	Name types.String `tfsdk:"name"`
	Hues types.List   `tfsdk:"hues"`
}
type workspaceAPI struct {
	Slug string  `json:"slug"`
	Name string  `json:"name"`
	Hues []int64 `json:"hues"`
}
type workspaceResponse struct {
	Workspace workspaceAPI `json:"workspace"`
}

// hueSeed mirrors the control plane's FNV-1a derivation (strHash/hueSeed in
// src/lib/client/data.ts) so a workspace created here gets the same brand hues
// the dashboard would pick for the same name. The API requires hues on create.
func hueSeed(name string) []int64 {
	h := uint32(2166136261)
	for _, unit := range utf16.Encode([]rune(name)) {
		h ^= uint32(unit)
		h *= 16777619
	}
	first := h % 360
	return []int64{int64(first), int64((first + 45 + ((h >> 8) % 270)) % 360)}
}

func NewWorkspaceResource() resource.Resource { return &workspaceResource{} }
func (r *workspaceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace"
}
func (r *workspaceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "A Cloady workspace. Creating one is free; what it pays follows what its applications declare, and nothing is charged while they fit the free allowance. Deletion removes its applications and data.", Attributes: map[string]schema.Attribute{
		"id":   schema.StringAttribute{Computed: true, Description: "Workspace slug, used as the import ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"slug": schema.StringAttribute{Required: true, Description: "Unique workspace slug. Changing it replaces the workspace.", Validators: []validator.String{stringvalidator.LengthBetween(1, 40), stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`), "must contain lowercase letters, digits and hyphens")}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"name": schema.StringAttribute{Required: true, Description: "Display name (1–60 characters; no surrounding whitespace).", Validators: []validator.String{stringvalidator.LengthBetween(1, 60), stringvalidator.RegexMatches(regexp.MustCompile(`^\S(?:[\s\S]*\S)?$`), "must not have surrounding whitespace")}},
		"hues": schema.ListAttribute{Optional: true, Computed: true, ElementType: types.Int64Type, Description: "Two brand hue angles, from 0 to 359. Derived from the workspace name when omitted, matching the dashboard, and then held steady across renames.", PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}, Validators: []validator.List{listvalidator.SizeBetween(2, 2), listvalidator.ValueInt64sAre(int64validator.Between(0, 359))}},
	}}
}
func (r *workspaceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
	}
}
func (m *workspaceModel) setAPI(ctx context.Context, w workspaceAPI) {
	m.ID = types.StringValue(w.Slug)
	m.Slug = types.StringValue(w.Slug)
	m.Name = types.StringValue(w.Name)
	m.Hues, _ = types.ListValueFrom(ctx, types.Int64Type, w.Hues)
}
func resolveHues(ctx context.Context, data workspaceModel) ([]int64, diag.Diagnostics) {
	if data.Hues.IsNull() || data.Hues.IsUnknown() {
		return hueSeed(data.Name.ValueString()), nil
	}
	var hues []int64
	return hues, data.Hues.ElementsAs(ctx, &hues, false)
}
func (r *workspaceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data workspaceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	hues, huesDiags := resolveHues(ctx, data)
	resp.Diagnostics.Append(huesDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	wantedSlug := data.Slug.ValueString()
	var result workspaceResponse
	err := r.client.Do(ctx, "POST", "/api/workspaces", map[string]any{"name": data.Name.ValueString(), "slug": wantedSlug, "hues": hues}, &result)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create workspace", err.Error())
		return
	}
	data.setAPI(ctx, result.Workspace)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if result.Workspace.Slug != wantedSlug {
		resp.Diagnostics.AddError("Workspace slug was already taken", fmt.Sprintf("Cloady created %q instead of %q. Its actual ID has been saved in state. Update the configuration to that slug or remove the new workspace before retrying.", result.Workspace.Slug, wantedSlug))
	}
}
func (r *workspaceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data workspaceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var result workspaceResponse
	err := r.client.Do(ctx, "GET", client.WorkspacePath(data.ID.ValueString()), nil, &result)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read workspace", err.Error())
		return
	}
	data.setAPI(ctx, result.Workspace)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *workspaceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, old workspaceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	hues, huesDiags := resolveHues(ctx, data)
	resp.Diagnostics.Append(huesDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	var result workspaceResponse
	err := r.client.Do(ctx, "PATCH", client.WorkspacePath(old.ID.ValueString()), map[string]any{"name": data.Name.ValueString(), "slug": data.Slug.ValueString(), "hues": hues}, &result)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update workspace", err.Error())
		return
	}
	data.setAPI(ctx, result.Workspace)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *workspaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data workspaceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Do(ctx, "DELETE", client.WorkspacePath(data.ID.ValueString()), nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete workspace", err.Error())
	}
}
func (r *workspaceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if len(req.ID) > 40 || !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", "Expected a workspace slug.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("slug"), req.ID)...)
}

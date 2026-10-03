package provider

import (
	"context"
	"fmt"

	"github.com/Cloady/terraform-provider-cloady/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type workspaceDataSource struct{ client *client.Client }

func NewWorkspaceDataSource() datasource.DataSource { return &workspaceDataSource{} }
func (d *workspaceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace"
}
func (d *workspaceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Look up an existing workspace without managing its lifecycle.", Attributes: map[string]schema.Attribute{
		"slug": schema.StringAttribute{Required: true, Description: "Workspace slug."},
		"id":   schema.StringAttribute{Computed: true, Description: "Workspace slug."},
		"name": schema.StringAttribute{Computed: true, Description: "Display name."},
		"hues": schema.ListAttribute{Computed: true, ElementType: types.Int64Type, Description: "Workspace brand hue angles."},
	}}
}
func (d *workspaceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	d.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
	}
}
func (d *workspaceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data workspaceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var result workspaceResponse
	if err := d.client.Do(ctx, "GET", client.WorkspacePath(data.Slug.ValueString()), nil, &result); err != nil {
		resp.Diagnostics.AddError("Unable to read workspace", err.Error())
		return
	}
	data.setAPI(ctx, result.Workspace)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

type regionsDataSource struct{ client *client.Client }
type regionModel struct {
	ID      types.String `tfsdk:"id"`
	Code    types.String `tfsdk:"code"`
	Country types.String `tfsdk:"country"`
	Status  types.String `tfsdk:"status"`
	IPv4    types.String `tfsdk:"ipv4"`
	IPv6    types.String `tfsdk:"ipv6"`
}

func NewRegionsDataSource() datasource.DataSource { return &regionsDataSource{} }
func (d *regionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_regions"
}
func (d *regionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "List Cloady deployment regions and their public IP addresses.", Attributes: map[string]schema.Attribute{
		"regions": schema.ListNestedAttribute{Computed: true, Description: "Available region records.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, Description: "Region ID to pass to app resources."},
			"code":    schema.StringAttribute{Computed: true, Description: "Region code."},
			"country": schema.StringAttribute{Computed: true, Description: "Country."},
			"status":  schema.StringAttribute{Computed: true, Description: "Region status."},
			"ipv4":    schema.StringAttribute{Computed: true, Description: "Public IPv4 address, when available."},
			"ipv6":    schema.StringAttribute{Computed: true, Description: "Public IPv6 address, when available."},
		}}},
	}}
}
func (d *regionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	d.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
	}
}
func (d *regionsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	var result struct {
		Regions []struct {
			ID, Code, Country, Status string
			IPv4                      *string `json:"ipv4"`
			IPv6                      *string `json:"ipv6"`
		} `json:"regions"`
	}
	if err := d.client.Do(ctx, "GET", "/api/regions", nil, &result); err != nil {
		resp.Diagnostics.AddError("Unable to list regions", err.Error())
		return
	}
	regions := make([]regionModel, 0, len(result.Regions))
	for _, r := range result.Regions {
		regions = append(regions, regionModel{ID: types.StringValue(r.ID), Code: types.StringValue(r.Code), Country: types.StringValue(r.Country), Status: types.StringValue(r.Status), IPv4: types.StringPointerValue(r.IPv4), IPv6: types.StringPointerValue(r.IPv6)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &struct {
		Regions []regionModel `tfsdk:"regions"`
	}{regions})...)
}

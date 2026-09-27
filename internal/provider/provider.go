package provider

import (
	"context"
	"os"
	"time"

	"github.com/Cloady/terraform-provider-cloady/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &cloadyProvider{}

type cloadyProvider struct{ version string }
type providerModel struct {
	Token          types.String `tfsdk:"token"`
	BaseURL        types.String `tfsdk:"base_url"`
	RequestTimeout types.Int64  `tfsdk:"request_timeout"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &cloadyProvider{version: version} }
}
func (p *cloadyProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "cloady"
	resp.Version = p.version
}
func (p *cloadyProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Manage Cloady workspaces, applications, environment variables, and custom domains.", Attributes: map[string]schema.Attribute{
		"token":           schema.StringAttribute{Optional: true, Sensitive: true, Description: "Personal API token. Defaults to CLOADY_TOKEN. Workspace creation requires full scope."},
		"base_url":        schema.StringAttribute{Optional: true, Description: "Control-plane URL. Defaults to CLOADY_CONTROL_PLANE_URL, then https://cloady.com. Supply the origin without /api."},
		"request_timeout": schema.Int64Attribute{Optional: true, Description: "Positive per-request timeout in seconds. Defaults to 180.", Validators: []validator.Int64{int64validator.Between(1, int64((1<<63-1)/time.Second))}},
	}}
}
func (p *cloadyProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.Token.IsUnknown() || config.BaseURL.IsUnknown() || config.RequestTimeout.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider configuration", "Provider token, base_url, and request_timeout must be known before managing resources.")
		return
	}
	token := os.Getenv("CLOADY_TOKEN")
	if !config.Token.IsNull() {
		token = config.Token.ValueString()
	}
	baseURL := os.Getenv("CLOADY_CONTROL_PLANE_URL")
	if baseURL == "" {
		baseURL = "https://cloady.com"
	}
	if !config.BaseURL.IsNull() {
		baseURL = config.BaseURL.ValueString()
	}
	timeout := int64(180)
	if !config.RequestTimeout.IsNull() {
		timeout = config.RequestTimeout.ValueInt64()
	}
	c, err := client.New(baseURL, token, p.version, time.Duration(timeout)*time.Second)
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}
func (p *cloadyProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewWorkspaceResource, NewAppResource, NewVariableResource, NewDomainResource}
}
func (p *cloadyProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{NewWorkspaceDataSource, NewRegionsDataSource}
}

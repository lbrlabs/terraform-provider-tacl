package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure interface compliance for Terraform Plugin Framework.
var (
	_ datasource.DataSource              = &sshDataSource{}
	_ datasource.DataSourceWithConfigure = &sshDataSource{}
)

// NewSSHDataSource => constructor for "tacl_ssh" data source.
func NewSSHDataSource() datasource.DataSource {
	return &sshDataSource{}
}

// sshDataSource => fetches a single SSH rule by *UUID* (stable ID).
type sshDataSource struct {
	httpClient *http.Client
	endpoint   string
}

// sshDataSourceModel => local struct for reading the SSH rule by ID (UUID).
type sshDataSourceModel struct {
	// We’ll use "id" to store the UUID the user provides (and keep it in State).
	ID          types.String   `tfsdk:"id"`
	Action      types.String   `tfsdk:"action"`
	Src         []types.String `tfsdk:"src"`
	Dst         []types.String `tfsdk:"dst"`
	Users       []types.String `tfsdk:"users"`
	CheckPeriod types.String   `tfsdk:"check_period"`
	AcceptEnv   []types.String `tfsdk:"accept_env"`
}

// --------------------------------------------------------------------------------
// Configure / Metadata / Schema
// --------------------------------------------------------------------------------

func (d *sshDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	provider, ok := req.ProviderData.(*taclProvider)
	if !ok {
		return
	}
	d.httpClient = provider.httpClient
	d.endpoint = provider.endpoint
}

func (d *sshDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh"
}

func (d *sshDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Data source for reading a single SSH rule by UUID in TACL’s /ssh.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The stable UUID of the SSH rule in TACL.",
				Required:    true,
			},
			"action": schema.StringAttribute{
				Description: "SSH action: 'accept' or 'check'.",
				Computed:    true,
			},
			"src": schema.ListAttribute{
				Description: "Sources for the SSH rule (tags, CIDRs, etc.).",
				Computed:    true,
				ElementType: types.StringType,
			},
			"dst": schema.ListAttribute{
				Description: "Destinations for the SSH rule (host:port, etc.).",
				Computed:    true,
				ElementType: types.StringType,
			},
			"users": schema.ListAttribute{
				Description: "SSH users allowed.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"check_period": schema.StringAttribute{
				Description: "Duration (e.g. '12h') for check actions.",
				Computed:    true,
			},
			"accept_env": schema.ListAttribute{
				Description: "Environment variable patterns allowed.",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// --------------------------------------------------------------------------------
// Read
// --------------------------------------------------------------------------------

func (d *sshDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data sshDataSourceModel
	diags := req.Config.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// 1) Extract the UUID from "id"
	id := data.ID.ValueString()
	if id == "" {
		resp.Diagnostics.AddError("Missing SSH rule ID", "The 'id' attribute must contain a valid UUID.")
		return
	}

	// 2) GET /ssh/<UUID>
	getURL := fmt.Sprintf("%s/ssh/%s", d.endpoint, id)
	tflog.Debug(ctx, "Reading SSH data source by UUID", map[string]interface{}{
		"url": getURL,
		"id":  id,
	})

	body, err := doSSHDataSourceRequest(ctx, d.httpClient, http.MethodGet, getURL, nil)
	if err != nil {
		if IsNotFound(err) {
			resp.Diagnostics.AddWarning("Not found", fmt.Sprintf("No SSH rule found for id '%s'", id))
			// The data source won't be populated in the state if it's not found.
			return
		}
		resp.Diagnostics.AddError("Error reading SSH data source", err.Error())
		return
	}

	// 3) Parse the TaclSSHResponse
	var sshResp TaclSSHResponse
	if e := json.Unmarshal(body, &sshResp); e != nil {
		resp.Diagnostics.AddError("JSON parse error", e.Error())
		return
	}

	// 4) Update data with fetched info
	data.ID = types.StringValue(sshResp.ID)
	data.Action = types.StringValue(sshResp.Action)
	data.Src = toTerraformStringSlice(sshResp.Src)
	data.Dst = toTerraformStringSlice(sshResp.Dst)
	data.Users = toTerraformStringSlice(sshResp.Users)
	data.CheckPeriod = types.StringValue(sshResp.CheckPeriod)
	data.AcceptEnv = toTerraformStringSlice(sshResp.AcceptEnv)

	diags = resp.State.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}

// --------------------------------------------------------------------------------
// Helper Function
// --------------------------------------------------------------------------------

// doSSHDataSourceRequest => simpler GET helper for the data source
func doSSHDataSourceRequest(ctx context.Context, client *http.Client, method, url string, payload interface{}) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		body = bytes.NewBuffer(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == 404 {
		return nil, &NotFoundError{Message: "SSH rule not found"}
	}
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("TACL returned %d: %s", res.StatusCode, string(msg))
	}

	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	return respBody, nil
}

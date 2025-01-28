// ssh_data_source.go

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



// --------------------------------------------------------------------------------
// sshDataSource
// --------------------------------------------------------------------------------

var (
	_ datasource.DataSource              = &sshDataSource{}
	_ datasource.DataSourceWithConfigure = &sshDataSource{}
)

func NewSSHDataSource() datasource.DataSource {
	return &sshDataSource{}
}

type sshDataSource struct {
	httpClient *http.Client
	endpoint   string
}

// sshDataSourceModel => data source model
type sshDataSourceModel struct {
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
	p, ok := req.ProviderData.(*taclProvider)
	if !ok {
		return
	}
	d.httpClient = p.httpClient
	d.endpoint = p.endpoint
}

func (d *sshDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh"
}

func (d *sshDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Data source for reading a single SSH rule by UUID from /ssh/:id.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The stable UUID of the SSH rule in TACL.",
				Required:    true,
			},
			"action": schema.StringAttribute{
				Description: "SSH rule action: 'accept' or 'check'.",
				Computed:    true,
			},
			"src": schema.ListAttribute{
				Description: "Source tags/CIDRs.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"dst": schema.ListAttribute{
				Description: "Destination tags/CIDRs.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"users": schema.ListAttribute{
				Description: "SSH users allowed.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"check_period": schema.StringAttribute{
				Description: "CheckPeriod for 'check' actions, e.g. '12h'.",
				Computed:    true,
			},
			"accept_env": schema.ListAttribute{
				Description: "List of environment variables allowed.",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// --------------------------------------------------------------------------------
// Read => GET /ssh/:id
// --------------------------------------------------------------------------------

func (d *sshDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data sshDataSourceModel
	diags := req.Config.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := data.ID.ValueString()
	if id == "" {
		resp.Diagnostics.AddError("Missing ID", "Must provide an SSH rule UUID for data source.")
		return
	}

	getURL := fmt.Sprintf("%s/ssh/%s", d.endpoint, id)
	tflog.Debug(ctx, "Reading SSH data source by UUID", map[string]interface{}{
		"url": getURL,
		"id":  id,
	})

	body, err := doSSHDSRequest(ctx, d.httpClient, http.MethodGet, getURL, nil)
	if err != nil {
		if IsNotFound(err) {
			// Not found => no state
			return
		}
		resp.Diagnostics.AddError("Read SSH DS error", err.Error())
		return
	}

	var fetched TaclSSHResponse
	if e := json.Unmarshal(body, &fetched); e != nil {
		resp.Diagnostics.AddError("Parse DS JSON error", e.Error())
		return
	}

	data.ID = types.StringValue(fetched.ID)
	data.Action = types.StringValue(fetched.Action)
	data.Src = toTerraformStringSlice(fetched.Src)
	data.Dst = toTerraformStringSlice(fetched.Dst)
	data.Users = toTerraformStringSlice(fetched.Users)

	if fetched.CheckPeriod != "" {
		data.CheckPeriod = types.StringValue(fetched.CheckPeriod)
	} else {
		data.CheckPeriod = types.StringNull()
	}

	if len(fetched.AcceptEnv) > 0 {
		data.AcceptEnv = toTerraformStringSlice(fetched.AcceptEnv)
	} else {
		data.AcceptEnv = nilListOfString()
	}

	diags = resp.State.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}

func doSSHDSRequest(ctx context.Context, client *http.Client, method, url string, payload interface{}) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal DS payload: %w", err)
		}
		body = bytes.NewBuffer(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("SSH DS request creation error: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SSH DS request error: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == 404 {
		return nil, &NotFoundError{Message: "SSH rule not found"}
	}
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("TACL returned HTTP %d: %s", res.StatusCode, string(msg))
	}

	return io.ReadAll(res.Body)
}

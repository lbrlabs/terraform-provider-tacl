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
	_ datasource.DataSource              = &aclDataSource{}
	_ datasource.DataSourceWithConfigure = &aclDataSource{}
)

// NewACLDataSource (new-style) => "tacl_acl" data source.
func NewACLDataSource() datasource.DataSource {
	return &aclDataSource{}
}

// aclDataSource => for a single ACL looked up by stable UUID.
type aclDataSource struct {
	httpClient *http.Client
	endpoint   string
}

// aclDataSourceModel => mirrors the shape of the data source’s attributes in Terraform.
type aclDataSourceModel struct {
	ID     types.String   `tfsdk:"id"`     // The TACL stable UUID
	Action types.String   `tfsdk:"action"` // e.g. "accept"/"deny"
	Src    []types.String `tfsdk:"src"`
	Proto  types.String   `tfsdk:"proto"`
	Dst    []types.String `tfsdk:"dst"`
}

// extendedACLResponse => shape returned by GET /acls/:id
// (which is the server’s ExtendedACLEntry: ID + ACL fields).
type extendedACLResponse struct {
	ID     string   `json:"id"`
	Action string   `json:"action"`
	Src    []string `json:"src"`
	Proto  string   `json:"proto,omitempty"`
	Dst    []string `json:"dst"`
}

// Configure => capture the provider’s httpClient + base endpoint.
func (d *aclDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Metadata => tells Terraform our data source name: "tacl_acl".
func (d *aclDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_acl"
}

// Schema => defines the TF attributes for this data source.
func (d *aclDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Data source for reading a single ACL entry by stable UUID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Stable UUID of the ACL entry in TACL.",
				Required:    true,
			},
			"action": schema.StringAttribute{
				Description: "ACL action, e.g. 'accept' or 'deny'.",
				Computed:    true,
			},
			"src": schema.ListAttribute{
				Description: "List of ACL sources (CIDRs, tags, etc.).",
				Computed:    true,
				ElementType: types.StringType,
			},
			"proto": schema.StringAttribute{
				Description: "Protocol, e.g. 'tcp' or 'udp'.",
				Computed:    true,
			},
			"dst": schema.ListAttribute{
				Description: "List of ACL destinations (CIDRs, tags, etc.).",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// Read => performs the HTTP GET /acls/<uuid> and sets the data source state.
func (d *aclDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// 1. Parse user input config from the data source "id" (UUID).
	var data aclDataSourceModel
	diags := req.Config.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	uuid := data.ID.ValueString()
	if uuid == "" {
		resp.Diagnostics.AddError(
			"Missing UUID",
			"The 'id' attribute is required (must be a valid UUID).",
		)
		return
	}

	// 2. Construct GET /acls/<uuid>
	getURL := fmt.Sprintf("%s/acls/%s", d.endpoint, uuid)
	tflog.Debug(ctx, "Reading ACL data source by UUID", map[string]interface{}{
		"url":  getURL,
		"uuid": uuid,
	})

	respBody, err := doACLDSRequest(ctx, d.httpClient, http.MethodGet, getURL, nil)
	if err != nil {
		if IsNotFound(err) {
			// If the server returns 404, we can either set empty or return a warning.
			resp.Diagnostics.AddWarning(
				"ACL Not Found",
				fmt.Sprintf("No ACL with UUID %q was found on the server.", uuid),
			)
			return
		}
		resp.Diagnostics.AddError("Error reading ACL data source", err.Error())
		return
	}

	// 3. Parse the JSON => extendedACLResponse
	var fetched extendedACLResponse
	if err := json.Unmarshal(respBody, &fetched); err != nil {
		resp.Diagnostics.AddError("JSON parse error", err.Error())
		return
	}

	// 4. Populate Terraform state from the fetched data.
	data.ID = types.StringValue(fetched.ID)
	data.Action = types.StringValue(fetched.Action)
	data.Src = toTerraformStringSlice(fetched.Src)
	data.Proto = types.StringValue(fetched.Proto)
	data.Dst = toTerraformStringSlice(fetched.Dst)

	diags = resp.State.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}

// doACLDSRequest => minimal helper to do JSON-based HTTP for the data source.
func doACLDSRequest(ctx context.Context, client *http.Client, method, url string, payload interface{}) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		body = bytes.NewBuffer(data)
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

	// Check for 404 separately so we can handle “not found” logic differently in Read.
	if res.StatusCode == 404 {
		return nil, &NotFoundError{Message: "ACL not found"}
	}
	// For 300+, return error with body.
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

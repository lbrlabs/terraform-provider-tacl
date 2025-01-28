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

// Ensure postureDataSource implements these interfaces
var (
	_ datasource.DataSource              = &postureDataSource{}
	_ datasource.DataSourceWithConfigure = &postureDataSource{}
)

// NewPostureDataSource => constructor
func NewPostureDataSource() datasource.DataSource {
	return &postureDataSource{}
}

type postureDataSource struct {
	httpClient *http.Client
	endpoint   string
}

// postureDSModel => the data source model
// We store "id" = name, "rules" is a list of strings. If name=default, we read the default posture.
type postureDSModel struct {
	ID    types.String `tfsdk:"id"`    // user must set this to posture name (or "default")
	Rules types.List   `tfsdk:"rules"` // read from server
}

// Configure => get the provider’s httpClient/endpoint
func (d *postureDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Metadata => set data source name
func (d *postureDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_posture"
}

// Schema => user sets "id" = name of posture. We'll store "rules".
func (d *postureDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Data source for reading a posture (named or default). If 'id' is 'default', we read /postures/default. Otherwise, /postures/:name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Either a named posture (e.g. 'latestMac') or 'default'.",
				Required:    true,
			},
			"rules": schema.ListAttribute{
				Description: "Rules for this posture (strings).",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// Read => fetch posture by name or default
func (d *postureDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data postureDSModel
	diags := req.Config.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := data.ID.ValueString()

	var getURL string
	if name == "default" {
		getURL = fmt.Sprintf("%s/postures/default", d.endpoint)
	} else {
		getURL = fmt.Sprintf("%s/postures/%s", d.endpoint, name)
	}

	tflog.Debug(ctx, "Reading posture (data source)", map[string]interface{}{
		"url":  getURL,
		"name": name,
	})

	respBody, err := doDSHTTPRequest(ctx, d.httpClient, http.MethodGet, getURL, nil)
	if err != nil {
		if IsNotFound(err) {
			// no posture => do nothing
			return
		}
		resp.Diagnostics.AddError("Error reading posture data source", err.Error())
		return
	}

	if name == "default" {
		// Expect shape: { "defaultSourcePosture": [...] }
		var fetched map[string][]string
		if e := json.Unmarshal(respBody, &fetched); e != nil {
			resp.Diagnostics.AddError("JSON parse error", e.Error())
			return
		}
		rules := fetched["defaultSourcePosture"]
		data.Rules, _ = toStringListValue(ctx, rules)
	} else {
		// Normal posture => { "name":"...","rules":[] }
		var fetched struct {
			Name  string   `json:"name"`
			Rules []string `json:"rules"`
		}
		if e := json.Unmarshal(respBody, &fetched); e != nil {
			resp.Diagnostics.AddError("JSON parse error", e.Error())
			return
		}
		data.Rules, _ = toStringListValue(ctx, fetched.Rules)
	}

	diags = resp.State.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}

// doDSHTTPRequest => minimal helper for data source
func doDSHTTPRequest(ctx context.Context, client *http.Client, method, url string, payload interface{}) ([]byte, error) {
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
		return nil, fmt.Errorf("request creation error: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	r, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer r.Body.Close()

	if r.StatusCode == 404 {
		return nil, &NotFoundError{Message: "posture not found"}
	}
	if r.StatusCode >= 300 {
		respB, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("TACL returned HTTP %d: %s", r.StatusCode, string(respB))
	}

	return io.ReadAll(r.Body)
}



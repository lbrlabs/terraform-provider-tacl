package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// nodeattrDataSource => read a single node attribute by numeric index (example).
var (
	_ datasource.DataSource              = &nodeattrDataSource{}
	_ datasource.DataSourceWithConfigure = &nodeattrDataSource{}
)

func NewNodeAttrDataSource() datasource.DataSource {
	return &nodeattrDataSource{}
}

type nodeattrDataSource struct {
	httpClient *http.Client
	endpoint   string
}

// nodeattrDSModel => we can store target/attr as types.List if we want
type nodeattrDSModel struct {
	ID      types.String `tfsdk:"id"`
	Target  types.List   `tfsdk:"target"`
	Attr    types.List   `tfsdk:"attr"`
	AppJSON types.String `tfsdk:"app_json"`
}

func (d *nodeattrDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *nodeattrDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nodeattr"
}

func (d *nodeattrDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Data source for reading a single node attribute by numeric index (example).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Index of the node attribute in TACL’s array (example).",
				Required:    true,
			},
			"target": schema.ListAttribute{
				Description: "List of target strings.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"attr": schema.ListAttribute{
				Description: "Optional list of attribute strings.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"app_json": schema.StringAttribute{
				Description: "If present, TACL's 'app' data as JSON.",
				Computed:    true,
			},
		},
	}
}

// Read => GET /nodeattrs/:index  (example usage).
func (d *nodeattrDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data nodeattrDSModel
	diags := req.Config.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	idxStr := data.ID.ValueString()
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", fmt.Sprintf("Could not parse '%s' as integer", idxStr))
		return
	}

	getURL := fmt.Sprintf("%s/nodeattrs/%d", d.endpoint, idx)
	tflog.Debug(ctx, "Reading nodeattr (data source)", map[string]interface{}{
		"url":   getURL,
		"index": idx,
	})

	body, err := doNodeAttrDSHTTP(ctx, d.httpClient, http.MethodGet, getURL, nil)
	if err != nil {
		if IsNotFound(err) {
			// Not found => do nothing or set empty
			tflog.Warn(ctx, "No nodeattr found at index", map[string]interface{}{"index": idx})
			return
		}
		resp.Diagnostics.AddError("Read nodeattr DS error", err.Error())
		return
	}

	// Suppose TACL returns JSON like: { "id":"...", "target":["..."], "attr":[] or omitted, "app":{} or omitted }
	var fetched map[string]interface{}
	if err := json.Unmarshal(body, &fetched); err != nil {
		resp.Diagnostics.AddError("Parse DS response error", err.Error())
		return
	}

	// If the JSON has "target": [...], "attr": [...], "app": {...}
	data.ID = types.StringValue(idxStr)

	// Convert "target"
	if rawTarget, ok := fetched["target"].([]interface{}); ok {
		strTarget := interfaceSliceToStringSlice(rawTarget)
		tfTarget, convErr := stringSliceToList(ctx, strTarget)
		if convErr == nil {
			data.Target = tfTarget
		} else {
			resp.Diagnostics.AddError("Error converting target list", convErr.Error())
		}
	} else {
		data.Target = types.ListNull(types.StringType)
	}

	// Convert "attr"
	if rawAttr, ok := fetched["attr"].([]interface{}); ok {
		strAttr := interfaceSliceToStringSlice(rawAttr)
		tfAttr, convErr := stringSliceToList(ctx, strAttr)
		if convErr == nil {
			data.Attr = tfAttr
		} else {
			resp.Diagnostics.AddError("Error converting attr list", convErr.Error())
		}
	} else {
		data.Attr = types.ListNull(types.StringType)
	}

	// Convert "app" => store as JSON
	if app, ok := fetched["app"]; ok && app != nil {
		appBytes, _ := json.Marshal(app)
		data.AppJSON = types.StringValue(string(appBytes))
	} else {
		data.AppJSON = types.StringNull()
	}

	diags = resp.State.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}

func doNodeAttrDSHTTP(ctx context.Context, client *http.Client, method, url string, payload interface{}) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewBuffer(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("nodeattr DS request error: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nodeattr DS request error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, &NotFoundError{Message: "nodeattr not found"}
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TACL returned %d: %s", resp.StatusCode, string(msg))
	}

	return io.ReadAll(resp.Body)
}

// Helpers

func interfaceSliceToStringSlice(in []interface{}) []string {
	out := make([]string, len(in))
	for i, v := range in {
		if s, ok := v.(string); ok {
			out[i] = s
		}
	}
	return out
}

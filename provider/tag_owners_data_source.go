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

var (
    _ datasource.DataSource              = &tagOwnersDataSource{}
    _ datasource.DataSourceWithConfigure = &tagOwnersDataSource{}
)

// NewTagOwnersDataSource => constructor for "tacl_tag_owner" data source
func NewTagOwnersDataSource() datasource.DataSource {
    return &tagOwnersDataSource{}
}

type tagOwnersDataSource struct {
    httpClient *http.Client
    endpoint   string
}

// dsModel => the DS schema model: user sets "name" => we read "owners"
type tagOwnersDSModel struct {
    Name   types.String   `tfsdk:"name"`   // user must provide the tag name
    Owners []types.String `tfsdk:"owners"` // we populate from the server
}

func (d *tagOwnersDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *tagOwnersDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
    resp.TypeName = req.ProviderTypeName + "_tag_owner"
}

func (d *tagOwnersDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
    resp.Schema = schema.Schema{
        Description: "Data source for reading a single TagOwner by name from /tagowners/:name.",
        Attributes: map[string]schema.Attribute{
            "name": schema.StringAttribute{
                Description: "Name of the tag (e.g. 'webserver') to look up.",
                Required:    true,
            },
            "owners": schema.ListAttribute{
                Description: "List of owners for this tag.",
                Computed:    true,
                ElementType: types.StringType,
            },
        },
    }
}

func (d *tagOwnersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
    var data tagOwnersDSModel
    diags := req.Config.Get(ctx, &data)
    resp.Diagnostics.Append(diags...)
    if resp.Diagnostics.HasError() {
        return
    }

    name := data.Name.ValueString()
    if name == "" {
        resp.Diagnostics.AddError("Invalid Tag Name", "Must provide a non-empty 'name' for the data source.")
        return
    }

    // GET /tagowners/:name
    getURL := fmt.Sprintf("%s/tagowners/%s", d.endpoint, name)
    tflog.Debug(ctx, "Reading TagOwner (data source)", map[string]interface{}{
        "url":  getURL,
        "name": name,
    })

    body, err := doTagOwnersDSRequest(ctx, d.httpClient, http.MethodGet, getURL, nil)
    if err != nil {
        if isNotFound(err) {
            // If the server returns 404 => no data => do nothing
            tflog.Warn(ctx, "No TagOwner found", map[string]interface{}{"name": name})
            return
        }
        resp.Diagnostics.AddError("Read tagowner DS error", err.Error())
        return
    }

    var fetched TagOwnerResponse
    if e := json.Unmarshal(body, &fetched); e != nil {
        resp.Diagnostics.AddError("Parse DS response error", e.Error())
        return
    }

    // Fill DS model
    data.Name = types.StringValue(fetched.Name)
    data.Owners = toTerraformStringSlice(fetched.Owners)

    diags = resp.State.Set(ctx, &data)
    resp.Diagnostics.Append(diags...)
}

// doTagOwnersDSRequest => minimal HTTP for the DS
func doTagOwnersDSRequest(ctx context.Context, client *http.Client, method, url string, payload interface{}) ([]byte, error) {
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
        return nil, fmt.Errorf("request creation error: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")

    r, err := client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("tagowners DS request error: %w", err)
    }
    defer r.Body.Close()

    if r.StatusCode == 404 {
        return nil, &NotFoundError{"TagOwner not found"}
    }
    if r.StatusCode >= 300 {
        msg, _ := io.ReadAll(r.Body)
        return nil, fmt.Errorf("TACL returned HTTP %d: %s", r.StatusCode, string(msg))
    }

    return io.ReadAll(r.Body)
}

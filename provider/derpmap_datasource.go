package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	tsclient "github.com/tailscale/tailscale-client-go/v2"
)

// Ensure interface compliance
var (
	_ datasource.DataSource              = &derpMapDataSource{}
	_ datasource.DataSourceWithConfigure = &derpMapDataSource{}
)

// NewDERPMapDataSource => single-object typed DS for /derpmap.
func NewDERPMapDataSource() datasource.DataSource {
	return &derpMapDataSource{}
}

// derpMapDataSource => manages a typed read of the DERPMap object.
type derpMapDataSource struct {
	httpClient *http.Client
	endpoint   string
}

//------------------------------
// Schema Model
//------------------------------

type derpMapDataSourceModel struct {
	ID                 types.String    `tfsdk:"id"`                   // Always "derpmap" if found
	OmitDefaultRegions types.Bool      `tfsdk:"omit_default_regions"` // read from the server
	Regions            []dsRegionModel `tfsdk:"regions"`
}

type dsRegionModel struct {
	RegionID   types.Int64   `tfsdk:"region_id"`
	RegionCode types.String  `tfsdk:"region_code"`
	RegionName types.String  `tfsdk:"region_name"`
	Nodes      []dsNodeModel `tfsdk:"nodes"`
}

type dsNodeModel struct {
	Name     types.String `tfsdk:"name"`
	RegionID types.Int64  `tfsdk:"region_id"`
	HostName types.String `tfsdk:"host_name"`
	IPv4     types.String `tfsdk:"ipv4"`
	IPv6     types.String `tfsdk:"ipv6"`
}

//------------------------------
// Configure + Metadata
//------------------------------

func (d *derpMapDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *derpMapDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_derpmap"
}

//------------------------------
// Schema
//------------------------------

func (d *derpMapDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Data source for reading the single DERPMap object at /derpmap (typed).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always 'derpmap' if a DERPMap exists on the server.",
				Computed:    true,
			},
			"omit_default_regions": schema.BoolAttribute{
				Description: "If the server sets OmitDefaultRegions to true, the default Tailscale DERP regions won't be included.",
				Computed:    true,
			},
			"regions": schema.ListNestedAttribute{
				Description: "List of DERP regions from the server, typed read-only.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"region_id": schema.Int64Attribute{
							Description: "Numerical region ID, e.g. 901.",
							Computed:    true,
						},
						"region_code": schema.StringAttribute{
							Description: "Short region code, e.g. 'sea-lbr'.",
							Computed:    true,
						},
						"region_name": schema.StringAttribute{
							Description: "Descriptive region name, e.g. 'Seattle [LBR]'.",
							Computed:    true,
						},
						"nodes": schema.ListNestedAttribute{
							Description: "List of DERP nodes in this region, typed read-only.",
							Computed:    true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"name": schema.StringAttribute{
										Description: "Node name, e.g. 'sea-lbr1'.",
										Computed:    true,
									},
									"region_id": schema.Int64Attribute{
										Description: "Region ID the node belongs to.",
										Computed:    true,
									},
									"host_name": schema.StringAttribute{
										Description: "Hostname or domain, e.g. 'sea-derp1.lbrlabs.com'.",
										Computed:    true,
									},
									"ipv4": schema.StringAttribute{
										Description: "IPv4 address for the node.",
										Computed:    true,
									},
									"ipv6": schema.StringAttribute{
										Description: "IPv6 address for the node.",
										Computed:    true,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

//------------------------------
// Read => GET /derpmap
//------------------------------

func (d *derpMapDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	tflog.Debug(ctx, "Reading DERPMap data source")

	// 1) GET /derpmap
	getURL := fmt.Sprintf("%s/derpmap", d.endpoint)
	dm, err := doDERPMapDSRequest(ctx, d.httpClient, getURL)
	if err != nil {
		if isNotFound(err) {
			// no DERPMap => data source is empty
			return
		}
		resp.Diagnostics.AddError("DERPMap data source read error", err.Error())
		return
	}

	// 2) Convert Tailscale struct => typed DS model, sorting for stable ordering
	data := derpMapDataSourceModel{
		ID:                 types.StringValue("derpmap"),
		OmitDefaultRegions: types.BoolValue(dm.OmitDefaultRegions),
		Regions:            mapDSRegions(dm.Regions),
	}

	diags := resp.State.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}

//------------------------------
// Helpers
//------------------------------

func doDERPMapDSRequest(ctx context.Context, client *http.Client, url string) (*tsclient.ACLDERPMap, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create DS GET request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DS request error: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == 404 {
		return nil, fmt.Errorf("NotFound")
	}
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("DERPMap DS returned %d: %s", res.StatusCode, string(body))
	}

	var dm tsclient.ACLDERPMap
	if err := json.NewDecoder(res.Body).Decode(&dm); err != nil {
		return nil, fmt.Errorf("decode DS response: %w", err)
	}
	return &dm, nil
}

// mapDSRegions => gather region IDs, sort them, build dsRegionModel list.
func mapDSRegions(regions map[int]*tsclient.ACLDERPRegion) []dsRegionModel {
	if len(regions) == 0 {
		return []dsRegionModel{}
	}
	var rIDs []int
	for id := range regions {
		rIDs = append(rIDs, id)
	}
	sort.Ints(rIDs)

	var out []dsRegionModel
	for _, rID := range rIDs {
		regPtr := regions[rID]
		if regPtr == nil {
			continue
		}

		// Sort the nodes by e.g. name
		nodeRefs := regPtr.Nodes
		sort.Slice(nodeRefs, func(i, j int) bool {
			if nodeRefs[i] == nil {
				return false
			}
			if nodeRefs[j] == nil {
				return true
			}
			return nodeRefs[i].Name < nodeRefs[j].Name
		})

		var dsNodes []dsNodeModel
		for _, nptr := range nodeRefs {
			if nptr == nil {
				continue
			}
			dsNodes = append(dsNodes, dsNodeModel{
				Name:     types.StringValue(nptr.Name),
				RegionID: types.Int64Value(int64(nptr.RegionID)),
				HostName: types.StringValue(nptr.HostName),
				IPv4:     types.StringValue(nptr.IPv4),
				IPv6:     types.StringValue(nptr.IPv6),
			})
		}

		out = append(out, dsRegionModel{
			RegionID:   types.Int64Value(int64(rID)),
			RegionCode: types.StringValue(regPtr.RegionCode),
			RegionName: types.StringValue(regPtr.RegionName),
			Nodes:      dsNodes,
		})
	}
	return out
}

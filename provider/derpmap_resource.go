package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	tsclient "github.com/tailscale/tailscale-client-go/v2"
)

// Ensure interface compliance with the Terraform Plugin Framework.
var (
	_ resource.Resource              = &derpMapResource{}
	_ resource.ResourceWithConfigure = &derpMapResource{}
)

// NewDERPMapResource => a typed resource for /derpmap.
func NewDERPMapResource() resource.Resource {
	return &derpMapResource{}
}

// derpMapResource => manages the single DERPMap object. ID is always "derpmap".
type derpMapResource struct {
	httpClient *http.Client
	endpoint   string
}

// derpMapResourceModel => top-level Terraform attributes for the DERPMap.
type derpMapResourceModel struct {
	ID                 types.String         `tfsdk:"id"`                   // "derpmap"
	OmitDefaultRegions types.Bool           `tfsdk:"omit_default_regions"` // new
	Regions            []derpMapRegionModel `tfsdk:"regions"`              // list of regions
}

// derpMapRegionModel => one region block (region_id, region_code, region_name, nodes).
type derpMapRegionModel struct {
	RegionID   types.Int64        `tfsdk:"region_id"`
	RegionCode types.String       `tfsdk:"region_code"`
	RegionName types.String       `tfsdk:"region_name"`
	Nodes      []derpMapNodeModel `tfsdk:"nodes"`
}

// derpMapNodeModel => one node block (name, region_id, host_name, ipv4, ipv6).
type derpMapNodeModel struct {
	Name     types.String `tfsdk:"name"`
	RegionID types.Int64  `tfsdk:"region_id"`
	HostName types.String `tfsdk:"host_name"`
	IPv4     types.String `tfsdk:"ipv4"`
	IPv6     types.String `tfsdk:"ipv6"`
}

//------------------------------------------------------------------------------

func (r *derpMapResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	prov, ok := req.ProviderData.(*taclProvider)
	if !ok {
		return
	}
	r.httpClient = prov.httpClient
	r.endpoint = prov.endpoint
}

func (r *derpMapResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_derpmap"
}

// Schema => typed blocks for `omit_default_regions`, `regions`, and `nodes`.
func (r *derpMapResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the single ACLDERPMap object at /derpmap with typed fields.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always 'derpmap' once created.",
				Computed:    true,
			},
			"omit_default_regions": schema.BoolAttribute{
				Description: "If true, Tailscale's default DERP regions are omitted.",
				Optional:    true,
				Computed:    true,
			},
			"regions": schema.ListNestedAttribute{
				Description: "List of DERP regions.",
				Required:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"region_id": schema.Int64Attribute{
							Description: "Numerical region ID (e.g. 901).",
							Required:    true,
						},
						"region_code": schema.StringAttribute{
							Description: "Short region code, e.g. 'sea-lbr'.",
							Required:    true,
						},
						"region_name": schema.StringAttribute{
							Description: "Descriptive region name, e.g. 'Seattle [LBR]'.",
							Optional:    true,
						},
						"nodes": schema.ListNestedAttribute{
							Description: "List of DERP nodes in this region.",
							Optional:    true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"name": schema.StringAttribute{
										Description: "Node name, e.g. 'sea-lbr1'.",
										Required:    true,
									},
									"region_id": schema.Int64Attribute{
										Description: "Region ID the node belongs to.",
										Required:    true,
									},
									"host_name": schema.StringAttribute{
										Description: "Hostname, e.g. 'sea-derp1.lbrlabs.com'.",
										Required:    true,
									},
									"ipv4": schema.StringAttribute{
										Description: "IPv4 address.",
										Optional:    true,
									},
									"ipv6": schema.StringAttribute{
										Description: "IPv6 address.",
										Optional:    true,
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

// ------------------------------------------------------------------------------
// Create => POST /derpmap
// ------------------------------------------------------------------------------
func (r *derpMapResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan derpMapResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Convert plan => Tailscale's ACLDERPMap
	newDM := resourceModelToDERPMap(plan)

	// POST /derpmap
	postURL := fmt.Sprintf("%s/derpmap", r.endpoint)
	tflog.Debug(ctx, "Creating DERPMap", map[string]interface{}{"url": postURL})

	created, err := doDERPMapRequest(ctx, r.httpClient, http.MethodPost, postURL, newDM)
	if err != nil {
		resp.Diagnostics.AddError("Create DERPMap error", err.Error())
		return
	}

	final := derpMapToResourceModel(created)
	final.ID = types.StringValue("derpmap")

	diags = resp.State.Set(ctx, &final)
	resp.Diagnostics.Append(diags...)
}

// ------------------------------------------------------------------------------
// Read => GET /derpmap
// ------------------------------------------------------------------------------
func (r *derpMapResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state derpMapResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	getURL := fmt.Sprintf("%s/derpmap", r.endpoint)
	tflog.Debug(ctx, "Reading DERPMap", map[string]interface{}{"url": getURL})

	dm, err := doDERPMapRequest(ctx, r.httpClient, http.MethodGet, getURL, nil)
	if err != nil {
		if isNotFound(err) {
			// no DERPMap => remove from state
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read DERPMap error", err.Error())
		return
	}

	newState := derpMapToResourceModel(dm)
	newState.ID = types.StringValue("derpmap")

	diags = resp.State.Set(ctx, &newState)
	resp.Diagnostics.Append(diags...)
}

// ------------------------------------------------------------------------------
// Update => PUT /derpmap
// ------------------------------------------------------------------------------
func (r *derpMapResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan derpMapResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	updatedDM := resourceModelToDERPMap(plan)

	putURL := fmt.Sprintf("%s/derpmap", r.endpoint)
	tflog.Debug(ctx, "Updating DERPMap", map[string]interface{}{"url": putURL})

	res, err := doDERPMapRequest(ctx, r.httpClient, http.MethodPut, putURL, updatedDM)
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Update DERPMap error", err.Error())
		return
	}

	newState := derpMapToResourceModel(res)
	newState.ID = types.StringValue("derpmap")

	diags = resp.State.Set(ctx, &newState)
	resp.Diagnostics.Append(diags...)
}

// ------------------------------------------------------------------------------
// Delete => DELETE /derpmap
// ------------------------------------------------------------------------------
func (r *derpMapResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	delURL := fmt.Sprintf("%s/derpmap", r.endpoint)
	_, err := doDERPMapRequest(ctx, r.httpClient, http.MethodDelete, delURL, nil)
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Delete DERPMap error", err.Error())
		return
	}
	resp.State.RemoveResource(ctx)
}

//------------------------------------------------------------------------------
// Helpers
//------------------------------------------------------------------------------

func doDERPMapRequest(ctx context.Context, client *http.Client, method, url string, payload *tsclient.ACLDERPMap) (*tsclient.ACLDERPMap, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
		body = bytes.NewBuffer(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("DERPMap request creation error: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DERPMap request error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("NotFound")
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("DERPMap returned %d: %s", resp.StatusCode, string(msg))
	}

	var dm tsclient.ACLDERPMap
	if e := json.NewDecoder(resp.Body).Decode(&dm); e != nil {
		return nil, fmt.Errorf("decode DERPMap: %w", e)
	}
	return &dm, nil
}

// resourceModelToDERPMap => convert typed TF plan => Tailscale struct
func resourceModelToDERPMap(model derpMapResourceModel) *tsclient.ACLDERPMap {
	tsMap := tsclient.ACLDERPMap{
		// This depends on Tailscale's struct naming
		OmitDefaultRegions: model.OmitDefaultRegions.ValueBool(),
		Regions:            make(map[int]*tsclient.ACLDERPRegion),
	}
	for _, r := range model.Regions {
		rID := int(r.RegionID.ValueInt64())

		var nodePtrs []*tsclient.ACLDERPNode
		for _, node := range r.Nodes {
			nodePtrs = append(nodePtrs, &tsclient.ACLDERPNode{
				Name:     node.Name.ValueString(),
				RegionID: int(node.RegionID.ValueInt64()),
				HostName: node.HostName.ValueString(),
				IPv4:     node.IPv4.ValueString(),
				IPv6:     node.IPv6.ValueString(),
			})
		}

		tsMap.Regions[rID] = &tsclient.ACLDERPRegion{
			// If Tailscale's ACLDERPRegion has a RegionID field, set it here:
			RegionCode: r.RegionCode.ValueString(),
			RegionName: r.RegionName.ValueString(),
			Nodes:      nodePtrs,
		}
	}
	return &tsMap
}

// derpMapToResourceModel => convert Tailscale struct => typed TF state
// derpMapToResourceModel => convert Tailscale struct => typed TF state
func derpMapToResourceModel(dm *tsclient.ACLDERPMap) derpMapResourceModel {
	if dm == nil {
		return derpMapResourceModel{}
	}

	// 1) Collect region IDs into a slice so we can sort them
	var regionIDs []int
	for rID := range dm.Regions {
		regionIDs = append(regionIDs, rID)
	}
	sort.Ints(regionIDs) // stable ascending order

	var regionList []derpMapRegionModel

	// 2) Iterate over sorted region IDs
	for _, rID := range regionIDs {
		regionPtr := dm.Regions[rID]
		if regionPtr == nil {
			continue
		}
		// Gather node data in a slice
		var nodes []derpMapNodeModel
		// If you want a stable node order, e.g. by node's Name:
		// create a slice of node references first
		var nodeRefs []*tsclient.ACLDERPNode
		for _, n := range regionPtr.Nodes {
			if n != nil {
				nodeRefs = append(nodeRefs, n)
			}
		}
		// sort nodeRefs by name (or hostName, or regionID—whatever you prefer)
		sort.Slice(nodeRefs, func(i, j int) bool {
			return nodeRefs[i].Name < nodeRefs[j].Name
		})

		// now build the typed list
		for _, nptr := range nodeRefs {
			nodes = append(nodes, derpMapNodeModel{
				Name:     types.StringValue(nptr.Name),
				RegionID: types.Int64Value(int64(nptr.RegionID)),
				HostName: types.StringValue(nptr.HostName),
				IPv4:     types.StringValue(nptr.IPv4),
				IPv6:     types.StringValue(nptr.IPv6),
			})
		}

		// Build one region
		regionList = append(regionList, derpMapRegionModel{
			RegionID:   types.Int64Value(int64(rID)), // from map key
			RegionCode: types.StringValue(regionPtr.RegionCode),
			RegionName: types.StringValue(regionPtr.RegionName),
			Nodes:      nodes,
		})
	}

	return derpMapResourceModel{
		ID:                 types.StringValue("derpmap"),
		OmitDefaultRegions: types.BoolValue(dm.OmitDefaultRegions),
		Regions:            regionList,
	}
}

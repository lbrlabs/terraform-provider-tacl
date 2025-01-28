package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// -----------------------------------------------------------------------------
// Resource Declaration
// -----------------------------------------------------------------------------

var (
	_ resource.Resource              = &nodeattrResource{}
	_ resource.ResourceWithConfigure = &nodeattrResource{}
)

// NewNodeAttrResource => constructor
func NewNodeAttrResource() resource.Resource {
	return &nodeattrResource{}
}

type nodeattrResource struct {
	httpClient *http.Client
	endpoint   string
}

// nodeattrResourceModel => The Terraform schema model.
// "target" & "attr" are both types.List so we can handle unknown values, etc.
type nodeattrResourceModel struct {
	ID      types.String `tfsdk:"id"`
	Target  types.List   `tfsdk:"target"`  // Terraform list of strings
	Attr    types.List   `tfsdk:"attr"`    // Terraform list of strings
	AppJSON types.String `tfsdk:"app_json"`
}

// NodeAttrGrantInput => Request shape for create/update
type NodeAttrGrantInput struct {
	Target []string               `json:"target"`
	Attr   []string               `json:"attr,omitempty"`
	App    map[string]interface{} `json:"app,omitempty"`
}

// NodeAttrResponse => TACL's response object
type NodeAttrResponse struct {
	ID     string                 `json:"id"`
	Target []string               `json:"target"`
	Attr   []string               `json:"attr,omitempty"`
	App    map[string]interface{} `json:"app,omitempty"`
}

// -----------------------------------------------------------------------------
// Configure / Metadata / Schema
// -----------------------------------------------------------------------------

func (r *nodeattrResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	p, ok := req.ProviderData.(*taclProvider)
	if !ok {
		return
	}
	r.httpClient = p.httpClient
	r.endpoint = p.endpoint
}

func (r *nodeattrResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nodeattr"
}

func (r *nodeattrResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a nodeattr entry by stable ID in TACL’s /nodeattrs.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "TACL's stable ID for this nodeattr.",
				Computed:    true,
			},
			"target": schema.ListAttribute{
				Description: "Optional list of targets (the server may overwrite if `app_json` is used).",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					// If user omits target => unknown => the server can fill in ["*"] or whatever
					listplanmodifier.UseStateForUnknown(),
				},
			},
			"attr": schema.ListAttribute{
				Description: "Optional list of attributes (mutually exclusive with `app_json`).",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,

				// Default to empty list if user omits
				Default: listdefault.StaticValue(
					types.ListValueMust(types.StringType, []attr.Value{}),
				),
			},
			"app_json": schema.StringAttribute{
				Description: "Optional JSON for `app`. Must be empty if `attr` is used.",
				Optional:    true,
			},
		},
	}
}

// -----------------------------------------------------------------------------
// Create => POST /nodeattrs
// -----------------------------------------------------------------------------

func (r *nodeattrResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan nodeattrResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Convert from types.List => []string
	targetSlice, err := listToStringSlice(ctx, plan.Target)
	if err != nil {
		resp.Diagnostics.AddError("Error reading target", err.Error())
		return
	}
	attrSlice, err := listToStringSlice(ctx, plan.Attr)
	if err != nil {
		resp.Diagnostics.AddError("Error reading attr", err.Error())
		return
	}

	hasAttr := len(attrSlice) > 0
	hasApp := !plan.AppJSON.IsNull() && plan.AppJSON.ValueString() != ""

	// Exactly one of attr or app must be set
	if (hasAttr && hasApp) || (!hasAttr && !hasApp) {
		resp.Diagnostics.AddError("Invalid config",
			"Exactly one of `attr` or `app_json` must be set.")
		return
	}

	// Build request
	input := NodeAttrGrantInput{
		Target: targetSlice,
	}
	if hasAttr {
		input.Attr = attrSlice
	} else {
		// parse app JSON
		var app map[string]interface{}
		if err := json.Unmarshal([]byte(plan.AppJSON.ValueString()), &app); err != nil {
			resp.Diagnostics.AddError("Invalid app_json", err.Error())
			return
		}
		input.App = app

		// Option A fix => if app is set, force target=["*"]
		input.Target = []string{"*"}
	}

	url := fmt.Sprintf("%s/nodeattrs", r.endpoint)
	tflog.Debug(ctx, "Creating nodeattr", map[string]interface{}{
		"url":     url,
		"payload": input,
	})

	body, err := doNodeAttrRequest(ctx, r.httpClient, http.MethodPost, url, input)
	if err != nil {
		resp.Diagnostics.AddError("Create nodeattr error", err.Error())
		return
	}

	var created NodeAttrResponse
	if e := json.Unmarshal(body, &created); e != nil {
		resp.Diagnostics.AddError("Parse create response error", e.Error())
		return
	}

	// Fill final plan from server
	plan.ID = types.StringValue(created.ID)

	plan.Target, err = stringSliceToList(ctx, created.Target)
	if err != nil {
		resp.Diagnostics.AddError("Error converting target from server", err.Error())
		return
	}

	if len(created.Attr) > 0 {
		// We got an attr-based nodeattr
		plan.Attr, err = stringSliceToList(ctx, created.Attr)
		if err != nil {
			resp.Diagnostics.AddError("Error converting attr from server", err.Error())
			return
		}
		plan.AppJSON = types.StringNull()
	} else if created.App != nil {
		// We got an app-based nodeattr
		b, _ := json.Marshal(created.App)
		plan.AppJSON = types.StringValue(string(b))

		emptyList, diags2 := types.ListValue(types.StringType, []attr.Value{})
		resp.Diagnostics.Append(diags2...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.Attr = emptyList
	} else {
		// Neither attr nor app returned -> set them empty
		emptyList, diags2 := types.ListValue(types.StringType, []attr.Value{})
		resp.Diagnostics.Append(diags2...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.Attr = emptyList
		plan.AppJSON = types.StringNull()
	}

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// -----------------------------------------------------------------------------
// Read => GET /nodeattrs/:id
// -----------------------------------------------------------------------------

func (r *nodeattrResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state nodeattrResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if id == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	url := fmt.Sprintf("%s/nodeattrs/%s", r.endpoint, id)
	tflog.Debug(ctx, "Reading nodeattr by ID", map[string]interface{}{
		"url": url,
		"id":  id,
	})

	body, err := doNodeAttrRequest(ctx, r.httpClient, http.MethodGet, url, nil)
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read nodeattr error", err.Error())
		return
	}

	var fetched NodeAttrResponse
	if e := json.Unmarshal(body, &fetched); e != nil {
		resp.Diagnostics.AddError("Parse read response error", e.Error())
		return
	}

	state.ID = types.StringValue(fetched.ID)

	// Convert from []string => types.List
	state.Target, err = stringSliceToList(ctx, fetched.Target)
	if err != nil {
		resp.Diagnostics.AddError("Error converting target from server", err.Error())
		return
	}

	if len(fetched.Attr) > 0 {
		state.Attr, err = stringSliceToList(ctx, fetched.Attr)
		if err != nil {
			resp.Diagnostics.AddError("Error converting attr from server", err.Error())
			return
		}
		state.AppJSON = types.StringNull()
	} else if fetched.App != nil {
		b, _ := json.Marshal(fetched.App)
		state.AppJSON = types.StringValue(string(b))

		emptyList, diags2 := types.ListValue(types.StringType, []attr.Value{})
		resp.Diagnostics.Append(diags2...)
		if resp.Diagnostics.HasError() {
			return
		}
		state.Attr = emptyList
	} else {
		emptyList, diags2 := types.ListValue(types.StringType, []attr.Value{})
		resp.Diagnostics.Append(diags2...)
		if resp.Diagnostics.HasError() {
			return
		}
		state.Attr = emptyList
		state.AppJSON = types.StringNull()
	}

	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// -----------------------------------------------------------------------------
// Update => PUT /nodeattrs => { "id":..., "grant":{...} }
// -----------------------------------------------------------------------------

func (r *nodeattrResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var oldState nodeattrResourceModel
	diags := req.State.Get(ctx, &oldState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var plan nodeattrResourceModel
	diags = req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Keep same ID
	plan.ID = oldState.ID
	id := plan.ID.ValueString()
	if id == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	targetSlice, err := listToStringSlice(ctx, plan.Target)
	if err != nil {
		resp.Diagnostics.AddError("Error reading target", err.Error())
		return
	}
	attrSlice, err := listToStringSlice(ctx, plan.Attr)
	if err != nil {
		resp.Diagnostics.AddError("Error reading attr", err.Error())
		return
	}

	hasAttr := len(attrSlice) > 0
	hasApp := !plan.AppJSON.IsNull() && plan.AppJSON.ValueString() != ""
	if (hasAttr && hasApp) || (!hasAttr && !hasApp) {
		resp.Diagnostics.AddError("Invalid config",
			"Exactly one of `attr` or `app_json` must be set.")
		return
	}

	input := NodeAttrGrantInput{
		Target: targetSlice,
	}
	if hasAttr {
		input.Attr = attrSlice
	} else {
		var app map[string]interface{}
		if err := json.Unmarshal([]byte(plan.AppJSON.ValueString()), &app); err != nil {
			resp.Diagnostics.AddError("Invalid app_json", err.Error())
			return
		}
		input.App = app

		// Option A fix => if app is used, force Target=["*"]
		input.Target = []string{"*"}
	}

	payload := map[string]interface{}{
		"id":    id,
		"grant": input,
	}

	url := fmt.Sprintf("%s/nodeattrs", r.endpoint)
	tflog.Debug(ctx, "Updating nodeattr", map[string]interface{}{
		"url":     url,
		"payload": payload,
	})

	body, err := doNodeAttrRequest(ctx, r.httpClient, http.MethodPut, url, payload)
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Update nodeattr error", err.Error())
		return
	}

	var updated NodeAttrResponse
	if e := json.Unmarshal(body, &updated); e != nil {
		resp.Diagnostics.AddError("Parse update response error", e.Error())
		return
	}

	plan.ID = types.StringValue(updated.ID)

	plan.Target, err = stringSliceToList(ctx, updated.Target)
	if err != nil {
		resp.Diagnostics.AddError("Error converting target from server", err.Error())
		return
	}

	if len(updated.Attr) > 0 {
		plan.Attr, err = stringSliceToList(ctx, updated.Attr)
		if err != nil {
			resp.Diagnostics.AddError("Error converting attr from server", err.Error())
			return
		}
		plan.AppJSON = types.StringNull()
	} else if updated.App != nil {
		b, _ := json.Marshal(updated.App)
		plan.AppJSON = types.StringValue(string(b))

		emptyList, diags2 := types.ListValue(types.StringType, []attr.Value{})
		resp.Diagnostics.Append(diags2...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.Attr = emptyList
	} else {
		emptyList, diags2 := types.ListValue(types.StringType, []attr.Value{})
		resp.Diagnostics.Append(diags2...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.Attr = emptyList
		plan.AppJSON = types.StringNull()
	}

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// Delete => no changes from your last version
func (r *nodeattrResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data nodeattrResourceModel
	diags := req.State.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := data.ID.ValueString()
	if id == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	payload := map[string]string{"id": id}
	url := fmt.Sprintf("%s/nodeattrs", r.endpoint)
	tflog.Debug(ctx, "Deleting nodeattr by ID", map[string]interface{}{
		"url":     url,
		"payload": payload,
	})

	_, err := doNodeAttrRequest(ctx, r.httpClient, http.MethodDelete, url, payload)
	if err != nil {
		if isNotFound(err) {
			// already gone
		} else {
			resp.Diagnostics.AddError("Delete nodeattr error", err.Error())
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

// -----------------------------------------------------------------------------
// Helper Functions
// -----------------------------------------------------------------------------

func doNodeAttrRequest(ctx context.Context, client *http.Client, method, url string, payload interface{}) ([]byte, error) {
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

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nodeattr request error: %w", err)
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
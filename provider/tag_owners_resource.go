package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// TagOwnerResponse => shape from the server
type TagOwnerResponse struct {
	Name   string   `json:"name"`
	Owners []string `json:"owners"`
}

// Ensure we match the Terraform Resource interfaces
var (
	_ resource.Resource              = &tagOwnersResource{}
	_ resource.ResourceWithConfigure = &tagOwnersResource{}
)

func NewTagOwnersResource() resource.Resource {
	return &tagOwnersResource{}
}

type tagOwnersResource struct {
	httpClient *http.Client
	endpoint   string
}

// tagOwnersResourceModel => user sets name + owners, we store ID same as name
type tagOwnersResourceModel struct {
	ID     types.String   `tfsdk:"id"`     // same as "name"
	Name   types.String   `tfsdk:"name"`   // required
	Owners []types.String `tfsdk:"owners"` // required
}

func (r *tagOwnersResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *tagOwnersResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	// e.g. "tacl_tag_owner"
	resp.TypeName = req.ProviderTypeName + "_tag_owner"
}

func (r *tagOwnersResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a single TagOwner by name in TACL’s /tagowners.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Same as 'name' once created.",
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The unique tag name (e.g. 'webserver').",
				Required:    true,
			},
			"owners": schema.ListAttribute{
				Description: "List of owners for this tag.",
				Required:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// --------------------------------------------------------------------------------
// Create => POST /tagowners => { name, owners }
// --------------------------------------------------------------------------------

func (r *tagOwnersResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tagOwnersResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	payload := map[string]interface{}{
		"name":   plan.Name.ValueString(),
		"owners": toGoStringSlice(plan.Owners),
	}

	postURL := fmt.Sprintf("%s/tagowners", r.endpoint)
	tflog.Debug(ctx, "Creating TagOwner", map[string]interface{}{
		"url":     postURL,
		"payload": payload,
	})

	body, err := doTagOwnersRequest(ctx, r.httpClient, http.MethodPost, postURL, payload)
	if err != nil {
		resp.Diagnostics.AddError("Create tagowner error", err.Error())
		return
	}

	var created TagOwnerResponse
	if e := json.Unmarshal(body, &created); e != nil {
		resp.Diagnostics.AddError("Parse create response error", e.Error())
		return
	}

	// set ID => name
	plan.ID = types.StringValue(created.Name)
	plan.Name = types.StringValue(created.Name)
	plan.Owners = toTerraformStringSlice(created.Owners)

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// --------------------------------------------------------------------------------
// Read => GET /tagowners/:name
// --------------------------------------------------------------------------------

func (r *tagOwnersResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data tagOwnersResourceModel
	diags := req.State.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := data.Name.ValueString()
	if name == "" {
		// no resource
		resp.State.RemoveResource(ctx)
		return
	}

	getURL := fmt.Sprintf("%s/tagowners/%s", r.endpoint, name)
	tflog.Debug(ctx, "Reading TagOwner by name", map[string]interface{}{
		"url":  getURL,
		"name": name,
	})

	body, err := doTagOwnersRequest(ctx, r.httpClient, http.MethodGet, getURL, nil)
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read tagowner error", err.Error())
		return
	}

	var fetched TagOwnerResponse
	if e := json.Unmarshal(body, &fetched); e != nil {
		resp.Diagnostics.AddError("Parse read response error", e.Error())
		return
	}

	data.ID = types.StringValue(fetched.Name)
	data.Name = types.StringValue(fetched.Name)
	data.Owners = toTerraformStringSlice(fetched.Owners)

	diags = resp.State.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}

// --------------------------------------------------------------------------------
// Update => PUT /tagowners => { name, owners }
// --------------------------------------------------------------------------------

func (r *tagOwnersResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var oldState tagOwnersResourceModel
	diags := req.State.Get(ctx, &oldState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var plan tagOwnersResourceModel
	diags = req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// keep name the same
	plan.ID = oldState.ID
	plan.Name = oldState.Name
	name := plan.Name.ValueString()
	if name == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	payload := map[string]interface{}{
		"name":   name,
		"owners": toGoStringSlice(plan.Owners),
	}

	putURL := fmt.Sprintf("%s/tagowners", r.endpoint)
	tflog.Debug(ctx, "Updating TagOwner by name", map[string]interface{}{
		"url":     putURL,
		"payload": payload,
	})

	body, err := doTagOwnersRequest(ctx, r.httpClient, http.MethodPut, putURL, payload)
	if err != nil {
		if isNotFound(err) {
			// no such tag => remove
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Update tagowner error", err.Error())
		return
	}

	var updated TagOwnerResponse
	if e := json.Unmarshal(body, &updated); e != nil {
		resp.Diagnostics.AddError("Parse update response error", e.Error())
		return
	}

	plan.ID = types.StringValue(updated.Name)
	plan.Name = types.StringValue(updated.Name)
	plan.Owners = toTerraformStringSlice(updated.Owners)

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// --------------------------------------------------------------------------------
// Delete => DELETE /tagowners => { "name":"..." }
// --------------------------------------------------------------------------------

func (r *tagOwnersResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data tagOwnersResourceModel
	diags := req.State.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := data.Name.ValueString()
	if name == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	delPayload := map[string]string{"name": name}
	delURL := fmt.Sprintf("%s/tagowners", r.endpoint)
	tflog.Debug(ctx, "Deleting TagOwner", map[string]interface{}{
		"url":  delURL,
		"name": name,
	})

	_, err := doTagOwnersRequest(ctx, r.httpClient, http.MethodDelete, delURL, delPayload)
	if err != nil {
		if isNotFound(err) {
			// already gone
		} else {
			resp.Diagnostics.AddError("Delete tagowner error", err.Error())
			return
		}
	}

	resp.State.RemoveResource(ctx)
}

// --------------------------------------------------------------------------------
// HTTP helper
// --------------------------------------------------------------------------------

func doTagOwnersRequest(ctx context.Context, client *http.Client, method, url string, payload interface{}) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tagOwner request: %w", err)
		}
		body = bytes.NewBuffer(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("tagOwner request creation error: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	respHTTP, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tagOwner request error: %w", err)
	}
	defer respHTTP.Body.Close()

	if respHTTP.StatusCode == 404 {
		return nil, &NotFoundError{Message: "TagOwner not found"}
	}
	if respHTTP.StatusCode >= 300 {
		msg, _ := io.ReadAll(respHTTP.Body)
		return nil, fmt.Errorf("TACL returned %d: %s", respHTTP.StatusCode, string(msg))
	}

	return io.ReadAll(respHTTP.Body)
}

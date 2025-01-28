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

// Ensure postureResource implements Resource/WithConfigure
var (
	_ resource.Resource              = &postureResource{}
	_ resource.ResourceWithConfigure = &postureResource{}
)

// NewPostureResource => constructor
func NewPostureResource() resource.Resource {
	return &postureResource{}
}

type postureResource struct {
	httpClient *http.Client
	endpoint   string
}

// postureResourceModel => name + rules
// If name="default", we treat it as the default posture route
type postureResourceModel struct {
	ID    types.String `tfsdk:"id"`
	Name  types.String `tfsdk:"name"`
	Rules types.List   `tfsdk:"rules"` // list of strings
}

// -----------------------------------------------------------------------------
// TACL JSON shapes
// -----------------------------------------------------------------------------

// postureCreatePayload => for POST /postures => { "name":"...", "rules":[] }
type postureCreatePayload struct {
	Name  string   `json:"name"`
	Rules []string `json:"rules"`
}

// postureUpdatePayload => for PUT /postures => { "name":"...", "rules":[] } (same shape)
type postureUpdatePayload struct {
	Name  string   `json:"name"`
	Rules []string `json:"rules"`
}

// postureDeletePayload => for DELETE /postures => { "name":"..." }
type postureDeletePayload struct {
	Name string `json:"name"`
}

// defaultPosturePayload => for PUT /postures/default => { "defaultSourcePosture":[] }
type defaultPosturePayload struct {
	DefaultSourcePosture []string `json:"defaultSourcePosture"`
}

// -----------------------------------------------------------------------------
// Configure/Metadata/Schema
// -----------------------------------------------------------------------------

func (r *postureResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *postureResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_posture"
}

// We define "name" (string) + "rules" (list of strings).
// "ID" is a computed field storing the posture's name
func (r *postureResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a single posture (named or default). If 'name' = 'default', we manage the default posture at /postures/default.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Same as 'name'.",
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "Unique name of posture (or 'default').",
				Required:    true,
			},
			"rules": schema.ListAttribute{
				Description: "List of posture rules (strings).",
				Required:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// -----------------------------------------------------------------------------
// Create => if name=default => PUT /postures/default
//           else => POST /postures
// -----------------------------------------------------------------------------

func (r *postureResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan postureResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()
	rules, err := listToGoStrings(ctx, plan.Rules)
	if err != nil {
		resp.Diagnostics.AddError("Rules conversion error", err.Error())
		return
	}

	if name == "default" {
		// => PUT /postures/default => { "defaultSourcePosture": rules }
		putURL := fmt.Sprintf("%s/postures/default", r.endpoint)
		payload := defaultPosturePayload{DefaultSourcePosture: rules}

		tflog.Debug(ctx, "Creating default posture via TACL", map[string]interface{}{
			"url":     putURL,
			"payload": payload,
		})

		_, err := doPostureRequest(ctx, r.httpClient, http.MethodPut, putURL, payload)
		if err != nil {
			resp.Diagnostics.AddError("Create default posture error", err.Error())
			return
		}
		plan.ID = plan.Name // store "default" in ID
	} else {
		// => POST /postures => { "name":"...", "rules":[] }
		postURL := fmt.Sprintf("%s/postures", r.endpoint)
		payload := postureCreatePayload{
			Name:  name,
			Rules: rules,
		}
		tflog.Debug(ctx, "Creating named posture via TACL", map[string]interface{}{
			"url":     postURL,
			"payload": payload,
		})

		respBody, err := doPostureRequest(ctx, r.httpClient, http.MethodPost, postURL, payload)
		if err != nil {
			resp.Diagnostics.AddError("Create posture error", err.Error())
			return
		}

		// Typically server responds with { "name":"...", "rules":[...] }
		var created struct {
			Name  string   `json:"name"`
			Rules []string `json:"rules"`
		}
		if e := json.Unmarshal(respBody, &created); e != nil {
			resp.Diagnostics.AddError("Error parsing create response", e.Error())
			return
		}

		plan.ID = types.StringValue(created.Name)
	}

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// -----------------------------------------------------------------------------
// Read => if name=default => GET /postures/default
//         else => GET /postures/:name
// -----------------------------------------------------------------------------

func (r *postureResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state postureResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := state.Name.ValueString()
	if name == "" {
		// no posture
		resp.State.RemoveResource(ctx)
		return
	}

	if name == "default" {
		// GET /postures/default => { "defaultSourcePosture":[] }
		getURL := fmt.Sprintf("%s/postures/default", r.endpoint)
		tflog.Debug(ctx, "Reading default posture", map[string]interface{}{
			"url": getURL,
		})
		body, err := doPostureRequest(ctx, r.httpClient, http.MethodGet, getURL, nil)
		if err != nil {
			if IsNotFound(err) {
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Read default posture error", err.Error())
			return
		}
		var fetched map[string][]string // e.g. { "defaultSourcePosture": [...] }
		if e := json.Unmarshal(body, &fetched); e != nil {
			resp.Diagnostics.AddError("Parse default posture error", e.Error())
			return
		}
		rules := fetched["defaultSourcePosture"]
		state.Rules, _ = goStringsToList(rules)

	} else {
		// GET /postures/:name => { "name":"...", "rules":[] }
		getURL := fmt.Sprintf("%s/postures/%s", r.endpoint, name)
		tflog.Debug(ctx, "Reading named posture", map[string]interface{}{
			"url":  getURL,
			"name": name,
		})
		body, err := doPostureRequest(ctx, r.httpClient, http.MethodGet, getURL, nil)
		if err != nil {
			if IsNotFound(err) {
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Read named posture error", err.Error())
			return
		}
		var fetched struct {
			Name  string   `json:"name"`
			Rules []string `json:"rules"`
		}
		if e := json.Unmarshal(body, &fetched); e != nil {
			resp.Diagnostics.AddError("Parse named posture error", e.Error())
			return
		}
		state.Rules, _ = goStringsToList(fetched.Rules)
	}

	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// -----------------------------------------------------------------------------
// Update => if name=default => PUT /postures/default
//           else => PUT /postures => { "name":"...", "rules":[] }
// -----------------------------------------------------------------------------

func (r *postureResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var oldState postureResourceModel
	diags := req.State.Get(ctx, &oldState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var plan postureResourceModel
	diags = req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()
	rules, err := listToGoStrings(ctx, plan.Rules)
	if err != nil {
		resp.Diagnostics.AddError("Rules conversion error", err.Error())
		return
	}

	if name == "default" {
		// PUT /postures/default => { "defaultSourcePosture":[] }
		putURL := fmt.Sprintf("%s/postures/default", r.endpoint)
		payload := defaultPosturePayload{
			DefaultSourcePosture: rules,
		}
		tflog.Debug(ctx, "Updating default posture", map[string]interface{}{
			"url":     putURL,
			"payload": payload,
		})
		_, err := doPostureRequest(ctx, r.httpClient, http.MethodPut, putURL, payload)
		if err != nil {
			if IsNotFound(err) {
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Update default posture error", err.Error())
			return
		}
		plan.ID = plan.Name

	} else {
		// PUT /postures => { "name":"...", "rules":[] }
		putURL := fmt.Sprintf("%s/postures", r.endpoint)
		payload := postureUpdatePayload{
			Name:  name,
			Rules: rules,
		}
		tflog.Debug(ctx, "Updating named posture", map[string]interface{}{
			"url":     putURL,
			"payload": payload,
		})
		body, err := doPostureRequest(ctx, r.httpClient, http.MethodPut, putURL, payload)
		if err != nil {
			if IsNotFound(err) {
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Update named posture error", err.Error())
			return
		}
		// We might parse the response if needed, but presumably the server returns { "name":"...", "rules":[] }
		var updated struct {
			Name  string   `json:"name"`
			Rules []string `json:"rules"`
		}
		if e := json.Unmarshal(body, &updated); e != nil {
			resp.Diagnostics.AddError("Parse update response error", e.Error())
			return
		}
		plan.ID = types.StringValue(updated.Name)
	}

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// -----------------------------------------------------------------------------
// Delete => if name=default => DELETE /postures/default
//           else => DELETE /postures => { "name":"..." }
// -----------------------------------------------------------------------------

func (r *postureResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data postureResourceModel
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

	if name == "default" {
		// DELETE /postures/default
		delURL := fmt.Sprintf("%s/postures/default", r.endpoint)
		tflog.Debug(ctx, "Deleting default posture", map[string]interface{}{
			"url": delURL,
		})
		_, err := doPostureRequest(ctx, r.httpClient, http.MethodDelete, delURL, nil)
		if err != nil {
			if IsNotFound(err) {
				// already gone
			} else {
				resp.Diagnostics.AddError("Delete default posture error", err.Error())
				return
			}
		}
		resp.State.RemoveResource(ctx)
	} else {
		// DELETE /postures => body { "name": name }
		delURL := fmt.Sprintf("%s/postures", r.endpoint)
		tflog.Debug(ctx, "Deleting named posture", map[string]interface{}{
			"url":  delURL,
			"name": name,
		})
		payload := postureDeletePayload{Name: name}
		_, err := doPostureRequest(ctx, r.httpClient, http.MethodDelete, delURL, payload)
		if err != nil {
			if IsNotFound(err) {
				// already gone
			} else {
				resp.Diagnostics.AddError("Delete named posture error", err.Error())
				return
			}
		}
		resp.State.RemoveResource(ctx)
	}
}

// -----------------------------------------------------------------------------
// Helper Functions
// -----------------------------------------------------------------------------

// doPostureRequest => basic HTTP for the posture resource
func doPostureRequest(ctx context.Context, client *http.Client, method, url string, payload interface{}) ([]byte, error) {
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

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, &NotFoundError{Message: "posture not found"}
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TACL returned %d: %s", resp.StatusCode, string(msg))
	}

	return io.ReadAll(resp.Body)
}

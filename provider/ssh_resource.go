// ssh_resource.go

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

// TaclSSHResponse => server's shape for a single SSH entry
type TaclSSHResponse struct {
	ID          string   `json:"id"`
	Action      string   `json:"action"`
	Src         []string `json:"src,omitempty"`
	Dst         []string `json:"dst,omitempty"`
	Users       []string `json:"users,omitempty"`
	CheckPeriod string   `json:"checkPeriod,omitempty"`
	AcceptEnv   []string `json:"acceptEnv,omitempty"`
}

var (
	_ resource.Resource              = &sshResource{}
	_ resource.ResourceWithConfigure = &sshResource{}
)

func NewSSHResource() resource.Resource {
	return &sshResource{}
}

type sshResource struct {
	httpClient *http.Client
	endpoint   string
}

type sshResourceModel struct {
	ID          types.String   `tfsdk:"id"`
	Action      types.String   `tfsdk:"action"`
	Src         []types.String `tfsdk:"src"`
	Dst         []types.String `tfsdk:"dst"`
	Users       []types.String `tfsdk:"users"`
	CheckPeriod types.String   `tfsdk:"check_period"`
	AcceptEnv   []types.String `tfsdk:"accept_env"`
}

func (r *sshResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *sshResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh"
}

func (r *sshResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a single SSH rule by stable ID in TACL’s /ssh.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Stable UUID of the SSH rule.",
				Computed:    true,
			},
			"action": schema.StringAttribute{
				Description: "SSH action: 'accept' or 'check'.",
				Required:    true,
			},
			"src": schema.ListAttribute{
				Description: "Sources (tags, CIDRs).",
				Required:    true,
				ElementType: types.StringType,
			},
			"dst": schema.ListAttribute{
				Description: "Destinations (tags, host:port, etc.).",
				Required:    true,
				ElementType: types.StringType,
			},
			"users": schema.ListAttribute{
				Description: "List of SSH users allowed.",
				Required:    true,
				ElementType: types.StringType,
			},
			"check_period": schema.StringAttribute{
				Description: "Optional duration if action='check', e.g. '12h'.",
				Optional:    true,
			},
			"accept_env": schema.ListAttribute{
				Description: "Optional list of environment variables to allow.",
				Optional:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// CREATE => POST /ssh
func (r *sshResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sshResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	payload := map[string]interface{}{
		"action":      plan.Action.ValueString(),
		"src":         toGoStringSlice(plan.Src),
		"dst":         toGoStringSlice(plan.Dst),
		"users":       toGoStringSlice(plan.Users),
		"checkPeriod": plan.CheckPeriod.ValueString(),
		"acceptEnv":   toGoStringSlice(plan.AcceptEnv),
	}

	postURL := fmt.Sprintf("%s/ssh", r.endpoint)
	tflog.Debug(ctx, "Creating SSH rule", map[string]interface{}{
		"url":     postURL,
		"payload": payload,
	})

	body, err := doSSHIDRequest(ctx, r.httpClient, http.MethodPost, postURL, payload)
	if err != nil {
		resp.Diagnostics.AddError("Create SSH error", err.Error())
		return
	}

	var created TaclSSHResponse
	if e := json.Unmarshal(body, &created); e != nil {
		resp.Diagnostics.AddError("Parse create response error", e.Error())
		return
	}

	plan.ID = types.StringValue(created.ID)
	plan.Action = types.StringValue(created.Action)
	plan.Src = toTerraformStringSlice(created.Src)
	plan.Dst = toTerraformStringSlice(created.Dst)
	plan.Users = toTerraformStringSlice(created.Users)

	if created.CheckPeriod != "" {
		plan.CheckPeriod = types.StringValue(created.CheckPeriod)
	} else {
		plan.CheckPeriod = types.StringNull()
	}

	if len(created.AcceptEnv) > 0 {
		plan.AcceptEnv = toTerraformStringSlice(created.AcceptEnv)
	} else {
		// Return null to match a plan that omitted it
		plan.AcceptEnv = nilListOfString()
	}

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// READ => GET /ssh/:id
func (r *sshResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data sshResourceModel
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

	getURL := fmt.Sprintf("%s/ssh/%s", r.endpoint, id)
	tflog.Debug(ctx, "Reading SSH rule", map[string]interface{}{
		"url": getURL,
		"id":  id,
	})

	body, err := doSSHIDRequest(ctx, r.httpClient, http.MethodGet, getURL, nil)
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read SSH error", err.Error())
		return
	}

	var fetched TaclSSHResponse
	if e := json.Unmarshal(body, &fetched); e != nil {
		resp.Diagnostics.AddError("Parse read response error", e.Error())
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

// UPDATE => PUT /ssh => payload { "id":"...", "rule": {...} }
func (r *sshResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var old sshResourceModel
	diags := req.State.Get(ctx, &old)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var plan sshResourceModel
	diags = req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = old.ID
	id := plan.ID.ValueString()
	if id == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	payload := map[string]interface{}{
		"id": id,
		"rule": map[string]interface{}{
			"action":      plan.Action.ValueString(),
			"src":         toGoStringSlice(plan.Src),
			"dst":         toGoStringSlice(plan.Dst),
			"users":       toGoStringSlice(plan.Users),
			"checkPeriod": plan.CheckPeriod.ValueString(),
			"acceptEnv":   toGoStringSlice(plan.AcceptEnv),
		},
	}

	putURL := fmt.Sprintf("%s/ssh", r.endpoint)
	tflog.Debug(ctx, "Updating SSH rule", map[string]interface{}{
		"url":     putURL,
		"payload": payload,
	})

	body, err := doSSHIDRequest(ctx, r.httpClient, http.MethodPut, putURL, payload)
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Update SSH error", err.Error())
		return
	}

	var updated TaclSSHResponse
	if e := json.Unmarshal(body, &updated); e != nil {
		resp.Diagnostics.AddError("Parse update response error", e.Error())
		return
	}

	plan.ID = types.StringValue(updated.ID)
	plan.Action = types.StringValue(updated.Action)
	plan.Src = toTerraformStringSlice(updated.Src)
	plan.Dst = toTerraformStringSlice(updated.Dst)
	plan.Users = toTerraformStringSlice(updated.Users)

	if updated.CheckPeriod != "" {
		plan.CheckPeriod = types.StringValue(updated.CheckPeriod)
	} else {
		plan.CheckPeriod = types.StringNull()
	}

	if len(updated.AcceptEnv) > 0 {
		plan.AcceptEnv = toTerraformStringSlice(updated.AcceptEnv)
	} else {
		plan.AcceptEnv = nilListOfString()
	}

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// DELETE => DELETE /ssh => { "id":"..." }
func (r *sshResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data sshResourceModel
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

	delPayload := map[string]string{"id": id}
	delURL := fmt.Sprintf("%s/ssh", r.endpoint)
	tflog.Debug(ctx, "Deleting SSH rule", map[string]interface{}{
		"url":     delURL,
		"payload": delPayload,
	})

	_, err := doSSHIDRequest(ctx, r.httpClient, http.MethodDelete, delURL, delPayload)
	if err != nil {
		if isNotFound(err) {
			// gone
		} else {
			resp.Diagnostics.AddError("Delete SSH error", err.Error())
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

// --------------------------------------------------------------------------------
// HTTP helper
// --------------------------------------------------------------------------------

func doSSHIDRequest(ctx context.Context, client *http.Client, method, url string, payload interface{}) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("SSH ID request marshal error: %w", err)
		}
		body = bytes.NewBuffer(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH ID request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	respHTTP, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SSH ID request error: %w", err)
	}
	defer respHTTP.Body.Close()

	if respHTTP.StatusCode == 404 {
		return nil, &NotFoundError{Message: "SSH rule not found"}
	}
	if respHTTP.StatusCode >= 300 {
		msg, _ := io.ReadAll(respHTTP.Body)
		return nil, fmt.Errorf("TACL returned %d: %s", respHTTP.StatusCode, string(msg))
	}

	return io.ReadAll(respHTTP.Body)
}



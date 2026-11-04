package provider

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// childBase is embedded in every resource that lives under a status page.
type childBase struct {
	ID         types.String `tfsdk:"id"`
	StatusPage types.String `tfsdk:"status_page"`
}

func (b *childBase) base() *childBase { return b }

type childModel[M any] interface {
	*M
	base() *childBase
}

// childResource implements CRUD + import for /status-pages/{slug}/{collection}/{id}.
// Schema attribute names match the API's JSON keys; fields tagged api:"w" are sent.
type childResource[M any, P childModel[M]] struct {
	client     *Client
	typeName   string
	collection string
	desc       string
	attrs      map[string]schema.Attribute
	createOnly []string // keys the API ignores on PATCH
}

func (r *childResource[M, P]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.typeName
}

func (r *childResource[M, P]) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, PlanModifiers: keepString, Description: "Numeric id."},
		"status_page": schema.StringAttribute{
			Required:    true,
			Description: "Slug of the status page this belongs to (e.g. `beacon_status_page.main.slug`). Renaming the page's slug is followed in place; moving to a different page is not supported (taint the resource instead).",
		},
	}
	for k, v := range r.attrs {
		attrs[k] = v
	}
	resp.Schema = schema.Schema{Description: r.desc, Attributes: attrs}
}

func (r *childResource[M, P]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, err := clientFrom(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Provider error", err.Error())
	}
	r.client = c
}

func (r *childResource[M, P]) path(b *childBase) string {
	p := pagePath(b.StatusPage.ValueString()) + "/" + r.collection
	if b.ID.ValueString() != "" {
		p += "/" + url.PathEscape(b.ID.ValueString())
	}
	return p
}

func (r *childResource[M, P]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m M
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b := P(&m).base()
	b.ID = types.StringNull()
	data, err := r.client.object(ctx, http.MethodPost, r.path(b), toAPI(&m))
	if err != nil {
		resp.Diagnostics.AddError("Creating "+r.typeName+" failed", err.Error())
		return
	}
	fromAPI(data, &m)
	fromAPI(data, b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *childResource[M, P]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m M
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	data, err := r.client.object(ctx, http.MethodGet, r.path(P(&m).base()), nil)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading "+r.typeName+" failed", err.Error())
		return
	}
	fromAPI(data, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *childResource[M, P]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m M
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := toAPI(&m)
	for _, k := range r.createOnly {
		delete(body, k)
	}
	// status_page may be a renamed slug of the same page (already applied, since we depend on it).
	data, err := r.client.object(ctx, http.MethodPatch, r.path(P(&m).base()), body)
	if err != nil {
		if isNotFound(err) {
			err = &APIError{Status: 404, Code: "not_found", Message: "not found on status page " + P(&m).base().StatusPage.ValueString() + "; moving a " + r.typeName + " to another page is not supported — replace it instead (terraform apply -replace=…)"}
		}
		resp.Diagnostics.AddError("Updating "+r.typeName+" failed", err.Error())
		return
	}
	fromAPI(data, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *childResource[M, P]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m M
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.do(ctx, http.MethodDelete, r.path(P(&m).base()), nil); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Deleting "+r.typeName+" failed", err.Error())
	}
}

// ImportState takes "<status_page_slug>/<id>".
func (r *childResource[M, P]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	slug, id, ok := strings.Cut(req.ID, "/")
	if !ok || slug == "" || id == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected <status_page_slug>/<id>, got "+req.ID)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("status_page"), slug)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// keepPrior plans an unconfigured computed value as its prior state, even when
// that prior value is null (UseStateForUnknown skips nulls). Used for the
// webhook secret, which the API never returns after creation.
type keepPrior struct{}

func (keepPrior) Description(context.Context) string         { return "Keeps the prior state value." }
func (keepPrior) MarkdownDescription(context.Context) string { return "Keeps the prior state value." }
func (keepPrior) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.State.Raw.IsNull() && req.ConfigValue.IsNull() {
		resp.PlanValue = req.StateValue
	}
}

// --- beacon_component -------------------------------------------------------

var componentStatuses = []string{"operational", "degraded", "degraded_performance", "partial_outage", "major_outage", "maintenance", "unknown"}

type componentModel struct {
	childBase
	Name        types.String `tfsdk:"name" api:"w"`
	Description types.String `tfsdk:"description" api:"w"`
	Status      types.String `tfsdk:"status" api:"w"`
	Position    types.Int64  `tfsdk:"position" api:"w"`
}

var keepInt = []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}

func newComponentResource() resource.Resource {
	return &childResource[componentModel, *componentModel]{
		typeName: "component", collection: "components",
		desc: "A component (service) shown on a status page.",
		attrs: map[string]schema.Attribute{
			"name":        schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 255)}},
			"description": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 5000)}},
			"status": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keepString,
				Description: "Defaults to `operational`. Monitors and the ingest API change this at runtime; leave it unset (or add `lifecycle { ignore_changes = [status] }`) unless Terraform should own it.",
				Validators:  []validator.String{stringvalidator.OneOf(componentStatuses...)},
			},
			"position": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt, Validators: []validator.Int64{int64validator.AtLeast(0)}},
		},
	}
}

// --- beacon_monitor ---------------------------------------------------------

type monitorModel struct {
	childBase
	Name                    types.String `tfsdk:"name" api:"w"`
	Type                    types.String `tfsdk:"type" api:"w"`
	URL                     types.String `tfsdk:"url" api:"w"`
	Host                    types.String `tfsdk:"host" api:"w"`
	Port                    types.Int64  `tfsdk:"port" api:"w"`
	DNSRecordType           types.String `tfsdk:"dns_record_type" api:"w"`
	DNSExpectedValue        types.String `tfsdk:"dns_expected_value" api:"w"`
	ConfirmFromLocations    types.Bool   `tfsdk:"confirm_from_locations" api:"w"`
	ConfirmationQuorum      types.Int64  `tfsdk:"confirmation_quorum" api:"w"`
	ExpiryAlertsEnabled     types.Bool   `tfsdk:"expiry_alerts_enabled" api:"w"`
	ExpiryAlertDays         types.String `tfsdk:"expiry_alert_days" api:"w"`
	HTTPMethod              types.String `tfsdk:"http_method" api:"w"`
	ExpectedStatusCode      types.Int64  `tfsdk:"expected_status_code" api:"w"`
	Keyword                 types.String `tfsdk:"keyword" api:"w"`
	KeywordMatchType        types.String `tfsdk:"keyword_match_type" api:"w"`
	IntervalMinutes         types.Int64  `tfsdk:"interval_minutes" api:"w"`
	TimeoutSeconds          types.Int64  `tfsdk:"timeout_seconds" api:"w"`
	ConsecutiveThreshold    types.Int64  `tfsdk:"consecutive_threshold" api:"w"`
	AllowColdStart          types.Bool   `tfsdk:"allow_cold_start" api:"w"`
	ColdStartTimeoutSeconds types.Int64  `tfsdk:"cold_start_timeout_seconds" api:"w"`
	CheckWindowStart        types.String `tfsdk:"check_window_start" api:"w"`
	CheckWindowEnd          types.String `tfsdk:"check_window_end" api:"w"`
	StatusComponentID       types.Int64  `tfsdk:"status_component_id" api:"w"`
	Enabled                 types.Bool   `tfsdk:"enabled" api:"w"`
	HeartbeatURL            types.String `tfsdk:"heartbeat_url"`
}

func optString(desc string, v ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Description: desc, Validators: append(v, stringvalidator.LengthAtLeast(1))}
}

func computedString(desc string, v ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepString, Description: desc, Validators: v}
}

func computedInt(desc string, min, max int64) schema.Int64Attribute {
	return schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt, Description: desc, Validators: []validator.Int64{int64validator.Between(min, max)}}
}

func computedBool(desc string) schema.BoolAttribute {
	return schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, Description: desc}
}

var hhmm = stringvalidator.RegexMatches(regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`), "must be HH:MM (24h)")

func newMonitorResource() resource.Resource {
	return &childResource[monitorModel, *monitorModel]{
		typeName: "monitor", collection: "monitors", createOnly: []string{"type"},
		desc: "An uptime monitor (http, heartbeat, tcp, ping or dns) on a status page. Unset optional+computed arguments take the server default (which can depend on your plan, e.g. `interval_minutes`).",
		attrs: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 255)}},
			"type": schema.StringAttribute{
				Optional: true, Computed: true, Description: "`http` (default), `heartbeat`, `tcp`, `ping` or `dns`. Changing it replaces the monitor.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.OneOf("http", "heartbeat", "tcp", "ping", "dns")},
			},
			"url":                    optString("URL to check. Required for `http`."),
			"host":                   optString("Hostname or IP. Required for `tcp`, `ping` and `dns`. Must be lowercase (the API lowercases it).", stringvalidator.LengthAtMost(253), stringvalidator.RegexMatches(regexp.MustCompile(`^[^A-Z\s]+$`), "must be lowercase with no spaces")),
			"port":                   schema.Int64Attribute{Optional: true, Description: "Required for `tcp`.", Validators: []validator.Int64{int64validator.Between(1, 65535)}},
			"dns_record_type":        optString("`A`, `AAAA`, `CNAME`, `MX` or `TXT`. Required for `dns`.", stringvalidator.OneOf("A", "AAAA", "CNAME", "MX", "TXT")),
			"dns_expected_value":     optString("Expected DNS answer (optional).", stringvalidator.LengthAtMost(255)),
			"confirm_from_locations": computedBool("Re-check from other regions before alerting. Defaults to `true`."),
			"confirmation_quorum":    schema.Int64Attribute{Optional: true, Description: "Regions that must agree (1-10).", Validators: []validator.Int64{int64validator.Between(1, 10)}},
			"expiry_alerts_enabled":  computedBool("SSL/domain expiry alerts. Defaults to `true`."),
			"expiry_alert_days": computedString("Comma-separated days before expiry to alert, e.g. `14,7,1` (default). No spaces.",
				stringvalidator.RegexMatches(regexp.MustCompile(`^\d{1,3}(,\d{1,3}){0,9}$`), "must look like 14,7,1")),
			"http_method":                computedString("`GET` (default) or `HEAD`.", stringvalidator.OneOf("GET", "HEAD")),
			"expected_status_code":       computedInt("Defaults to 200.", 100, 599),
			"keyword":                    optString("Text that must (or must not) appear in the response body.", stringvalidator.LengthAtMost(255)),
			"keyword_match_type":         computedString("`contains` (default) or `not_contains`.", stringvalidator.OneOf("contains", "not_contains")),
			"interval_minutes":           computedInt("Minutes between checks. Default and minimum depend on your plan.", 1, 1440),
			"timeout_seconds":            computedInt("Defaults to 10.", 1, 30),
			"consecutive_threshold":      computedInt("Failed checks before the monitor is down. Defaults to 3.", 1, 10),
			"allow_cold_start":           computedBool("Retry once with a longer timeout before failing (serverless hosts). Auto-enabled for known serverless hosts when unset."),
			"cold_start_timeout_seconds": computedInt("Timeout for the cold-start retry. Defaults to 30.", 5, 40),
			"check_window_start":         optString("Only check from this time (HH:MM, page timezone). Set together with `check_window_end`.", hhmm),
			"check_window_end":           optString("Only check until this time (HH:MM).", hhmm),
			"status_component_id":        schema.Int64Attribute{Optional: true, Description: "Component (on the same page) whose status this monitor drives."},
			"enabled":                    computedBool("Defaults to `true`."),
			"heartbeat_url":              schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: keepString, Description: "Ping URL for `heartbeat` monitors."},
		},
	}
}

// --- beacon_webhook ---------------------------------------------------------

type webhookModel struct {
	childBase
	URL               types.String `tfsdk:"url" api:"w"`
	Enabled           types.Bool   `tfsdk:"enabled" api:"w"`
	StatusComponentID types.Int64  `tfsdk:"status_component_id" api:"w"`
	Secret            types.String `tfsdk:"secret" api:"w"`
}

func newWebhookResource() resource.Resource {
	return &childResource[webhookModel, *webhookModel]{
		typeName: "webhook", collection: "webhooks",
		desc: "An outgoing webhook endpoint for incident events, signed with HMAC-SHA256.",
		attrs: map[string]schema.Attribute{
			"url":                 schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 2048)}},
			"enabled":             computedBool("Defaults to `true`."),
			"status_component_id": schema.Int64Attribute{Optional: true, Description: "Only send events for incidents affecting this component."},
			"secret": schema.StringAttribute{
				Optional: true, Computed: true, Sensitive: true,
				Description:   "Signing secret (16-255 chars). Generated by Beacon when omitted and captured from the create response; the API never returns it again, so the value in state is the one from creation (or from your config). Imported webhooks have no secret in state. This is a sensitive computed attribute rather than a write-only one because write-only attributes cannot hold a server-generated value; mark outputs that use it `sensitive`.",
				PlanModifiers: []planmodifier.String{keepPrior{}},
				Validators:    []validator.String{stringvalidator.LengthBetween(16, 255)},
			},
		},
	}
}

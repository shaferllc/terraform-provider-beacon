package provider

import (
	"context"
	"net/http"
	"net/url"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type statusPageModel struct {
	ID                           types.String `tfsdk:"id"`
	Slug                         types.String `tfsdk:"slug" api:"w"`
	Name                         types.String `tfsdk:"name" api:"w"`
	Description                  types.String `tfsdk:"description" api:"w"`
	Timezone                     types.String `tfsdk:"timezone" api:"w"`
	IsPublic                     types.Bool   `tfsdk:"is_public" api:"w"`
	Theme                        types.String `tfsdk:"theme" api:"w"`
	IncidentNotificationsEnabled types.Bool   `tfsdk:"incident_notifications_enabled" api:"w"`
	PublicURL                    types.String `tfsdk:"public_url"`
}

var keepString = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
var keepBool = []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}

func pagePath(slug string) string { return "/status-pages/" + url.PathEscape(slug) }

type statusPageResource struct{ client *Client }

func newStatusPageResource() resource.Resource { return &statusPageResource{} }

func (r *statusPageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_status_page"
}

func (r *statusPageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Beacon status page.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Numeric page id.", PlanModifiers: keepString},
			"slug": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keepString,
				Description: "URL slug. Generated from `name` when omitted. Changing it renames the page in place.",
				Validators:  []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`), "must be lowercase letters, digits and single dashes"), stringvalidator.LengthAtMost(120)},
			},
			"name":        schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 120)}},
			"description": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 2000)}},
			"timezone":    schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepString, Description: "IANA timezone. Defaults to `UTC`."},
			"is_public":   schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, Description: "Defaults to `true`."},
			"theme": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keepString, Description: "`dark` (default), `light` or `minimal`.",
				Validators: []validator.String{stringvalidator.OneOf("dark", "light", "minimal")},
			},
			"incident_notifications_enabled": schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, Description: "Defaults to `true`."},
			"public_url":                     schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{keepUnlessSlugChanges{}}},
		},
	}
}

func (r *statusPageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, err := clientFrom(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Provider error", err.Error())
	}
	r.client = c
}

func (r *statusPageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan statusPageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	data, err := r.client.object(ctx, http.MethodPost, "/status-pages", toAPI(&plan))
	if err != nil {
		resp.Diagnostics.AddError("Creating status page failed", err.Error())
		return
	}
	fromAPI(data, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *statusPageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state statusPageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	data, err := r.client.object(ctx, http.MethodGet, pagePath(state.Slug.ValueString()), nil)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading status page failed", err.Error())
		return
	}
	fromAPI(data, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *statusPageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state statusPageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Address the page by its current slug; the body may carry a new one.
	data, err := r.client.object(ctx, http.MethodPatch, pagePath(state.Slug.ValueString()), toAPI(&plan))
	if err != nil {
		resp.Diagnostics.AddError("Updating status page failed", err.Error())
		return
	}
	fromAPI(data, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *statusPageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state statusPageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.do(ctx, http.MethodDelete, pagePath(state.Slug.ValueString()), nil); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Deleting status page failed", err.Error())
	}
}

// keepUnlessSlugChanges keeps public_url stable across plans unless the slug is being changed.
type keepUnlessSlugChanges struct{}

func (keepUnlessSlugChanges) Description(context.Context) string {
	return "Keeps the prior value unless slug changes."
}
func (m keepUnlessSlugChanges) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}
func (keepUnlessSlugChanges) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || !req.PlanValue.IsUnknown() {
		return
	}
	var cfgSlug, stateSlug types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("slug"), &cfgSlug)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("slug"), &stateSlug)...)
	if cfgSlug.IsNull() || cfgSlug.Equal(stateSlug) {
		resp.PlanValue = req.StateValue
	}
}

// ImportState takes the page slug.
func (r *statusPageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("slug"), req, resp)
}

// --- data sources -----------------------------------------------------------

func pageDataAttributes(slugRequired bool) map[string]dsschema.Attribute {
	attrs := map[string]dsschema.Attribute{}
	for _, name := range []string{"id", "name", "description", "timezone", "theme", "public_url"} {
		attrs[name] = dsschema.StringAttribute{Computed: true}
	}
	for _, name := range []string{"is_public", "incident_notifications_enabled"} {
		attrs[name] = dsschema.BoolAttribute{Computed: true}
	}
	attrs["slug"] = dsschema.StringAttribute{Required: slugRequired, Computed: !slugRequired}
	return attrs
}

type statusPageDataSource struct{ client *Client }

func newStatusPageDataSource() datasource.DataSource { return &statusPageDataSource{} }

func (d *statusPageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_status_page"
}

func (d *statusPageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{Description: "Look up one status page by slug.", Attributes: pageDataAttributes(true)}
}

func (d *statusPageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, err := clientFrom(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Provider error", err.Error())
	}
	d.client = c
}

func (d *statusPageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg statusPageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	data, err := d.client.object(ctx, http.MethodGet, pagePath(cfg.Slug.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Reading status page failed", err.Error())
		return
	}
	fromAPI(data, &cfg)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

type statusPagesDataSource struct{ client *Client }

type statusPagesModel struct {
	StatusPages []statusPageModel `tfsdk:"status_pages"`
}

func newStatusPagesDataSource() datasource.DataSource { return &statusPagesDataSource{} }

func (d *statusPagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_status_pages"
}

func (d *statusPagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{
		Description: "Every status page in the token's workspace.",
		Attributes: map[string]dsschema.Attribute{
			"status_pages": dsschema.ListNestedAttribute{
				Computed:     true,
				NestedObject: dsschema.NestedAttributeObject{Attributes: pageDataAttributes(false)},
			},
		},
	}
}

func (d *statusPagesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, err := clientFrom(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Provider error", err.Error())
	}
	d.client = c
}

func (d *statusPagesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	items, err := d.client.list(ctx, "/status-pages")
	if err != nil {
		resp.Diagnostics.AddError("Listing status pages failed", err.Error())
		return
	}
	out := statusPagesModel{StatusPages: make([]statusPageModel, len(items))}
	for i, item := range items {
		var m statusPageModel // zero values are null
		fromAPI(item, &m)
		out.StatusPages[i] = m
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

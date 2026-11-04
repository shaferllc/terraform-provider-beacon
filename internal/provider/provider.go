package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const defaultURL = "https://usebeacon.pro"

type beaconProvider struct{ version string }

type providerModel struct {
	Token types.String `tfsdk:"token"`
	URL   types.String `tfsdk:"url"`
}

// New returns the provider factory used by main and the tests.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &beaconProvider{version: version} }
}

func (p *beaconProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "beacon"
	resp.Version = p.version
}

func (p *beaconProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Beacon (https://usebeacon.pro) status pages, components, monitors and webhooks through the REST API v1.",
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				Description: "Workspace API token (`bchk_…`) with the `write` scope. Defaults to the `BEACON_API_TOKEN` environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
			"url": schema.StringAttribute{
				Description: "Beacon base URL. Defaults to the `BEACON_URL` environment variable, then `" + defaultURL + "`.",
				Optional:    true,
			},
		},
	}
}

func (p *beaconProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	token := firstNonEmpty(cfg.Token.ValueString(), os.Getenv("BEACON_API_TOKEN"))
	base := firstNonEmpty(cfg.URL.ValueString(), os.Getenv("BEACON_URL"), defaultURL)
	if token == "" {
		resp.Diagnostics.AddError("Missing Beacon API token", "Set the provider `token` argument or the BEACON_API_TOKEN environment variable.")
		return
	}

	client := &Client{
		BaseURL:   strings.TrimRight(base, "/") + "/api/v1",
		Token:     token,
		UserAgent: "terraform-provider-beacon/" + p.version,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *beaconProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newStatusPageResource,
		newComponentResource,
		newMonitorResource,
		newWebhookResource,
	}
}

func (p *beaconProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newStatusPageDataSource,
		newStatusPagesDataSource,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// toAPI builds a JSON request body from every model field tagged `api:"w"`.
// Unknown values are left out (the server picks its default); null values are
// sent as JSON null so removing an optional argument clears it.
func toAPI(model any) map[string]any {
	out := map[string]any{}
	v := reflect.ValueOf(model).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if f.Tag.Get("api") != "w" {
			continue
		}
		val := v.Field(i).Interface().(attr.Value)
		key := f.Tag.Get("tfsdk")
		switch {
		case val.IsUnknown():
		case val.IsNull():
			out[key] = nil
		default:
			switch x := val.(type) {
			case types.String:
				out[key] = x.ValueString()
			case types.Int64:
				out[key] = x.ValueInt64()
			case types.Bool:
				out[key] = x.ValueBool()
			}
		}
	}
	return out
}

// fromAPI copies API JSON into every model field whose tfsdk name is present
// in data. Absent keys leave the field untouched (e.g. a webhook's secret).
func fromAPI(data map[string]any, model any) {
	v := reflect.ValueOf(model).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		raw, ok := data[f.Tag.Get("tfsdk")]
		if !ok {
			continue
		}
		var val attr.Value
		switch v.Field(i).Interface().(type) {
		case types.String:
			switch x := raw.(type) {
			case nil:
				val = types.StringNull()
			case string:
				val = types.StringValue(x)
			case float64: // numeric ids are exposed as strings
				val = types.StringValue(strconv.FormatInt(int64(x), 10))
			default:
				val = types.StringValue(fmt.Sprint(x))
			}
		case types.Int64:
			if x, ok := raw.(float64); ok {
				val = types.Int64Value(int64(x))
			} else {
				val = types.Int64Null()
			}
		case types.Bool:
			if x, ok := raw.(bool); ok {
				val = types.BoolValue(x)
			} else {
				val = types.BoolNull()
			}
		default:
			continue
		}
		v.Field(i).Set(reflect.ValueOf(val))
	}
}

func clientFrom(data any) (*Client, error) {
	if data == nil {
		return nil, nil // provider not configured yet (validation phase)
	}
	c, ok := data.(*Client)
	if !ok {
		return nil, fmt.Errorf("unexpected provider data %T", data)
	}
	return c, nil
}

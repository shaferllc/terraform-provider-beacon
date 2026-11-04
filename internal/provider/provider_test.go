package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// fakeAPI emulates the slice of Beacon's REST API v1 the provider uses.
type fakeAPI struct {
	mu       sync.Mutex
	nextID   int
	pages    map[int]map[string]any
	children map[string]map[int]map[string]any // collection -> id -> object
	secrets  map[int]string                    // webhook id -> secret (never serialized)
	browser  map[int]map[string]any            // monitor id -> browser_secrets (write-only, never serialized)
}

func newFakeAPI(t *testing.T) (*fakeAPI, *httptest.Server) {
	f := &fakeAPI{pages: map[int]map[string]any{}, children: map[string]map[int]map[string]any{}, secrets: map[int]string{}, browser: map[int]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func notFound(w http.ResponseWriter) {
	reply(w, 404, map[string]any{"message": "Not found (or belongs to another workspace).", "code": "not_found"})
}

func merge(dst, src map[string]any) map[string]any {
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer bchk_test" {
		reply(w, 401, map[string]any{"message": "Missing or invalid token.", "code": "unauthenticated"})
		return
	}
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	seg := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	if seg[0] != "status-pages" {
		notFound(w)
		return
	}

	if len(seg) == 1 {
		switch r.Method {
		case http.MethodGet:
			f.paginate(w, r, f.pages)
		case http.MethodPost:
			f.nextID++
			slug, _ := body["slug"].(string)
			if slug == "" {
				slug = strings.ReplaceAll(strings.ToLower(body["name"].(string)), " ", "-")
			}
			page := merge(map[string]any{
				"id": f.nextID, "description": nil, "timezone": "UTC", "is_public": true, "theme": "dark",
				"incident_notifications_enabled": true,
			}, body)
			page["slug"] = slug
			page["public_url"] = "https://usebeacon.pro/status/acme/" + slug
			f.pages[f.nextID] = page
			reply(w, 201, map[string]any{"data": page})
		}
		return
	}

	var page map[string]any
	for _, p := range f.pages {
		if p["slug"] == seg[1] {
			page = p
		}
	}
	if page == nil {
		notFound(w)
		return
	}

	if len(seg) == 2 {
		switch r.Method {
		case http.MethodGet:
			reply(w, 200, map[string]any{"data": page})
		case http.MethodPatch:
			merge(page, body)
			page["public_url"] = "https://usebeacon.pro/status/acme/" + page["slug"].(string)
			reply(w, 200, map[string]any{"data": page})
		case http.MethodDelete:
			delete(f.pages, page["id"].(int))
			reply(w, 204, nil)
		}
		return
	}

	coll := seg[2]
	if f.children[coll] == nil {
		f.children[coll] = map[int]map[string]any{}
	}
	items := map[int]map[string]any{}
	for id, c := range f.children[coll] {
		if c["status_page_id"] == page["id"] {
			items[id] = c
		}
	}

	if len(seg) == 3 {
		switch r.Method {
		case http.MethodGet:
			f.paginate(w, r, items)
		case http.MethodPost:
			f.nextID++
			id := f.nextID
			obj := map[string]any{"id": id, "status_page_id": page["id"]}
			switch coll {
			case "components":
				merge(obj, map[string]any{"description": nil, "status": "operational", "position": len(items)})
			case "monitors":
				merge(obj, map[string]any{
					"type": "http", "url": nil, "host": nil, "port": nil, "dns_record_type": nil, "dns_expected_value": nil,
					"confirm_from_locations": true, "confirmation_quorum": nil, "expiry_alerts_enabled": true,
					"expiry_alert_days": "14,7,1", "http_method": "GET", "expected_status_code": 200, "keyword": nil,
					"keyword_match_type": "contains", "interval_minutes": 5, "timeout_seconds": 10, "consecutive_threshold": 3,
					"allow_cold_start": false, "cold_start_timeout_seconds": 30, "check_window_start": nil,
					"check_window_end": nil, "status_component_id": nil, "enabled": true, "heartbeat_url": nil, "browser_steps": nil,
				})
			case "webhooks":
				merge(obj, map[string]any{"enabled": true, "status_component_id": nil})
			}
			if bs, ok := body["browser_secrets"].(map[string]any); ok {
				f.browser[id] = bs
			}
			delete(body, "browser_secrets")
			for k, v := range body {
				if v != nil {
					obj[k] = v
				}
			}
			extra := map[string]any{"data": obj}
			if coll == "monitors" && obj["type"] == "heartbeat" {
				obj["heartbeat_url"] = "https://usebeacon.pro/api/v1/heartbeat/tok" + strconv.Itoa(id)
			}
			if coll == "webhooks" {
				secret, _ := obj["secret"].(string)
				if secret == "" {
					secret = "whsec_generated_by_server"
				}
				delete(obj, "secret")
				f.secrets[id] = secret
				extra["secret"] = secret
			}
			f.children[coll][id] = obj
			reply(w, 201, extra)
		}
		return
	}

	id, _ := strconv.Atoi(seg[3])
	obj, ok := items[id]
	if !ok {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		reply(w, 200, map[string]any{"data": obj})
	case http.MethodPatch:
		if coll == "monitors" {
			delete(body, "type") // the API ignores type on update
			if bs, ok := body["browser_secrets"].(map[string]any); ok { // patch by name; null removes
				if f.browser[id] == nil {
					f.browser[id] = map[string]any{}
				}
				for k, v := range bs {
					if v == nil {
						delete(f.browser[id], k)
					} else {
						f.browser[id][k] = v
					}
				}
			} else if _, sent := body["browser_secrets"]; sent {
				delete(f.browser, id)
			}
			delete(body, "browser_secrets")
		}
		if s, ok := body["secret"].(string); ok {
			f.secrets[id] = s
		}
		delete(body, "secret")
		merge(obj, body)
		reply(w, 200, map[string]any{"data": obj})
	case http.MethodDelete:
		delete(f.children[coll], id)
		reply(w, 204, nil)
	}
}

// paginate serves one item per page so the provider has to follow next_cursor.
func (f *fakeAPI) paginate(w http.ResponseWriter, r *http.Request, items map[int]map[string]any) {
	ids := []int{}
	for id := range items {
		ids = append(ids, id)
	}
	for i := range ids { // tiny insertion sort keeps ordering stable
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	data := []any{}
	var next any
	if start < len(ids) {
		data = append(data, items[ids[start]])
		if start+1 < len(ids) {
			next = strconv.Itoa(start + 1)
		}
	}
	reply(w, 200, map[string]any{"data": data, "meta": map[string]any{"next_cursor": next, "per_page": 1}})
}

func (f *fakeAPI) empty() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.pages)
	for _, c := range f.children {
		n += len(c)
	}
	if n != 0 {
		return fmt.Errorf("%d objects left after destroy", n)
	}
	return nil
}

func factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"beacon": providerserver.NewProtocol6WithError(New("test")()),
	}
}

func providerBlock(url string) string {
	return fmt.Sprintf("provider \"beacon\" {\n  url   = %q\n  token = \"bchk_test\"\n}\n", url)
}

func childImportID(name string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs := s.RootModule().Resources[name]
		return rs.Primary.Attributes["status_page"] + "/" + rs.Primary.ID, nil
	}
}

func TestStatusPageAndDataSources(t *testing.T) {
	f, srv := newFakeAPI(t)
	p := providerBlock(srv.URL)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy:             func(*terraform.State) error { return f.empty() },
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "beacon_status_page" "main" { name = "Acme Cloud" }
resource "beacon_status_page" "other" {
  name = "Other"
  slug = "other"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("beacon_status_page.main", "slug", "acme-cloud"),
					resource.TestCheckResourceAttr("beacon_status_page.main", "theme", "dark"),
					resource.TestCheckResourceAttr("beacon_status_page.main", "timezone", "UTC"),
					resource.TestCheckResourceAttr("beacon_status_page.main", "is_public", "true"),
					resource.TestCheckNoResourceAttr("beacon_status_page.main", "description"),
					resource.TestMatchResourceAttr("beacon_status_page.main", "id", regexp.MustCompile(`^\d+$`)),
				),
			},
			{
				Config: p + `
resource "beacon_status_page" "main" {
  name        = "Acme Cloud"
  slug        = "acme"
  theme       = "minimal"
  description = "Production services"
  is_public   = false
}
resource "beacon_status_page" "other" {
  name = "Other"
  slug = "other"
}
data "beacon_status_page" "acme" { slug = beacon_status_page.main.slug }
data "beacon_status_pages" "all" { depends_on = [beacon_status_page.main, beacon_status_page.other] }`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("beacon_status_page.main", "slug", "acme"),
					resource.TestCheckResourceAttr("beacon_status_page.main", "public_url", "https://usebeacon.pro/status/acme/acme"),
					resource.TestCheckResourceAttr("beacon_status_page.main", "theme", "minimal"),
					resource.TestCheckResourceAttrPair("data.beacon_status_page.acme", "id", "beacon_status_page.main", "id"),
					resource.TestCheckResourceAttr("data.beacon_status_page.acme", "description", "Production services"),
					resource.TestCheckResourceAttr("data.beacon_status_page.acme", "is_public", "false"),
					resource.TestCheckResourceAttr("data.beacon_status_pages.all", "status_pages.#", "2"),
					resource.TestCheckTypeSetElemNestedAttrs("data.beacon_status_pages.all", "status_pages.*", map[string]string{"slug": "other", "theme": "dark"}),
				),
			},
			{
				ResourceName:      "beacon_status_page.main",
				ImportState:       true,
				ImportStateId:     "acme",
				ImportStateVerify: true,
			},
		},
	})
}

func TestChildResources(t *testing.T) {
	f, srv := newFakeAPI(t)
	p := providerBlock(srv.URL)
	config := func(slug, status, interval, hookURL string) string {
		return p + fmt.Sprintf(`
resource "beacon_status_page" "main" {
  name = "Acme"
  slug = %q
}
resource "beacon_component" "api" {
  status_page = beacon_status_page.main.slug
  name        = "API"
  status      = %q
}
resource "beacon_monitor" "api" {
  status_page         = beacon_status_page.main.slug
  name                = "API health"
  url                 = "https://api.example.com/health"
  keyword             = "ok"
  interval_minutes    = %s
  allow_cold_start    = true
  check_window_start  = "08:00"
  check_window_end    = "18:00"
  status_component_id = beacon_component.api.id
}
resource "beacon_monitor" "cron" {
  status_page = beacon_status_page.main.slug
  name        = "Nightly backup"
  type        = "heartbeat"
}
resource "beacon_monitor" "db" {
  status_page = beacon_status_page.main.slug
  name        = "Postgres"
  type        = "tcp"
  host        = "db.example.com"
  port        = 5432
}
resource "beacon_webhook" "generated" {
  status_page = beacon_status_page.main.slug
  url         = %q
}
resource "beacon_webhook" "own_secret" {
  status_page = beacon_status_page.main.slug
  url         = "https://hooks.example.com/b"
  secret      = "my-own-secret-0123456789"
  enabled     = false
}`, slug, status, interval, hookURL)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy:             func(*terraform.State) error { return f.empty() },
		Steps: []resource.TestStep{
			{
				Config: config("acme", "operational", "5", "https://hooks.example.com/a"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("beacon_component.api", "position", "0"),
					resource.TestCheckResourceAttr("beacon_monitor.api", "type", "http"),
					resource.TestCheckResourceAttr("beacon_monitor.api", "http_method", "GET"),
					resource.TestCheckResourceAttr("beacon_monitor.api", "expected_status_code", "200"),
					resource.TestCheckResourceAttr("beacon_monitor.api", "keyword_match_type", "contains"),
					resource.TestCheckResourceAttr("beacon_monitor.api", "allow_cold_start", "true"),
					resource.TestCheckResourceAttr("beacon_monitor.api", "check_window_end", "18:00"),
					resource.TestCheckResourceAttrPair("beacon_monitor.api", "status_component_id", "beacon_component.api", "id"),
					resource.TestMatchResourceAttr("beacon_monitor.cron", "heartbeat_url", regexp.MustCompile(`/api/v1/heartbeat/tok\d+$`)),
					resource.TestCheckResourceAttr("beacon_monitor.db", "port", "5432"),
					resource.TestCheckResourceAttr("beacon_webhook.generated", "secret", "whsec_generated_by_server"),
					resource.TestCheckResourceAttr("beacon_webhook.generated", "enabled", "true"),
					resource.TestCheckResourceAttr("beacon_webhook.own_secret", "secret", "my-own-secret-0123456789"),
					resource.TestCheckResourceAttr("beacon_webhook.own_secret", "enabled", "false"),
				),
			},
			{
				// Renaming the page slug is followed in place by every child.
				Config: config("acme-prod", "degraded", "10", "https://hooks.example.com/a2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("beacon_component.api", "status_page", "acme-prod"),
					resource.TestCheckResourceAttr("beacon_component.api", "status", "degraded"),
					resource.TestCheckResourceAttr("beacon_monitor.api", "interval_minutes", "10"),
					resource.TestCheckResourceAttr("beacon_webhook.generated", "url", "https://hooks.example.com/a2"),
					resource.TestCheckResourceAttr("beacon_webhook.generated", "secret", "whsec_generated_by_server"),
				),
			},
			{ResourceName: "beacon_component.api", ImportState: true, ImportStateIdFunc: childImportID("beacon_component.api"), ImportStateVerify: true},
			{ResourceName: "beacon_monitor.api", ImportState: true, ImportStateIdFunc: childImportID("beacon_monitor.api"), ImportStateVerify: true},
			{ResourceName: "beacon_monitor.cron", ImportState: true, ImportStateIdFunc: childImportID("beacon_monitor.cron"), ImportStateVerify: true},
			{
				ResourceName: "beacon_webhook.generated", ImportState: true, ImportStateIdFunc: childImportID("beacon_webhook.generated"),
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secret"},
			},
		},
	})
}

func TestBrowserMonitor(t *testing.T) {
	f, srv := newFakeAPI(t)
	config := func(steps string, secrets string) string {
		return providerBlock(srv.URL) + fmt.Sprintf(`
resource "beacon_status_page" "main" {
  name = "Acme"
  slug = "acme"
}
resource "beacon_monitor" "login" {
  status_page     = beacon_status_page.main.slug
  name            = "Login flow"
  type            = "browser"
  browser_steps   = jsonencode(%s)
  browser_secrets = %s
}`, steps, secrets)
	}
	stored := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, m := range f.browser {
				if got := fmt.Sprint(m); got != want {
					return fmt.Errorf("server secrets = %s, want %s", got, want)
				}
			}
			return nil
		}
	}
	steps1 := `[{ action = "goto", url = "https://app.example.com/login" }, { action = "fill", selector = "#pw", value = "{{secret:PW}}" }, { action = "assert_text", text = "Welcome", timeout = 10000 }]`
	steps2 := `[{ action = "goto", url = "https://app.example.com/login" }, { action = "assert_text", text = "Sign in" }]`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy:             func(*terraform.State) error { return f.empty() },
		Steps: []resource.TestStep{
			{
				Config: config(steps1, `{ PW = "hunter2", OTP = "123456" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("beacon_monitor.login", "type", "browser"),
					resource.TestCheckResourceAttr("beacon_monitor.login", "browser_secrets.PW", "hunter2"),
					stored("map[OTP:123456 PW:hunter2]"),
				),
			},
			// Same steps written differently: no diff.
			{Config: config(`[{ url = "https://app.example.com/login", action = "goto" }, { selector = "#pw", action = "fill", value = "{{secret:PW}}" }, { timeout = 10000, text = "Welcome", action = "assert_text" }]`, `{ PW = "hunter2", OTP = "123456" }`), PlanOnly: true},
			{
				// Dropping a key deletes that secret on the server; changing a value updates it.
				Config: config(steps2, `{ PW = "new-pass" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("beacon_monitor.login", "browser_steps", `[{"action":"goto","url":"https://app.example.com/login"},{"action":"assert_text","text":"Sign in"}]`),
					stored("map[PW:new-pass]"),
				),
			},
			{
				ResourceName: "beacon_monitor.login", ImportState: true, ImportStateIdFunc: childImportID("beacon_monitor.login"),
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"browser_secrets"},
			},
		},
	})
}

func TestInvalidMonitorArguments(t *testing.T) {
	_, srv := newFakeAPI(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: providerBlock(srv.URL) + `
resource "beacon_monitor" "bad" {
  status_page        = "acme"
  name               = "x"
  host               = "DB.Example.com"
  check_window_start = "8am"
  expiry_alert_days  = "14, 7"
}`,
			ExpectError: regexp.MustCompile(`(?s)must be lowercase.*must be HH:MM|must be HH:MM.*must be lowercase`),
		}},
	})
}

func TestClientErrorsAndRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			reply(w, 429, map[string]any{"message": "Too many requests.", "code": "rate_limited"})
			return
		}
		reply(w, 422, map[string]any{"message": "Validation failed.", "code": "validation_failed", "errors": map[string][]string{"url": {"The url field is required."}}})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	start := time.Now()
	_, err := c.object(context.Background(), http.MethodPost, "/x", map[string]any{})
	if calls != 2 || time.Since(start) < time.Second {
		t.Fatalf("expected one retry after ~1s, got %d calls in %s", calls, time.Since(start))
	}
	want := "Beacon API 422 validation_failed: Validation failed.\n  url: The url field is required."
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

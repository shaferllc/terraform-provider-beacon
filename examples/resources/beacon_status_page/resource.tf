resource "beacon_status_page" "main" {
  name        = "Acme Cloud"
  slug        = "acme-cloud"
  description = "Live status of Acme's production services."
  timezone    = "America/New_York"
  theme       = "light"
}

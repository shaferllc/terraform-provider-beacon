resource "beacon_component" "api" {
  status_page = beacon_status_page.main.slug
  name        = "API"
  description = "Public REST API"

  # Monitors and the ingest API change status at runtime.
  lifecycle {
    ignore_changes = [status]
  }
}

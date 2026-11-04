# HTTP check with keyword, cold-start retry and an office-hours check window.
resource "beacon_monitor" "api" {
  status_page         = beacon_status_page.main.slug
  name                = "API health"
  url                 = "https://api.example.com/health"
  keyword             = "ok"
  interval_minutes    = 5
  allow_cold_start    = true
  check_window_start  = "08:00"
  check_window_end    = "20:00"
  status_component_id = beacon_component.api.id
}

# Heartbeat (cron) monitor: ping heartbeat_url from your job.
resource "beacon_monitor" "backup" {
  status_page      = beacon_status_page.main.slug
  name             = "Nightly backup"
  type             = "heartbeat"
  interval_minutes = 1440
}

output "backup_ping_url" {
  value     = beacon_monitor.backup.heartbeat_url
  sensitive = true
}

resource "beacon_monitor" "db" {
  status_page = beacon_status_page.main.slug
  name        = "Postgres"
  type        = "tcp"
  host        = "db.example.com"
  port        = 5432
}

resource "beacon_monitor" "gateway" {
  status_page = beacon_status_page.main.slug
  name        = "Gateway ping"
  type        = "ping"
  host        = "gw.example.com"
}

resource "beacon_monitor" "mx" {
  status_page        = beacon_status_page.main.slug
  name               = "Mail DNS"
  type               = "dns"
  host               = "example.com"
  dns_record_type    = "MX"
  dns_expected_value = "mx1.example.com"
}

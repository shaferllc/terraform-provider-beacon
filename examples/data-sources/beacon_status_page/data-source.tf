data "beacon_status_page" "main" {
  slug = "acme-cloud"
}

output "status_url" {
  value = data.beacon_status_page.main.public_url
}

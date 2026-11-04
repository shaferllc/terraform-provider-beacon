data "beacon_status_pages" "all" {}

output "slugs" {
  value = [for p in data.beacon_status_pages.all.status_pages : p.slug]
}

# Terraform provider for Beacon

Manage [Beacon](https://usebeacon.pro) status pages, components, monitors and webhooks as code,
through the Beacon REST API v1.

> **Not yet published to the Terraform Registry.** Build it from source and use a
> `dev_overrides` block (below) until it is.

| Type | Name |
|------|------|
| Resource | `beacon_status_page`, `beacon_component`, `beacon_monitor` (http / heartbeat / tcp / ping / dns), `beacon_webhook` |
| Data source | `beacon_status_page`, `beacon_status_pages` |

Full reference: [`docs/`](docs/index.md). Examples: [`examples/`](examples/).

## Install from source

Requires Go 1.25+ and Terraform 1.0+.

```sh
git clone https://github.com/shaferllc/terraform-provider-beacon.git
cd terraform-provider-beacon
go install .   # puts terraform-provider-beacon in $(go env GOPATH)/bin
```

Point Terraform at the binary in `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "shaferllc/beacon" = "/Users/you/go/bin" # $(go env GOPATH)/bin
  }
  direct {}
}
```

With `dev_overrides` you skip `terraform init` for this provider and go straight to `terraform plan`.

## Usage

```hcl
terraform {
  required_providers {
    beacon = { source = "shaferllc/beacon" }
  }
}

provider "beacon" {
  # token = "bchk_…"            # or BEACON_API_TOKEN (needs the write scope)
  # url   = "https://usebeacon.pro" # or BEACON_URL, for self-hosted Beacon
}

resource "beacon_status_page" "main" {
  name = "Acme Cloud"
}

resource "beacon_component" "api" {
  status_page = beacon_status_page.main.slug
  name        = "API"
}

resource "beacon_monitor" "api" {
  status_page         = beacon_status_page.main.slug
  name                = "API health"
  url                 = "https://api.example.com/health"
  status_component_id = beacon_component.api.id
}
```

### Notes

- **IDs and import.** Status pages import by slug (`terraform import beacon_status_page.main acme-cloud`);
  everything else imports as `<page_slug>/<id>`.
- **Renaming a page slug** updates it in place; components, monitors and webhooks that reference
  `beacon_status_page.x.slug` follow it without being recreated.
- **Server defaults.** Optional arguments you leave out take Beacon's default (some, like
  `interval_minutes`, depend on your plan) and are not reset when later removed from config.
- **Monitor `type`** can't be changed through the API, so changing it replaces the monitor.
- **Webhook `secret`** is captured from the create response (or taken from your config) and kept in
  state as a sensitive value; the API never returns it again.
- **Component `status`** is changed at runtime by monitors and the ingest API — leave it unset or use
  `lifecycle { ignore_changes = [status] }`.

## Development

```sh
go test ./...        # unit tests against an in-process fake of the Beacon API
go generate ./...    # regenerate docs/ from the schema + examples/ (tfplugindocs)
```

## Releasing (owner)

The Terraform Registry needs a dedicated public GitHub repo named `terraform-provider-beacon`
with this directory at its root, GPG-signed releases, and the public key registered in the Registry.

1. Split this directory into `shaferllc/terraform-provider-beacon` (e.g. add it to the monorepo's
   `split-packages.yml` matrix, or `git subtree split --prefix=sdks/terraform-provider-beacon`).
2. Create a GPG key (RSA), add the public key under Registry → Settings → Signing keys, and set the
   `GPG_PRIVATE_KEY` and `PASSPHRASE` secrets on the split repo.
3. Publish the repo in the Terraform Registry (Publish → Provider).
4. Push a `v0.1.0` tag to the split repo; `.github/workflows/release.yml` runs GoReleaser
   (`.goreleaser.yml`, `terraform-registry-manifest.json`) and the Registry picks up the release.

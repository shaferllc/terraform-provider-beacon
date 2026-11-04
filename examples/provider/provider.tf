terraform {
  required_providers {
    beacon = {
      source = "shaferllc/beacon"
    }
  }
}

# Token from BEACON_API_TOKEN, URL from BEACON_URL (default https://usebeacon.pro).
provider "beacon" {}

terraform {
  required_providers {
    tacl = {
      source  = "lbrlabs/tacl"
      version = "~> 1.0"
    }
  }
}

provider "tacl" {
  endpoint = "http://tacl:8080"
}


resource "tacl_nodeattr" "example" {
  target = [tacl_host.example.id]
  attr   = ["nextdns:no-device-info"]
}

resource "tacl_nodeattr" "example_app_connector" {

  app_json = jsonencode({
    "tailscale.com/app-connectors" = [
      {
        name       = "ipleak"
        connectors = ["tag:router"]
        domains    = ["ipleak.net"]
      }
    ]
  })
}

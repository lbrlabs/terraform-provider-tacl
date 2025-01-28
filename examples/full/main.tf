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

resource "tacl_acl" "tacl_web_port" {
  action = "accept"
  src    = ["mail@lbrlabs.com"]
  proto  = "tcp"
  dst    = ["tag:tacl:8080", ]
}

data "tacl_acl" "tacl_lookup" {
  # Reads the same entry from TACL by index:
  id = tacl_acl.tacl_web_port.id
}

output "tacl_proto" {
  value = data.tacl_acl.tacl_lookup.proto
}

resource "tacl_auto_approvers" "main" {
  routes = {
    "0.0.0.0/0" = ["tag:router"]
  }
  exit_node = ["tag:router"]
}

data "tacl_auto_approvers" "check" {}

resource "tacl_derpmap" "myderp" {
  regions = [
    {
      region_id   = 901
      region_code = "sea-lbr"
      region_name = "Seattle [LBR]"
      nodes = [{
        name      = "sea-lbr1"
        region_id = 901
        host_name = "sea-derp1.lbrlabs.com"
        ipv4      = "172.234.249.157"
        ipv6      = "2600:3c0a::f03c:95ff:fef1:7124"
      }]
    },
    {
      region_id   = 902
      region_code = "lon-lbr"
      region_name = "London [LBR]"
      nodes = [{
        name      = "lon-lbr1"
        region_id = 902
        host_name = "lon-derp1.lbrlabs.com"
        ipv4      = "172.236.28.218"
        ipv6      = "2600:3c13::f03c:95ff:fef1:fd29"
      }]
    }
  ]
}

resource "tacl_group" "example" {
  name        = "example"
  members     = ["mail@lbrlabs.com"]
}

data "tacl_group" "example" {
  name = tacl_group.example.name
}

output "eng_members" {
  value = data.tacl_group.example.members
}

resource "tacl_host" "example" {
  name = "example-host-1"
  ip   = "10.1.2.3"
}

data "tacl_host" "lookup" {
  name = tacl_host.example.name
}

output "host_ip" {
  value = data.tacl_host.lookup.ip
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

resource "tacl_posture" "example_posture" {
  name  = "latestMac"
  rules = ["node:os in ['macos']", "node:tsVersion >= '1.40'"]
}

resource "tacl_ssh" "accept_example" {
  action  = "accept"
  src = [
    "group:${tacl_group.example.id}",
  ]
  dst = [
    "tag:router",
  ]
  users = [
    "root",
  ]
}

resource "tacl_ssh" "check_example" {
  action = "check"

  src = [
    "group:${tacl_group.example.id}",
  ]

  dst = [
    "tag:router"
  ]

  users = [
    "root",
  ]

  # If "action" = "check",
  # you can specify a "check_period"
  check_period = "12h"

  # Optionally accept environment variable patterns:
  accept_env = [
    "LANG*",
    "LC_*"
  ]
}

resource "tacl_tag_owner" "parent" {
  name   = "parent"
  owners = ["group:${tacl_group.example.id}"]
}

resource "tacl_tag_owner" "child" {
  name   = "child"
  owners = ["tag:${tacl_tag_owner.parent.id}"]
}









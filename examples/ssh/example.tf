
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
  check_period = "12h"
  accept_env = [
    "LANG*",
    "LC_*"
  ]
}
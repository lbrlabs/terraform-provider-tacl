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

resource "tacl_tag_owner" "parent" {
  name   = "parent"
  owners = ["group:${tacl_group.example.id}"]
}

resource "tacl_tag_owner" "child" {
  name   = "child"
  owners = ["tag:${tacl_tag_owner.parent.id}"]
}
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


resource "tacl_posture" "example_posture" {
  name  = "latestMac"
  rules = ["node:os in ['macos']", "node:tsVersion >= '1.40'"]
}
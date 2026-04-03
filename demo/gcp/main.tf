provider "google" {
  project = var.project_id
  region  = var.region
  zone    = var.zone
}

locals {
  instance_names = [
    "p2pfs-bootstrap",
    "p2pfs-peer-b",
    "p2pfs-peer-c",
  ]

  common_tags = ["p2pfs-demo"]
}

resource "google_compute_network" "p2pfs_demo" {
  name                    = "p2pfs-demo-network"
  auto_create_subnetworks = true
}

resource "google_compute_firewall" "p2pfs_demo_ingress" {
  name    = "p2pfs-demo-ingress"
  network = google_compute_network.p2pfs_demo.name

  allow {
    protocol = "tcp"
    ports    = ["22", "4001-4010"]
  }

  source_ranges = ["0.0.0.0/0"]
  target_tags   = local.common_tags
}

resource "google_compute_instance" "p2pfs_demo" {
  for_each     = toset(local.instance_names)
  name         = each.value
  machine_type = var.machine_type
  zone         = var.zone
  tags         = local.common_tags

  boot_disk {
    initialize_params {
      image = "projects/debian-cloud/global/images/family/debian-12"
      size  = 20
      type  = "pd-balanced"
    }
  }

  network_interface {
    network = google_compute_network.p2pfs_demo.id

    access_config {}
  }

  metadata_startup_script = <<-EOT
    #!/bin/bash
    set -euxo pipefail
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y ca-certificates curl git

    cd /tmp
    curl -fsSLO https://go.dev/dl/go${var.go_version}.linux-amd64.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf go${var.go_version}.linux-amd64.tar.gz
    ln -sf /usr/local/go/bin/go /usr/local/bin/go
    ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

    if [ ! -d /opt/p2pfs/.git ]; then
      git clone --branch ${var.repo_branch} --single-branch ${var.repo_url} /opt/p2pfs
    else
      cd /opt/p2pfs
      git fetch origin
      git checkout ${var.repo_branch}
      git pull --ff-only origin ${var.repo_branch}
    fi

    cd /opt/p2pfs
    /usr/local/go/bin/go version
    /usr/local/go/bin/go build -o p2pfs
  EOT
}

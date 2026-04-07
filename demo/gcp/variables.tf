variable "project_id" {
  description = "GCP project ID to deploy into."
  type        = string
}

variable "region" {
  description = "GCP region for the demo network."
  type        = string
  default     = "us-east5"
}

variable "zone" {
  description = "GCP zone for the demo instances."
  type        = string
  default     = "us-east5-b"
}

variable "machine_type" {
  description = "Machine type for the demo VMs."
  type        = string
  default     = "e2-medium"
}

# demo/terraform/backend.tf
#
# Terraform state backend.
# By default uses local state — safe for a single-developer demo.
# Uncomment the S3 block for shared team use.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

terraform {
  # Local state (default for demo)
  # backend "local" {}

  # Uncomment for shared state:
  # backend "s3" {
  #   bucket = "kardinal-terraform-state"
  #   key    = "demo/eks/terraform.tfstate"
  #   region = "us-east-2"
  # }
}

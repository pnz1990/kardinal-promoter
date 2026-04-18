# demo/terraform/outputs.tf
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

output "cluster_name" {
  description = "EKS cluster name"
  value       = module.eks.cluster_name
}

output "cluster_region" {
  description = "AWS region"
  value       = var.region
}

output "cluster_endpoint" {
  description = "EKS cluster API endpoint"
  value       = module.eks.cluster_endpoint
}

output "kubeconfig_update_command" {
  description = "Command to update kubeconfig for this cluster"
  value       = "aws eks update-kubeconfig --name ${module.eks.cluster_name} --region ${var.region} --alias ${var.kubeconfig_alias}"
}

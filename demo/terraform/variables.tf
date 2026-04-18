# demo/terraform/variables.tf
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

variable "region" {
  description = "AWS region for the demo EKS cluster"
  type        = string
  default     = "us-east-2"
}

variable "cluster_name" {
  description = "EKS cluster name"
  type        = string
  default     = "kardinal-demo-prod"
}

variable "kubernetes_version" {
  description = "Kubernetes version for the EKS cluster"
  type        = string
  default     = "1.29"
}

variable "kubeconfig_alias" {
  description = "kubectl context alias to use for this cluster"
  type        = string
  default     = "kardinal-prod"
}

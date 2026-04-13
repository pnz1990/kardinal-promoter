variable "cluster_name" {
  description = "EKS cluster name"
  type        = string
  default     = "kardinal-e2e-prod"
}

variable "region" {
  description = "AWS region — intentionally separate from any existing clusters"
  type        = string
  default     = "us-east-2"
}

variable "kubernetes_version" {
  description = "Kubernetes version for the EKS cluster"
  type        = string
  default     = "1.31"
}

variable "node_instance_type" {
  description = "EC2 instance type for EKS managed node group"
  type        = string
  default     = "t3.medium"
}

variable "node_count" {
  description = "Number of worker nodes"
  type        = number
  default     = 2
}

variable "argocd_chart_version" {
  description = "Argo CD Helm chart version"
  type        = string
  default     = "7.1.3"
}

# kardinal-e2e-prod — EKS cluster for kardinal-promoter multi-cluster E2E tests
#
# This is the "prod" cluster in the Journey 2 multi-cluster scenario.
# Intentionally minimal: 2 x t3.medium nodes.
#
# Usage:
#   cd terraform/eks-e2e
#   terraform init
#   terraform apply
#
# After apply, run:
#   $(terraform output -raw kubeconfig_update_command)
#   make setup-multi-cluster-env
#   make test-e2e-journey-2
#
# To destroy when done:
#   terraform destroy

terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.40"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.27"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.12"
    }
  }
}

provider "aws" {
  region = var.region
}

# ── Data ────────────────────────────────────────────────────────────────────

data "aws_availability_zones" "available" {
  state = "available"
}

data "aws_caller_identity" "current" {}

# ── VPC ─────────────────────────────────────────────────────────────────────

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.5"

  name = "kardinal-e2e"
  cidr = "10.100.0.0/16"

  azs             = slice(data.aws_availability_zones.available.names, 0, 2)
  private_subnets = ["10.100.1.0/24", "10.100.2.0/24"]
  public_subnets  = ["10.100.101.0/24", "10.100.102.0/24"]

  enable_nat_gateway   = true
  single_nat_gateway   = true
  enable_dns_hostnames = true

  public_subnet_tags = {
    "kubernetes.io/role/elb" = 1
  }

  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = 1
  }

  tags = local.tags
}

# ── EKS ─────────────────────────────────────────────────────────────────────

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.8"

  cluster_name    = var.cluster_name
  cluster_version = var.kubernetes_version

  cluster_endpoint_public_access  = true
  cluster_endpoint_private_access = true

  vpc_id     = module.vpc.vpc_id
  subnet_ids = module.vpc.private_subnets

  # Allow the agent's IAM identity to manage the cluster
  enable_cluster_creator_admin_permissions = true

  eks_managed_node_groups = {
    standard = {
      instance_types = [var.node_instance_type]
      min_size       = 1
      max_size       = 3
      desired_size   = var.node_count

      labels = {
        role = "standard"
      }
    }
  }

  tags = local.tags
}

# ── ArgoCD ───────────────────────────────────────────────────────────────────

provider "kubernetes" {
  host                   = module.eks.cluster_endpoint
  cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.region]
  }
}

provider "helm" {
  kubernetes {
    host                   = module.eks.cluster_endpoint
    cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)

    exec {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "aws"
      args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.region]
    }
  }
}

resource "kubernetes_namespace" "argocd" {
  metadata {
    name = "argocd"
  }

  depends_on = [module.eks]
}

resource "helm_release" "argocd" {
  name       = "argocd"
  repository = "https://argoproj.github.io/argo-helm"
  chart      = "argo-cd"
  version    = var.argocd_chart_version
  namespace  = kubernetes_namespace.argocd.metadata[0].name

  set {
    name  = "server.service.type"
    value = "ClusterIP"
  }

  # Reduce resource usage for E2E cluster
  set {
    name  = "redis.resources.requests.memory"
    value = "64Mi"
  }
  set {
    name  = "server.resources.requests.memory"
    value = "64Mi"
  }
  set {
    name  = "repoServer.resources.requests.memory"
    value = "64Mi"
  }

  depends_on = [kubernetes_namespace.argocd]
}

# ── Test app namespace ───────────────────────────────────────────────────────

resource "kubernetes_namespace" "prod" {
  metadata {
    name = "kardinal-test-app-prod"
    labels = {
      "kardinal.io/environment" = "prod"
    }
  }

  depends_on = [module.eks]
}

# ── Locals ───────────────────────────────────────────────────────────────────

locals {
  tags = {
    Project     = "kardinal-promoter"
    Environment = "e2e"
    ManagedBy   = "terraform"
    Cluster     = var.cluster_name
  }
}

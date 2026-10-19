---
section: Added
---

- **Remote health checks on EKS without a long-lived token** (#2008). `health.kubeconfigSecretRef.eks` (`clusterName`, `region`, `roleARN`) has the controller assume the role with its own AWS identity (IRSA or EKS Pod Identity), with the session tags `kardinal-namespace` and `kardinal-pipeline`, and sign 15-minute EKS tokens as `aws eks get-token` does, with no command run. The endpoint and CA come from `eks:DescribeCluster` as the role, and the token is sent to that endpoint only (a kubeconfig in the Secret is optional; a `server` it names must be that endpoint). Only roles the operator allows can be named: `health.eks.allowedRoleARNsByNamespace` per namespace, `health.eks.allowedRoleARNs` for every namespace, both empty by default ([Remote clusters on EKS](health-adapters.md#remote-clusters-on-eks))

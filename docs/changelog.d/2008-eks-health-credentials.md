---
section: Added
---

- **Remote health checks on EKS without a long-lived token** (#2008). `health.kubeconfigSecretRef.eks` (`clusterName`, `region`, `roleARN`) has the controller assume the role with its own AWS identity (IRSA or EKS Pod Identity), with the session tags `kardinal-namespace` and `kardinal-pipeline`, and sign 15-minute EKS tokens as `aws eks get-token` does, with no command run. The kubeconfig Secret holds only the server and its CA. Only roles in the operator's `health.eks.allowedRoleARNs` (empty by default) can be named ([Remote clusters on EKS](health-adapters.md#remote-clusters-on-eks))

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

// LabelSecretReferenceable is the label a Secret must carry, with the value
// "true", before a Subscription, MetricCheck or NotificationHook may read
// it. Without it the resource's reconciler reports the condition reason
// ReasonSecretNotReferenceable and sends nothing. A user who can create those
// resources in a namespace can thus use only the Secrets someone labelled
// for that purpose, not every Secret of the namespace.
const LabelSecretReferenceable = "kardinal.io/referenceable"

// ReasonSecretNotReferenceable is the condition reason for a Secret without
// LabelSecretReferenceable: "true".
const ReasonSecretNotReferenceable = "SecretNotReferenceable"

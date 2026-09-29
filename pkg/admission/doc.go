// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package admission implements ValidatingAdmissionWebhook handlers for
// kardinal-promoter CRDs. The handlers are mounted on the existing webhook
// server (--webhook-bind-address) and require a separately-configured
// ValidatingWebhookConfiguration to be installed in the cluster.
package admission

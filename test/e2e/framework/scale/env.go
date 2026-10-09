// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"os"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// WebhookURL is the controller's webhook URL as the git server reaches it,
// or "" when the suite has none.
func WebhookURL() string { return os.Getenv(framework.EnvWebhookURL) }

// WebhookSecret is the webhook HMAC secret.
func WebhookSecret() string { return os.Getenv(framework.EnvWebhookSecret) }

// Keep reports KARDINAL_E2E_KEEP=1: namespaces and repos stay after the test.
func Keep() bool { return os.Getenv(framework.EnvKeep) == "1" }

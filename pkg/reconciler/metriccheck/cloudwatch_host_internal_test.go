// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAWSMonitoringHost (QA round 3 on #1479): with the controller's own AWS
// identity only CloudWatch API hosts are accepted: the regional and FIPS
// endpoints and the VPC endpoint names, each in its own partition. S3 bucket
// hosts and other services under amazonaws.com are refused.
func TestAWSMonitoringHost(t *testing.T) {
	for host, want := range map[string]bool{
		"monitoring.eu-west-1.amazonaws.com":                                                 true,
		"monitoring.us-east-1.amazonaws.com":                                                 true,
		"monitoring-fips.us-gov-west-1.amazonaws.com":                                        true,
		"monitoring.ap-southeast-2.amazonaws.com":                                            true,
		"monitoring.cn-north-1.amazonaws.com.cn":                                             true,
		"monitoring.cn-northwest-1.amazonaws.com.cn":                                         true,
		"vpce-0a1b2c3d4e5f60718-abcdefgh.monitoring.eu-west-1.vpce.amazonaws.com":            true,
		"vpce-0a1b2c3d4e5f60718-abcdefgh-eu-west-1a.monitoring.eu-west-1.vpce.amazonaws.com": true,
		"vpce-0a1b2c3d4e5f60718-abcdefgh.monitoring.cn-north-1.vpce.amazonaws.com.cn":        true,

		"monitoring.s3.amazonaws.com":                                            false, // bucket "monitoring", legacy global S3
		"monitoring.s3-website-us-east-1.amazonaws.com":                          false, // bucket website endpoint
		"monitoring.s3-eu-west-1.amazonaws.com":                                  false,
		"monitoring.s3.eu-west-1.amazonaws.com":                                  false,
		"s3.eu-west-1.amazonaws.com":                                             false,
		"logs.eu-west-1.amazonaws.com":                                           false,
		"monitoring.amazonaws.com":                                               false,
		"monitoring.eu-west-1.amazonaws.com.cn":                                  false, // wrong partition
		"monitoring.cn-north-1.amazonaws.com":                                    false,
		"monitoring.eu-west-1.amazonaws.com.evil.example":                        false,
		"evil.monitoring.eu-west-1.amazonaws.com":                                false,
		"vpce-xyz.monitoring.eu-west-1.vpce.amazonaws.com":                       false,
		"vpce-0a1b2c3d4e5f60718-abcdefgh.s3.eu-west-1.vpce.amazonaws.com":        false,
		"bucket.vpce-0a1b2c3d4e5f60718-abcdefgh.s3.eu-west-1.vpce.amazonaws.com": false,
		"MONITORING.EU-WEST-1.AMAZONAWS.COM":                                     false,
		"":                                                                       false,
	} {
		assert.Equal(t, want, awsMonitoringHost(host), host)
	}
}

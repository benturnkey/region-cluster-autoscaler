package main

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	klog "k8s.io/klog/v2"
)

var awsRegionEnvMu sync.Mutex

func buildProviderForRegion(
	region string,
	build func() cloudprovider.CloudProvider,
) cloudprovider.CloudProvider {
	awsRegionEnvMu.Lock()
	defer awsRegionEnvMu.Unlock()
	klog.Background().WithValues("region", region).Info("building provider")

	previous, hadPrevious := os.LookupEnv("AWS_REGION")
	if err := os.Setenv("AWS_REGION", region); err != nil {
		panic(fmt.Sprintf("failed to set AWS_REGION for %q: %v", region, err))
	}
	defer func() {
		if hadPrevious {
			if err := os.Setenv("AWS_REGION", previous); err != nil {
				klog.Background().WithValues("region", region).Error(err, "failed to restore AWS_REGION")
			}
		} else {
			if err := os.Unsetenv("AWS_REGION"); err != nil {
				klog.Background().WithValues("region", region).Error(err, "failed to unset AWS_REGION")
			}
		}

		if r := recover(); r != nil {
			klog.Background().WithValues("region", region).Error(nil, "panic while building provider", "error", r)
			panic(r)
		}
	}()

	// Upstream's AWS provider is single-region and does not expose a public
	// constructor that accepts an explicit region. We keep one upstream provider
	// per region and scope region selection to construction time here rather than
	// reaching into upstream internals via reflection.
	return build()
}

func regionFromProviderID(providerID string) string {
	if !strings.HasPrefix(providerID, "aws:///") {
		klog.Warningf("failed to parse AWS region from providerID %q: does not have aws:/// prefix", providerID)
		return ""
	}

	parts := strings.Split(strings.TrimPrefix(providerID, "aws:///"), "/")
	if len(parts) < 2 {
		klog.Warningf("failed to parse AWS region from providerID %q: no zone info available", providerID)
		return ""
	}

	zone := strings.TrimSpace(parts[0])
	if len(zone) < 2 {
		klog.Warningf("failed to parse AWS region from providerID %q: zone %q is too short", providerID, zone)
		return ""
	}

	return zone[:len(zone)-1]
}

package main

import (
	klog "k8s.io/klog/v2"
)

func logRegionInfo(region string, operation string, kv ...any) {
	args := append([]any{"region", region, "op", operation}, kv...)
	klog.InfoSDepth(1, "regional operation", args...)
}

func logRegionError(region string, operation string, kv ...any) {
	args := append([]any{"region", region, "op", operation}, kv...)
	klog.ErrorSDepth(1, nil, "regional operation", args...)
}

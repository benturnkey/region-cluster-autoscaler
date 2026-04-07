package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	klog "k8s.io/klog/v2"
)

var (
	regionLogScopeMu sync.Mutex
	activeLogRegion  atomic.Pointer[string]
)

// regionAwareLogWriter prefixes klog lines with the active AWS region when a
// regional provider operation is in progress. This keeps upstream AWS logs
// readable without patching every call site.
type regionAwareLogWriter struct {
	output io.Writer
}

func configureRegionAwareLogging() {
	klog.LogToStderr(false)
	klog.SetOutput(&regionAwareLogWriter{output: os.Stderr})
}

func beginRegionLogScope(region string) func() {
	regionLogScopeMu.Lock()
	activeLogRegion.Store(&region)
	return func() {
		activeLogRegion.Store(nil)
		regionLogScopeMu.Unlock()
	}
}

func (w *regionAwareLogWriter) Write(p []byte) (int, error) {
	regionPtr := activeLogRegion.Load()
	if regionPtr == nil || *regionPtr == "" {
		return w.output.Write(p)
	}

	prefixed := prefixRegionLogLines(p, *regionPtr)
	_, err := w.output.Write(prefixed)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func prefixRegionLogLines(p []byte, region string) []byte {
	lines := bytes.SplitAfter(p, []byte{'\n'})
	prefix := fmt.Sprintf("[region=%s] ", region)
	var out strings.Builder
	out.Grow(len(p) + len(lines)*len(prefix))

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		text := string(line)
		newline := strings.HasSuffix(text, "\n")
		text = strings.TrimSuffix(text, "\n")
		if text == "" {
			if newline {
				out.WriteByte('\n')
			}
			continue
		}

		if idx := strings.Index(text, "] "); idx >= 0 {
			out.WriteString(text[:idx+2])
			out.WriteString(prefix)
			out.WriteString(text[idx+2:])
		} else {
			out.WriteString(prefix)
			out.WriteString(text)
		}

		if newline {
			out.WriteByte('\n')
		}
	}

	return []byte(out.String())
}

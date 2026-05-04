package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"k8s.io/component-base/metrics/legacyregistry"
	klog "k8s.io/klog/v2"
)

type workerHealthReporter interface {
	ConfiguredWorkerCount() int
	HealthyWorkerCount() int
	UnhealthyWorkerCount() int
}

func newRouterHTTPServer(addr string, reporter workerHealthReporter) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprintf(
			w,
			"ok configured=%d healthy=%d unhealthy=%d",
			reporter.ConfiguredWorkerCount(),
			reporter.HealthyWorkerCount(),
			reporter.UnhealthyWorkerCount(),
		); err != nil {
			klog.Errorf("failed to write health response: %v", err)
		}
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		if reporter.HealthyWorkerCount() < 1 {
			http.Error(w, "no healthy workers available", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, "ready"); err != nil {
			klog.Errorf("failed to write readiness response: %v", err)
		}
	})
	mux.Handle("/metrics", legacyregistry.Handler())

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func serveHTTPServer(ctx context.Context, server *http.Server, name string) {
	go func() {
		klog.Infof("starting %s on %s", name, server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Fatalf("%s failed: %v", name, err)
		}
	}()

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			klog.Errorf("failed to shut down %s cleanly: %v", name, err)
		}
	}()
}

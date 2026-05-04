package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/externalgrpc/protos"
	k8smetrics "k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
	klog "k8s.io/klog/v2"
)

const (
	defaultWorkerProbeTimeout  = 2 * time.Second
	defaultWorkerProbeInterval = 5 * time.Second
)

var (
	registerRouterMetricsOnce sync.Once

	routerConfiguredWorkers = k8smetrics.NewGauge(
		&k8smetrics.GaugeOpts{
			Namespace: "cluster_autoscaler_provider",
			Subsystem: "router",
			Name:      "configured_workers",
			Help:      "Number of configured backend workers.",
		},
	)
	routerHealthyWorkers = k8smetrics.NewGauge(
		&k8smetrics.GaugeOpts{
			Namespace: "cluster_autoscaler_provider",
			Subsystem: "router",
			Name:      "healthy_workers",
			Help:      "Number of backend workers currently marked healthy.",
		},
	)
	routerUnhealthyWorkers = k8smetrics.NewGauge(
		&k8smetrics.GaugeOpts{
			Namespace: "cluster_autoscaler_provider",
			Subsystem: "router",
			Name:      "unhealthy_workers",
			Help:      "Number of backend workers currently marked unhealthy.",
		},
	)
)

type regionalClient struct {
	region string
	client protos.CloudProviderClient
	conn   *grpc.ClientConn
}

type workerStatus struct {
	healthy     bool
	lastChecked time.Time
	lastError   string
}

type CachingRouter struct {
	protos.UnimplementedCloudProviderServer

	clients     map[string]regionalClient
	clientOrder []string

	mu                sync.RWMutex
	cache             map[string]cacheEntry
	cacheTTL          time.Duration
	backendRPCTimeout time.Duration
	workerState       map[string]workerStatus
}

type cacheEntry struct {
	data       interface{}
	expiration time.Time
}

func registerRouterMetrics() {
	registerRouterMetricsOnce.Do(func() {
		legacyregistry.MustRegister(
			routerConfiguredWorkers,
			routerHealthyWorkers,
			routerUnhealthyWorkers,
		)
	})
}

func newCachingRouter(cfg *Config) (*CachingRouter, error) {
	registerRouterMetrics()

	clients := make(map[string]regionalClient, len(cfg.Backends))
	clientOrder := make([]string, 0, len(cfg.Backends))
	workerState := make(map[string]workerStatus, len(cfg.Backends))
	for _, backend := range cfg.Backends {
		klog.Infof("configuring router backend region=%s address=localhost:%d provider=%s", backend.Region, backend.Port, backend.Provider)

		addr := fmt.Sprintf("localhost:%d", backend.Port)
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize backend client for %s: %w", addr, err)
		}
		clients[backend.Region] = regionalClient{
			region: backend.Region,
			client: protos.NewCloudProviderClient(conn),
			conn:   conn,
		}
		clientOrder = append(clientOrder, backend.Region)
		workerState[backend.Region] = workerStatus{}
	}

	r := &CachingRouter{
		clients:           clients,
		clientOrder:       clientOrder,
		cache:             make(map[string]cacheEntry),
		cacheTTL:          time.Duration(cfg.CacheTTL),
		backendRPCTimeout: time.Duration(cfg.BackendRPCTimeout),
		workerState:       workerState,
	}
	r.updateWorkerMetricsLocked()
	return r, nil
}

func (r *CachingRouter) Start(ctx context.Context) {
	go func() {
		r.probeWorkers(ctx)

		ticker := time.NewTicker(defaultWorkerProbeInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.probeWorkers(ctx)
			}
		}
	}()
}

func (r *CachingRouter) Close() error {
	var errs []string
	for _, client := range r.clients {
		if err := client.conn.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", client.region, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to close backend connections: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (r *CachingRouter) HealthyWorkerCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, status := range r.workerState {
		if status.healthy {
			count++
		}
	}
	return count
}

func (r *CachingRouter) ConfiguredWorkerCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.workerState)
}

func (r *CachingRouter) UnhealthyWorkerCount() int {
	return r.ConfiguredWorkerCount() - r.HealthyWorkerCount()
}

func (r *CachingRouter) probeWorkers(ctx context.Context) {
	type probeResult struct {
		region string
		err    error
	}

	results := make(chan probeResult, len(r.clients))
	var wg sync.WaitGroup

	for _, client := range r.clients {
		wg.Add(1)
		go func(rc regionalClient) {
			defer wg.Done()

			probeCtx, cancel := context.WithTimeout(ctx, defaultWorkerProbeTimeout)
			defer cancel()

			_, err := rc.client.NodeGroups(probeCtx, &protos.NodeGroupsRequest{})
			results <- probeResult{region: rc.region, err: err}
		}(client)
	}

	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			klog.Errorf("worker probe failed for region %s: %v", result.region, result.err)
			r.markWorkerUnhealthy(result.region, result.err)
			continue
		}
		klog.Infof("worker probe succeeded for region %s", result.region)
		r.markWorkerHealthy(result.region)
	}
}

func (r *CachingRouter) getClientForNode(providerID string) (regionalClient, error) {
	region := regionFromProviderID(providerID)
	if region == "" {
		return regionalClient{}, fmt.Errorf("could not determine region from providerID %q", providerID)
	}
	return r.getClientForRegion(region)
}

func (r *CachingRouter) getClientForRegion(region string) (regionalClient, error) {
	client, ok := r.clients[region]
	if !ok {
		return regionalClient{}, fmt.Errorf("no backend configured for region %q", region)
	}
	return client, nil
}

func (r *CachingRouter) getClientForGroup(groupID string) (regionalClient, string, error) {
	parts := strings.SplitN(groupID, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return regionalClient{}, "", fmt.Errorf("invalid group ID %q: expected region/id format", groupID)
	}

	client, err := r.getClientForRegion(parts[0])
	if err != nil {
		return regionalClient{}, "", err
	}
	return client, parts[1], nil
}

func (r *CachingRouter) getHealthyClients() []regionalClient {
	r.mu.RLock()
	defer r.mu.RUnlock()

	clients := make([]regionalClient, 0, len(r.clientOrder))
	for _, region := range r.clientOrder {
		if !r.workerState[region].healthy {
			continue
		}
		clients = append(clients, r.clients[region])
	}
	return clients
}

func (r *CachingRouter) backendContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, r.backendRPCTimeout)
}

func (r *CachingRouter) clearCache() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearCacheLocked()
}

func (r *CachingRouter) clearCacheLocked() {
	r.cache = make(map[string]cacheEntry)
}

func (r *CachingRouter) updateWorkerMetricsLocked() {
	configured := len(r.workerState)
	healthy := 0
	for _, status := range r.workerState {
		if status.healthy {
			healthy++
		}
	}

	routerConfiguredWorkers.Set(float64(configured))
	routerHealthyWorkers.Set(float64(healthy))
	routerUnhealthyWorkers.Set(float64(configured - healthy))
}

func (r *CachingRouter) markWorkerHealthy(region string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	status := r.workerState[region]
	status.healthy = true
	status.lastChecked = time.Now()
	status.lastError = ""
	r.workerState[region] = status
	r.updateWorkerMetricsLocked()
}

func (r *CachingRouter) markWorkerUnhealthy(region string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	status := r.workerState[region]
	status.healthy = false
	status.lastChecked = time.Now()
	if err != nil {
		status.lastError = err.Error()
	}
	r.workerState[region] = status

	healthy := 0
	for _, worker := range r.workerState {
		if worker.healthy {
			healthy++
		}
	}
	if healthy == 0 {
		r.clearCacheLocked()
	}
	r.updateWorkerMetricsLocked()
}

func (r *CachingRouter) NodeGroups(ctx context.Context, req *protos.NodeGroupsRequest) (*protos.NodeGroupsResponse, error) {
	r.mu.RLock()
	if entry, ok := r.cache["NodeGroups"]; ok && time.Now().Before(entry.expiration) {
		r.mu.RUnlock()
		return proto.Clone(entry.data.(*protos.NodeGroupsResponse)).(*protos.NodeGroupsResponse), nil
	}
	r.mu.RUnlock()

	type result struct {
		region string
		resp   *protos.NodeGroupsResponse
		err    error
	}

	results := make(chan result, len(r.clients))
	var wg sync.WaitGroup
	for _, client := range r.clients {
		wg.Add(1)
		go func(rc regionalClient) {
			defer wg.Done()

			callCtx, cancel := r.backendContext(ctx)
			defer cancel()

			resp, err := rc.client.NodeGroups(callCtx, req)
			results <- result{region: rc.region, resp: resp, err: err}
		}(client)
	}

	wg.Wait()
	close(results)

	allGroups := make([]*protos.NodeGroup, 0)
	successes := 0
	for result := range results {
		if result.err != nil {
			klog.Errorf("failed to fetch node groups from %s: %v", result.region, result.err)
			r.markWorkerUnhealthy(result.region, result.err)
			continue
		}

		r.markWorkerHealthy(result.region)
		successes++
		for _, group := range result.resp.GetNodeGroups() {
			group.Id = fmt.Sprintf("%s/%s", result.region, group.GetId())
			allGroups = append(allGroups, group)
		}
	}

	if successes == 0 {
		return nil, fmt.Errorf("failed to fetch node groups from all configured workers")
	}

	resp := &protos.NodeGroupsResponse{NodeGroups: allGroups}
	r.mu.Lock()
	r.cache["NodeGroups"] = cacheEntry{
		data:       proto.Clone(resp).(*protos.NodeGroupsResponse),
		expiration: time.Now().Add(r.cacheTTL),
	}
	r.mu.Unlock()
	return proto.Clone(resp).(*protos.NodeGroupsResponse), nil
}

func (r *CachingRouter) NodeGroupForNode(ctx context.Context, req *protos.NodeGroupForNodeRequest) (*protos.NodeGroupForNodeResponse, error) {
	if req.GetNode() == nil {
		return nil, fmt.Errorf("node is required")
	}

	client, err := r.getClientForNode(req.GetNode().GetProviderID())
	if err != nil {
		return nil, err
	}

	callCtx, cancel := r.backendContext(ctx)
	defer cancel()

	resp, err := client.client.NodeGroupForNode(callCtx, req)
	if err != nil {
		r.markWorkerUnhealthy(client.region, err)
		return nil, err
	}

	r.markWorkerHealthy(client.region)
	if resp.GetNodeGroup() != nil && resp.GetNodeGroup().GetId() != "" {
		resp.GetNodeGroup().Id = fmt.Sprintf("%s/%s", client.region, resp.GetNodeGroup().GetId())
	}
	return resp, nil
}

func (r *CachingRouter) NodeGroupIncreaseSize(ctx context.Context, req *protos.NodeGroupIncreaseSizeRequest) (*protos.NodeGroupIncreaseSizeResponse, error) {
	client, backendID, err := r.getClientForGroup(req.GetId())
	if err != nil {
		return nil, err
	}

	callCtx, cancel := r.backendContext(ctx)
	defer cancel()

	resp, err := client.client.NodeGroupIncreaseSize(callCtx, &protos.NodeGroupIncreaseSizeRequest{
		Id:    backendID,
		Delta: req.GetDelta(),
	})
	if err != nil {
		r.markWorkerUnhealthy(client.region, err)
		return nil, err
	}

	r.markWorkerHealthy(client.region)
	r.clearCache()
	return resp, nil
}

func (r *CachingRouter) NodeGroupDeleteNodes(ctx context.Context, req *protos.NodeGroupDeleteNodesRequest) (*protos.NodeGroupDeleteNodesResponse, error) {
	client, backendID, err := r.getClientForGroup(req.GetId())
	if err != nil {
		return nil, err
	}

	callCtx, cancel := r.backendContext(ctx)
	defer cancel()

	resp, err := client.client.NodeGroupDeleteNodes(callCtx, &protos.NodeGroupDeleteNodesRequest{
		Id:    backendID,
		Nodes: req.GetNodes(),
	})
	if err != nil {
		r.markWorkerUnhealthy(client.region, err)
		return nil, err
	}

	r.markWorkerHealthy(client.region)
	r.clearCache()
	return resp, nil
}

func (r *CachingRouter) NodeGroupDecreaseTargetSize(ctx context.Context, req *protos.NodeGroupDecreaseTargetSizeRequest) (*protos.NodeGroupDecreaseTargetSizeResponse, error) {
	client, backendID, err := r.getClientForGroup(req.GetId())
	if err != nil {
		return nil, err
	}

	callCtx, cancel := r.backendContext(ctx)
	defer cancel()

	resp, err := client.client.NodeGroupDecreaseTargetSize(callCtx, &protos.NodeGroupDecreaseTargetSizeRequest{
		Id:    backendID,
		Delta: req.GetDelta(),
	})
	if err != nil {
		r.markWorkerUnhealthy(client.region, err)
		return nil, err
	}

	r.markWorkerHealthy(client.region)
	r.clearCache()
	return resp, nil
}

func (r *CachingRouter) Refresh(ctx context.Context, req *protos.RefreshRequest) (*protos.RefreshResponse, error) {
	r.clearCache()

	type result struct {
		region string
		err    error
	}

	results := make(chan result, len(r.clients))
	var wg sync.WaitGroup
	for _, client := range r.clients {
		wg.Add(1)
		go func(rc regionalClient) {
			defer wg.Done()

			callCtx, cancel := r.backendContext(ctx)
			defer cancel()

			_, err := rc.client.Refresh(callCtx, req)
			results <- result{region: rc.region, err: err}
		}(client)
	}

	wg.Wait()
	close(results)

	successes := 0
	for result := range results {
		if result.err != nil {
			klog.Errorf("failed to refresh backend %s: %v", result.region, result.err)
			r.markWorkerUnhealthy(result.region, result.err)
			continue
		}
		r.markWorkerHealthy(result.region)
		successes++
	}

	if successes == 0 {
		return nil, fmt.Errorf("failed to refresh all configured workers")
	}
	return &protos.RefreshResponse{}, nil
}

func (r *CachingRouter) Cleanup(ctx context.Context, req *protos.CleanupRequest) (*protos.CleanupResponse, error) {
	type result struct {
		region string
		err    error
	}

	results := make(chan result, len(r.clients))
	var wg sync.WaitGroup
	for _, client := range r.clients {
		wg.Add(1)
		go func(rc regionalClient) {
			defer wg.Done()

			callCtx, cancel := r.backendContext(ctx)
			defer cancel()

			_, err := rc.client.Cleanup(callCtx, req)
			results <- result{region: rc.region, err: err}
		}(client)
	}

	wg.Wait()
	close(results)

	successes := 0
	for result := range results {
		if result.err != nil {
			klog.Errorf("failed to clean up backend %s: %v", result.region, result.err)
			r.markWorkerUnhealthy(result.region, result.err)
			continue
		}
		r.markWorkerHealthy(result.region)
		successes++
	}

	if successes == 0 {
		return nil, fmt.Errorf("failed to clean up all configured workers")
	}
	return &protos.CleanupResponse{}, nil
}

func (r *CachingRouter) NodeGroupTargetSize(ctx context.Context, req *protos.NodeGroupTargetSizeRequest) (*protos.NodeGroupTargetSizeResponse, error) {
	client, backendID, err := r.getClientForGroup(req.GetId())
	if err != nil {
		return nil, err
	}

	callCtx, cancel := r.backendContext(ctx)
	defer cancel()

	resp, err := client.client.NodeGroupTargetSize(callCtx, &protos.NodeGroupTargetSizeRequest{Id: backendID})
	if err != nil {
		r.markWorkerUnhealthy(client.region, err)
		return nil, err
	}
	r.markWorkerHealthy(client.region)
	return resp, nil
}

func (r *CachingRouter) NodeGroupNodes(ctx context.Context, req *protos.NodeGroupNodesRequest) (*protos.NodeGroupNodesResponse, error) {
	client, backendID, err := r.getClientForGroup(req.GetId())
	if err != nil {
		return nil, err
	}

	callCtx, cancel := r.backendContext(ctx)
	defer cancel()

	resp, err := client.client.NodeGroupNodes(callCtx, &protos.NodeGroupNodesRequest{Id: backendID})
	if err != nil {
		r.markWorkerUnhealthy(client.region, err)
		return nil, err
	}
	r.markWorkerHealthy(client.region)
	return resp, nil
}

func (r *CachingRouter) NodeGroupTemplateNodeInfo(ctx context.Context, req *protos.NodeGroupTemplateNodeInfoRequest) (*protos.NodeGroupTemplateNodeInfoResponse, error) {
	client, backendID, err := r.getClientForGroup(req.GetId())
	if err != nil {
		return nil, err
	}

	callCtx, cancel := r.backendContext(ctx)
	defer cancel()

	resp, err := client.client.NodeGroupTemplateNodeInfo(callCtx, &protos.NodeGroupTemplateNodeInfoRequest{Id: backendID})
	if err != nil {
		r.markWorkerUnhealthy(client.region, err)
		return nil, err
	}
	r.markWorkerHealthy(client.region)
	return resp, nil
}

func (r *CachingRouter) NodeGroupGetOptions(ctx context.Context, req *protos.NodeGroupAutoscalingOptionsRequest) (*protos.NodeGroupAutoscalingOptionsResponse, error) {
	client, backendID, err := r.getClientForGroup(req.GetId())
	if err != nil {
		return nil, err
	}

	callCtx, cancel := r.backendContext(ctx)
	defer cancel()

	resp, err := client.client.NodeGroupGetOptions(callCtx, &protos.NodeGroupAutoscalingOptionsRequest{
		Id:       backendID,
		Defaults: req.GetDefaults(),
	})
	if err != nil {
		r.markWorkerUnhealthy(client.region, err)
		return nil, err
	}
	r.markWorkerHealthy(client.region)
	return resp, nil
}

func (r *CachingRouter) PricingNodePrice(ctx context.Context, req *protos.PricingNodePriceRequest) (*protos.PricingNodePriceResponse, error) {
	for _, client := range r.getHealthyClients() {
		callCtx, cancel := r.backendContext(ctx)
		resp, err := client.client.PricingNodePrice(callCtx, req)
		cancel()
		if err != nil {
			r.markWorkerUnhealthy(client.region, err)
			klog.Errorf("pricing node price failed for region %s: %v", client.region, err)
			continue
		}
		r.markWorkerHealthy(client.region)
		return resp, nil
	}
	return nil, fmt.Errorf("no healthy backends available")
}

func (r *CachingRouter) PricingPodPrice(ctx context.Context, req *protos.PricingPodPriceRequest) (*protos.PricingPodPriceResponse, error) {
	for _, client := range r.getHealthyClients() {
		callCtx, cancel := r.backendContext(ctx)
		resp, err := client.client.PricingPodPrice(callCtx, req)
		cancel()
		if err != nil {
			r.markWorkerUnhealthy(client.region, err)
			klog.Errorf("pricing pod price failed for region %s: %v", client.region, err)
			continue
		}
		r.markWorkerHealthy(client.region)
		return resp, nil
	}
	return nil, fmt.Errorf("no healthy backends available")
}

func (r *CachingRouter) GPULabel(ctx context.Context, req *protos.GPULabelRequest) (*protos.GPULabelResponse, error) {
	for _, client := range r.getHealthyClients() {
		callCtx, cancel := r.backendContext(ctx)
		resp, err := client.client.GPULabel(callCtx, req)
		cancel()
		if err != nil {
			r.markWorkerUnhealthy(client.region, err)
			klog.Errorf("gpu label lookup failed for region %s: %v", client.region, err)
			continue
		}
		r.markWorkerHealthy(client.region)
		return resp, nil
	}
	return nil, fmt.Errorf("no healthy backends available")
}

func (r *CachingRouter) GetAvailableGPUTypes(ctx context.Context, req *protos.GetAvailableGPUTypesRequest) (*protos.GetAvailableGPUTypesResponse, error) {
	for _, client := range r.getHealthyClients() {
		callCtx, cancel := r.backendContext(ctx)
		resp, err := client.client.GetAvailableGPUTypes(callCtx, req)
		cancel()
		if err != nil {
			r.markWorkerUnhealthy(client.region, err)
			klog.Errorf("available gpu types lookup failed for region %s: %v", client.region, err)
			continue
		}
		r.markWorkerHealthy(client.region)
		return resp, nil
	}
	return nil, fmt.Errorf("no healthy backends available")
}

package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/component-base/metrics/legacyregistry"
	"github.com/prometheus/client_golang/prometheus"
	klog "k8s.io/klog/v2"
)

var (
	awsRegionEnvMu           sync.Mutex
	awsMetricsRegisteredOnce bool
)

type noopRegisterer struct{}

func (n *noopRegisterer) Register(prometheus.Collector) error  { return nil }
func (n *noopRegisterer) MustRegister(...prometheus.Collector) {}
func (n *noopRegisterer) Unregister(prometheus.Collector) bool { return true }

type regionalProvider struct {
	region   string
	provider cloudprovider.CloudProvider
	log      klog.Logger
}

type multiRegionCloudProvider struct {
	providers []regionalProvider
	primary   cloudprovider.CloudProvider
}

type regionalNodeGroup struct {
	region string
	group  cloudprovider.NodeGroup
	log    klog.Logger
}

func parseAWSRegions(values []string) []string {
	regions := make([]string, 0, len(values))

	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			region := strings.TrimSpace(item)
			if region == "" {
				continue
			}
			regions = append(regions, region)
		}
	}

	slices.Sort(regions)
	return slices.Compact(regions)
}

func buildProviderForRegion(
	region string,
	build func() cloudprovider.CloudProvider,
) cloudprovider.CloudProvider {
	awsRegionEnvMu.Lock()
	defer awsRegionEnvMu.Unlock()
	klog.Background().WithValues("region", region).Info("Building provider")

	previous, hadPrevious := os.LookupEnv("AWS_REGION")
	if err := os.Setenv("AWS_REGION", region); err != nil {
		panic(fmt.Sprintf("failed to set AWS_REGION for %q: %v", region, err))
	}
	defer func() {
		if hadPrevious {
			_ = os.Setenv("AWS_REGION", previous)
		} else {
			_ = os.Unsetenv("AWS_REGION")
		}

		if r := recover(); r != nil {
			klog.Background().WithValues("region", region).Error(nil, "Panic in building provider", "error", r)
			panic(r)
		}
	}()

	// Upstream's AWS provider is single-region and does not expose a public
	// constructor that accepts an explicit region. We keep one upstream provider
	// per region and scope region selection to construction time here rather than
	// reaching into upstream internals via reflection.
	//
	// BuildAWS also registers global Prometheus metrics on every invocation, which
	// panics if metrics are already registered. We swap the global registerer with
	// a no-op after the first successful registration to prevent this.
	if awsMetricsRegisteredOnce {
		oldRegistererFunc := legacyregistry.Registerer
		legacyregistry.Registerer = func() prometheus.Registerer {
			return &noopRegisterer{}
		}
		defer func() { legacyregistry.Registerer = oldRegistererFunc }()
	}

	p := build()
	awsMetricsRegisteredOnce = true
	return p
}

func newMultiRegionCloudProvider(providers []regionalProvider) cloudprovider.CloudProvider {
	if len(providers) == 1 {
		return providers[0].provider
	}

	return &multiRegionCloudProvider{
		providers: providers,
		primary:   providers[0].provider,
	}
}

func regionFromProviderID(providerID string) string {
	if !strings.HasPrefix(providerID, "aws:///") {
		klog.Warningf("Failed to parse AWS region from providerID %q: does not have aws:/// prefix", providerID)
		return ""
	}

	parts := strings.Split(strings.TrimPrefix(providerID, "aws:///"), "/")
	if len(parts) < 2 {
		klog.Warningf("Failed to parse AWS region from providerID %q: no zone info available", providerID)
		return ""
	}

	zone := strings.TrimSpace(parts[0])
	if len(zone) < 2 {
		klog.Warningf("Failed to parse AWS region from providerID %q: zone %q is too short", providerID, zone)
		return ""
	}

	return zone[:len(zone)-1]
}

func (p *multiRegionCloudProvider) providerForNode(node *apiv1.Node) cloudprovider.CloudProvider {
	region := regionFromProviderID(node.Spec.ProviderID)
	if region == "" {
		klog.Warningf("Failed to find provider for node %q: unable to parse region from providerID %q", node.Name, node.Spec.ProviderID)
		return nil
	}

	for _, provider := range p.providers {
		if provider.region == region {
			return provider.provider
		}
	}

	return nil
}

func (p *multiRegionCloudProvider) Name() string {
	return cloudprovider.AwsProviderName
}

func (p *multiRegionCloudProvider) NodeGroups() []cloudprovider.NodeGroup {
	var groups []cloudprovider.NodeGroup
	for _, provider := range p.providers {
		for _, group := range provider.provider.NodeGroups() {
			groups = append(groups, &regionalNodeGroup{
				region: provider.region,
				group:  group,
				log:    klog.Background().WithValues("region", provider.region, "nodegroup", group.Id()),
			})
		}
	}
	return groups
}

func (p *multiRegionCloudProvider) NodeGroupForNode(node *apiv1.Node) (cloudprovider.NodeGroup, error) {
	if provider := p.providerForNode(node); provider != nil {
		group, err := provider.NodeGroupForNode(node)
		if err != nil {
			return nil, err
		}
		if group != nil {
			return wrapRegionalNodeGroup(regionFromProviderID(node.Spec.ProviderID), group), nil
		}
	}

	for _, regional := range p.providers {
		group, err := regional.provider.NodeGroupForNode(node)
		if err != nil {
			return nil, err
		}
		if group != nil {
			return wrapRegionalNodeGroup(regional.region, group), nil
		}
	}

	return nil, nil
}

func (p *multiRegionCloudProvider) HasInstance(node *apiv1.Node) (bool, error) {
	if provider := p.providerForNode(node); provider != nil {
		found, err := provider.HasInstance(node)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}

	for _, regional := range p.providers {
		found, err := regional.provider.HasInstance(node)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}

	return false, nil
}

func (p *multiRegionCloudProvider) Pricing() (cloudprovider.PricingModel, errors.AutoscalerError) {
	return p.primary.Pricing()
}

func (p *multiRegionCloudProvider) GetAvailableMachineTypes() ([]string, error) {
	return p.primary.GetAvailableMachineTypes()
}

// NewNodeGroup builds a theoretical node group based on the node definition
// provided. It is used by the Cluster Autoscaler's Node Auto-Provisioning (NAP)
// feature to dynamically create new node groups.
// Multi-region NAP is not currently supported because the upstream interface
// does not provide enough context (like a target region) to safely delegate
// node group creation without making assumptions.
func (p *multiRegionCloudProvider) NewNodeGroup(
	machineType string,
	labels map[string]string,
	systemLabels map[string]string,
	taints []apiv1.Taint,
	extraResources map[string]resource.Quantity,
) (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}

func (p *multiRegionCloudProvider) GetResourceLimiter() (*cloudprovider.ResourceLimiter, error) {
	return p.primary.GetResourceLimiter()
}

func (p *multiRegionCloudProvider) GPULabel() string {
	return p.primary.GPULabel()
}

func (p *multiRegionCloudProvider) GetAvailableGPUTypes() map[string]struct{} {
	return p.primary.GetAvailableGPUTypes()
}

func (p *multiRegionCloudProvider) GetNodeGpuConfig(node *apiv1.Node) *cloudprovider.GpuConfig {
	if provider := p.providerForNode(node); provider != nil {
		return provider.GetNodeGpuConfig(node)
	}
	return p.primary.GetNodeGpuConfig(node)
}

func (p *multiRegionCloudProvider) Cleanup() error {
	for _, provider := range p.providers {
		provider.log.Info("Cleaning up provider")
		if err := provider.provider.Cleanup(); err != nil {
			provider.log.Error(err, "Failed to clean up provider")
			return err
		}
	}
	return nil
}

func (p *multiRegionCloudProvider) Refresh() error {
	for _, provider := range p.providers {
		provider.log.Info("Refreshing provider")
		if err := provider.provider.Refresh(); err != nil {
			provider.log.Error(err, "Failed to refresh provider")
			return err
		}
	}
	return nil
}

func wrapRegionalNodeGroup(region string, group cloudprovider.NodeGroup) cloudprovider.NodeGroup {
	if group == nil {
		return nil
	}

	if wrapped, ok := group.(*regionalNodeGroup); ok {
		return wrapped
	}

	return &regionalNodeGroup{
		region: region,
		group:  group,
		log:    klog.Background().WithValues("region", region, "nodegroup", group.Id()),
	}
}

func (g *regionalNodeGroup) MaxSize() int {
	return g.group.MaxSize()
}

func (g *regionalNodeGroup) MinSize() int {
	return g.group.MinSize()
}

func (g *regionalNodeGroup) TargetSize() (int, error) {
	return g.group.TargetSize()
}

func (g *regionalNodeGroup) IncreaseSize(delta int) error {
	g.log.Info("Increasing size", "delta", delta)
	err := g.group.IncreaseSize(delta)
	if err != nil {
		g.log.Error(err, "Failed to increase size", "delta", delta)
	}
	return err
}

func (g *regionalNodeGroup) AtomicIncreaseSize(delta int) error {
	g.log.Info("Atomic increasing size", "delta", delta)
	err := g.group.AtomicIncreaseSize(delta)
	if err != nil {
		g.log.Error(err, "Failed to atomic increase size", "delta", delta)
	}
	return err
}

func (g *regionalNodeGroup) DeleteNodes(nodes []*apiv1.Node) error {
	g.log.Info("Deleting nodes", "count", len(nodes))
	err := g.group.DeleteNodes(nodes)
	if err != nil {
		g.log.Error(err, "Failed to delete nodes", "count", len(nodes))
	}
	return err
}

func (g *regionalNodeGroup) ForceDeleteNodes(nodes []*apiv1.Node) error {
	g.log.Info("Force deleting nodes", "count", len(nodes))
	err := g.group.ForceDeleteNodes(nodes)
	if err != nil {
		g.log.Error(err, "Failed to force delete nodes", "count", len(nodes))
	}
	return err
}

func (g *regionalNodeGroup) DecreaseTargetSize(delta int) error {
	g.log.Info("Decreasing target size", "delta", delta)
	err := g.group.DecreaseTargetSize(delta)
	if err != nil {
		g.log.Error(err, "Failed to decrease target size", "delta", delta)
	}
	return err
}

func (g *regionalNodeGroup) Id() string {
	return fmt.Sprintf("%s/%s", g.region, g.group.Id())
}

func (g *regionalNodeGroup) Debug() string {
	return fmt.Sprintf("%s [%s]", g.group.Debug(), g.region)
}

func (g *regionalNodeGroup) Nodes() ([]cloudprovider.Instance, error) {
	return g.group.Nodes()
}

func (g *regionalNodeGroup) TemplateNodeInfo() (*framework.NodeInfo, error) {
	return g.group.TemplateNodeInfo()
}

func (g *regionalNodeGroup) Exist() bool {
	return g.group.Exist()
}

// Create provisions the NodeGroup on the cloud provider. It is the second
// phase of the Cluster Autoscaler's Node Auto-Provisioning (NAP) feature
// after NewNodeGroup. Since we do not currently support multi-region NAP,
// we return ErrNotImplemented here as well.
func (g *regionalNodeGroup) Create() (cloudprovider.NodeGroup, error) {
	g.log.Info("Create nodegroup called but not implemented")
	return nil, cloudprovider.ErrNotImplemented
}

func (g *regionalNodeGroup) Delete() error {
	g.log.Info("Deleting nodegroup")
	err := g.group.Delete()
	if err != nil {
		g.log.Error(err, "Failed to delete nodegroup")
	}
	return err
}

func (g *regionalNodeGroup) Autoprovisioned() bool {
	return g.group.Autoprovisioned()
}

func (g *regionalNodeGroup) GetOptions(defaults config.NodeGroupAutoscalingOptions) (*config.NodeGroupAutoscalingOptions, error) {
	return g.group.GetOptions(defaults)
}

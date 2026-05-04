# cluster-autoscaler-provider

Out-of-tree AWS cluster-autoscaler cloudprovider service built around the upstream external-gRPC wrapper.

## Design

This repository builds a single binary in `cmd/cluster-autoscaler-provider` with two execution modes:

- `provider` mode wraps one regional upstream AWS cloudprovider instance behind the external-gRPC service
- `router` mode fronts multiple provider instances and routes requests to the appropriate region

The current design goal is to preserve the upstream AWS provider implementation as-is for normal regional behavior and keep repository-specific logic in the router and integration layer. That separation is intentional: we want multi-region behavior without carrying an invasive fork of the upstream AWS provider code.

EKS support is available because the provider mode delegates to the upstream AWS provider, but EKS-specific behavior is not the main design target for this repository. The priority is a conservative multi-region deployment model that remains compatible with upstream AWS provider behavior.

## Scope

This repository is responsible for:

1. Keep the upstream AWS provider behavior intact as the starting point.
2. Provide a router mode that aggregates multiple regional provider instances behind one external cloudprovider endpoint.
3. Keep repository-specific multi-region logic outside the upstream provider implementation.
4. Fork only the upstream AWS provider code that must actually diverge.

## Build

From the repository root:

```bash
nix develop
go build ./cmd/cluster-autoscaler-provider
```

This module depends on the upstream `k8s.io/autoscaler/cluster-autoscaler` module and documents the pinned autoscaler tag in `go.mod`.

## Deployment model

The deployment model is a single binary with explicit `router` and `provider` modes backed by a required shared runtime config file.

- Router instances read the shared config file to determine which provider backends should be available.
- Provider instances read the same shared config file and select their own regional configuration from it.
- `--config` or `CLUSTER_AUTOSCALER_PROVIDER_CONFIG` is required for both modes.
- Provider-specific AWS settings such as `--cloud-config`, cluster name, and node group discovery remain pass-through inputs to the upstream AWS provider.

## Runtime config

The shared runtime config file is the source of truth for router/provider topology.

- `router` defines shared router settings such as listen addresses and default backend RPC behavior.
- `providerDefaults` defines provider settings shared across regions.
- `providers` is keyed by region and defines the port plus any per-region overrides.
- router mode reads all configured regions from `providers` and derives its backend list from that map.
- provider mode takes a single `region` selector, reads the same file, and resolves its runtime settings from the matching region entry.

A region-keyed config looks like this:

```yaml
router:
  grpcAddress: :8086
  httpAddress: :8080
  cacheTTL: 15s
  backendRPCTimeout: 5s
providerDefaults:
  clusterName: dev
  nodeGroupAutoDiscovery:
    - asg:tag=k8s.io/cluster-autoscaler/dev
  skipNodesWithLocalStorage: false
  skipNodesWithSystemPods: false
providers:
  us-east-1:
    port: 8081
    rpcTimeout: 5s
  us-west-2:
    port: 8082
    clusterName: dev-west
    skipNodesWithSystemPods: true
```

Notes:

- Use region as the provider key in `providers`.
- Keep the shared file focused on router/provider topology and light per-region runtime overrides such as port or timeout.
- `providerDefaults` can supply shared values for `clusterName`, `nodeGroupAutoDiscovery`, `skipNodesWithLocalStorage`, and `skipNodesWithSystemPods`.
- Any of those YAML fields can be overridden in a specific region entry when a region must diverge.
- Provider runtime settings come from the shared config file. Provider startup only supplies the region selector used to choose an entry from `providers`.
- AWS-provider-specific settings that are not shared runtime topology should stay outside this file unless they are truly common across provider instances.

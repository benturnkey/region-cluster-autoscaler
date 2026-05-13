# Manifests

This directory contains Kustomize manifests for deploying `cluster-autoscaler-provider`.

## Base

`manifests/base/` defines the common resources:

- `deployment.yaml`: Runs the router process as a single-replica `Deployment`. It exposes gRPC on `8086` and HTTP health/metrics on `8080`.
- `service.yaml`: Exposes the router pod on the gRPC and HTTP ports.
- `serviceaccount.yaml`: Creates the service account used by the deployment. The IAM role annotation is left as a placeholder in the base.
- `servicemonitor.yaml`: Configures Prometheus Operator scraping for the router `/metrics` endpoint.
- `kustomization.yaml`: Assembles the base resources.

The base is intentionally generic. It provides the router deployment shape, but leaves environment-specific image pinning, runtime config, and IAM wiring to overlays.

## Dev Overlay

`manifests/overlays/dev/` is an example environment overlay.

It makes these changes on top of the base:

- Generates a `ConfigMap` from `provider-config.yaml`.
- Patches the deployment to mount that config file into the pods.
- Configures the main container to run in `router` mode using the shared config file.
- Adds two sidecar provider containers, one for `us-east-1` and one for `us-west-2`.
- Patches the service account with a concrete IRSA role ARN for the dev environment.
- Pins the image to a specific published digest.

`provider-config.yaml` shows the shared runtime configuration format:

- `router`: router listener addresses and router behavior like cache TTL and backend RPC timeout.
- `providerDefaults`: shared provider settings such as cluster name and node group auto-discovery.
- `providers`: per-region provider entries, including the local gRPC port each provider container listens on.

This overlay is meant as a working example, not a universal production layout. Other environments can follow the same pattern with their own runtime config, IAM role annotation, image pin, and provider region set.

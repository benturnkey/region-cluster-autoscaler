## Local Upstream Forks

This directory contains local copies of upstream dependencies that require a
small patch for this repository.

### `cluster-autoscaler`

This repository wraps the upstream AWS Cluster Autoscaler provider to support
multiple AWS regions in a single process. The upstream AWS provider assumes a
single provider instance per process and unconditionally registers its
Prometheus metrics during `BuildAWS()`.

That behavior panics in this repository's multi-region mode because the same
collector is registered once for each regional provider:

`panic: duplicate metrics collector registration attempted`

#### Local patch

Patched file:

- `third_party/cluster-autoscaler/cloudprovider/aws/aws_metrics.go`

Patch summary:

- import `sync`
- add `registerMetricsOnce sync.Once`
- wrap `legacyregistry.MustRegister(requestSummary)` in
  `registerMetricsOnce.Do(...)`

This changes AWS metrics registration from "once per provider construction" to
"once per process", which is the correct behavior for this wrapper.

#### How this fork is wired in

The root module uses:

- `replace k8s.io/autoscaler/cluster-autoscaler => ./third_party/cluster-autoscaler`

The OCI build also copies this fork's `go.mod` and `go.sum` before
`go mod download`, because the replacement target must exist during dependency
resolution.

#### Upgrade procedure

When upgrading `k8s.io/autoscaler/cluster-autoscaler`:

1. Update the required upstream version in the root `go.mod`.
2. Refresh `third_party/cluster-autoscaler` from that exact upstream version.
3. Reapply the patch in `cloudprovider/aws/aws_metrics.go`.
4. Run `go build ./...` and the OCI build.
5. Check whether upstream has fixed this behavior. If they have, remove the
   local patch and delete the `replace` directive.

#### Keep the fork small

Do not make unrelated edits in this fork. The goal is to keep the diff against
upstream narrow so upgrades stay cheap and reviewable.

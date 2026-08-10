# Install

> Last verified against: cluster-api-provider-kairos-fleet v0.1.0,
> Cluster API v1.13.4 (v1beta2 contract), release pipeline in
> `.github/workflows/release.yml`.

This registers the Kairos Fleet infrastructure provider with `clusterctl` and
installs it into a management cluster. `kairos-fleet` is not in `clusterctl`'s
built-in provider list, so it needs a one-time `clusterctl.yaml` entry before
`clusterctl init` can find it.

## Prerequisites

- A management cluster running a supported Kubernetes version: v1.32 to v1.36
  (the Cluster API v1.13 range), built and tested against v1.34 to v1.35. See
  the version table in [../README.md](../README.md#target-versions).
- `clusterctl`, matching Cluster API v1.13.
- Cluster API core components and the Kairos bootstrap and control-plane
  providers (from `cluster-api-provider-kairos`), either already installed or
  installed in the same `clusterctl init` call below.
- Network access from your workstation (or wherever `clusterctl init` runs)
  to `github.com`, to fetch the release assets, or a local/air-gapped
  clusterctl override repository if you mirror releases internally.

## 1. Register the provider with clusterctl

Add a `providers` entry to your `clusterctl.yaml` (default location
`~/.cluster-api/clusterctl.yaml`), pointing at this repository's release
assets:

```yaml
providers:
  - name: "kairos-fleet"
    url: "https://github.com/kairos-io/cluster-api-provider-kairos-fleet/releases/latest/download/infrastructure-components.yaml"
    type: "InfrastructureProvider"
```

Each tagged release (`release.yml`) publishes three assets clusterctl expects
alongside each other in the same release: `infrastructure-components.yaml`
(the CRDs, RBAC, and controller Deployment, with the image pinned by digest),
`metadata.yaml` (the clusterctl contract and version-series mapping), and
`cluster-template.yaml` (the default `clusterctl generate cluster` template).
Point `url` at a specific tag instead of `latest` to pin a version, for
example `.../releases/download/v0.1.0/infrastructure-components.yaml`. Do not
pin `v0.1.0-beta.1`: it is superseded and is not installable through
`clusterctl init` at all (see
[docs/release-notes/v0.1.0.md](release-notes/v0.1.0.md)).

## 2. Initialize the management cluster

```bash
clusterctl init \
  --infrastructure kairos-fleet \
  --bootstrap kairos \
  --control-plane kairos
```

This installs the Cluster API core provider (if not already present), this
infrastructure provider, and the Kairos bootstrap and control-plane
providers. The `kairos` short name above assumes the sibling providers from
`cluster-api-provider-kairos` are registered under that name in your
`clusterctl.yaml`; if you registered them under a different name, use that
name instead, and consult that repository's own installation docs for its
`clusterctl.yaml` entry.

## 3. Verify the deployment

```bash
kubectl get pods -n capi-kairos-fleet-system
kubectl get crds | grep kairosfleet
```

Expect a `capi-kairos-fleet-controller-manager` Deployment
with one available replica, and four CRDs: `kairosfleetclusters`,
`kairosfleetclustertemplates`, `kairosfleetmachines`, and
`kairosfleetmachinetemplates`.

```bash
kubectl get deployment -n capi-kairos-fleet-system \
  capi-kairos-fleet-controller-manager
```

## Uninstall

```bash
clusterctl delete --infrastructure kairos-fleet
```

This does not release AuroraBoot nodes claimed by existing
KairosFleetMachines: delete the Clusters first (see
[QUICKSTART.md](QUICKSTART.md#6-tear-down)) so their nodes are released, then
uninstall the provider.

## Next steps

[QUICKSTART.md](QUICKSTART.md) provisions a workload cluster end to end.

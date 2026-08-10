# Cluster API Provider Kairos Fleet

A [Cluster API](https://cluster-api.sigs.k8s.io/) infrastructure provider that
provisions machines from a [Kairos](https://kairos.io) fleet managed by
[AuroraBoot](https://github.com/kairos-io/AuroraBoot).

Where a cloud infrastructure provider talks to an IaaS API to create VMs, this
provider talks to the AuroraBoot fleet API: it claims an already-enrolled Kairos
node from a group, hands it the machine's bootstrap cloud-config, waits for the
node to reboot and rejoin, tracks its reported addresses, and releases the node
when the machine is deleted. It targets bare metal and pre-provisioned nodes
that phone home to AuroraBoot.

## Status

Stable, v1alpha1 API. First usable release: v0.1.0.

`v0.1.0-beta.1` is superseded and must not be used: its generated metrics
Service name exceeded Kubernetes' 63-character limit, so `clusterctl init`
could not apply it, and even a manually patched install crash-looped on a
real management cluster because the manager's scheme never registered the
Cluster API core types. v0.1.0 fixes both, plus a group-claim bug where
`spec.group` names were passed straight to AuroraBoot's UUID-keyed claim
endpoint and always failed. See
[docs/release-notes/v0.1.0.md](docs/release-notes/v0.1.0.md) for the full
list of fixes.

The exercised topology is a single control-plane machine plus a
`MachineDeployment` of worker machines, claimed from named AuroraBoot groups.
It has been validated end to end on real hardware for both k3s and k0s
workload clusters, together with the [Kairos bootstrap and control-plane
providers](https://github.com/kairos-io/cluster-api-provider-kairos) at
v0.1.0. High-availability control planes (`replicas` greater than 1) are out
of scope for this provider. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
for the full design and current limitations, and
[docs/QUICKSTART.md](docs/QUICKSTART.md) to provision a cluster.

## API types

`infrastructure.cluster.x-k8s.io`:

- `KairosFleetCluster` / `KairosFleetClusterTemplate`: the infrastructure side
  of a CAPI `Cluster`.
- `KairosFleetMachine` / `KairosFleetMachineTemplate`: the infrastructure side
  of a CAPI `Machine`; each one is backed by a claimed AuroraBoot node.

## How it fits

```
Cluster API (Machine)
  -> KairosFleetMachine (this provider)
    -> AuroraBoot fleet API (claim, apply-cloud-config, reboot, release)
      -> Kairos node (claimed from a group)
```

A fleet cluster composes three Cluster API providers, installed together with
`clusterctl init`:

- this infrastructure provider (`kairos-fleet`), which claims and configures
  nodes;
- the [Kairos bootstrap provider](https://github.com/kairos-io/cluster-api-provider-kairos),
  which renders the cloud-config each machine applies;
- the Kairos control-plane provider (same repository), which manages the
  control-plane Machines.

## Target versions

| Component | Version |
| --- | --- |
| Cluster API | v1.13.x (v1beta2 contract) |
| Kubernetes, management cluster | Supported v1.32 to v1.36 (the Cluster API v1.13 range). Built and tested against v1.34 to v1.35. |
| Kubernetes, workload cluster | Chosen per cluster. Supported range v1.30 to v1.36 (Cluster API v1.13), further bounded by the k3s or k0s version the chosen Kairos image ships. |
| Kairos | v4.1.2 (the v4.x line) |
| Distribution | k3s and k0s. Both have fleet providerID self-discovery; k3s is the reference path and the most exercised (see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#providerid)). |
| AuroraBoot | Fleet API (`/api/v1`): group claim, node get, apply-cloud-config, reboot, release. |
| Provider API | v1alpha1 (`infrastructure.cluster.x-k8s.io`) |

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full version
rationale and known limitations.

## Getting started

- [docs/INSTALL.md](docs/INSTALL.md): register the provider with `clusterctl`
  and verify the deployment.
- [docs/QUICKSTART.md](docs/QUICKSTART.md): provision a workload cluster end
  to end.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): the fleet model, the CRDs, the
  machine lifecycle, and current limitations.
- [docs/API.md](docs/API.md): field reference for the four API kinds.

## Development

Standard Cluster API provider layout (kubebuilder). Build and test targets
live in the `Makefile`. See [CONTRIBUTING.md](CONTRIBUTING.md) for the
development workflow, tests, and commit policy.

## License

Apache License 2.0. See [LICENSE](LICENSE).

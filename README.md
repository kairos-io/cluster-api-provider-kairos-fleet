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

> **Found a bug, or want to request a feature?** Open it on
> [kairos-io/kairos](https://github.com/kairos-io/kairos/issues), including
> issues about this repository. Every Kairos issue lives in one place, so you
> never have to work out which repository to file against.

## Status

**Latest release:**
[`v0.1.4`](https://github.com/kairos-io/cluster-api-provider-kairos-fleet/releases/tag/v0.1.4).
Pre-1.0: the `v1alpha1` API may still change before v1.0. Read the
[v0.1.4 release notes](docs/release-notes/v0.1.4.md) before installing or
upgrading. Do not use `v0.1.0-beta.1`, which cannot be installed with
`clusterctl`; v0.1.0 was the first usable release.

The exercised topology is a single control-plane machine plus a
`MachineDeployment` of worker machines, claimed from named AuroraBoot groups.
It has been validated end to end on real hardware for both k3s and k0s
workload clusters, together with the [Kairos bootstrap and control-plane
providers](https://github.com/kairos-io/cluster-api-provider-kairos) at
v0.1.3. That validation predates this provider's v0.1.4, whose fixes are
covered by the unit and envtest suites rather than a new hardware run. This
repository records no hardware validation with a later release of the Kairos
bootstrap and control-plane providers. Nothing in this provider limits the
number of control-plane machines, but a control plane of more than one
Machine has not been validated against a fleet.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full design and
current limitations, and [docs/QUICKSTART.md](docs/QUICKSTART.md) to
provision a cluster.

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
    -> AuroraBoot fleet API
       (list groups, claim, apply-cloud-config, reboot,
        get node, list commands, release)
      -> Kairos node (claimed from a group)
```

The `KairosFleetCluster` controller does not call AuroraBoot; it only checks
that the connection settings are complete.

A fleet cluster composes three Cluster API providers, installed together with
`clusterctl init`:

- this infrastructure provider (`kairos-io-fleet`), which claims and
  configures nodes;
- the [Kairos bootstrap provider](https://github.com/kairos-io/cluster-api-provider-kairos)
  (`kairos-io`), which produces the cloud-config this provider applies to each
  node;
- the Kairos control-plane provider (`kairos-io`, same repository), which
  manages the control-plane Machines.

The names in parentheses are the `clusterctl` provider names. See
[docs/INSTALL.md](docs/INSTALL.md).

## Target versions

| Component | Version |
| --- | --- |
| Cluster API | v1.13.x (v1beta2 contract) |
| Kubernetes, management cluster | v1.32 to v1.36, the Cluster API v1.13 range (v1.36 needs Cluster API v1.13.1 or later). CI runs the envtest suite against v1.35; [CONTRIBUTING.md](CONTRIBUTING.md#tests) lists what each test covers. |
| Kubernetes, workload cluster | Chosen per cluster. Supported range v1.30 to v1.36 (Cluster API v1.13), further bounded by the k3s or k0s version the chosen Kairos image ships. |
| Kairos | v4.1.2, the version validated on hardware |
| Distribution | k3s and k0s. Both were validated on real hardware; k3s is the reference path and the most exercised. The distribution is chosen in the Kairos bootstrap provider's resources, not in this provider's. |
| AuroraBoot | v0.27.0 or later, the first release with the group claim and release endpoints this provider calls. It uses the fleet API (`/api/v1`): group list and claim, node get, the `apply-cloud-config` and `reboot` node commands and their outcome, and release. |
| Provider API | v1alpha1 (`infrastructure.cluster.x-k8s.io`) |

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full version
rationale and known limitations.

## Install

The provider's `clusterctl` name is `kairos-io-fleet`. `clusterctl` v1.15
knows it built in; with `clusterctl` v1.13 or v1.14, add the one-time
`clusterctl.yaml` entry from [docs/INSTALL.md](docs/INSTALL.md) first. Then:

```bash
clusterctl init \
  --infrastructure kairos-io-fleet \
  --bootstrap kairos-io \
  --control-plane kairos-io
```

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

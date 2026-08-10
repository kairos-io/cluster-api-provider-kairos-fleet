# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/).
Entries are grouped by Added, Changed, Fixed, Security, and Breaking as
needed; see [docs/release-notes/](docs/release-notes/) for the fuller
narrative behind each release.

## [v0.1.0] - 2026-08-10

First stable release. `v0.1.0-beta.1` is superseded and must not be used.

### Fixed

- The manager crash-looped on any real management cluster: the Cluster API
  core `v1beta2` types were never registered on the controller-manager's
  scheme, so watching `Cluster`/`Machine` failed at startup.
- The provider was not installable through `clusterctl init`: the generated
  metrics Service name exceeded Kubernetes' 63-character limit.
- Claiming a node by `spec.group` name always failed with a 404: the
  controller now resolves a group name (or ID) to AuroraBoot's group ID
  before claiming (`Client.ResolveGroupID`); an unresolved group name
  requeues as a transient `GroupNotFound`, not a terminal failure.

### Changed

- Provider namespace and resource name prefix changed from
  `cluster-api-provider-kairos-fleet-` to `capi-kairos-fleet-` (namespace
  `capi-kairos-fleet-system`), aligning with the sibling
  `cluster-api-provider-kairos` bootstrap and control-plane providers.
- Documented and validated the control-plane-plus-worker topology end to end
  on real hardware for both k0s and k3s, alongside
  `cluster-api-provider-kairos` v0.1.0.

## [v0.1.0-beta.1] - 2026-08-09

Initial release.

### Added

- `KairosFleetCluster` and `KairosFleetMachine` (plus their `*Template`
  kinds), satisfying the Cluster API v1beta2 infrastructure contract.
- The full claim, apply-cloud-config, reboot, rejoin, release lifecycle
  against a real AuroraBoot fleet API.
- `providerID` self-discovery for k3s, deriving `kairos-fleet://<node-id>`
  from the Kairos phone-home agent's persisted credentials.
- `clusterctl` installability: `metadata.yaml`, a default
  `cluster-template.yaml`, and a release pipeline publishing
  `infrastructure-components.yaml` with the controller image pinned by
  digest, signed with cosign, and accompanied by an SBOM.

[v0.1.0]: https://github.com/kairos-io/cluster-api-provider-kairos-fleet/releases/tag/v0.1.0
[v0.1.0-beta.1]: https://github.com/kairos-io/cluster-api-provider-kairos-fleet/releases/tag/v0.1.0-beta.1

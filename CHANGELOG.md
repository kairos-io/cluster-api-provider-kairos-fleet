# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/).
Entries are grouped by Added, Changed, Fixed, Security, and Breaking as
needed; see [docs/release-notes/](docs/release-notes/) for the fuller
narrative behind each release.

## [v0.1.2] - 2026-09-09

A fix release. Four defects found by running the provider against a live
AuroraBoot fleet on real hardware, for both k3s and k0s. No CRD or API change;
no migration steps from v0.1.1. See the
[v0.1.2 release notes](docs/release-notes/v0.1.2.md).

### Fixed

- A rejected `apply-cloud-config` stranded a machine permanently. Two faults
  combined: `applyState` returned the phase of the *first* `apply-cloud-config`
  in a node's command history, which AuroraBoot never prunes, so one early
  failure shadowed every later success; and the failure branch set a terminal
  `failureReason`/`failureMessage` and returned with no requeue and nothing to
  re-trigger it. The controller now records the ID of the command it queued and
  reads the outcome back from that command, and a failed apply is a
  non-terminal `CloudConfigFailed` `Ready` condition that is retried with a
  fresh command about once a minute. A machine left terminally failed by an
  earlier build clears on its first pass through this path.
- The worker `KairosConfigTemplate` in `templates/cluster-template.yaml`
  carried neither a role nor a join token, so the Kairos bootstrap webhook
  rejected every clone (`spec.k3sToken: Required value`) and the
  `MachineDeployment` could never create a Machine — a cluster rendered from
  the template came up with a control plane and no workers, reporting only
  `ScalingUp`. The template now sets `role: worker` and references a join-token
  Secret named by the new `K3S_TOKEN_SECRET_NAME` variable.

### Added

- `K3S_TOKEN_SECRET_NAME` cluster template variable, defaulting to
  `${CLUSTER_NAME}-k3s-token`, naming the Secret (key `token`) that holds the
  k3s server join token for worker machines. The Secret is not created
  automatically: its value comes from `/var/lib/rancher/k3s/server/token` on
  the running control-plane node.
- `kairos-fleet.infrastructure.cluster.x-k8s.io/cloud-config-command-id`
  annotation, recording the `apply-cloud-config` command the controller
  queued.

### Security

- `golang.org/x/net` and the Go standard library in the published image are
  bumped past their fixable HIGH CVEs. The pinned `golang:1.26-bookworm`
  builder digest had gone stale at Go 1.26.5, carrying eight stdlib CVEs fixed
  in 1.26.6; the digest is re-resolved to the tag's current 1.26.8. Indirect
  `google.golang.org/grpc` v1.82.1 to v1.83.2 fixes CVE-2026-84304 and
  CVE-2026-84445. The image scan now reports zero fixable CRITICAL or HIGH
  findings.

### Documentation

- The AuroraBoot `phonehome.allowed_commands` prerequisite is documented. This
  provider issues `apply-cloud-config`, `reboot` and `reset`, but AuroraBoot's
  install-agent enrolment script defaults to a narrower policy that excludes
  `apply-cloud-config` and `reset`, so a node enrolled the documented way fails
  its very first bootstrap apply. Both ways to enrol correctly, and the repair
  for an already-enrolled node, are in the quickstart and install guide.
- The worker join-token Secret has its own quickstart step, with the k0s
  equivalent (`workerTokenSecretRef` from `k0s token create --role worker`).
- Corrected the claim that a failed `apply-cloud-config` is terminal, and
  documented the `CloudConfigFailed` and `NodeMissing` condition reasons and
  the new annotation.

## [v0.1.1] - 2026-08-10

Documented retrospectively: v0.1.1 shipped without a changelog entry or
release notes. A single-line packaging change; no controller, CRD, RBAC or
image change, and no migration steps from v0.1.0.

### Changed

- The clusterctl provider identity is now `kairos-io-fleet`: the
  `cluster.x-k8s.io/provider` label on the clusterctl components changed from
  `infrastructure-kairos-fleet` to `infrastructure-kairos-io-fleet`, ahead of
  registering the provider in the upstream clusterctl built-in list under the
  `kairos-io` organisation name. Applied with `includeSelectors: false`, so
  Deployment and Service selectors are unchanged.

  This label is what `clusterctl move` and `clusterctl delete` key off. A
  management cluster initialised under v0.1.0 carries the old label on its
  existing objects; re-running `clusterctl init` (or `clusterctl upgrade`)
  restamps them.

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

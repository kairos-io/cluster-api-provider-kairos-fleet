# API reference

> Last verified against: cluster-api-provider-kairos-fleet commit `23b1416`,
> API group `infrastructure.cluster.x-k8s.io/v1alpha1`.

A hand-written field reference for the four Kairos Fleet API kinds. For the
generated schema (types, validation, defaults), use `kubectl explain` against
a cluster with the CRDs installed, for example `kubectl explain
kairosfleetmachine.spec`, or read `api/v1alpha1/*_types.go` directly.

## KairosFleetCluster

The infrastructure side of a Cluster API `Cluster`.

### spec

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `controlPlaneEndpoint.host` | string | Only if `controlPlaneEndpoint` is set | Host of the workload cluster's API server. Operator-supplied; this provider does not allocate it. |
| `controlPlaneEndpoint.port` | int32 | Only if `controlPlaneEndpoint` is set | Port of the workload cluster's API server. |
| `auroraboot.url` | string | Yes | Base URL of the AuroraBoot fleet API. |
| `auroraboot.adminTokenSecretRef.name` | string | Yes | Name of a Secret in the same namespace holding the AuroraBoot admin bearer token under its `token` data key. |

### status

| Field | Type | Description |
| --- | --- | --- |
| `initialization.provisioned` | bool | The Cluster API v1beta2 InfraCluster readiness signal. True once `spec.controlPlaneEndpoint.host` is set and the AuroraBoot connection is valid. |
| `conditions` | `[]metav1.Condition` | Includes a `Ready` condition describing the current wait state or failure reason. |
| `failureReason` / `failureMessage` | string | Set only on a terminal, unrecoverable failure. |

## KairosFleetMachine

The infrastructure side of a Cluster API `Machine`, backed by a claimed
AuroraBoot node.

### spec

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `group` | string | Yes | AuroraBoot group to claim a node from. Must already exist and hold unclaimed, enrolled nodes. |
| `providerID` | string | No, set by the controller | `kairos-fleet://<node-id>` once a node is claimed and confirmed rejoined. Immutable once set; do not set it yourself. |

### status

| Field | Type | Description |
| --- | --- | --- |
| `initialization.provisioned` | bool | The Cluster API v1beta2 InfraMachine readiness signal. True once the node is claimed, its cloud-config applied, and it has rejoined `Online`. |
| `addresses` | `[]clusterv1.MachineAddress` | A single `Hostname` address in v0.1 (see [ARCHITECTURE.md](ARCHITECTURE.md)). |
| `conditions` | `[]metav1.Condition` | Includes a `Ready` condition; see [QUICKSTART.md](QUICKSTART.md) for the reason values it cycles through. |
| `failureReason` / `failureMessage` | string | Set only on a terminal, unrecoverable failure (a failed apply-cloud-config command, or a claimed node that disappears from AuroraBoot). |

### Annotations the controller manages

These are set by the controller, not the user:

| Annotation | Purpose |
| --- | --- |
| `kairos-fleet.infrastructure.cluster.x-k8s.io/node-id` | The claimed AuroraBoot node's ID. Source of `spec.providerID` and the release call on delete. |
| `kairos-fleet.infrastructure.cluster.x-k8s.io/cloud-config-applied` | Marks that the bootstrap cloud-config has been handed to AuroraBoot, so it is not re-applied on every reconcile. |
| `kairos-fleet.infrastructure.cluster.x-k8s.io/reboot-requested-at` | RFC 3339 timestamp of the reboot the controller requested to apply the staged config; used to detect rejoin. |

## KairosFleetClusterTemplate

Wraps `KairosFleetClusterSpec` for `ClusterClass` use.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `template.metadata` | `clusterv1.ObjectMeta` | No | Metadata stamped onto each created `KairosFleetCluster`. |
| `template.spec` | `KairosFleetClusterSpec` | Yes | Same shape as `KairosFleetCluster.spec`, above. |

## KairosFleetMachineTemplate

Wraps `KairosFleetMachineSpec` for `MachineDeployment`/`MachineSet` (and
`ClusterClass`) use.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `template.metadata` | `clusterv1.ObjectMeta` | No | Metadata stamped onto each created `KairosFleetMachine`. |
| `template.spec` | `KairosFleetMachineSpec` | Yes | Same shape as `KairosFleetMachine.spec`, above. A `MachineDeployment` of N replicas referencing this template claims N nodes from `template.spec.group`. |

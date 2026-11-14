# API reference

> Last verified against: cluster-api-provider-kairos-fleet v0.1.4 (CRDs in
> `config/crd/bases/` and controllers at the v0.1.4 tag), API group
> `infrastructure.cluster.x-k8s.io/v1alpha1`.

A hand-written field reference for the four Kairos Fleet API kinds. For the
generated schema (types, validation, defaults), use `kubectl explain` against
a cluster with the CRDs installed, for example `kubectl explain
kairosfleetmachine.spec`, or read `api/v1alpha1/*_types.go` directly.

## KairosFleetCluster

The infrastructure side of a Cluster API `Cluster`.

### spec

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `controlPlaneEndpoint` | object | No | Endpoint of the workload cluster's API server. Operator-supplied; this provider does not allocate it. The object has no required fields in the schema, but the `KairosFleetCluster` does not become provisioned until `host` is set. |
| `controlPlaneEndpoint.host` | string | No (needed for provisioned) | Host of the workload cluster's API server. |
| `controlPlaneEndpoint.port` | int32 | No | Port of the workload cluster's API server, 1 to 65535. The default template sets 6443. |
| `auroraboot.url` | string | Yes | Base URL of the AuroraBoot fleet API. |
| `auroraboot.adminTokenSecretRef.name` | string | Yes | Name of a Secret in the same namespace holding the AuroraBoot admin bearer token under its `token` data key. |

### status

| Field | Type | Description |
| --- | --- | --- |
| `initialization.provisioned` | bool | The Cluster API v1beta2 InfraCluster readiness signal. True once the AuroraBoot connection is complete (`spec.auroraboot.url` is set and the admin-token Secret exists with a non-empty `token`) and `spec.controlPlaneEndpoint.host` is set. The controller does not call AuroraBoot to check the connection. |
| `conditions` | `[]metav1.Condition` | Includes a `Ready` condition with the reason `AuroraBootConnectionNotReady`, `WaitingForControlPlaneEndpoint` or `Provisioned`. |
| `failureReason` / `failureMessage` | string | Reserved for a terminal failure. The controller does not set them on a `KairosFleetCluster`: every state it reports can be fixed. |

## KairosFleetMachine

The infrastructure side of a Cluster API `Machine`, backed by a claimed
AuroraBoot node.

### spec

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `group` | string | Yes | AuroraBoot group to claim a node from. Accepts either the group's name or its ID; the controller resolves it to the ID before claiming. Must already exist and hold unclaimed, enrolled nodes; a name that does not (yet) resolve is a transient `GroupNotFound` wait, not a terminal failure. Only read while no node is claimed; changing it afterwards does not move the machine. |
| `providerID` | string | No, set by the controller | `kairos-fleet://<node-id>` once a node is claimed and confirmed rejoined. Do not set it yourself: the controller writes it, and the CRD does not enforce immutability or reject a user-supplied value. |

### status

| Field | Type | Description |
| --- | --- | --- |
| `initialization.provisioned` | bool | The Cluster API v1beta2 InfraMachine readiness signal. True once the node is claimed, its cloud-config applied, and it has rejoined `Online`. |
| `addresses` | `[]clusterv1.MachineAddress` | The node's hostname, followed by the addresses the node reported to AuroraBoot, in the order it reported them (see [ARCHITECTURE.md](ARCHITECTURE.md)). An agent that reports no addresses leaves only the hostname. |
| `conditions` | `[]metav1.Condition` | Includes a `Ready` condition; see [QUICKSTART.md](QUICKSTART.md) for the reason values it cycles through. |
| `failureReason` / `failureMessage` | string | Set only on a terminal, unrecoverable failure: a claimed node that disappears from AuroraBoot entirely. A failed or expired `apply-cloud-config` or `reboot` command is not terminal; it sets a `CloudConfigFailed` or `RebootFailed` `Ready` condition and is retried automatically (see [QUICKSTART.md](QUICKSTART.md)). |

### Annotations the controller manages

These are set by the controller, not the user:

| Annotation | Purpose |
| --- | --- |
| `kairos-fleet.infrastructure.cluster.x-k8s.io/node-id` | The claimed AuroraBoot node's ID. Source of `spec.providerID` and the release call on delete. |
| `kairos-fleet.infrastructure.cluster.x-k8s.io/claim-key` | The key the node was claimed with: the KairosFleetMachine's UID at claim time. The release on delete presents this key, not the current UID, because `clusterctl move` and a backup and restore recreate the object with a new UID and AuroraBoot refuses a release with any other key. A machine claimed before this annotation existed falls back to its UID. |
| `kairos-fleet.infrastructure.cluster.x-k8s.io/cloud-config-applied` | Marks that the bootstrap cloud-config has been handed to AuroraBoot, so it is not re-applied on every reconcile. Cleared automatically if the apply-cloud-config command later reports `Failed` or `Expired`, so a fixed node gets a fresh apply on the next reconcile. |
| `kairos-fleet.infrastructure.cluster.x-k8s.io/cloud-config-command-id` | The ID of the apply-cloud-config command the controller queued. Its outcome is read back from this exact command, not the first apply-cloud-config found for the node: AuroraBoot never prunes a node's command history, so an earlier failed attempt would otherwise shadow every later success. Cleared alongside `cloud-config-applied` on a `Failed` or `Expired` outcome. |
| `kairos-fleet.infrastructure.cluster.x-k8s.io/reboot-requested-at` | RFC 3339 timestamp of the reboot the controller requested to apply the staged config; used to detect rejoin. Cleared, with `reboot-command-id`, if that reboot reports `Failed` or `Expired`, so a fresh one is issued. |
| `kairos-fleet.infrastructure.cluster.x-k8s.io/reboot-command-id` | The ID of the reboot command the controller queued, so its outcome is read back from that exact command and an earlier failed reboot of the same node cannot shadow it. Same reasoning as `cloud-config-command-id`. |
| `kairos-fleet.infrastructure.cluster.x-k8s.io/retry-not-before` | RFC 3339 timestamp before which the controller will not queue a fresh `apply-cloud-config` or `reboot` after the node reported the previous one `Failed` or `Expired`. It paces those retries to about once a minute. It has to be recorded on the object, because clearing the command markers to allow the retry triggers an immediate reconcile. Removed when the fresh command is queued. |

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

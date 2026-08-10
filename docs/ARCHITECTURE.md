# Architecture

> Last verified against: cluster-api-provider-kairos-fleet v0.1.0,
> Cluster API v1.13.4 (v1beta2 contract), Kairos v4.1.2.

This is the public overview of how the Kairos Fleet infrastructure provider
works: the fleet model, the four CRDs, the machine lifecycle, the providerID
contract, and the current (v0.1) limitations.

## The fleet model versus a cloud infrastructure provider

A cloud infrastructure provider (for example an AWS or vSphere provider) talks
to an IaaS API to create a VM on demand. This provider does not create
anything: it talks to the AuroraBoot fleet API to claim an already-enrolled
Kairos node from a named group, hand it a machine's bootstrap cloud-config,
and release it back to the pool when the machine is deleted. "No capacity in a
group" is an expected, transient state, not a failure: it means wait for more
nodes to be enrolled or claimed nodes to be released, not that provisioning is
broken.

This makes the provider a good fit for bare metal and pre-provisioned edge
nodes that phone home to AuroraBoot, and a poor fit for anything that expects
on-demand elastic capacity: capacity is whatever is enrolled and unclaimed in
a group at claim time.

## The four CRDs

All four live in the `infrastructure.cluster.x-k8s.io/v1alpha1` API group.

- **`KairosFleetCluster`**: the infrastructure side of a Cluster API
  `Cluster`. Holds the operator-supplied control-plane endpoint and the
  AuroraBoot connection (URL and a reference to a Secret holding the admin
  bearer token) shared by every machine in the cluster.
- **`KairosFleetMachine`**: the infrastructure side of a Cluster API
  `Machine`. Holds the target AuroraBoot group to claim a node from, and (once
  claimed) the resulting `providerID`.
- **`KairosFleetClusterTemplate`** / **`KairosFleetMachineTemplate`**: wrap
  the corresponding spec under `spec.template.spec`, for `ClusterClass` and
  `MachineDeployment`/`MachineSet` use.

Both `KairosFleetCluster` and `KairosFleetMachine` satisfy the Cluster API
v1beta2 infrastructure contract: readiness is
`status.initialization.provisioned` (not the v1beta1 `status.ready`), and both
carry `status.conditions`, `status.failureReason`, and
`status.failureMessage`.

The claimed node's AuroraBoot node ID is recorded on the `KairosFleetMachine`
as the annotation `kairos-fleet.infrastructure.cluster.x-k8s.io/node-id`. It
is an annotation rather than spec because the user does not choose it, and
rather than a status field because it must survive independently of status
rebuilds and is operationally useful to see directly.

## The KairosFleetMachine lifecycle

The `KairosFleetMachine` controller is a level-triggered state machine, safe
to re-run from any point:

| Step | Condition to advance | What happens |
| --- | --- | --- |
| Wait for bootstrap | `Machine.spec.bootstrap.dataSecretName` is not yet set | Requeue; `WaitingForBootstrapData`. |
| Claim | No `node-id` annotation yet | Resolve `spec.group` (a name or an ID) to the group's ID, then claim a node from it using the KairosFleetMachine's UID as a stable claim key, so a retried reconcile finds the same node instead of claiming a second one. A group that does not resolve requeues as `GroupNotFound`; no capacity in the resolved group requeues as `WaitingForCapacity`. Neither is an error. |
| Apply cloud-config | Node claimed, config not yet applied | Fetch the bootstrap Secret's `value` key and hand it to AuroraBoot unmodified as an apply-cloud-config command. AuroraBoot stages the config to the node's `/oem` overlay; it does not reboot on its own. |
| Reboot | The apply-cloud-config command reports `Completed` | Issue a reboot command and record the time it was requested. |
| Wait for rejoin | Reboot issued | Poll the node; it has rejoined once its phase is `Online` and its last heartbeat is newer than the recorded reboot time. Using heartbeat-after-reboot rather than a phase transition means a reconcile that misses the transient `Offline` window still detects the rejoin correctly. |
| Provisioned | Rejoin confirmed | Set `status.addresses`, `spec.providerID`, `status.initialization.provisioned = true`, and the `Ready` condition. |
| Delete | `deletionTimestamp` set | Release the claimed node back to its group using the same claim key, then remove the finalizer. |

A command that reports `Failed` or `Expired` during the apply step, or a
claimed node that disappears from AuroraBoot entirely, is a terminal failure:
`status.failureReason` and `status.failureMessage` are set and the machine
does not retry itself. Delete and re-create the Machine to try again.

The `KairosFleetCluster` controller is simpler: it validates the AuroraBoot
connection (the admin-token Secret must exist and hold a non-empty `token`
key) and sets `status.initialization.provisioned = true` once
`spec.controlPlaneEndpoint.host` is set. It owns no external infrastructure of
its own, since nodes are claimed and released per machine, so its own delete
path only removes its finalizer.

## providerID

`spec.providerID` is set by the controller to `kairos-fleet://<auroraboot-node-id>`
once a node is claimed and confirmed rejoined. Cluster API matches a
`KairosFleetMachine` to its workload `Node` by this string, so the node's
kubelet must report the identical value, and the value must be stable across
reboots.

The node side of this match is a cross-provider contract: it lives in the
Kairos bootstrap provider's rendered cloud-config, not in this repository.
For k3s, the bootstrap provider writes a self-discovery script that runs
before k3s starts. It reads the AuroraBoot node ID from the Kairos phone-home
agent's persisted credentials file (written to the node's persistent state by
the agent after enrollment) and writes it into a k3s kubelet argument
drop-in as `provider-id=kairos-fleet://<node-id>`, so the kubelet registers
with the exact string this controller set. This requires the node image to
run the Kairos phone-home agent and to have already phoned home (so the
credentials file exists) before k3s starts.

k0s derives the same node ID from the phone-home credentials, but delivers it
differently because k0s has no equivalent pre-start kubelet config drop-in. A
k0s control-plane node patches its own `Node.spec.providerID` once the cluster
is up, using the local admin kubeconfig. A k0s worker writes a
`KubeletConfiguration` drop-in that the kubelet reads through a static config
directory before it registers. Both paths produce the same
`kairos-fleet://<node-id>` value. k3s is the reference path and the most
exercised; prefer k3s if you do not have a specific reason to choose k0s.

This provider never patches a workload `Node`'s `spec.providerID` directly:
doing so fights the kubelet and is fragile. It only sets the value on the
`KairosFleetMachine` and relies on the node-side self-discovery to make the
kubelet agree.

## Control-plane endpoint

For a fleet cluster, the control-plane endpoint is operator-supplied: set
`KairosFleetCluster.spec.controlPlaneEndpoint` to a kube-vip VIP, a load
balancer, or a DNS name you manage. This provider does not allocate or derive
one. `status.initialization.provisioned` on the KairosFleetCluster stays
false until it is set.

## Delete: release versus reset

Deleting a `KairosFleetMachine` releases its claimed node back to its
AuroraBoot group's pool; the claim is cleared but the node is not wiped, so
its next claim re-applies fresh bootstrap configuration. AuroraBoot also
exposes a reset lifecycle (wipe-and-reuse), which is the more correct choice
when a node must be scrubbed before reuse (multi-tenant handoff,
decommissioning). This provider does not use it yet: v0.1 always releases. A
`deletePolicy` field to opt into reset is a planned follow-up.

## AuroraBoot connection and RBAC

The AuroraBoot admin bearer token lives in a Secret referenced by
`KairosFleetCluster.spec.auroraboot.adminTokenSecretRef`, read from the
Secret's `token` key. One connection is shared by every machine in the
cluster. The controller's RBAC is scoped to its own CRDs (plus their `status`
and `finalizers` subresources), read-only access to Cluster API `Clusters`
and `Machines`, and read-only access to `Secrets`. The token is never written
to logs.

The provider does not import AuroraBoot's own Go client module: doing so
would pull a newer Kubernetes and controller-runtime toolchain than Cluster
API v1.13 targets, dragging an unrelated dependency graph into the build.
Instead it ships a small, dependency-free HTTP client for the handful of
fleet endpoints it needs (claim, node get, apply-cloud-config, command list,
reboot, release).

## v0.1 limitations

- **The exercised topology is one control-plane machine plus a
  `MachineDeployment` of workers.** High-availability control planes
  (`KairosControlPlane.spec.replicas` greater than 1) are out of scope for
  this provider; nothing here prevents claiming more control-plane nodes, but
  the multi-control-plane join path has not been validated against a fleet.
- **Addresses are hostname-only.** `status.addresses` mirrors a single
  `Hostname` address derived from the node's reported hostname; AuroraBoot
  does not yet expose a structured internal IP through the client this
  provider uses.
- **The control-plane endpoint is operator-supplied.** There is no VIP or
  load-balancer allocation; you provide and manage it.
- **Delete always releases, never resets.** See "Delete: release versus
  reset" above.
- **k0s fleet support is newer than k3s.** Both distributions self-discover
  the providerID (see "providerID" above); k3s is the reference path and the
  most exercised. Prefer k3s if in doubt.
- **Group selection is a single named group.** There is no label or
  capability selector over nodes; `KairosFleetMachine.spec.group` names one
  AuroraBoot group directly.
- **The fleet flow is not covered by automated end-to-end tests yet.** The
  `test-e2e` CI job is the kubebuilder scaffold placeholder (deploys the
  manager to kind and checks it starts); it does not exercise claim,
  apply-cloud-config, reboot, or rejoin against a real AuroraBoot instance. It
  runs on manual dispatch only and does not gate pull requests.

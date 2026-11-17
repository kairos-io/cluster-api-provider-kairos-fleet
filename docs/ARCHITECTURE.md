# Architecture

> Last verified against: cluster-api-provider-kairos-fleet v0.1.4 (code at
> the v0.1.4 tag), Cluster API v1.13.4 (v1beta2 contract), Kairos v4.1.2.

This is the overview of how the Kairos Fleet infrastructure provider works:
the fleet model, the four CRDs, the machine lifecycle, the providerID
contract with the node, and the current (v0.1) limitations. It describes this
provider only. The Kairos bootstrap and control-plane providers are separate
projects with their own documentation; this provider relies on two things from
them, stated in the "Contracts with the other providers" section.

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
| Wait for bootstrap | `Machine.spec.bootstrap.dataSecretName` is not yet set | `WaitingForBootstrapData`. No requeue is scheduled; the watch on the owning `Machine` reconciles again when the field is set. |
| Resolve the AuroraBoot connection | The `KairosFleetCluster` is missing, or its connection is unusable | Requeue (15 seconds when the `KairosFleetCluster` is missing, otherwise 30 seconds) with a reason that names the cause: `WaitingForClusterInfrastructure` (no `KairosFleetCluster` yet, or a Secret read that can clear on its own), `WaitingForAuroraBootSecret` (the admin-token Secret does not exist yet) or `AuroraBootConnectionIncomplete` (no `auroraboot.url`, or a Secret with no `token`, which needs an edit). The message names the cause and is also logged. None of these sets `status.failureReason`. Any other failure to read the `KairosFleetCluster` is returned as an error and retried with backoff. |
| Claim | No `node-id` annotation yet | Resolve `spec.group` (a name or an ID) to the group's ID, then claim a node from it using the KairosFleetMachine's UID as the claim key, so a retried reconcile finds the same node instead of claiming a second one. The key is recorded in the `claim-key` annotation, because a UID does not survive `clusterctl move` or a backup and restore. A group that does not resolve requeues (30 seconds) as `GroupNotFound`; no capacity in the resolved group requeues (30 seconds) as `WaitingForCapacity`. Neither is an error. Any other failure of the group or claim call is returned as an error and retried with backoff. |
| Apply cloud-config | Node claimed, config not yet applied | Read the `value` key of the Secret named by `Machine.spec.bootstrap.dataSecretName` and hand it to AuroraBoot unmodified as an apply-cloud-config command. AuroraBoot stages the config on the node under `/oem`; it does not reboot on its own. A Secret that is missing or has no `value` key is returned as an error and retried with backoff. |
| Reboot | The apply-cloud-config command reports `Completed` | Issue a reboot command and record both the time it was requested and the command's ID. |
| Wait for rejoin | Reboot issued | Poll the node; it has rejoined once its phase is `Online` and its last heartbeat is newer than the recorded reboot time. Using heartbeat-after-reboot rather than a phase transition means a reconcile that misses the transient `Offline` window still detects the rejoin correctly. While it has not rejoined but still reports `Online`, read the reboot command back: a `Failed` or `Expired` reboot is retried like a rejected apply, because waiting for a boot the node refused to perform never ends. A node that is not `Online` is left to finish rebooting. |
| Provisioned | Rejoin confirmed | Set `status.addresses`, `spec.providerID`, `status.initialization.provisioned = true`, and the `Ready` condition. |
| Delete | `deletionTimestamp` set | Release the claimed node back to its group using the recorded claim key, then remove the finalizer. A machine that never recorded a `node-id` has nothing to release. A node AuroraBoot no longer knows (404) is treated as already released. If AuroraBoot refuses the release because the node is claimed by a different key (`ClaimMismatch`), the node is no longer this machine's to release, so the finalizer is removed and the node ID is logged; if no other machine owns the node, release it in AuroraBoot to return it to its group. Any other release error is retried. If the AuroraBoot connection cannot be resolved at all any more, the finalizer is still removed; see "Delete: release versus reset". |

A reconcile does nothing while the `Cluster` is paused (`spec.paused`) or the
`KairosFleetMachine` carries the `cluster.x-k8s.io/paused` annotation. That
includes a delete: a paused machine does not release its node until it is
unpaused.

A command that reports `Failed` or `Expired` during the apply-cloud-config
step sets a `CloudConfigFailed` `Ready` condition and is not terminal: the
controller clears the applied-config marker and re-issues a fresh
apply-cloud-config command about once a minute, so fixing whatever rejected
the command (most commonly the node's AuroraBoot phonehome policy, see
[QUICKSTART.md](QUICKSTART.md)) unsticks the machine with no manual
intervention. The reboot step behaves the same way, with a `RebootFailed`
`Ready` condition: `reboot` is a separate command with its own entry in the
node's `allowed_commands` policy, so a node that accepted the apply can still
refuse the reboot. Only a terminal phase counts there, and only while the node
reports `Online`. A reboot the node did accept usually never reports
`Completed`, because the node goes down in the middle of running it, so the
controller waits on the heartbeat rather than on the command for every
non-terminal phase. A node that is offline is most likely rebooting and cannot
report on its own reboot, so a terminal phase seen then is not acted on.

The earliest time for the next attempt is recorded on the object, in the
`retry-not-before` annotation, rather than left to a requeue delay. Clearing
the command markers to allow the retry is itself an update to the object, and
the controller's watch answers it with an immediate reconcile, so a requeue
delay alone would not pace anything.

A claimed node that disappears from AuroraBoot entirely is still a terminal
failure: `status.failureReason` and `status.failureMessage` are set (reason
`NodeMissing`) and the machine does not retry itself; delete and re-create the
Machine to try again. The controller looks the node up at the rejoin step, so
it notices the loss only once the reboot has been requested.

The controller does not poll a machine once it is provisioned. It runs again
on changes to the `KairosFleetMachine` or its `Machine`, and on the manager's
periodic resync, and each run repeats the rejoin check. A run that finds the
node no longer `Online` reports `WaitingForNodeRejoin` on the `Ready`
condition.

The `KairosFleetCluster` controller is simpler. It checks that the
AuroraBoot connection is complete (`spec.auroraboot.url` is set, and the
admin-token Secret exists and holds a non-empty `token` key) without calling
AuroraBoot, then sets `status.initialization.provisioned = true` once
`spec.controlPlaneEndpoint.host` is set. Until then its `Ready` condition
reason is `AuroraBootConnectionNotReady` or `WaitingForControlPlaneEndpoint`.
It owns no external infrastructure of its own, since nodes are claimed and
released per machine, so its own delete path only removes its finalizer.

## providerID

`spec.providerID` is set by the controller to `kairos-fleet://<auroraboot-node-id>`
once a node is claimed and confirmed rejoined. The node ID is the same value
recorded in the `node-id` annotation. Cluster API matches a
`KairosFleetMachine` to its workload `Node` by comparing this string with
`Node.spec.providerID`.

The match depends on the node side. The node's kubelet must register with the
providerID `kairos-fleet://<node-id>`, using the AuroraBoot node ID, and the
value must be stable across reboots. This provider never configures the
kubelet and never patches a workload `Node`. How the node arrives at that
value is decided by the cloud-config in the bootstrap data, which another
provider produces.

If the two values differ, or the `Node` has none, Cluster API does not link
the `Machine` to the `Node`. To check, compare the `PROVIDERID` column of
`kubectl get kairosfleetmachines` with the workload cluster's
`kubectl get nodes -o custom-columns=NAME:.metadata.name,PROVIDERID:.spec.providerID`.

## Contracts with the other providers

A fleet cluster is assembled from three providers, and this one relies on
exactly two things from the others:

- The bootstrap data Secret named by `Machine.spec.bootstrap.dataSecretName`
  holds the cloud-config under the `value` key. This provider applies that
  value to the claimed node unmodified.
- The node's kubelet registers with the providerID
  `kairos-fleet://<node-id>`, stable across reboots (see "providerID" above).

Anything else about the cloud-config, the node's Kubernetes distribution, or
the control-plane lifecycle belongs to the Kairos bootstrap and control-plane
providers, at
[kairos-io/cluster-api-provider-kairos](https://github.com/kairos-io/cluster-api-provider-kairos).

## Control-plane endpoint

For a fleet cluster, the control-plane endpoint is operator-supplied: set
`KairosFleetCluster.spec.controlPlaneEndpoint` to a kube-vip VIP, a load
balancer, or a DNS name you manage. This provider does not allocate or derive
one. The `KairosFleetCluster` does not report
`status.initialization.provisioned: true` until the endpoint host is set.

## Delete: release versus reset

Deleting a `KairosFleetMachine` releases its claimed node back to its
AuroraBoot group's pool; the claim is cleared but the node is not wiped, so
its next claim re-applies fresh bootstrap configuration. AuroraBoot also
defines a reset command (wipe-and-reuse), which would be the more correct
choice when a node must be scrubbed before reuse (multi-tenant handoff,
decommissioning). This provider never sends it: v0.1 always releases. A
`deletePolicy` field to opt into reset is a planned follow-up. The release is
a call to the AuroraBoot server, not a command to the node, so it needs no
node permission.

The release is best effort. The `KairosFleetCluster` removes its own finalizer
as soon as it is deleted, so it and its admin-token Secret can already be gone
while machines are still terminating, and then no release is possible from any
later reconcile either. Deletion proceeds in that case rather than stranding
the Machine, and the Cluster behind it, in `Terminating`. A read error that is
not a NotFound is different: it can clear on the next attempt, so the
reconcile fails and retries instead of removing the finalizer and leaving the
node claimed with nothing left to release it.

Deletion goes ahead without reaching AuroraBoot when the `KairosFleetCluster`
is gone, the admin-token Secret is gone, or the connection has no url or
token. To have nodes returned to their groups, delete the Cluster rather than
the `KairosFleetCluster`, and keep the admin-token Secret until the machines
are gone. Release a node left claimed this way in AuroraBoot.

## AuroraBoot connection and RBAC

The AuroraBoot admin bearer token lives in a Secret referenced by
`KairosFleetCluster.spec.auroraboot.adminTokenSecretRef`, read from the
Secret's `token` key. One connection is shared by every machine in the
cluster. The controller's cluster-wide RBAC (`config/rbac/role.yaml`) covers
`KairosFleetCluster` and `KairosFleetMachine` (plus their `status` and
`finalizers` subresources; the two template kinds have no controller),
read-only access to Cluster API `Clusters` and `Machines`, and read-only
access to `Secrets` in every namespace. The controller does not log the
token.

The provider does not import AuroraBoot's own Go client module: doing so
would pull a newer Kubernetes and controller-runtime toolchain than Cluster
API v1.13 targets, dragging an unrelated dependency graph into the build.
Instead it ships a small, dependency-free HTTP client for the handful of
fleet endpoints it needs: group list, claim, node get, queueing the
apply-cloud-config and reboot commands, command list, and release. Each
request has a 30 second timeout.

## v0.1 limitations

- **The exercised topology is one control-plane machine plus a
  `MachineDeployment` of workers.** Nothing in this provider limits the number
  of control-plane machines, but a control plane of more than one Machine has
  not been validated against a fleet.
- **Addresses come from the agent, and are only as good as what it
  reports.** `status.addresses` leads with a `Hostname` address derived from
  the node's hostname and then mirrors AuroraBoot's `addresses` list, which the
  Kairos agent fills in at register and heartbeat time. An agent that reports
  none, or one older than that field, leaves the hostname alone. Cluster API
  constrains `MachineAddress.type` to `Hostname`, `InternalIP`, `ExternalIP`,
  `InternalDNS` and `ExternalDNS`, while AuroraBoot stores the type as a
  free-form string, so an address of any other type is dropped here rather than
  passed through to a status the apiserver would reject. An empty address, or
  one longer than the 256 characters Cluster API accepts, is dropped for the
  same reason, and an exact repeat is listed once. AuroraBoot's
  server-observed `remoteIP` is not surfaced: it is what the server saw the node
  connect from, not an address the node claims, and it can be a NAT gateway.
- **The control-plane endpoint is operator-supplied.** There is no VIP or
  load-balancer allocation; you provide and manage it.
- **Delete always releases, never resets.** See "Delete: release versus
  reset" above.
- **k0s has seen less use than k3s.** Both were validated on real hardware
  (release notes v0.1.0 and v0.1.2); k3s is the reference path and the most
  exercised. Prefer k3s if in doubt.
- **Group selection is a single named group.** There is no label or
  capability selector over nodes; `KairosFleetMachine.spec.group` names one
  AuroraBoot group directly.
- **The fleet flow is not covered by an end-to-end test against a real
  AuroraBoot.** The `test-e2e` CI job is the kubebuilder scaffold placeholder
  (deploys the manager to kind and checks that it starts and serves metrics);
  it does not exercise claim, apply-cloud-config, reboot, or rejoin. It runs on
  manual dispatch only and does not gate pull requests. The envtest suite
  includes a lifecycle test that drives the controller through the whole flow
  against an in-memory AuroraBoot server.

# Quickstart

> Last verified against: cluster-api-provider-kairos-fleet commit `23b1416`,
> Cluster API v1.13.4 (v1beta2 contract), Kairos v4.1.2, k3s.

This walks through provisioning a Kairos fleet-backed workload cluster end to
end: a single control-plane machine and one worker, claimed from an
AuroraBoot fleet.

## Prerequisites

- A management cluster running a supported Kubernetes version (v1.32 to
  v1.36; see the version table in [../README.md](../README.md#target-versions))
  with Cluster API v1.13 and this provider, the Kairos bootstrap provider, and
  the Kairos control-plane provider installed. If the providers are not
  registered and installed yet, do that first: [INSTALL.md](INSTALL.md).
- The `clusterctl` CLI, matching the Cluster API version installed in the
  management cluster.
- An AuroraBoot instance reachable from the management cluster, with:
  - an admin bearer token,
  - at least one enrolled, unclaimed node in a control-plane group (default
    name `control-plane`),
  - at least one enrolled, unclaimed node in a worker group (default name
    `workers`).
- A control-plane endpoint (host and port) the workload cluster's API server
  will be reachable on. This provider does not allocate one: use a kube-vip
  VIP, a load balancer, or a DNS name you manage, pointed at the
  control-plane node once it is up.
- k3s is the only distribution with fleet providerID self-discovery
  implemented (see [ARCHITECTURE.md](ARCHITECTURE.md#providerid)); use
  `distribution: k3s`, as the default template already does.

## 1. Create the AuroraBoot admin-token Secret

The `KairosFleetCluster` references a Secret holding the AuroraBoot admin
bearer token. Create it in the namespace the cluster will live in, before
generating the cluster manifests:

```bash
kubectl create namespace demo
kubectl create secret generic auroraboot-admin-token \
  --namespace demo \
  --from-literal=token="$AURORABOOT_ADMIN_TOKEN"
```

Do not put the token in a manifest that gets committed to version control.
`$AURORABOOT_ADMIN_TOKEN` should come from your own secret store or shell
environment.

## 2. Set the cluster variables

`clusterctl generate cluster` reads these from the environment (or a
`--from` values file). All but `CLUSTER_NAME`, `AURORABOOT_URL`, and
`CONTROL_PLANE_ENDPOINT_HOST` have defaults, defined in
`templates/cluster-template.yaml`:

| Variable | Default | Meaning |
| --- | --- | --- |
| `CLUSTER_NAME` | (required) | Name stamped onto the Cluster and its infrastructure/control-plane/bootstrap resources. |
| `AURORABOOT_URL` | (required) | Base URL of the AuroraBoot fleet API, for example `https://auroraboot.example.com:8080`. |
| `AURORABOOT_TOKEN_SECRET` | `auroraboot-admin-token` | Name of the Secret created in step 1. |
| `CONTROL_PLANE_ENDPOINT_HOST` | (required) | Host of the workload cluster's API endpoint. |
| `CONTROL_PLANE_ENDPOINT_PORT` | `6443` | Port of the workload cluster's API endpoint. |
| `FLEET_CONTROL_PLANE_GROUP` | `control-plane` | AuroraBoot group the control-plane machine is claimed from. |
| `FLEET_WORKER_GROUP` | `workers` | AuroraBoot group worker machines are claimed from. |
| `POD_CIDR` | `192.168.0.0/16` | Cluster pod network. |
| `SERVICE_CIDR` | `10.128.0.0/12` | Cluster service network. |

```bash
export CLUSTER_NAME=demo
export AURORABOOT_URL=https://auroraboot.example.com:8080
export CONTROL_PLANE_ENDPOINT_HOST=203.0.113.10
```

## 3. Generate and apply the cluster manifests

```bash
clusterctl generate cluster "$CLUSTER_NAME" \
  --target-namespace demo \
  --infrastructure kairos-fleet \
  --kubernetes-version v1.34.0 \
  --control-plane-machine-count 1 \
  --worker-machine-count 1 > cluster.yaml

kubectl apply -f cluster.yaml
```

`--kubernetes-version` is the workload cluster's Kubernetes version; it is
bounded by the k3s version the target Kairos image ships, not by this
provider. v1.34.0 is the template's own example; adjust it to what your
Kairos image actually ships.

## 4. Watch provisioning

The `KairosFleetMachine` state machine progresses through a sequence of
`Ready` condition reasons as it claims and configures each node:

```bash
kubectl get kairosfleetmachines -n demo -w
```

| Reason | Meaning |
| --- | --- |
| `WaitingForBootstrapData` | Waiting for the Kairos bootstrap provider to publish the cloud-config Secret. |
| `WaitingForClusterInfrastructure` | Waiting for the KairosFleetCluster's AuroraBoot connection to become valid. |
| `WaitingForCapacity` | The target group has no unclaimed nodes. Enroll more nodes in AuroraBoot or free one up. |
| `NodeClaimed` | A node has been claimed; applying its bootstrap cloud-config next. |
| `ApplyingCloudConfig` | The cloud-config has been handed to AuroraBoot and is being written to the node, or the controller is waiting for that write to complete. |
| `Rebooting` | The controller has requested a reboot so the node applies the staged config. |
| `WaitingForNodeRejoin` | Waiting for the node to come back `Online` with a heartbeat newer than the reboot request. |
| `Provisioned` | The node is claimed, configured, and Online. `spec.providerID` and `status.addresses` are set. |

A machine stuck on `WaitingForCapacity` needs more enrolled nodes in that
group. A machine that reaches `CloudConfigFailed` or `NodeMissing` has failed
terminally (`status.failureReason` / `status.failureMessage` are set);
inspect the AuroraBoot node's command history and re-create the Machine.

## 5. Retrieve the kubeconfig and confirm the node joined correctly

```bash
clusterctl get kubeconfig "$CLUSTER_NAME" -n demo > demo-kubeconfig.yaml

kubectl get kairosfleetmachine -n demo -o jsonpath='{.items[0].spec.providerID}'
kubectl --kubeconfig demo-kubeconfig.yaml get nodes -o jsonpath='{.items[0].spec.providerID}'
```

Both commands should print the same `kairos-fleet://<node-id>` value. If they
do not match, the workload Node never registers against the right Machine and
the Machine stays unhealthy; see
[ARCHITECTURE.md](ARCHITECTURE.md#providerid) for the self-discovery
mechanism this depends on (the Kairos phone-home agent must be running on the
node image, with persisted credentials).

## 6. Tear down

```bash
kubectl delete cluster "$CLUSTER_NAME" -n demo
```

Deleting the Cluster deletes its Machines, which deletes their
KairosFleetMachines; each releases its claimed node back to its AuroraBoot
group. The node is not wiped; the next claim re-applies fresh bootstrap
configuration. See
[ARCHITECTURE.md](ARCHITECTURE.md#delete-release-versus-reset) for the v0.1
delete policy.

## Troubleshooting

- **Machine stuck at `WaitingForClusterInfrastructure`.** The
  KairosFleetCluster cannot resolve its AuroraBoot connection: check that the
  admin-token Secret exists in the same namespace and has a non-empty `token`
  key, and that `spec.auroraboot.url` is reachable from the controller pod.
- **Machine stuck at `WaitingForCapacity`.** The target group has no
  unclaimed nodes. Confirm the group name matches an AuroraBoot group with
  enrolled nodes: `kubectl get kairosfleetmachine -n demo -o jsonpath='{.items[0].spec.group}'`.
- **KairosFleetCluster never reports `Provisioned`.**
  `spec.controlPlaneEndpoint.host` is unset or empty. This provider does not
  allocate an endpoint; set it explicitly (see step 2).
- **Node comes back after reboot but the Machine never leaves
  `WaitingForNodeRejoin`.** The controller requires the node's heartbeat to be
  newer than the recorded reboot time. Check that the AuroraBoot node's
  `lastHeartbeat` is advancing; a node that never phones home after reboot
  (agent not running, network unreachable) will not satisfy the gate.
- **Workload Node has no `providerID`, or it does not match
  `kairos-fleet://...`.** The bootstrap cloud-config must include the fleet
  providerID self-discovery path (implemented for k3s and k0s in the Kairos
  bootstrap provider). Confirm the `KairosConfigTemplate` distribution matches
  your Kairos image (this quickstart uses k3s) and that the node image runs the
  Kairos phone-home agent with persisted credentials.

# Quickstart

> Last verified against: cluster-api-provider-kairos-fleet v0.1.4,
> Cluster API v1.13.4 (v1beta2 contract), Kairos v4.1.2, k3s (k0s also
> supported). The steps were checked against the code and the template; they
> were not re-run on hardware for this revision.

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
- Nodes whose kubelet registers with the providerID
  `kairos-fleet://<node-id>` (see
  [ARCHITECTURE.md](ARCHITECTURE.md#providerid)). That is a contract with the
  node and its bootstrap data, which this provider does not control. This
  quickstart uses `distribution: k3s`, the reference and most-exercised path,
  matching the default template. To use k0s instead, set `distribution: k0s`
  on the `KairosControlPlane` and on both `KairosConfigTemplate`s (see
  `templates/cluster-template.yaml`).
- AuroraBoot nodes enrolled with a `phonehome.allowed_commands` policy that
  permits `apply-cloud-config` and `reboot`. AuroraBoot's install-agent
  default does not include `apply-cloud-config`. This is the most common
  first-run failure; see "AuroraBoot node enrolment prerequisite" below
  before enrolling nodes.

## AuroraBoot node enrolment prerequisite

This provider sends a claimed node two AuroraBoot commands:
`apply-cloud-config`, to stage the bootstrap cloud-config, and `reboot`, to
apply it. Releasing a node on delete is a call to the AuroraBoot server and
needs no node permission. AuroraBoot's node-enrolment helper (`GET
/api/v1/install-agent`, the `curl ... | sh` script most nodes run to enrol)
defaults a node's `phonehome.allowed_commands` policy to
`upgrade,upgrade-recovery,reboot,unregister`, which does not include
`apply-cloud-config`. A node enrolled with that default fails its very first
bootstrap apply, and its `KairosFleetMachine` surfaces this on the `Ready`
condition:

```
command "apply-cloud-config" is not permitted by the phonehome policy; add it to phonehome.allowed_commands in cloud-config to opt in
```

Fix this before enrolling nodes, either of two ways:

- At enrolment, set the allowed-commands environment variable before running
  the install-agent script:

  ```bash
  export REGISTRATION_TOKEN=<the fleet's registration token>
  export AURORABOOT_ALLOWED_COMMANDS=upgrade,upgrade-recovery,reboot,unregister,apply-cloud-config
  curl -fsSL "$AURORABOOT_URL/api/v1/install-agent" | sh
  ```

  The script refuses to run without `REGISTRATION_TOKEN`; set
  `AURORABOOT_GROUP` too if the node should land in a specific group.

- Or set `phonehome.allowed_commands` directly in the node's own cloud-config,
  to a list containing at least `apply-cloud-config` and `reboot`.

The list is a replacement, not an addition: a `phonehome.allowed_commands`
that names only `apply-cloud-config` drops `reboot` along with the rest of the
default set, and the machine then fails at the reboot step instead of the
apply step, with `RebootFailed` on its `Ready` condition.

This provider does not send `reset` (see
[ARCHITECTURE.md](ARCHITECTURE.md#delete-release-versus-reset)), so the
policy does not need to allow it. Earlier versions of this guide listed it.

### Fixing an already-enrolled node

If a node was already enrolled with the default policy, add the missing
entries to `phonehome.allowed_commands` on the node. The install-agent script
writes that block to `/oem/phonehome.yaml`; a node whose phonehome settings
come from its own cloud-config keeps them wherever that file defines them. Then
restart the phonehome agent so it picks up the change:

```bash
systemctl restart kairos-agent-phonehome
```

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

`clusterctl generate cluster` reads these from the environment or its
`clusterctl.yaml`. `KUBERNETES_VERSION`, `CONTROL_PLANE_MACHINE_COUNT` and
`WORKER_MACHINE_COUNT` are set by the `--kubernetes-version`,
`--control-plane-machine-count` and `--worker-machine-count` flags in step 3.
Of the rest, all but `CLUSTER_NAME`, `AURORABOOT_URL`, and
`CONTROL_PLANE_ENDPOINT_HOST` have defaults, defined in
`templates/cluster-template.yaml`:

| Variable | Default | Meaning |
| --- | --- | --- |
| `CLUSTER_NAME` | (required) | Name stamped onto the Cluster and its infrastructure/control-plane/bootstrap resources. |
| `AURORABOOT_URL` | (required) | Base URL of the AuroraBoot fleet API, for example `https://auroraboot.example.com:8080`. |
| `AURORABOOT_TOKEN_SECRET` | `auroraboot-admin-token` | Name of the Secret created in step 1. |
| `K3S_TOKEN_SECRET_NAME` | `${CLUSTER_NAME}-k3s-token` | Name of the Secret holding the k3s server token, referenced by the worker `KairosConfigTemplate`'s `k3sTokenSecretRef` (key `token`). Not created automatically; see step 5. |
| `CONTROL_PLANE_ENDPOINT_HOST` | (required) | Host of the workload cluster's API endpoint. |
| `CONTROL_PLANE_ENDPOINT_PORT` | `6443` | Port of the workload cluster's API endpoint. |
| `FLEET_CONTROL_PLANE_GROUP` | `control-plane` | Name (or ID) of the AuroraBoot group the control-plane machine is claimed from; resolved to the group's ID before claiming. |
| `FLEET_WORKER_GROUP` | `workers` | Name (or ID) of the AuroraBoot group worker machines are claimed from; resolved to the group's ID before claiming. |
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
  --infrastructure kairos-io-fleet \
  --kubernetes-version v1.34.0 \
  --control-plane-machine-count 1 \
  --worker-machine-count 1 > cluster.yaml

kubectl apply -f cluster.yaml
```

`--kubernetes-version` fills the version fields in the template. This provider
does not install or select a Kubernetes version: the workload cluster runs
whatever k3s or k0s version the Kairos image ships. v1.34.0 is the template's
own example; set it to the version your Kairos image ships.

With `clusterctl` v1.13 or v1.14 the `kairos-io-fleet` name must be registered
in `clusterctl.yaml` first; see [INSTALL.md](INSTALL.md).

## 4. Watch provisioning

The `KairosFleetMachine` state machine progresses through a sequence of
`Ready` condition reasons as it claims and configures each node:

```bash
kubectl get kairosfleetmachines -n demo -w
```

| Reason | Meaning |
| --- | --- |
| `WaitingForBootstrapData` | `Machine.spec.bootstrap.dataSecretName` is not set yet. The machine continues once it is. |
| `WaitingForClusterInfrastructure` | The `KairosFleetCluster` does not exist yet, or reading its admin-token Secret failed in a way that can clear on its own. |
| `WaitingForAuroraBootSecret` | The admin-token Secret the `KairosFleetCluster` names does not exist yet. Creating it (step 1) is enough. |
| `AuroraBootConnectionIncomplete` | The connection cannot work as written: the `KairosFleetCluster` has no `auroraboot.url`, or the admin-token Secret has no `token` key. Waiting does not fix it; edit the `KairosFleetCluster` or the Secret. The condition message names which. |
| `GroupNotFound` | `spec.group` does not match any AuroraBoot group by name or ID. Not terminal: create the group or fix the name and the machine picks it up on the next reconcile. |
| `WaitingForCapacity` | The target group has no unclaimed nodes. Enroll more nodes in AuroraBoot or free one up. |
| `NodeClaimed` | A node has been claimed; applying its bootstrap cloud-config next. |
| `ApplyingCloudConfig` | The cloud-config has been handed to AuroraBoot and is being written to the node, or the controller is waiting for that write to complete. |
| `CloudConfigFailed` | The apply-cloud-config command reported `Failed` or `Expired`, commonly because the node's AuroraBoot phonehome policy does not permit the command (see "AuroraBoot node enrolment prerequisite" above). Not terminal: the controller clears the applied-config marker and queues a fresh command about once a minute, so the machine moves on once the node accepts it. |
| `Rebooting` | The controller has requested a reboot so the node applies the staged config. |
| `RebootFailed` | The reboot command reported `Failed` or `Expired` while the node was still `Online`. `reboot` has its own entry in the node's `allowed_commands` policy, so a node that accepted the apply can still refuse it. Not terminal: the controller clears the reboot markers and issues a fresh reboot about once a minute. |
| `WaitingForNodeRejoin` | Waiting for the node to come back `Online` with a heartbeat newer than the reboot request. |
| `NodeMissing` | The claimed AuroraBoot node no longer exists. Terminal: `status.failureReason` and `status.failureMessage` are set; the machine does not retry itself. |
| `Provisioned` | The node is claimed, configured, and Online. `spec.providerID` and `status.addresses` are set. |

A machine stuck on `WaitingForCapacity` needs more enrolled nodes in that
group. A machine that reaches `CloudConfigFailed` or `RebootFailed` is not
terminal and needs no manual action beyond fixing the node (see "AuroraBoot
node enrolment prerequisite" above); the controller retries on its own. A
machine that reaches `NodeMissing` has failed terminally
(`status.failureReason` / `status.failureMessage` are set); inspect the
AuroraBoot node's command history and re-create the Machine.

A worker machine that stays at `WaitingForBootstrapData` after the
control-plane machine is `Provisioned` is expected until the join-token Secret
from step 5 below exists, because the default template's worker
`KairosConfigTemplate` references it.

## 5. Create the worker join-token Secret

The worker `KairosConfigTemplate` in the default template references a
`k3sTokenSecretRef` pointing at the Secret named by `K3S_TOKEN_SECRET_NAME`
(default `${CLUSTER_NAME}-k3s-token`, key `token`). This Secret does not exist
until you create it, and its value is the k3s server token read from the
control-plane node, so create it once the control-plane `KairosFleetMachine`
reaches `Provisioned` in step 4 above:

```bash
# On the control-plane node (SSH, console, or however you reach it):
cat /var/lib/rancher/k3s/server/token
```

```bash
kubectl create secret generic "${CLUSTER_NAME}-k3s-token" \
  --namespace demo \
  --from-literal=token="<value from the command above>"
```

The worker's `KairosFleetMachine` stays at `WaitingForBootstrapData` until
its `Machine` has `spec.bootstrap.dataSecretName` set. To check:

```bash
kubectl get machines -n demo \
  -o custom-columns=NAME:.metadata.name,DATASECRET:.spec.bootstrap.dataSecretName
```

If the field is still empty after the Secret exists, the bootstrap provider
has not published the data; see the Kairos bootstrap provider's documentation
and logs.

If you set `distribution: k0s` instead of the default k3s (see Prerequisites
above), generate a worker token on the control-plane node with `k0s token
create --role worker` instead, store it under the key `token` in a Secret, and
change the worker `KairosConfigTemplate` in the generated `cluster.yaml` to use
`workerTokenSecretRef` in place of `k3sTokenSecretRef`.

## 6. Retrieve the kubeconfig and confirm the node joined correctly

```bash
clusterctl get kubeconfig "$CLUSTER_NAME" -n demo > demo-kubeconfig.yaml

kubectl get kairosfleetmachines -n demo
kubectl --kubeconfig demo-kubeconfig.yaml get nodes \
  -o custom-columns=NAME:.metadata.name,PROVIDERID:.spec.providerID
```

Each `kairos-fleet://<node-id>` value in the `PROVIDERID` column of the first
command should appear in the second. If a value does not match, Cluster API
does not link the Machine to its Node; see
[ARCHITECTURE.md](ARCHITECTURE.md#providerid) for what this provider expects
of the node.

## 7. Tear down

```bash
kubectl delete cluster "$CLUSTER_NAME" -n demo
```

Deleting the Cluster deletes its Machines, which deletes their
KairosFleetMachines; each releases its claimed node back to its AuroraBoot
group. The node is not wiped; the next claim re-applies fresh bootstrap
configuration. See
[ARCHITECTURE.md](ARCHITECTURE.md#delete-release-versus-reset) for the v0.1
delete policy.

Delete the Cluster, not the `KairosFleetCluster`, and keep the admin-token
Secret until the Cluster is gone: each machine releases its node through that
connection. If the `KairosFleetCluster` or the Secret is already gone, or the
connection has no url or token, the machine finishes deleting without a
release and its node stays claimed in AuroraBoot. Release such a node in
AuroraBoot.

## Troubleshooting

- **Machine stuck at `CloudConfigFailed`, with a `Ready` condition message
  containing `is not permitted by the phonehome policy`.** The node's
  AuroraBoot phonehome policy does not allow the `apply-cloud-config`
  command this provider issues. Fix the node's
  `phonehome.allowed_commands` (see "AuroraBoot node enrolment prerequisite"
  above) and restart its phonehome agent. No Machine deletion or re-creation
  is needed: the controller retries automatically once the node accepts the
  command.
- **Machine stuck at `RebootFailed`.** The node accepted the apply but
  refused the reboot. The usual cause is a `phonehome.allowed_commands` list
  that names `apply-cloud-config` without `reboot`: the list replaces the
  default rather than adding to it. Add `reboot` and restart the phonehome
  agent; the controller issues a fresh reboot about once a minute.
- **Worker machine stuck at `WaitingForBootstrapData` after the
  control-plane machine reaches `Provisioned`.** The Machine's
  `spec.bootstrap.dataSecretName` is empty. The default template's worker
  `KairosConfigTemplate` references a
  join-token Secret (`K3S_TOKEN_SECRET_NAME`/`k3sTokenSecretRef` for k3s,
  `workerTokenSecretRef` for k0s) that this quickstart does not create until
  step 5. Create it first. If the field stays empty, look at the Kairos
  bootstrap provider.
- **Machine stuck at `WaitingForClusterInfrastructure`.** The Cluster's
  `KairosFleetCluster` does not exist, or the controller cannot read the
  admin-token Secret. Check that the `KairosFleetCluster` named by the
  Cluster's `infrastructureRef` exists, and look in the controller logs for
  the read error.
- **Machine stuck at `WaitingForAuroraBootSecret` or
  `AuroraBootConnectionIncomplete`.** The `Ready` condition message names the
  missing Secret or field. Create the admin-token Secret in the Cluster's
  namespace with a `token` key (step 1), or set `spec.auroraboot.url` on the
  `KairosFleetCluster`.
- **AuroraBoot unreachable, or `spec.auroraboot.url` wrong.** The controller
  does not test the URL until it first calls AuroraBoot, so no condition
  reason names this. The reconcile fails and is retried with backoff, and the
  controller logs show the request error (for a machine that has not claimed
  a node yet, `resolving group ...`). Check that the URL is reachable from
  the controller pod.
- **Machine stuck at `GroupNotFound`.** `spec.group` does not resolve to an
  AuroraBoot group by name or ID. Check the `GROUP` column of
  `kubectl get kairosfleetmachines -n demo`, and confirm a group with that
  exact name (or ID) exists in AuroraBoot.
- **Machine stuck at `WaitingForCapacity`.** The target group has no
  unclaimed nodes. Confirm the `GROUP` column of
  `kubectl get kairosfleetmachines -n demo` names an AuroraBoot group with
  enrolled, unclaimed nodes.
- **KairosFleetCluster never reports `Provisioned`.** Its `Ready` condition
  reason says what it is waiting on. `AuroraBootConnectionNotReady`:
  `spec.auroraboot.url` is empty, or the admin-token Secret is missing,
  unreadable, or has no `token` key; the controller log names which.
  `WaitingForControlPlaneEndpoint`: `spec.controlPlaneEndpoint.host` is unset
  or empty. This provider does not allocate an endpoint; set it explicitly
  (see step 2).
- **Node comes back after reboot but the Machine never leaves
  `WaitingForNodeRejoin`.** The controller requires the node's heartbeat to be
  newer than the recorded reboot time. Check that the AuroraBoot node's
  `lastHeartbeat` is advancing; a node that never phones home after reboot
  (agent not running, network unreachable) will not satisfy the gate.
- **Workload Node has no `providerID`, or it does not match the
  `KairosFleetMachine`'s `kairos-fleet://...` value.** This provider only sets
  the value on the `KairosFleetMachine`; the node's kubelet has to register
  with the same string (see [ARCHITECTURE.md](ARCHITECTURE.md#providerid)).
  Confirm the `distribution` in the template's `KairosConfigTemplate` and
  `KairosControlPlane` matches what your Kairos image ships, then look at the
  Kairos bootstrap provider's documentation for how the node derives its
  providerID.

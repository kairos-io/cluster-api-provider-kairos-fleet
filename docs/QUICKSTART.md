# Quickstart

> Last verified against: cluster-api-provider-kairos-fleet v0.1.2,
> Cluster API v1.13.4 (v1beta2 contract), Kairos v4.1.2, k3s (k0s also
> supported).

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
- Both k3s and k0s have fleet providerID self-discovery implemented (see
  [ARCHITECTURE.md](ARCHITECTURE.md#providerid)). This quickstart uses
  `distribution: k3s`, the reference and most-exercised path, matching the
  default template. To use k0s instead, set `distribution: k0s` on both the
  `KairosControlPlane` and the `KairosConfigTemplate` (see
  `templates/cluster-template.yaml`).
- AuroraBoot nodes enrolled with a `phonehome.allowed_commands` policy that
  permits `apply-cloud-config`, `reboot`, and `reset` — not AuroraBoot's
  install-agent default. This is the most common first-run failure; see
  "AuroraBoot node enrolment prerequisite" below before enrolling nodes.

## AuroraBoot node enrolment prerequisite

This provider issues three AuroraBoot node commands: `apply-cloud-config`,
`reboot`, and `reset`. AuroraBoot's node-enrolment helper (`GET
/api/v1/install-agent`, the `curl ... | sh` script most nodes run to enrol)
defaults a node's `phonehome.allowed_commands` policy to
`upgrade,upgrade-recovery,reboot,unregister`, which does not include
`apply-cloud-config` or `reset`. A node enrolled with that default fails its
very first bootstrap apply, and its `KairosFleetMachine` surfaces this on the
`Ready` condition:

```
command "apply-cloud-config" is not permitted by the phonehome policy; add it to phonehome.allowed_commands in cloud-config to opt in
```

Fix this before enrolling nodes, either of two ways:

- At enrolment, set the allowed-commands environment variable before running
  the install-agent script:

  ```bash
  export REGISTRATION_TOKEN=<the fleet's registration token>
  export AURORABOOT_ALLOWED_COMMANDS=upgrade,upgrade-recovery,reboot,unregister,apply-cloud-config,reset
  curl -fsSL "$AURORABOOT_URL/api/v1/install-agent" | sh
  ```

  The script refuses to run without `REGISTRATION_TOKEN`; set
  `AURORABOOT_GROUP` too if the node should land in a specific group.

- Or set `phonehome.allowed_commands` directly in the node's own cloud-config,
  to a list containing at least `apply-cloud-config`, `reboot`, and `reset`.

### Fixing an already-enrolled node

If a node was already enrolled with the default policy, append the missing
entries to the `phonehome` block in `/oem/90_custom.yaml` on the node, then
restart the phonehome agent so it re-reads the policy:

```bash
systemctl restart kairos-agent-phonehome
```

The agent reads `phonehome.allowed_commands` once at start; editing the file
without restarting the service does not take effect.

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
| `K3S_TOKEN_SECRET_NAME` | `${CLUSTER_NAME}-k3s-token` | Name of the Secret holding the k3s server join token, referenced by the worker `KairosConfigTemplate`'s `k3sTokenSecretRef` (key `token`). Not created automatically; see step 5. |
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
| `GroupNotFound` | `spec.group` does not match any AuroraBoot group by name or ID. Not terminal: create the group or fix the name and the machine picks it up on the next reconcile. |
| `WaitingForCapacity` | The target group has no unclaimed nodes. Enroll more nodes in AuroraBoot or free one up. |
| `NodeClaimed` | A node has been claimed; applying its bootstrap cloud-config next. |
| `ApplyingCloudConfig` | The cloud-config has been handed to AuroraBoot and is being written to the node, or the controller is waiting for that write to complete. |
| `CloudConfigFailed` | The apply-cloud-config command reported `Failed` or `Expired` — commonly because the node's AuroraBoot phonehome policy does not permit the command (see "AuroraBoot node enrolment prerequisite" above). Not terminal: the controller clears the applied-config marker and retries automatically once the node accepts the command. |
| `Rebooting` | The controller has requested a reboot so the node applies the staged config. |
| `WaitingForNodeRejoin` | Waiting for the node to come back `Online` with a heartbeat newer than the reboot request. |
| `NodeMissing` | The claimed AuroraBoot node no longer exists. Terminal: `status.failureReason` and `status.failureMessage` are set; the machine does not retry itself. |
| `Provisioned` | The node is claimed, configured, and Online. `spec.providerID` and `status.addresses` are set. |

A machine stuck on `WaitingForCapacity` needs more enrolled nodes in that
group. A machine that reaches `CloudConfigFailed` is not terminal and needs
no manual action beyond fixing the node (see "AuroraBoot node enrolment
prerequisite" above); the controller retries on its own. A machine that
reaches `NodeMissing` has failed terminally (`status.failureReason` /
`status.failureMessage` are set); inspect the AuroraBoot node's command
history and re-create the Machine.

A worker machine that stays at `WaitingForBootstrapData` past this point is
most likely waiting on the join-token Secret created in step 5 below; that
is expected, not a failure.

## 5. Create the worker join-token Secret

The worker `KairosConfigTemplate` in the default template joins the running
k3s cluster using a `k3sTokenSecretRef`, pointing at the Secret named by
`K3S_TOKEN_SECRET_NAME` (default `${CLUSTER_NAME}-k3s-token`, key `token`).
This Secret does not exist until you create it, and its value comes from the
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

Until this Secret exists, the bootstrap provider cannot render the worker's
cloud-config, so the worker's `KairosFleetMachine` stays at
`WaitingForBootstrapData`. Once the Secret exists, provisioning continues
with no further action.

If you set `distribution: k0s` instead of the default k3s (see Prerequisites
above), generate a worker join token on the control-plane node with `k0s
token create --role worker` instead, and change the worker
`KairosConfigTemplate` in the generated `cluster.yaml` to use
`workerTokenSecretRef` in place of `k3sTokenSecretRef`.

## 6. Retrieve the kubeconfig and confirm the node joined correctly

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

## Troubleshooting

- **Machine stuck at `CloudConfigFailed`, with a `Ready` condition message
  containing `is not permitted by the phonehome policy`.** The node's
  AuroraBoot phonehome policy does not allow the `apply-cloud-config` (or
  `reset`) command this provider issues. Fix the node's
  `phonehome.allowed_commands` (see "AuroraBoot node enrolment prerequisite"
  above) and restart its phonehome agent. No Machine deletion or re-creation
  is needed: the controller retries automatically once the node accepts the
  command.
- **Worker machine stuck at `WaitingForBootstrapData` after the
  control-plane machine reaches `Provisioned`.** The worker's
  `KairosConfigTemplate` needs a join-token Secret
  (`K3S_TOKEN_SECRET_NAME`/`k3sTokenSecretRef` for k3s,
  `workerTokenSecretRef` for k0s) that this quickstart does not create until
  step 5. Create it and provisioning continues with no further action.
- **Machine stuck at `WaitingForClusterInfrastructure`.** The
  KairosFleetCluster cannot resolve its AuroraBoot connection: check that the
  admin-token Secret exists in the same namespace and has a non-empty `token`
  key, and that `spec.auroraboot.url` is reachable from the controller pod.
- **Machine stuck at `GroupNotFound`.** `spec.group` does not resolve to an
  AuroraBoot group by name or ID. Check the value:
  `kubectl get kairosfleetmachine -n demo -o jsonpath='{.items[0].spec.group}'`,
  and confirm a group with that exact name (or ID) exists in AuroraBoot.
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
  your Kairos image (this quickstart uses k3s; k0s also works, see
  [ARCHITECTURE.md](ARCHITECTURE.md#providerid)) and that the node image runs
  the Kairos phone-home agent with persisted credentials.

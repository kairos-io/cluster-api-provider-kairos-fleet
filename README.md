# Cluster API Provider Kairos Fleet

A [Cluster API](https://cluster-api.sigs.k8s.io/) **infrastructure provider** that
provisions machines from a [Kairos](https://kairos.io) fleet managed by
[AuroraBoot](https://github.com/kairos-io/AuroraBoot).

Where a cloud infrastructure provider talks to an IaaS API to create VMs, this
provider talks to the **AuroraBoot fleet API**: it claims an already-enrolled
Kairos node from a group, hands it the machine's bootstrap cloud-config, tracks
the node's reported addresses and boot state, and releases (or resets) the node
when the machine is deleted. It targets bare metal and pre-provisioned nodes that
phone home to AuroraBoot.

> **Status: pre-alpha, under active design.** The API and behaviour are not yet
> stable. See the design notes in the repo's development tooling before relying on
> anything here.

## Group of API types

`infrastructure.cluster.x-k8s.io`:

- **`KairosFleetCluster`** / `KairosFleetClusterTemplate` — the infrastructure
  side of a CAPI `Cluster`.
- **`KairosFleetMachine`** / `KairosFleetMachineTemplate` — the infrastructure
  side of a CAPI `Machine`; each one is backed by a claimed AuroraBoot node.

## How it fits

```
Cluster API  ──▶  KairosFleet* (this provider)  ──▶  AuroraBoot fleet API  ──▶  Kairos nodes
 (Machine)         (claim / apply-cloud-config /        (POST /groups/:id/claim,
                    addresses / release)                 apply-cloud-config, …)
```

## Target versions

- Cluster API: v1.13+ (v1beta2 contracts)
- Kubernetes (management + workload): v1.36+
- AuroraBoot: the fleet API (`/api/v1`), node claim + addresses + reset lifecycle
- Kairos: latest stable (currently v4.1.2)

## Development

Standard Cluster API provider layout (kubebuilder). Build and test targets live in
the `Makefile`. Contributions follow Conventional Commits with a DCO sign-off.

## License

Apache License 2.0 — see [LICENSE](LICENSE).

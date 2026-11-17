# Install

> Last verified against: cluster-api-provider-kairos-fleet v0.1.4,
> Cluster API v1.13.4 (v1beta2 contract), clusterctl v1.13.4. Step 1 was run
> on 2026-11-15: `clusterctl generate provider` fetched and rendered the
> v0.1.4 release assets through the `latest` URL, and through the pinned
> `v0.1.4` URL with `GOPROXY=off`. `clusterctl init` was not re-run against a
> management cluster for this revision.

This registers the Kairos Fleet infrastructure provider with `clusterctl` and
installs it into a management cluster. The provider's `clusterctl` name is
`kairos-io-fleet`. It is not in the built-in provider list of `clusterctl`
v1.13 or v1.14, so with those versions it needs a one-time `clusterctl.yaml`
entry before `clusterctl init` can find it. `clusterctl` v1.15 knows
`kairos-io-fleet` built in (first present in v1.15.0-beta.0), so the entry is
not needed there.

## Prerequisites

- A management cluster running a supported Kubernetes version: v1.32 to v1.36
  (the Cluster API v1.13 range). See the version table, including what the
  tests run against, in [../README.md](../README.md#target-versions).
- `clusterctl`, matching Cluster API v1.13.
- Cluster API core components and the Kairos bootstrap and control-plane
  providers (from `cluster-api-provider-kairos`, `clusterctl` name
  `kairos-io`), either already installed or installed in the same
  `clusterctl init` call below.
- Network access from your workstation (or wherever `clusterctl init` runs)
  to `github.com`, to fetch the release assets, or a local/air-gapped
  clusterctl override repository if you mirror releases internally.
- AuroraBoot v0.27.0 or later, the first release with the group claim and
  release endpoints this provider calls.
- AuroraBoot nodes enrolled with a `phonehome.allowed_commands` policy that
  permits `apply-cloud-config` and `reboot`, the two node commands this
  provider issues. AuroraBoot's install-agent enrolment script defaults to
  `upgrade,upgrade-recovery,reboot,unregister`, which lacks
  `apply-cloud-config`, so a node enrolled with the default fails its very
  first bootstrap apply. This is the most common first-run failure. See
  [QUICKSTART.md](QUICKSTART.md#auroraboot-node-enrolment-prerequisite) for
  both ways to set this, and how to fix an already-enrolled node.

## 1. Register the provider with clusterctl

Add a `providers` entry to your `clusterctl.yaml` (default location
`$XDG_CONFIG_HOME/cluster-api/clusterctl.yaml` or
`~/.cluster-api/clusterctl.yaml`, or pass `--config`), pointing at this
repository's release assets:

```yaml
providers:
  - name: "kairos-io-fleet"
    url: "https://github.com/kairos-io/cluster-api-provider-kairos-fleet/releases/latest/infrastructure-components.yaml"
    type: "InfrastructureProvider"
```

Each tagged release (`release.yml`) publishes three assets clusterctl expects
alongside each other in the same release: `infrastructure-components.yaml`
(the CRDs, RBAC, and controller Deployment, with the image pinned by digest),
`metadata.yaml` (the clusterctl contract and version-series mapping), and
`cluster-template.yaml` (the default `clusterctl generate cluster` template).
The release also carries an SBOM, `sbom.spdx.json`, which clusterctl does not
read. Point `url` at a specific tag instead of `latest` to pin a version, for
example `.../releases/v0.1.4/infrastructure-components.yaml`.

This is clusterctl's URL form, not a browser download link: there is no
`download/` segment. clusterctl reads the path as
`releases/<version>/<file>`. With a `download/` segment it still works
through its default HTTP download, but with `GOPROXY=off` or `direct` it
falls back to the GitHub API, looks for an asset named
`download/infrastructure-components.yaml`, and fails.

Do not pin `v0.1.0-beta.1`: it is superseded and is not installable through
`clusterctl init` at all (see
[the v0.1.0 release notes](release-notes/v0.1.0.md)).

## 2. Initialize the management cluster

```bash
clusterctl init \
  --infrastructure kairos-io-fleet \
  --bootstrap kairos-io \
  --control-plane kairos-io
```

This installs the Cluster API core provider (if not already present), this
infrastructure provider, and the Kairos bootstrap and control-plane
providers. `kairos-io` is the `clusterctl` name of the bootstrap and
control-plane providers from `cluster-api-provider-kairos`. With `clusterctl`
v1.13 or v1.14 those two also need `clusterctl.yaml` entries; that repository's
installation docs give them. If you registered them under a different name,
use that name instead.

## 3. Verify the deployment

```bash
kubectl get pods -n capi-kairos-fleet-system
kubectl get crds | grep kairosfleet
```

Expect a `capi-kairos-fleet-controller-manager` Deployment
with one available replica, and four CRDs: `kairosfleetclusters`,
`kairosfleetclustertemplates`, `kairosfleetmachines`, and
`kairosfleetmachinetemplates`.

```bash
kubectl get deployment -n capi-kairos-fleet-system \
  capi-kairos-fleet-controller-manager
```

## Uninstall

```bash
clusterctl delete --infrastructure kairos-io-fleet
```

`clusterctl` finds the provider's objects by the label
`cluster.x-k8s.io/provider: infrastructure-kairos-io-fleet`, which the release
manifest stamps on every component. By default it removes the controller and
RBAC but keeps the CRDs and the `capi-kairos-fleet-system` namespace; add
`--include-crd` and `--include-namespace` to remove those too, which also
deletes every `KairosFleet*` object.

This does not release AuroraBoot nodes claimed by existing
KairosFleetMachines: delete the Clusters first (see
[QUICKSTART.md](QUICKSTART.md#7-tear-down)) so their nodes are released, then
uninstall the provider.

## Next steps

[QUICKSTART.md](QUICKSTART.md) provisions a workload cluster end to end.

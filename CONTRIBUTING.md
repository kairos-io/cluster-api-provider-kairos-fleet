# Contributing

> Last verified against: cluster-api-provider-kairos-fleet commit `23b1416`;
> CI workflows in `.github/workflows/`.

## Development setup

Requires Go 1.26. Clone the repository and use the `Makefile` targets:

```bash
make manifests   # regenerate CRDs, RBAC, and webhook config from kubebuilder markers
make generate    # regenerate DeepCopy/DeepCopyInto/DeepCopyObject
make fmt vet     # go fmt, go vet
make test        # unit tests + envtest (downloads envtest binaries automatically)
make lint        # golangci-lint
make build       # build bin/manager
make docker-build IMG=<your-registry>/cluster-api-provider-kairos-fleet:<tag>
```

Run `make manifests` and `make generate` after any change to
`api/v1alpha1/*_types.go` or to a `+kubebuilder:rbac` marker, and commit the
regenerated files in `config/crd/bases/` and `config/rbac/` alongside the
source change.

## Tests

- **Unit and envtest** (`make test`): runs `go test` across the module with
  `KUBEBUILDER_ASSETS` pointed at `setup-envtest`-downloaded binaries.
  `ENVTEST_K8S_VERSION` is derived from the `k8s.io/api` version in `go.mod`
  (currently v0.35, so envtest runs against Kubernetes 1.35). This includes the
  fleet lifecycle end-to-end test, which drives the controller through claim,
  apply-cloud-config, reboot, rejoin, and release using the real HTTP fleet
  client against an in-memory AuroraBoot server.
- **govulncheck**: runs in CI against the module
  (`golang.org/x/vuln/cmd/govulncheck` v1.6.0).
- **Trivy**: scans the built manager image for fixable CRITICAL and HIGH
  vulnerabilities (`aquasecurity/trivy-action`).
- **Lint**: `golangci-lint` v2.12.2, configured in `.golangci.yml`.
- **End-to-end** (`make test-e2e`): the current suite is the kubebuilder
  scaffold placeholder; it deploys the manager to a kind cluster and checks it
  starts. It runs on manual `workflow_dispatch` only in CI and does not gate
  pull requests. Validating the fleet flow against real AuroraBoot-enrolled
  Kairos nodes is a manual lab procedure.

Pull requests are gated by the `Tests` workflow (unit tests, envtest,
govulncheck) and the `Lint` workflow. Both must pass before merge.

## Commit policy

- **Conventional Commits**: `feat:`, `fix:`, `chore:`, `refactor:`, `test:`,
  `docs:`, `ci:`, `build:`, `perf:`.
- **DCO sign-off is required on every commit**: use `git commit -s`, which
  appends a `Signed-off-by:` trailer. Commits without it are rejected.
- **No AI attribution trailers.** Do not add `Co-Authored-By: Claude` or
  similar; a commit is attributed to its human author.

## API and CRD changes

Any change to `api/v1alpha1/*_types.go` is user-visible: it changes what
`kubectl explain` shows and what the CRDs accept. Write field doc-comments as
complete, self-contained sentences (they become the CRD `description`), run
`make manifests`, and update `config/samples/*.yaml` and `docs/API.md` if the
change affects a field those documents describe.

## Local tooling

This repository uses Claude Code agent tooling for development (`.claude/`
and the root `CLAUDE.md`). Both are excluded from version control
(`.gitignore`) and are not required reading to contribute; they are local
working notes, not part of the published project.

## License

Apache License 2.0. See [LICENSE](LICENSE).

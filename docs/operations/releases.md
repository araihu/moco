# SemVer image and Helm releases

Publishing a GitHub Release triggers `.github/workflows/release.yml`. It
publishes the Moco image and Helm chart to GHCR, then attaches the chart,
`release.json` and `SHA256SUMS` to that GitHub Release. A tag push alone does not
publish artifacts. Stable releases and prereleases both use GitHub's
[`release.published` event](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#release).

## Versions and destinations

Tags must contain a complete, valid SemVer, with an optional lowercase `v`
prefix. Leading zeroes in core/numeric prerelease fields, incomplete versions,
arbitrary names and OCI tags longer than 128 characters are rejected. The
GitHub prerelease flag must match the presence of a SemVer prerelease suffix.

| Git tag | Image tag | Helm chart/app version | Chart OCI tag |
| --- | --- | --- | --- |
| `v1.2.3` or `1.2.3` | `1.2.3` | `1.2.3` | `1.2.3` |
| `v1.3.0-rc.1` | `1.3.0-rc.1` | `1.3.0-rc.1` | `1.3.0-rc.1` |
| `v1.2.3+build.007` | `1.2.3_build.007` | `1.2.3+build.007` | `1.2.3_build.007` |

Helm preserves build metadata in the chart and translates `+` to `_` for its
[OCI registry tag](https://helm.sh/docs/v3/topics/registries/). The image uses
the same OCI-safe mapping. The workflow creates exact version tags; it does not
move `latest`, major or minor aliases.

For `araihu/moco`, destinations are:

- Image: `ghcr.io/araihu/moco:<OCI-tag>`, with Linux **amd64 and arm64** manifests.
- Helm: `oci://ghcr.io/araihu/charts/moco`, selected with the SemVer `--version`.
- GitHub Release assets: `moco-<SemVer>.tgz`, `release.json`, `SHA256SUMS`.

Chart version and `appVersion` match the release version. The packaged chart
sets `image.repository` and the published **multi-platform image digest** in
its default values, and carries the Git source revision in its annotations.
The source chart remains unchanged, with development defaults requiring an
explicit image. Secrets, mirror identity and PVC settings remain external
deployment inputs.

## Validation and publication order

1. A read-only preparation job validates tag syntax, prerelease status and
   repository identity. The GitHub event SHA, checked-out commit and dereferenced
   Git tag must match, the source checkout must be clean, and the commit must
   be reachable from `origin/main`.
2. The release reuses the complete `check.yml` workflow against that commit:
   Dagger/application checks, production image/alert tests, CLI tests and the
   Helm 3/4 matrix with Kubernetes schema validation. Failed or cancelled
   prerequisites prevent publication.
3. Only the publishing job receives `contents: write` and `packages: write`.
   It checks out the validated commit, authenticates with the automatic
   `GITHUB_TOKEN` and checks whether the exact version already exists.
4. Buildx builds both architectures with the native pinned Go builder,
   cross-compiling static binaries without QEMU. The image includes source,
   revision and version metadata, BuildKit provenance and SBOM attestations.
   External actions and Go/BuildKit images are pinned by commit/digest.
5. The release helper pins the actual image digest into a temporary chart
   copy, sets versions/revision, runs lint and rendering with production-input
   fixtures, packages and verifies it, then pushes the OCI chart.
6. The workflow attaches the exact chart archive, a JSON manifest recording
   source/version/image/chart digests and archive checksum, and checksum file.

The workflow does not change cluster resources. Source/release tags should
remain immutable. Registry owners can still move tags manually; use immutable
digests for deployments and recovery records.

## One-time repository/package setup

Allow GitHub Actions to run and publish packages under `araihu`. No custom
registry password or long-lived PAT is needed by this workflow. If a package
already exists from a manual push, connect it to this repository and grant its
Actions access. See GitHub's [GITHUB_TOKEN/package permissions](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).

New GHCR packages are private by default. After initial publication, a package
owner can make the image and `charts/moco` package public for anonymous pulls.
For private distribution, consumers authenticate Helm and configure namespaced
`imagePullSecrets`. Publication does not change package visibility or
organization policy automatically.

Create releases from commits containing this workflow and helper. For example,
after choosing an unused version and updating local `main`:

```sh
git pull --ff-only origin main
git tag -a v1.2.3 -m 'Moco 1.2.3'
git push origin v1.2.3
gh release create v1.2.3 --verify-tag --title 'Moco 1.2.3' --generate-notes
```

For a prerelease, use a prerelease tag and GitHub's matching flag:

```sh
git tag -a v1.3.0-rc.1 -m 'Moco 1.3.0-rc.1'
git push origin v1.3.0-rc.1
gh release create v1.3.0-rc.1 --verify-tag --prerelease --generate-notes
```

Publishing a draft through the UI also starts the workflow. The Release exists
while CI runs; artifacts become available after validation/publication finish.
The versions above are illustrative, not a list of artifacts already published.

## Consume a published release

```sh
docker pull ghcr.io/araihu/moco:1.2.3
helm pull oci://ghcr.io/araihu/charts/moco --version 1.2.3
helm show values oci://ghcr.io/araihu/charts/moco --version 1.2.3
helm upgrade --install moco oci://ghcr.io/araihu/charts/moco \
  --version 1.2.3 --namespace moco -f /private/moco/values.yaml \
  --wait --timeout 20m
```

Supply the external Secret, mirror PVC/UUID and local-storage settings described
in the [chart runbook](../../charts/moco/README.md). Released charts already pin
their image; omit `image` from operator values unless deliberately selecting
another reviewed image.

For independent verification and offline installation:

```sh
gh release download v1.2.3 --repo araihu/moco \
  --pattern 'moco-1.2.3.tgz' --pattern release.json --pattern SHA256SUMS \
  --dir /private/moco/release
(cd /private/moco/release && sha256sum -c SHA256SUMS)
```

`release.json` records `imageDigest`, `chartDigest` (registry manifest digest)
and `chartSHA256` (`.tgz` bytes). These identify different objects. Retain this
manifest, chart, matching image and independently held key/configuration
material with recovery records. The image pin covers server, backup and init.

## Interrupted publication and retries

Publication is serialized per OCI version, including optional `v` aliases.
Re-run failed jobs after correcting an operational problem:

- Existing images are reused only when source/revision/version match and both
  supported Linux architectures are present.
- Existing charts are pulled and reused only when version, source revision,
  schema and image digest match. Their original archive bytes are retained.
- Existing GitHub assets are downloaded and checksum-compared. Identical assets
  are skipped, missing assets uploaded and conflicting assets never clobbered.
- Authentication, authorization and transport errors fail the job, rather than
  becoming an absent version. Do not delete packages/assets to work around a
  source/digest conflict; publish a new SemVer version for changed source.

The read-only helper artifact is kept seven days. After expiration, re-run
**all jobs** to regenerate it and repeat validation. This internal CI artifact
can be replaced on retry; the published artifact checks above still apply.
After an image push succeeds, later retries reuse that image pin.

## Local checks

```sh
make check
make chart-check HELM=/path/to/helm
```

Go tests cover SemVer normalization, prerelease policy, real Git tag/main
ancestry, registry absence versus failure, image/chart identity conflicts,
publication resumption and asset idempotency. Chart checks additionally package
stable, prerelease and build-metadata versions with real Helm, leaving source
charts unchanged. Tests do not require publication credentials.

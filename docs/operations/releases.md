# SemVer image and Helm releases

Pushing a version tag triggers `.github/workflows/release.yml`. It validates
the source, publishes the server and controller images and Helm charts to GHCR, creates a draft
GitHub Release and attaches both charts, `release.json` and `SHA256SUMS` before
publishing it. This order supports GitHub's
[immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases): assets lock at publication. Do not publish
the release manually while CI is running. Invalid version tags fail validation.

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

- Server image: `ghcr.io/araihu/moco:<OCI-tag>`.
- Controller image: `ghcr.io/araihu/moco-controller:<OCI-tag>`.
- Both images contain Linux **amd64 and arm64** manifests, provenance and SBOM attestations.
- Helm: `oci://ghcr.io/araihu/charts/moco` and `oci://ghcr.io/araihu/charts/moco-controller`, selected with the same SemVer `--version`.
- GitHub Release assets: `moco-<SemVer>.tgz`, `moco-controller-<SemVer>.tgz`, `release.json`, `SHA256SUMS`.

The controller artifacts are included starting with **v0.0.3**. Older releases contain only the server artifacts.

Chart version and `appVersion` match the release version. The packaged chart
sets `image.repository` and the published **multi-platform image digest** for its own component in
its default values, and carries the Git source revision in its annotations.
The source chart remains unchanged, with development defaults requiring an
explicit image. Secrets, mirror identity and PVC settings remain external
deployment inputs. Controller API endpoints and its credential Secret also remain operator inputs.

## Validation and publication order

1. A read-only preparation job validates tag syntax and repository identity,
   and derives prerelease status from the SemVer suffix. The GitHub event SHA,
   checked-out commit and dereferenced Git tag must match, the source checkout
   must be clean, and the commit must
   be reachable from `origin/main`.
2. The release reuses the complete `check.yml` workflow against that commit:
   Dagger/application checks, production image/alert tests, CLI tests and the
   Helm 3/4 matrix with helm-unittest suites, real Helm integration tests and
   Kubernetes schema validation. Failed or cancelled
   prerequisites prevent publication.
3. Only the publishing job receives `contents: write` and `packages: write`.
   It checks out the validated commit, authenticates with the automatic
   `GITHUB_TOKEN` and checks whether the exact version already exists.
4. Buildx builds both images for both architectures with the native pinned Go builder,
   cross-compiling static binaries without QEMU. The image includes source,
   revision and version metadata, BuildKit provenance and SBOM attestations.
   External actions and Go/BuildKit images are pinned by commit/digest.
5. The release helper creates or resumes a draft release and verifies both images
   against the same version, source commit, architectures and expected digests before
   publishing any chart. It pins the corresponding image digest into a temporary copy
   of each chart, sets versions/revision, runs lint and rendering with component-specific
   fixtures, packages and verifies each archive, then pushes both OCI charts. Controller
   chart verification also requires all three Moco CRDs.
6. The helper attaches both exact chart archives, a JSON manifest recording
   source/version and both components' image/chart digests and archive checksums, and
   `SHA256SUMS`. It publishes the draft only after all four uploads succeed. A partial
   controller upload leaves a resumable draft. SemVer prereleases are marked accordingly
   and never latest.

The workflow does not change cluster resources. Source/release tags should
remain immutable. Registry owners can still move tags manually; use immutable
digests for deployments and recovery records.

## One-time repository/package setup

Allow GitHub Actions to run and publish packages under `araihu`. No custom
registry password or long-lived PAT is needed by this workflow. If a package
already exists from a manual push, connect it to this repository and grant its
Actions access. See GitHub's [GITHUB_TOKEN/package permissions](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).

New GHCR packages are private by default. After initial publication, a package
owner can make both image packages and both `charts/moco` / `charts/moco-controller` packages public for anonymous pulls.
For private distribution, consumers authenticate Helm and configure namespaced
`imagePullSecrets`. Publication does not change package visibility or
organization policy automatically.

Create releases from commits containing this workflow and helper. For example,
after choosing an unused version and updating local `main`:

```sh
git pull --ff-only origin main
git tag -a v1.2.3 -m 'Moco 1.2.3'
git push origin v1.2.3
```

For a prerelease, push a prerelease tag; CI derives GitHub's matching flag:

```sh
git tag -a v1.3.0-rc.1 -m 'Moco 1.3.0-rc.1'
git push origin v1.3.0-rc.1
```

A manually created draft can be resumed if its tag and prerelease flag match.
Publishing through the UI does not trigger this workflow. The completed Release
appears after validation, OCI publication and asset uploads finish. The versions
above are illustrative, not a list of artifacts already published.

## Consume a published release

```sh
docker pull ghcr.io/araihu/moco:1.2.3
docker pull ghcr.io/araihu/moco-controller:1.2.3
helm pull oci://ghcr.io/araihu/charts/moco-controller --version 1.2.3
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
  --pattern 'moco-1.2.3.tgz' --pattern 'moco-controller-1.2.3.tgz' --pattern release.json --pattern SHA256SUMS \
  --dir /private/moco/release
(cd /private/moco/release && sha256sum -c SHA256SUMS)
```

`release.json` records an ordered `components` array (server, controller), each with `name`,
`imageRepository`, `imageDigest`, `chartRepository`, `chartDigest` (registry manifest digest),
`chartSHA256` (`.tgz` bytes) and `archive`. Top-level `imageDigest`, `chartDigest` and
`chartSHA256` retain their original server meaning for existing consumers. These identify different objects. Retain this
manifest, chart, matching image and independently held key/configuration
material with recovery records. The server image pin covers server, backup and init; the controller image has its own pin.

For controller installation, supply `publicURL`, `adminURL` and the existing same-namespace
credential Secret as described in the [controller runbook](kubernetes-controller.md):

```sh
helm upgrade --install moco-management oci://ghcr.io/araihu/charts/moco-controller \
  --version 0.0.3 --namespace moco -f /private/moco/controller-values.yaml
```

## Interrupted publication and retries

Publication is serialized per OCI version, including optional `v` aliases.
Re-run failed jobs after correcting an operational problem:

- Each existing image is reused only when source/revision/version match and both
  supported Linux architectures are present.
- Each existing chart is pulled and reused only when version, source revision,
  schema and image digest match. Their original archive bytes are retained.
- Existing draft assets are downloaded and checksum-compared. Identical assets
  are skipped, missing assets uploaded and conflicting assets never clobbered.
- Completed releases can be verified on retry if all expected assets exist.
  A published release missing assets fails before chart publication: assets
  cannot be added to an immutable release. Do not delete or retag it to retry.
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

## Initial release v0.0.1

The initial release used the previous `release.published` workflow. Its complete
validation passed and both public OCI packages were published, but GitHub's
immutability policy prevented attaching files afterward. The tag and packages
remain unchanged. Use the OCI chart for this version; verified digests and the
chart archive checksum are recorded in the
[v0.0.1 release notes](https://github.com/araihu/moco/releases/tag/v0.0.1). The
original workflow run reports an asset-upload failure. New versions use the
draft-first flow above.

## Documentation website

Stable releases whose source contains the website workflow call
`.github/workflows/site.yml` after artifact publication. The job builds Margo
and Manja from the validated release source and deploys the verified static
artifact to `moco.araihu.com`. Prereleases do not change the site. Website
publication requires the dedicated Cloudflare token in the `production`
environment; application publication does not use that credential.
See [site maintenance](../../site/README.md) for the initial v0.0.3 deployment,
manual documentation corrections, validation and rollback. Software tags remain
immutable when documentation changes.

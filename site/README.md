# Moco website

Product home and documentation for Moco **v0.0.3**, following
[Balemoh](https://github.com/araihu/balemoh)'s AraiHu static publishing pattern:
[Margo v0.0.26](https://github.com/araihu/margo) for the home and guides,
[Manja v0.1.17](https://github.com/araihu/manja) for read-only API reference,
and Cloudflare Workers Static Assets at **https://moco.araihu.com**.
The website runs no Moco server and accepts no credentials or secret values.

## Build and preview

Requires Go 1.27.3 or newer, Python 3 and Make. First build needs network access
for pinned Go publication tools; they are isolated under ignored `tools/bin/site/`.
Node is required only for Wrangler deployment or browser testing.

```sh
make site-check
make site-preview             # http://127.0.0.1:4173
# Or serve an existing build without rebuilding:
python3 -m http.server 4173 --bind 127.0.0.1 --directory dist/site
```

`site.yaml` configures Margo's built-in landing/docs layouts, local assets,
search, sidebar and table of contents. `site/theme.css` uses public semantic
theme tokens. There are no external fonts, analytics or hosted runtime dependencies.
The small vault mark and architecture/social illustrations in `site/assets/`
are repository-owned website artwork, not an organization-wide brand asset.
`social.png` is the raster preview of the checked-in `social.svg` (1280×640).

## Content ownership

Edit the canonical Markdown guides in `docs/`, `cli/README.md`, chart READMEs
and `openapi/README.md`. `scripts/build_site.py` lists their routes and stages
frontmatter under ignored `site/.build/content/`. It rewrites links between
published guides into site routes and links remaining repository files to
GitHub main. Historical `docs/superpowers/` plans are deliberately excluded.
Only the home and navigation overviews are authored in `site/content/`.

Manja reads only `openapi/bundled/public.yaml` and `internal.yaml` through
`site/renderer.yaml`. Both are intentionally public documentation; internal
contract publication does not make administrative endpoints public. Keep
credentials and private instance configuration out of contracts and examples.
Generate bundles with `make spec-bundle` after changing contracts; API validation
and generated drift remain part of the existing application checks.

The build exports into a temporary directory, verifies Manja's exact file
inventory and hashes, adds JSON-LD to Margo pages, validates canonical/social
metadata and local links, then replaces `dist/site` only on success. Reports
remain outside the publication in `site/.build/`. Manja HTML/fragments are not
modified after export. `SITE_VERSION` can override the displayed software version
for a stable release; contract `info.version` and the `v1` namespace are independent.
Avoid simultaneous builds in one checkout.

## CI and publication

`.github/workflows/site.yml` builds and validates relevant pull requests and main
changes without deployment credentials. `release.yml` calls the same workflow
**after** successful artifact publication for stable SemVer tags. This direct
workflow call is intentional: releases published with `GITHUB_TOKEN` do not
trigger a separate `release.published` workflow. Prereleases do not update the site.

Only the deployment job uses the GitHub `production` environment's
`CLOUDFLARE_API_TOKEN`. Configure a dedicated token with **Workers Editor** for
account `482241c1c361c24977ac6540df946c2f`, and **Zone Read** plus
**Workers Routes Write** restricted to the `araihu.com` zone. The configured token grants
Worker editing at the account level; it is not restricted to this Worker.
Workers Editor can update and deploy an existing Worker, but cannot create or
delete one. An account owner must provision `moco-site` once before the first
CI deployment (already done for this installation). Keep creation privileges
out of the recurring CI token.
Do not reuse a product API credential. Account, Worker and domain identifiers
in `wrangler.jsonc` are public configuration, not secrets.

The initial deployment documents the already published v0.0.3 using current main;
it does not move that immutable tag. The same manual path publishes reviewed
documentation corrections:

```sh
gh workflow run site.yml --repo araihu/moco --ref main -f release_tag=v0.0.3
```

The workflow verifies the release is published, stable and an ancestor of the
checkout. For future releases, update the current-version examples, landing
copy and `site.yaml` in the release source before tagging. Historical release
evidence pages retain their original version and source identity.

Deployment uses pinned Wrangler 4.151.3 and the verified build artifact, then
waits up to five minutes for DNS/TLS and the expected home page, then compares
**every deployed file** by SHA-256 and checks MIME types and true 404s.
Compare against the **exact uploaded artifact**, not an independent rebuild:
Manja's publication receipt includes build-specific metadata. To verify manually,
replace `RUN_ID` with the successful deployment run:

```sh
gh run download RUN_ID --repo araihu/moco --name moco-site-RUN_ID \
  --dir /tmp/moco-published-site
python3 -B scripts/check_site_live.py --root /tmp/moco-published-site
```

Keep `Cache-Control: no-transform` from `site/headers`. Disable HTML/JavaScript
rewriting, automatic minification and challenge injection for this static host;
Manja lazily verifies fragment hashes. Serve unknown paths as 404, never as an
SPA fallback. The `/openapi/sw.js` service worker is scoped to `/openapi/`.

## Rollback

Use Cloudflare Worker deployment history to restore the previous deployment,
or rerun a known-good site's workflow/build and deploy the saved verified
artifact. Confirm the artifact corresponds to the intended source/release and
rerun live verification. Do not retag a software release to fix documentation.
The first release with automatic website publication is the next stable tag
whose source includes these workflows; v0.0.3 remains unchanged.

#!/usr/bin/env python3
"""Stage canonical repository guides and atomically compose Margo + Manja output."""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
from urllib.parse import urlsplit, unquote

from site_brand import verify as verify_brand

ROOT = Path(__file__).resolve().parent.parent
# source, public route, navigation title, description
GUIDES = [
    ('docs/getting-started.md', 'getting-started', 'Get started', 'Run Moco locally and use its CLI with table, JSON and CSV output.'),
    ('docs/installation.md', 'guides/installation', 'Installation', 'Install the released server, controller and Helm charts with explicit storage and credentials.'),
    ('cli/README.md', 'guides/cli', 'CLI', 'Build and use the Moco command-line client, generated from OpenAPI overlays.'),
    ('docs/operations/production-sqlite.md', 'guides/production', 'Production SQLite', 'Single-instance production configuration, TLS, mirrored backups, metrics and recovery.'),
    ('docs/operations/kubernetes-controller.md', 'guides/controller', 'Controller and CRDs', 'Declare tenants, vaults and authorization with the Moco Kubernetes controller.'),
    ('docs/operations/kubernetes-eso-tests.md', 'guides/eso', 'ESO and cluster acceptance', 'Deliver Moco values through ESO and validate the published artifacts in vind.'),
    ('docs/security.md', 'guides/security', 'Security and keys', 'Configure scoped authentication, authorization, audit and root-key rotation.'),
    ('docs/operations/backup-restore.md', 'guides/backup-restore', 'Backup and restore', 'Verify backups, preserve historical root keys and restore an offline database.'),
    ('docs/operations/transactional-audit.md', 'guides/audit', 'Transactional audit', 'Understand audited commits, rollback behavior, retention and operational checks.'),
    ('docs/operations/load-fault-tests.md', 'guides/load-faults', 'Load and failures', 'Run bounded capacity and fault acceptance with retained reports.'),
    ('charts/moco/README.md', 'reference/server-chart', 'Server Helm chart', 'Server chart values, JSON Schema, storage, TLS, networking and Helm tests.'),
    ('charts/moco-controller/README.md', 'reference/controller-chart', 'Controller Helm chart', 'Controller chart values, CRDs, scoped credentials and RBAC.'),
    ('openapi/README.md', 'reference/openapi-pipeline', 'OpenAPI pipeline', 'Contracts, Vacuum bundles and separate CLI naming metadata overlays.'),
    ('docs/development.md', 'reference/development', 'Development', 'Architecture, repository layout, toolchain and development checks.'),
    ('docs/operations/releases.md', 'reference/releases', 'Releases', 'SemVer publication of OCI images, digest-pinned Helm charts and release assets.'),
    ('docs/releases/0.0.3.md', 'reference/release-0.0.3', 'Release 0.0.3 evidence', 'Published image identities, 21-scenario vind and ESO acceptance, and test limits.'),
]


def run(*args, report=None):
    if report:
        with report.open('w') as output:
            subprocess.run(args, cwd=ROOT, check=True, stdout=output)
    else:
        subprocess.run(args, cwd=ROOT, check=True)


def stage_content(build):
    content = build / 'content'
    if content.exists():
        shutil.rmtree(content)
    shutil.copytree(ROOT / 'site/content', content)
    routes = {(ROOT / src).resolve(): '/' + route + '/' for src, route, _, _ in GUIDES}
    routes[(ROOT / 'docs/README.md').resolve()] = '/guides/'
    routes[(ROOT / 'README.md').resolve()] = '/'
    for src, route, title, description in GUIDES:
        source = ROOT / src
        def link(match):
            label, href = match.groups()
            parsed = urlsplit(href)
            if parsed.scheme or href.startswith(('#', '/')):
                # Map canonical GitHub guide links into the site as well.
                prefix = 'https://github.com/araihu/moco/blob/main/'
                if not href.startswith(prefix):
                    return match.group(0)
                target = (ROOT / unquote(parsed.path.split('/blob/main/', 1)[1])).resolve()
            else:
                target = (source.parent / unquote(parsed.path)).resolve()
            if target in routes:
                dest = routes[target]
            else:
                if not target.is_relative_to(ROOT) or not target.exists():
                    raise ValueError(f'{src}: missing repository link {href}')
                kind = 'tree' if target.is_dir() else 'blob'
                dest = f'https://github.com/araihu/moco/{kind}/main/{target.relative_to(ROOT).as_posix()}'
            if parsed.query:
                dest += '?' + parsed.query
            if parsed.fragment:
                dest += '#' + parsed.fragment
            return f'[{label}]({dest})'
        body = re.sub(r'\[([^\]]+)\]\(([^\s)]+)\)', link, source.read_text())
        front = f'---\ntitle: {json.dumps(title)}\ndescription: {json.dumps(description)}\nlanguage: en\n---\n\n'
        dest = content / route / 'index.md'
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(front + body)
    media = content / 'media'
    media.mkdir()
    shutil.copyfile(ROOT / 'site/assets/architecture.svg', media / 'architecture.svg')


def main():
    os.chdir(ROOT)
    verify_brand()
    build = ROOT / 'site/.build'
    build.mkdir(parents=True, exist_ok=True)
    (ROOT / 'dist').mkdir(exist_ok=True)
    stage_content(build)
    config = (ROOT / 'site.yaml').read_text()
    version = os.environ.get('SITE_VERSION', 'v0.0.3')
    if not re.fullmatch(r'v?\d+\.\d+\.\d+(?:\+[0-9A-Za-z.-]+)?', version):
        raise ValueError('SITE_VERSION must be a stable SemVer release')
    config = re.sub(r'^  version: .+$', f'  version: {version}', config, flags=re.MULTILINE)
    # A root-level temporary config preserves paths relative to site.yaml.
    with tempfile.TemporaryDirectory(prefix='publication.', dir=build) as temp:
        stage = Path(temp)
        config_path = ROOT / '.site-build.yaml'
        if config_path.exists():
            raise ValueError('Another site build is active (.site-build.yaml exists)')
        try:
            config_path.write_text(config)
            run('tools/bin/site/margo', 'site', str(config_path), '--output-dir', str(stage / 'public'), '--diagnostics', 'text', report=build / 'margo-report.json')
        finally:
            config_path.unlink(missing_ok=True)
        run('tools/bin/site/manja', 'export', '--renderer-config', 'site/renderer.yaml', '--data-dir', str(build / 'manja-data'), '--output', str(stage / 'manja'), '--base-path', '/openapi/', report=build / 'manja-report.json')
        run('tools/bin/site/manja', 'export', 'verify', '--output', str(stage / 'manja'), report=build / 'manja-verification.json')
        shutil.move(stage / 'manja', stage / 'public/openapi')
        shutil.copyfile(ROOT / 'site/headers', stage / 'public/_headers')
        shutil.copytree(ROOT / 'site/assets/brand', stage / 'public/site/assets/brand', dirs_exist_ok=True)
        # Enrich only Margo HTML; Manja bytes remain covered by its export receipt.
        os.environ['SITE_VERSION'] = version
        run('python3', '-B', 'scripts/site_structured_data.py', str(stage / 'public'))
        run('python3', '-B', 'scripts/check_site.py', str(stage / 'public'))
        output = ROOT / 'dist/site'
        previous = stage / 'previous'
        if output.exists():
            output.rename(previous)
        try:
            (stage / 'public').rename(output)
        except OSError:
            if previous.exists():
                previous.rename(output)
            raise
    print('Static site ready: dist/site')


if __name__ == '__main__':
    main()

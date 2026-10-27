#!/usr/bin/env python3
"""Import and verify the site's unchanged artwork from a published Assets release."""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent
DESTINATION = ROOT / 'site/assets/brand'
REPOSITORY = 'araihu/assets'
VERSION = 'v0.2.6'
FILES = {
    'logo-light.svg': 'brand/moco/logo/light-transparent-optical.svg',
    'logo-dark.svg': 'brand/moco/logo/dark-transparent-optical.svg',
    'icon-dark.svg': 'icons/brand/moco-icon-dark-transparent-optical.svg',
    'icon.svg': 'icons/brand/moco-icon-dark-plate-optical.svg',
    'favicon.svg': 'platform/web/moco/favicon.svg',
    'NOTICE': 'NOTICE',
    'LICENSE': 'licenses/Apache-2.0.txt',
}


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def checksums(data):
    result = {}
    for line in data.decode().splitlines():
        match = re.fullmatch(r'([a-f0-9]{64})  (.+)', line)
        require(match is not None, 'Invalid checksum record')
        digest, path = match.groups()
        require(path not in result, f'Duplicate checksum: {path}')
        result[path] = digest
    return result


def gh(*arguments):
    return subprocess.check_output(['gh', *arguments])


def update():
    release = json.loads(gh('api', f'repos/{REPOSITORY}/releases/tags/{VERSION}'))
    require(release['tag_name'] == VERSION and not release['draft'] and not release['prerelease'],
            'Assets must be a published stable release')
    commit = json.loads(gh('api', f'repos/{REPOSITORY}/commits/{VERSION}'))['sha']
    require(re.fullmatch(r'[a-f0-9]{40}', commit), 'Invalid source commit')
    archive_name = f'araihu-assets-{VERSION}.tar.gz'
    with tempfile.TemporaryDirectory(prefix='moco-brand-download-') as directory:
        temp = Path(directory)
        subprocess.run(['gh', 'release', 'download', VERSION, '--repo', REPOSITORY,
                        '--pattern', archive_name, '--pattern', 'SHA256SUMS', '--dir', directory], check=True)
        archive = temp / archive_name
        outer = checksums((temp / 'SHA256SUMS').read_bytes())
        archive_hash = sha256(archive.read_bytes())
        require(outer.get(archive_name) == archive_hash, 'Assets archive checksum mismatch')
        members = {}
        total_size = 0
        with tarfile.open(archive, 'r:gz') as bundle:
            for member in bundle:
                path = PurePosixPath(member.name)
                require(member.isfile() and not path.is_absolute() and '..' not in path.parts
                        and str(path) == member.name and member.name not in members,
                        f'Unsafe or duplicate archive member: {member.name}')
                total_size += member.size
                require(member.size <= 32 * 1024 * 1024 and total_size <= 256 * 1024 * 1024,
                        'Assets archive exceeds import bounds')
                members[member.name] = bundle.extractfile(member).read()
        inner = checksums(members['checksums.txt'])
        require(set(members) == set(inner) | {'checksums.txt'}, 'Assets file inventory mismatch')
        for path, digest in inner.items():
            require(sha256(members[path]) == digest, f'Assets checksum mismatch: {path}')
        catalog_bytes = members['catalog.json']
        catalog = json.loads(catalog_bytes)
        metadata = json.loads(members['release.json'])
        require(catalog['release'] == VERSION and metadata['release'] == VERSION,
                'Assets release metadata mismatch')
        require(metadata['catalogSha256'] == sha256(catalog_bytes), 'Catalog identity mismatch')
        tagged_catalog = gh('api', f'repos/{REPOSITORY}/contents/dist/catalog.json?ref={commit}',
                            '--header', 'Accept: application/vnd.github.raw+json')
        require(tagged_catalog == catalog_bytes, 'Published catalog differs from tagged source')
        indexed = {asset['path']: asset for asset in catalog['assets']}
        records = {}
        for local, source in FILES.items():
            digest = sha256(members[source])
            if local.endswith('.svg'):
                entry = indexed[source]
                require(entry['product'] == 'moco' and entry['sha256'] == digest,
                        f'Catalog identity mismatch: {source}')
            records[local] = {'sourcePath': source, 'sha256': digest}
        receipt = {
            'schemaVersion': 1, 'repository': REPOSITORY, 'release': VERSION,
            'sourceCommit': commit, 'releaseURL': release['html_url'],
            'archive': {'name': archive_name, 'sha256': archive_hash},
            'catalogSha256': sha256(catalog_bytes), 'files': records,
        }
        DESTINATION.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix='.brand-import-', dir=DESTINATION.parent) as publication:
            stage = Path(publication) / 'brand'
            stage.mkdir()
            for local, source in FILES.items():
                (stage / local).write_bytes(members[source])
            (stage / 'provenance.json').write_text(json.dumps(receipt, indent=2) + '\n')
            previous = Path(publication) / 'previous'
            if DESTINATION.exists():
                DESTINATION.rename(previous)
            try:
                stage.rename(DESTINATION)
            except OSError:
                if previous.exists():
                    previous.rename(DESTINATION)
                raise
    verify()


def verify():
    receipt = json.loads((DESTINATION / 'provenance.json').read_text())
    require(receipt['schemaVersion'] == 1 and receipt['repository'] == REPOSITORY
            and receipt['release'] == VERSION, 'Unexpected brand provenance')
    require(re.fullmatch(r'[a-f0-9]{40}', receipt['sourceCommit']), 'Invalid brand source commit')
    require(set(receipt['files']) == set(FILES), 'Unexpected brand file set')
    require({p.name for p in DESTINATION.iterdir()} == set(FILES) | {'provenance.json'},
            'Unexpected file in brand directory')
    for local, source in FILES.items():
        record = receipt['files'][local]
        require(record['sourcePath'] == source and sha256((DESTINATION / local).read_bytes()) == record['sha256'],
                f'Brand artwork changed: {local}; import the published release instead')
    print(f'Verified Moco artwork from {REPOSITORY} {VERSION}.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['update', 'verify'], nargs='?', default='verify')
    arguments = parser.parse_args()
    update() if arguments.command == 'update' else verify()

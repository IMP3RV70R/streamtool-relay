#!/usr/bin/env python3
"""Create a release archive without host xattrs, links or unchecksummed entries."""
import argparse
import hashlib
import os
from pathlib import Path
import tarfile


def package(root: Path, destination: Path):
    if destination.exists() or destination.is_symlink():
        raise FileExistsError('Archive destination already exists')
    root = root.resolve(strict=True)
    entries = sorted(root.rglob('*'))
    for entry in entries:
        if entry.is_symlink() or not (entry.is_file() or entry.is_dir()):
            raise ValueError('Release tree contains links or special files')
        name = entry.relative_to(root).as_posix()
        if any(character in name for character in '\n\r\t\\') or any(part.startswith('._') for part in entry.relative_to(root).parts):
            raise ValueError('Invalid release filename or host metadata')
    files = [entry for entry in entries if entry.is_file() and entry != root / 'SHA256SUMS']
    # Recreate the exact complete list; it is consistent metadata, not a signature.
    with (root / 'SHA256SUMS').open('w', encoding='utf-8') as sums:
        for entry in files:
            with entry.open('rb') as source:
                digest = hashlib.file_digest(source, 'sha256').hexdigest()
            sums.write(digest + '  ' + entry.relative_to(root).as_posix() + '\n')
    created = False
    try:
        with destination.open('xb') as output:
            created = True
            with tarfile.open(fileobj=output, mode='w:gz', format=tarfile.PAX_FORMAT, dereference=True) as archive:
                for entry in [*files, root / 'SHA256SUMS']:
                    # Adding individual regular files avoids hidden platform metadata
                    # and directory recursion. No xattrs are copied into PAX records.
                    archive.add(entry, arcname=entry.relative_to(root).as_posix(), recursive=False)
            output.flush()
            os.fsync(output.fileno())
    except BaseException:
        if created:
            destination.unlink(missing_ok=True)
        raise


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('root', type=Path)
    parser.add_argument('destination', type=Path)
    arguments = parser.parse_args()
    package(arguments.root, arguments.destination)

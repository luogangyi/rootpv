#!/usr/bin/env python3
# Copyright (c) 2026 OpenFuyao Contributors.
# Licensed under the Mulan Permissive Software License, Version 2.
# See LICENSE for details.
"""Replace a domain consistently in Git-tracked UTF-8 files.

The default is a preview. Use --apply to write changes or --check for CI.
Literal domains and one/two levels of dot escaping (for JSONPath) are supported.
Ignored files, untracked local configuration, symlinks and this script are skipped.
"""

import argparse
from pathlib import Path
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--from-domain", default="ecloud.com")
    parser.add_argument("--to-domain", default="openfuyao.cn")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--apply", action="store_true", help="write replacements")
    mode.add_argument("--check", action="store_true", help="fail if old domain remains")
    args = parser.parse_args()
    if not args.from_domain or not args.to_domain or args.from_domain == args.to_domain:
        parser.error("domains must be nonempty and different")

    root = Path(subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True
    ).strip())
    paths = subprocess.check_output(["git", "ls-files", "-z"], cwd=root).split(b"\0")
    changes = []
    for name in paths:
        if not name:
            continue
        path = root / name.decode("utf-8", errors="surrogateescape")
        if path.is_symlink() or not path.is_file() or path.resolve() == Path(__file__).resolve():
            continue
        data = path.read_bytes()
        if b"\0" in data:
            continue
        try:
            content = data.decode("utf-8")
        except UnicodeDecodeError:
            continue
        # JSONPath keys contain escaped dots; preserve their escape depth.
        replacements = {
            (args.from_domain, args.to_domain),
            (args.from_domain.replace(".", r"\."), args.to_domain.replace(".", r"\.")),
            (args.from_domain.replace(".", r"\\."), args.to_domain.replace(".", r"\\.")),
        }
        count = 0
        for source, destination in sorted(replacements, key=lambda pair: len(pair[0]), reverse=True):
            count += content.count(source)
            content = content.replace(source, destination)
        if count:
            changes.append((path, content, count))

    for path, content, count in changes:
        print(f"{path.relative_to(root)}: {count}")
        if args.apply:
            path.write_bytes(content.encode("utf-8"))
    action = "Replaced" if args.apply else "Found"
    print(f"{action} {sum(count for _, _, count in changes)} occurrences in {len(changes)} files")
    return 1 if args.check and changes else 0


if __name__ == "__main__":
    raise SystemExit(main())

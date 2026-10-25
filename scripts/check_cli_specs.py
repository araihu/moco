#!/usr/bin/env python3
"""Require complete CLI naming metadata without changing the API contract."""
import argparse
import json
from pathlib import Path
import re

METHODS = {"get", "put", "post", "delete", "options", "head", "patch", "trace"}
NAME = re.compile(r"[a-z][a-z0-9]*(?:-[a-z0-9]+)*\Z")


def operations(document):
    return {(path, method): operation
            for path, item in document["paths"].items()
            for method, operation in item.items() if method in METHODS}


def strip_metadata(value, allowed_paths, path=()):
    if isinstance(value, list):
        return [strip_metadata(item, allowed_paths, (*path, index)) for index, item in enumerate(value)]
    if not isinstance(value, dict):
        return value
    result = {}
    for key, item in value.items():
        if key.startswith("x-cli-"):
            if path not in allowed_paths or key not in {"x-cli-name", "x-cli-group"}:
                raise ValueError("CLI metadata must be operation naming metadata in the CLI bundle")
        else:
            result[key] = strip_metadata(item, allowed_paths, (*path, key))
    return result


def validate(base, cli):
    if strip_metadata(base, set()) != base:
        raise ValueError("base contract contains CLI metadata")
    original, decorated = operations(base), operations(cli)
    if not original or original.keys() != decorated.keys():
        raise ValueError("CLI bundle changed the operation set")
    locations = {("paths", path, method) for path, method in original}
    if strip_metadata(cli, locations) != base:
        raise ValueError("CLI overlay changed the API contract beyond CLI metadata")
    commands, ids = set(), set()
    for key, operation in decorated.items():
        operation_id = operation.get("operationId")
        if not isinstance(operation_id, str) or not operation_id or operation_id in ids:
            raise ValueError("operationId must be present and unique")
        ids.add(operation_id)
        command = tuple(operation.get(field) for field in ("x-cli-group", "x-cli-name"))
        if any(not isinstance(part, str) or not NAME.fullmatch(part) for part in command):
            raise ValueError(f"missing or invalid CLI metadata for {operation_id}")
        if command in commands:
            raise ValueError(f"duplicate CLI command for {operation_id}")
        commands.add(command)
    return len(commands)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", type=Path, required=True)
    parser.add_argument("--cli", type=Path, required=True)
    options = parser.parse_args()
    count = validate(json.loads(options.base.read_text()), json.loads(options.cli.read_text()))
    print(json.dumps({"cliContract": "passed", "operations": count, "contractUnchanged": True}), flush=True)


if __name__ == "__main__":
    main()

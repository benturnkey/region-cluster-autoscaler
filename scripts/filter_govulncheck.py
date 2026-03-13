#!/usr/bin/env python3

import argparse
import json
import sys
from collections import defaultdict


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--allow", action="append", default=[])
    args = parser.parse_args()

    allowed = set(args.allow)
    osv_meta = {}
    findings = defaultdict(lambda: {"modules": set(), "packages": set(), "fixed_versions": set()})

    decoder = json.JSONDecoder()
    buffer = sys.stdin.read()
    index = 0

    while index < len(buffer):
        while index < len(buffer) and buffer[index].isspace():
            index += 1
        if index >= len(buffer):
            break

        event, next_index = decoder.raw_decode(buffer, index)
        index = next_index

        osv = event.get("osv")
        if osv:
            osv_meta[osv["id"]] = {
                "summary": osv.get("summary", ""),
                "url": osv.get("database_specific", {}).get("url", ""),
            }
            continue

        finding = event.get("finding")
        if not finding:
            continue

        vuln_id = finding["osv"]
        entry = findings[vuln_id]
        if finding.get("fixed_version"):
            entry["fixed_versions"].add(finding["fixed_version"])

        for trace in finding.get("trace", []):
            module = trace.get("module")
            package = trace.get("package")
            version = trace.get("version")
            if module:
                if version:
                    entry["modules"].add(f"{module}@{version}")
                else:
                    entry["modules"].add(module)
            if package:
                entry["packages"].add(package)

    unexpected = sorted(vuln_id for vuln_id in findings if vuln_id not in allowed)
    accepted = sorted(vuln_id for vuln_id in findings if vuln_id in allowed)

    if accepted:
        print("Accepted vulnerabilities:")
        for vuln_id in accepted:
            meta = osv_meta.get(vuln_id, {})
            print(f"- {vuln_id}: {meta.get('summary', '')}")

    if unexpected:
        print("Unexpected vulnerabilities found:")
        for vuln_id in unexpected:
            meta = osv_meta.get(vuln_id, {})
            print(f"- {vuln_id}: {meta.get('summary', '')}")

            modules = sorted(findings[vuln_id]["modules"])
            if modules:
                print(f"  modules: {', '.join(modules)}")

            fixed_versions = sorted(findings[vuln_id]["fixed_versions"])
            if fixed_versions:
                print(f"  fixed versions: {', '.join(fixed_versions)}")

            url = meta.get("url")
            if url:
                print(f"  more info: {url}")

        return 1

    if findings:
        print("Only accepted vulnerabilities were found.")
    else:
        print("No vulnerabilities found.")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())

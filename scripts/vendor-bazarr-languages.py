#!/usr/bin/env python3
"""Regenerate internal/lang/bazarr_languages.json from Bazarr's subsarr converter.

The names Bazarr sends are the only names subsarr may store: a subtitle filed
under any other spelling is unreachable. Run this after a Bazarr rename, commit
the result, and let internal/lang/lang_test.go tell you what broke.

    python3 scripts/vendor-bazarr-languages.py [--ref master]
"""

import argparse
import json
import re
import subprocess
import urllib.request

REPO = "https://github.com/morpheus65535/bazarr"
RAW = "https://raw.githubusercontent.com/morpheus65535/bazarr/{ref}/{path}"
API = "https://api.github.com/repos/morpheus65535/bazarr/commits?path={path}&sha={ref}&per_page=1"

CONVERTER = "custom_libs/subliminal_patch/converters/subsarr.py"
SUBSOURCE = "custom_libs/subliminal_patch/converters/subsource.py"

# SubsarrConverter._NAME_OVERRIDES — names subsarr spells differently.
OVERRIDES = {
    "Khmer": "cambodian-khmer",
    "Pushto": "pashto",
    "Espranto": "esperanto",
    "Ukrainian": "ukranian",
}
# SubsarrConverter._EXTRA_LANGUAGES — present in subsarr, missing from subsource.
EXTRA = {"kinyarwanda": ["kin"], "punjabi": ["pan"], "sundanese": ["sun"], "yoruba": ["yor"]}


def fetch(url):
    with urllib.request.urlopen(url) as response:
        return response.read().decode("utf-8")


def to_subsarr(name):
    return OVERRIDES.get(name, name.lower().replace(" ", "-"))


def parse(source):
    languages = {}
    for line in source.splitlines():
        if line.strip().startswith("#"):  # a commented-out, unsupported language
            continue
        match = re.search(r"'([^']+)':\s*\(([^)]*)\)", line)
        if not match:
            continue
        codes = [part.strip().strip("'") for part in match.group(2).split(",") if part.strip()]
        if codes:
            languages[to_subsarr(match.group(1))] = codes
    return languages


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--ref", default="master")
    parser.add_argument("--out", default="internal/lang/bazarr_languages.json")
    args = parser.parse_args()

    languages = parse(fetch(RAW.format(ref=args.ref, path=SUBSOURCE)))
    languages.update(EXTRA)

    commits = {}
    for path in (CONVERTER, SUBSOURCE):
        commits[path] = json.loads(fetch(API.format(ref=args.ref, path=path)))[0]["sha"]

    document = {
        "_source": {
            "description": (
                "Language names Bazarr's subsarr provider sends and accepts, generated from its "
                "converter by scripts/vendor-bazarr-languages.py. Regenerate after a Bazarr "
                "rename; internal/lang/lang_test.go fails when a name subsarr stores is no longer "
                "a name Bazarr can request."
            ),
            "repository": REPO,
            "files": commits,
            "vendored_at": subprocess.run(
                ["date", "-u", "+%Y-%m-%d"], capture_output=True, text=True, check=True
            ).stdout.strip(),
        },
        "languages": dict(sorted(languages.items())),
    }

    with open(args.out, "w", encoding="utf-8") as handle:
        handle.write(json.dumps(document, indent=2, ensure_ascii=False) + "\n")
    print(f"wrote {len(languages)} languages to {args.out}")


if __name__ == "__main__":
    main()

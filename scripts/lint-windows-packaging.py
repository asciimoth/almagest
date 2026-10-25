#!/usr/bin/env python3
"""Check the structural rules that apply to tracked WiX package sources."""

from pathlib import Path
import sys
import xml.etree.ElementTree as ET


ROOT = Path(__file__).resolve().parents[1]
WIX_NAMESPACE = "http://wixtoolset.org/schemas/v4/wxs"


def validate(path):
    try:
        root = ET.parse(path).getroot()
    except ET.ParseError as error:
        raise ValueError(f"{path.relative_to(ROOT)}: invalid XML: {error}") from error
    namespace, separator, local_name = root.tag[1:].partition("}")
    if not separator or namespace != WIX_NAMESPACE:
        raise ValueError(
            f"{path.relative_to(ROOT)}: root must use the WiX v4 namespace"
        )
    if local_name not in {"Package", "Wix"}:
        raise ValueError(
            f"{path.relative_to(ROOT)}: unexpected WiX root element {local_name}"
        )


def main():
    sources = sorted((ROOT / "packaging/windows").glob("*.wxs"))
    if not sources:
        print("no Windows WiX sources found", file=sys.stderr)
        return 1
    try:
        for source in sources:
            validate(source)
    except (OSError, ValueError) as error:
        print(error, file=sys.stderr)
        return 1
    print(f"Validated {len(sources)} WiX source files.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

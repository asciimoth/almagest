#!/usr/bin/env python3
"""Convert the project license to deterministic RTF for the MSI UI."""

import argparse
from pathlib import Path


def escape_rtf(text: str) -> str:
    output = []
    for character in text:
        if character in "\\{}":
            output.append("\\" + character)
        elif character == "\t":
            output.append("\\tab ")
        elif character == "\n":
            output.append("\\par\n")
        elif ord(character) <= 0x7F:
            output.append(character)
        else:
            value = ord(character)
            if value > 0x7FFF:
                value -= 0x10000
            output.append(f"\\u{value}?")
    return "".join(output)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()

    license_text = args.source.read_text(encoding="utf-8").replace("\r\n", "\n")
    rtf = "{\\rtf1\\ansi\\deff0{\\fonttbl{\\f0 Consolas;}}\n\\f0\\fs18\n"
    rtf += escape_rtf(license_text)
    rtf += "}\n"
    args.output.write_text(rtf, encoding="ascii", newline="\n")


if __name__ == "__main__":
    main()

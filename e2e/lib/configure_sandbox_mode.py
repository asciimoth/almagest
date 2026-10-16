#!/usr/bin/env python3
"""Set the E2E sandbox policy without parsing unrelated HuJSON syntax."""

import re
import sys
from pathlib import Path


SANDBOX_KEY = re.compile(r'"Sandbox"\s*:', re.IGNORECASE)
MODE_PLACEHOLDER = "%E2E_SANDBOX_MODE%"


def configure(path: Path, mode: str) -> None:
    if mode not in {"off", "auto", "strict"}:
        raise ValueError(f"invalid sandbox mode: {mode}")
    text = path.read_text(encoding="utf-8")
    if MODE_PLACEHOLDER in text:
        path.write_text(text.replace(MODE_PLACEHOLDER, mode), encoding="utf-8")
        return
    if SANDBOX_KEY.search(text):
        return
    offset = text.find("{")
    if offset < 0:
        raise ValueError(f"protected config is not an object: {path}")
    addition = f'\n\t"Sandbox": {{"Mode": "{mode}"}},'
    path.write_text(text[: offset + 1] + addition + text[offset + 1 :], encoding="utf-8")


def main() -> None:
    if len(sys.argv) < 3:
        raise SystemExit("usage: configure_sandbox_mode.py MODE FILE...")
    for value in sys.argv[2:]:
        configure(Path(value), sys.argv[1])


if __name__ == "__main__":
    main()

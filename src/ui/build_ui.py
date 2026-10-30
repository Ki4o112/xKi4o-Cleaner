#!/usr/bin/env python3
"""Builds ui/index.html (embedded into the exe) from ui/app.html.

It inlines the Inter font (Latin + Cyrillic), the logo (SVG) and the license texts of
the third-party components shown on the Credits page, so the app needs no
files next to it and works offline.

    python ui/build_ui.py
"""
import base64
import json
import pathlib

UI = pathlib.Path(__file__).resolve().parent
ROOT = UI.parent

RANGES = {
    "cyrillic": "U+0301, U+0400-045F, U+0490-0491, U+04B0-04B1, U+2116",
    "latin": "U+0000-00FF, U+0131, U+0152-0153, U+02BB-02BC, U+02C6, U+02DA, U+02DC, U+0304, "
             "U+0308, U+0329, U+2000-206F, U+20AC, U+2122, U+2191, U+2193, U+2212, U+2215, U+FEFF, U+FFFD",
}


def main():
    src = (UI / "app.html").read_text(encoding="utf-8")

    faces = []
    for subset, rng in RANGES.items():
        data = base64.b64encode((UI / "fonts" / f"inter-{subset}-wght-normal.woff2").read_bytes()).decode()
        faces.append('@font-face{font-family:"Inter";font-style:normal;font-display:block;'
                     f'font-weight:100 900;src:url(data:font/woff2;base64,{data}) format("woff2");unicode-range:{rng};}}')
    src = src.replace("/*@@FONTS@@*/", "\n".join(faces), 1)

    logo = base64.b64encode((UI / "logo.svg").read_bytes()).decode()
    assert "url(logo.svg)" in src
    src = src.replace("url(logo.svg)", f"url(data:image/svg+xml;base64,{logo})")

    licenses = {
        "mit": (ROOT / "third_party/go-webview2/LICENSE").read_text(encoding="utf-8"),
        "isc": (ROOT / "third_party/licenses/go-winloader-LICENSE.md").read_text(encoding="utf-8"),
        "bsd": (ROOT / "third_party/licenses/golang-x-sys-LICENSE").read_text(encoding="utf-8"),
        "ofl": (UI / "fonts/OFL.txt").read_text(encoding="utf-8"),
        "ms": (ROOT / "third_party/go-webview2/webviewloader/sdk/LICENSE.txt").read_text(encoding="utf-8"),
        "nvapi": (ROOT / "third_party/licenses/nvapi-LICENSE").read_text(encoding="utf-8"),
        "winutil": (ROOT / "third_party/licenses/winutil-LICENSE").read_text(encoding="utf-8"),
        "w11d": (ROOT / "debloat/LICENSE-Win11Debloat").read_text(encoding="utf-8"),
    }
    src = src.replace("/*@@LICENSES@@*/{}", json.dumps(licenses, ensure_ascii=False), 1)

    (UI / "index.html").write_text(src, encoding="utf-8", newline="\n")
    print(f"ui/index.html written ({len(src.encode()) // 1024} KB)")


if __name__ == "__main__":
    main()

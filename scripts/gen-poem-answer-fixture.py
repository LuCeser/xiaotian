# gen-poem-answer-fixture.py — renders the Issue #12 背默 answer-sheet
# fixture (tests/e2e/fixtures/photo-zh-poem-answer.jpg).
#
# The image is a REAL rendered notebook page in the child-handwriting
# register the OCR contract describes: the poem 《静夜思》 written from
# memory, one verse line per row, with ONE real mistake on line 4
# (低头思故香 — 香 for 乡). The local mimo stub answers the vision call
# with the canned sibling .reply.json (verbatim transcription, mistake
# included); the stub stands in for "what the real model would return for
# THIS image".
#
# JPEG (not PNG) on purpose: the check-stage OCR pipeline (ocr.Pipeline)
# validates the JPEG SOI signature — the photo_chunk channel only ever
# carries camera JPEGs.
#
# Regenerate: python3 scripts/gen-poem-answer-fixture.py
# (The stub matches BYTES — regenerate the pair together.)
"""Render the whole-poem 背默 answer-sheet fixture for the Issue #12 e2e suite."""

from __future__ import annotations

import pathlib

from PIL import Image, ImageDraw, ImageFont

REPO = pathlib.Path(__file__).resolve().parent.parent
OUT = REPO / "tests" / "e2e" / "fixtures"

FONT_CANDIDATES = [
    "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
    "C:/Windows/Fonts/msyh.ttc",
]


def load_font(size: int) -> ImageFont.FreeTypeFont:
    for path in FONT_CANDIDATES:
        if pathlib.Path(path).exists():
            return ImageFont.truetype(path, size)
    raise SystemExit("no CJK font found: install Noto Sans CJK or run on Windows")


def main() -> None:
    font = load_font(44)
    small = load_font(26)
    img = Image.new("RGB", (760, 560), "#f7f4ec")
    d = ImageDraw.Draw(img)

    d.text((48, 36), "默写：《静夜思》", font=small, fill="#9aa0a6")
    lines = [
        "床前明月光",
        "疑是地上霜",
        "举头望明月",
        "低头思故香",  # the real mistake the OCR must transcribe verbatim
    ]
    y = 110
    for line in lines:
        d.text((56, y), line, font=font, fill="#2a2f36")
        y += 92

    d.line((48, y + 12, 712, y + 12), fill="#d9d2c0", width=2)

    OUT.mkdir(parents=True, exist_ok=True)
    img.save(OUT / "photo-zh-poem-answer.jpg", "JPEG", quality=88)
    print(f"wrote {OUT / 'photo-zh-poem-answer.jpg'}")


if __name__ == "__main__":
    main()

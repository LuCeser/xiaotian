# gen-poem-fixture.py — renders the Issue #11 browser-e2e mixed 语文 page
# fixture (tests/e2e/fixtures/photo-zh-poem.png).
#
# The image is a REAL rendered page in the printed-textbook register the
# recognition contract describes: a 词语表 with pinyin annotations, a
# COMPLETE poem opened by its 诗题 (title, author, verse lines, an
# explicit 背诵 mark), a body-text paragraph and an exercise block. The
# local mimo stub (tests/e2e/helpers/mimo-stub.js) answers the vision
# call with the canned sibling .reply.json — the stub stands in for
# "what the real model would return for THIS image".
#
# Regenerate: python3 scripts/gen-poem-fixture.py
# (Windows runs fall back to the C:/Windows/Fonts copies — same output
# register; only the rasterizer differs, the stub matches BYTES, so
# regenerate the pair together.)
"""Render the mixed 语文 page photo fixture for the Issue #11 e2e suite."""

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
    title_font = load_font(30)
    text_font = load_font(24)
    muted_font = load_font(20)

    img = Image.new("RGB", (760, 880), "white")
    draw = ImageDraw.Draw(img)

    draw.text((40, 24), "语文园地二", fill="#111111", font=title_font)
    draw.line((40, 68, 720, 68), fill="#999999", width=2)

    y = 88
    draw.text((48, y), "词语表", fill="#111111", font=text_font)
    y += 40
    for word, pinyin in [("美丽", "měi lì"), ("世界", "shì jiè")]:
        draw.text((64, y), word, fill="#111111", font=text_font)
        draw.text((260, y), pinyin, fill="#333333", font=text_font)
        y += 38

    y += 24
    draw.text((48, y), "古诗背诵", fill="#111111", font=text_font)
    y += 42
    draw.text((64, y), "静夜思", fill="#111111", font=title_font)
    draw.text((260, y + 6), "〔唐〕李白", fill="#333333", font=text_font)
    y += 48
    for line in ["床前明月光，疑是地上霜。", "举头望明月，低头思故乡。"]:
        draw.text((64, y), line, fill="#111111", font=text_font)
        y += 38
    draw.text((64, y), "（背诵）", fill="#b45309", font=muted_font)
    y += 44

    draw.text((48, y), "课文节选：村居里，孩子们放学回来得早，赶忙趁着东风放起了纸鸢。",
              fill="#666666", font=muted_font)
    y += 40
    draw.text((48, y), "练习：读一读，记一记，再抄写一遍。", fill="#666666", font=muted_font)

    OUT.mkdir(parents=True, exist_ok=True)
    img.save(OUT / "photo-zh-poem.png")
    print(f"wrote {OUT / 'photo-zh-poem.png'}")


if __name__ == "__main__":
    main()

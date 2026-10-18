# gen-passage-fixture.py — renders the Issue #13 browser-e2e mixed 语文 page
# fixture (tests/e2e/fixtures/photo-zh-passage.png).
#
# The image is a REAL rendered page in the printed-textbook register the
# recognition contract describes: a 词语表 with pinyin annotations, a
# COMPLETE passage opened by its 课文标题 with FOUR 自然段 and an explicit
# per-paragraph 背诵 mark (背诵课文第2自然段), and an exercise block. The
# local mimo stub (tests/e2e/helpers/mimo-stub.js) answers the vision call
# with the canned sibling .reply.json — the stub stands in for "what the
# real model would return for THIS image".
#
# Regenerate: python3 scripts/gen-passage-fixture.py
# (the stub matches BYTES, so regenerate the pair together.)
"""Render the mixed 语文 passage-page photo fixture for the Issue #13 e2e suite."""

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

    img = Image.new("RGB", (760, 980), "white")
    draw = ImageDraw.Draw(img)

    draw.text((40, 24), "第二单元", fill="#111111", font=title_font)
    draw.line((40, 68, 720, 68), fill="#999999", width=2)

    y = 88
    draw.text((48, y), "词语表", fill="#111111", font=text_font)
    y += 40
    for word, pinyin in [("钥匙", "yào shi"), ("颜料", "yán liào")]:
        draw.text((64, y), word, fill="#111111", font=text_font)
        draw.text((260, y), pinyin, fill="#333333", font=text_font)
        y += 38

    y += 24
    draw.text((48, y), "6 秋天的雨", fill="#111111", font=title_font)
    y += 48
    for paragraph in [
        "秋天的雨，是一把钥匙。",
        "秋天的雨，有一盒五彩缤纷的颜料。",
        "秋天的雨，藏着非常好闻的气味。",
        "秋天的雨，吹起了金色的小喇叭。",
    ]:
        draw.text((64, y), paragraph, fill="#111111", font=text_font)
        y += 38
    draw.text((64, y), "（背诵课文第2自然段）", fill="#b45309", font=muted_font)
    y += 44

    draw.text((48, y), "练习：读一读，抄写第二自然段中的一句话。",
              fill="#666666", font=muted_font)

    OUT.mkdir(parents=True, exist_ok=True)
    img.save(OUT / "photo-zh-passage.png")
    print(f"wrote {OUT / 'photo-zh-passage.png'}")


if __name__ == "__main__":
    main()

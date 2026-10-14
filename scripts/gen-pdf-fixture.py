# gen-pdf-fixture.py — renders the Issue #7 scanned-PDF browser-e2e
# fixtures (tests/e2e/fixtures/pdf-*.pdf).
#
# The PDFs are REAL multi-page image PDFs (PIL save_all — each page an
# embedded raster, the shape a phone-scanner produces). Two consumers:
#   - the browser e2e, where the parent page splits them with pdf.js and
#     the local mimo stub (tests/e2e/helpers/mimo-stub.js) answers each
#     rendered page image with the canned <name>.reply.json — the stub
#     stands in for "what the real model would return for THIS page";
#   - manual real-service acceptance (iPad Safari checklist), where the
#     same PDFs feed the real vision service.
#
# The page IMAGES the stub registers are NOT produced here — they are the
# browser's own canvas renders, captured once via the capture spec
# (tests/e2e/pdf-capture.spec.js + HTA_CAPTURE_PDF_DIR; see ADR-0004).
#
# Regenerate: uv run --with pillow scripts/gen-pdf-fixture.py
# (PIL rides uv's --with; China mirror via UV_INDEX_URL if needed.)
"""Render the scanned-PDF fixtures for the Issue #7 e2e suite."""

from __future__ import annotations

import pathlib

from PIL import Image, ImageDraw, ImageFont

REPO = pathlib.Path(__file__).resolve().parent.parent
OUT = REPO / "tests" / "e2e" / "fixtures"

EN_FONT = "C:/Windows/Fonts/arial.ttf"
CN_FONT = "C:/Windows/Fonts/msyh.ttc"


def render_page(title: str, rows: list[tuple[str, str]]) -> Image.Image:
    img = Image.new("RGB", (760, 180 + 46 * len(rows) + 40), "white")
    draw = ImageDraw.Draw(img)
    title_font = ImageFont.truetype(CN_FONT, 30)
    word_font = ImageFont.truetype(EN_FONT, 26)
    cn_font = ImageFont.truetype(CN_FONT, 26)

    draw.text((40, 28), title, fill="#111111", font=title_font)
    draw.line((40, 74, 720, 74), fill="#999999", width=2)
    y = 96
    for word, meaning in rows:
        draw.text((48, y), word, fill="#111111", font=word_font)
        draw.text((330, y), meaning, fill="#333333", font=cn_font)
        y += 46
    return img


def save_pdf(path: pathlib.Path, pages: list[Image.Image]) -> None:
    pages[0].save(path, "PDF", save_all=True, append_images=pages[1:], resolution=150)
    print(f"wrote {path} ({path.stat().st_size} bytes, {len(pages)} pages)")


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)

    # Main fixture (test A: cross-lesson whole-textbook import). Page 3
    # repeats page 1's "pen" on purpose — the preview must flag the
    # cross-page duplicate (与第 N 行重复), never submit it twice.
    save_pdf(OUT / "pdf-textbook.pdf", [
        render_page("外研版 四年级上册 Module 1 第1课 单词表",
                    [("pen", "钢笔"), ("bag", "书包")]),
        render_page("外研版 四年级上册 Module 1 第2课 单词表",
                    [("cat", "猫"), ("dog", "狗")]),
        render_page("外研版 四年级上册 Module 1 第2课 单词表",
                    [("bird", "鸟"), ("pen", "钢笔")]),
        render_page("外研版 四年级上册 Module 1 第3课 单词表",
                    [("fish", "鱼")]),
    ])

    # Retry fixture (test B: page 2 fails first, 重试该页 succeeds; the
    # .failonce marker rides the captured page image, not this PDF).
    save_pdf(OUT / "pdf-retry.pdf", [
        render_page("北师大版 五年级上册 Unit 2 第1课 单词表",
                    [("goat", "山羊"), ("frog", "青蛙")]),
        render_page("北师大版 五年级上册 Unit 2 第2课 单词表",
                    [("duck", "鸭子")]),
        render_page("北师大版 五年级上册 Unit 2 第3课 单词表",
                    [("horse", "马")]),
    ])

    # Over-cap fixture (test C: 101 tiny pages — small file, page count
    # over the 100-page limit; the picker refuses before any processing).
    blank = Image.new("RGB", (60, 80), "white")
    draw = ImageDraw.Draw(blank)
    draw.text((8, 30), "p", fill="#111111")
    save_pdf(OUT / "pdf-toomany.pdf", [blank] * 101)


if __name__ == "__main__":
    main()

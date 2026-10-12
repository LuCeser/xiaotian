# gen-wordlist-fixture.py — renders the Issue #3 browser-e2e word-list
# photo fixtures (tests/e2e/fixtures/photo-wordlist*.png).
#
# The images are REAL rendered word lists (printed-textbook style: title,
# word/meaning table, plus body-text and exercise noise a correct
# recognition must skip). Two consumers share them:
#   - the browser e2e, where the local mimo stub (tests/e2e/helpers/
#     mimo-stub.js) answers with the canned <name>.reply.json — the stub
#     stands in for "what the real model would return for THIS image";
#   - the live recognition test (pkg/ocr/wordlist_live_test.go), which
#     sends the same image to the real vision service.
#
# Regenerate: uv run --with pillow scripts/gen-wordlist-fixture.py
# (PIL rides uv's --with; China mirror via UV_INDEX_URL if needed.)
"""Render the word-list photo fixtures for the Issue #3 e2e suites."""

from __future__ import annotations

import pathlib

from PIL import Image, ImageDraw, ImageFont

REPO = pathlib.Path(__file__).resolve().parent.parent
OUT = REPO / "tests" / "e2e" / "fixtures"

EN_FONT = "C:/Windows/Fonts/arial.ttf"
CN_FONT = "C:/Windows/Fonts/msyh.ttc"


def render(path: pathlib.Path, title: str, rows: list[tuple[str, str]], noise: list[str]) -> None:
    img = Image.new("RGB", (760, 180 + 46 * (len(rows) + len(noise)) + 40), "white")
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

    y += 18
    noise_font = ImageFont.truetype(CN_FONT, 22)
    for line in noise:
        draw.text((48, y), line, fill="#666666", font=noise_font)
        y += 46

    img.save(path, "PNG")
    print(f"wrote {path} ({path.stat().st_size} bytes)")


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    # Browser-e2e main fixture: 5 clear entries + noise. The stub reply
    # (photo-wordlist.reply.json) additionally carries a second "banana" —
    # the in-photo duplicate the preview must flag.
    render(
        OUT / "photo-wordlist.png",
        "Unit 3  Word List 单词表",
        [
            ("apple", "苹果"),
            ("banana", "香蕉"),
            ("ice cream", "冰激凌"),
            ("go for a walk", "散步"),
            ("river", "河流"),
        ],
        [
            "Read these words aloud, then copy each word twice.",
            "Exercise 1: Fill in the blanks with the words above.",
        ],
    )
    # Classroom-check fixture: two entries, minimal.
    render(
        OUT / "photo-wordlist2.png",
        "Unit 9  Word List 单词表",
        [("sun", "太阳"), ("moon", "月亮")],
        ["Copy each word three times."],
    )
    # Issue #5 fixtures: pages whose headers PRINT the textbook info the
    # stub replies claim to have read (photo-textbook-*.reply.json). The
    # PEP page's reply deliberately MISREADS the lesson (第2课 for 第1课) —
    # the e2e proves the batch correction governs the final attribution.
    render(
        OUT / "photo-textbook-pep.png",
        "人教版PEP 三年级上册 Unit 3 第1课 单词表",
        [("tiger", "老虎"), ("panda", "熊猫"), ("grape", "葡萄")],
        ["Read the words aloud twice."],
    )
    render(
        OUT / "photo-textbook-pep2.png",
        "人教版PEP 三年级上册 Unit 3 第2课 单词表",
        [("monkey", "猴子"), ("duck", "鸭子")],
        ["Copy each word twice."],
    )
    render(
        OUT / "photo-textbook-wai.png",
        "外研版 四年级下册 Module 2 第1课 单词表",
        [("lake", "湖"), ("boat", "小船")],
        ["Fill in the blanks."],
    )
    # Independence-fixture: a THIRD meta family so the two-book test never
    # collides with the PEP book another test in the same run created (the
    # per-run DB is shared; the preselect is meta-matched).
    render(
        OUT / "photo-textbook-jijiang.png",
        "冀教版 五年级上册 Unit 1 第1课 单词表",
        [("horse", "马"), ("sheep", "羊")],
        ["Read and copy."],
    )
    # Re-photo pair on a fourth family: same meta, next lesson — the
    # existing-book preselect + lesson-append path.
    render(
        OUT / "photo-textbook-lexin.png",
        "译林版 六年级下册 Unit 5 第1课 单词表",
        [("cow", "牛"), ("pig", "猪")],
        ["Listen and read."],
    )
    render(
        OUT / "photo-textbook-lexin2.png",
        "译林版 六年级下册 Unit 5 第2课 单词表",
        [("bird", "鸟"), ("frog", "青蛙")],
        ["Copy twice."],
    )


if __name__ == "__main__":
    main()

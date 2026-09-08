# -*- coding: utf-8 -*-
"""Generate handwriting-style JPEG fixtures for the Ticket 04 live OCR test.

One-off dev tool (throwaway venv, not part of the service). Renders dictation
answers with jittered per-character placement + slight rotation so the vision
model sees something closer to a student's page than typeset text. Outputs:
  cmd/service/testdata/handwriting-apple.jpg     (English dictation word)
  cmd/service/testdata/handwriting-piaoliang.jpg (Chinese dictation word)

Layout is measured, not guessed: each glyph is rendered into its own box
sized from the font's ink extent, and the canvas width follows the char
count (v1 clipped the last English char and overflowed the CJK glyphs —
the live OCR caught it: "appl" @0.90, prompt-echo @0.5).
"""
import random

from PIL import Image, ImageDraw, ImageFont

random.seed(20260908)

FONT_CANDIDATES = [
    r"C:\Windows\Fonts\msyh.ttc",    # Microsoft YaHei
    r"C:\Windows\Fonts\simhei.ttf",  # SimHei
    r"C:\Windows\Fonts\arial.ttf",
]

FONT_SIZE = 230
BOX = 470          # per-character square canvas
ADVANCE = 400      # x step between character boxes
MARGIN = 100
HEIGHT = 620


def pick_font():
    for path in FONT_CANDIDATES:
        try:
            with open(path, "rb"):
                return path
        except OSError:
            continue
    raise SystemExit("no usable system font found")


def render(word: str, out_path: str) -> None:
    font_path = pick_font()
    font = ImageFont.truetype(font_path, FONT_SIZE)

    width = MARGIN * 2 + ADVANCE * len(word)
    img = Image.new("L", (width, HEIGHT), 250)  # paper-ish off-white

    x = MARGIN
    for ch in word:
        ch_img = Image.new("L", (BOX, BOX), 0)
        ch_draw = ImageDraw.Draw(ch_img)
        # Measure the actual ink box and center it, so neither ascenders nor
        # CJK width can clip.
        bbox = ch_draw.textbbox((0, 0), ch, font=font)
        tx = (BOX - (bbox[2] - bbox[0])) // 2 - bbox[0]
        ty = (BOX - (bbox[3] - bbox[1])) // 2 - bbox[1]
        ch_draw.text((tx, ty), ch, font=font, fill=255)

        rot = random.uniform(-5, 5)
        ch_img = ch_img.rotate(rot, resample=Image.BICUBIC, fillcolor=0)
        # Grayscale ink, not pure black — like a pencil.
        ink = ch_img.point(lambda v: max(v, 30) if v > 40 else 0)
        y = 60 + random.randint(-15, 15)
        img.paste(45, (x, y), ink)
        x += ADVANCE

    img.save(out_path, "JPEG", quality=88)
    print(f"{out_path}: {img.size} chars={len(word)}")


if __name__ == "__main__":
    render("apple", "../../cmd/service/testdata/handwriting-apple.jpg")
    render("漂亮", "../../cmd/service/testdata/handwriting-piaoliang.jpg")

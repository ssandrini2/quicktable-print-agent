"""Draws the agent's tray icon (internal/tray/icon.ico): a white printer on QuickTable's orange.

Run it again only to change the icon: python scripts/make-icon.py (needs Pillow).
"""
from pathlib import Path

from PIL import Image, ImageDraw

ORANGE = (234, 88, 12, 255)
WHITE = (255, 255, 255, 255)
SIZE = 256  # drawn large, scaled down for each icon size


def draw() -> Image.Image:
    image = Image.new("RGBA", (SIZE, SIZE), (0, 0, 0, 0))
    d = ImageDraw.Draw(image)
    d.rounded_rectangle((8, 8, SIZE - 8, SIZE - 8), radius=56, fill=ORANGE)
    # Paper going in, the printer's body, the ticket coming out.
    d.rectangle((88, 48, 168, 104), fill=WHITE)
    d.rounded_rectangle((48, 96, 208, 176), radius=16, fill=WHITE)
    d.rectangle((80, 148, 176, 212), fill=WHITE, outline=ORANGE, width=10)
    d.rectangle((100, 172, 156, 180), fill=ORANGE)
    d.rectangle((100, 190, 140, 198), fill=ORANGE)
    d.ellipse((172, 112, 190, 130), fill=ORANGE)
    return image


if __name__ == "__main__":
    out = Path(__file__).resolve().parent.parent / "internal" / "tray" / "icon.ico"
    out.parent.mkdir(parents=True, exist_ok=True)
    draw().save(out, format="ICO", sizes=[(16, 16), (20, 20), (24, 24), (32, 32), (48, 48)], bitmap_format="bmp")
    print(f"wrote {out}")

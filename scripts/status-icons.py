"""Draws the tray icon's "something is off" variants from internal/tray/icon.ico.

    python scripts/status-icons.py

icon-offline.ico: the icon with a red dot (no connection); icon-unpaired.ico:
the icon in grey with an amber dot (not connected to a restaurant). Needs
Pillow. Run it again whenever icon.ico changes; the results are committed.
"""
from pathlib import Path

from PIL import Image, ImageDraw, ImageOps

TRAY = Path(__file__).resolve().parent.parent / "internal" / "tray"
SIZES = [16, 20, 24, 32, 48]
RED = (220, 38, 38, 255)
AMBER = (245, 158, 11, 255)


def base(size: int) -> Image.Image:
    icon = Image.open(TRAY / "icon.ico")
    # The largest frame, scaled down: sharper than upscaling a small one.
    icon.size = max(icon.info.get("sizes", {icon.size}))
    icon.load()
    return icon.convert("RGBA").resize((size, size), Image.LANCZOS)


def with_dot(image: Image.Image, color: tuple[int, int, int, int]) -> Image.Image:
    size = image.width
    # Drawn 4x and scaled down, for a round dot at 16 px.
    scale = 4
    layer = Image.new("RGBA", (size * scale, size * scale), (0, 0, 0, 0))
    draw = ImageDraw.Draw(layer)
    diameter = round(size * 0.56) * scale
    ring = max(1, round(size * 0.07)) * scale
    box = (size * scale - diameter, size * scale - diameter, size * scale - 1, size * scale - 1)
    draw.ellipse(box, fill=(255, 255, 255, 255))
    draw.ellipse((box[0] + ring, box[1] + ring, box[2] - ring, box[3] - ring), fill=color)
    return Image.alpha_composite(image, layer.resize((size, size), Image.LANCZOS))


def grey(image: Image.Image) -> Image.Image:
    alpha = image.getchannel("A")
    faded = ImageOps.grayscale(image).convert("RGBA")
    faded.putalpha(alpha.point(lambda value: round(value * 0.75)))
    return faded


def save(name: str, frames: list[Image.Image]) -> None:
    largest = frames[-1]
    largest.save(TRAY / name, format="ICO", sizes=[(f.width, f.height) for f in frames], append_images=frames[:-1])
    print(name, (TRAY / name).stat().st_size, "bytes")


save("icon-offline.ico", [with_dot(base(size), RED) for size in SIZES])
save("icon-unpaired.ico", [with_dot(grey(base(size)), AMBER) for size in SIZES])

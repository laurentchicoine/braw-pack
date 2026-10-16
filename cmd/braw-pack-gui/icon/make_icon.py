"""Draws the braw-pack app icon (archive box with a film-strip lid) and writes
icon.png and icon.ico next to this script. Requires Pillow.

After changing the icon, rebuild the Windows resource from the repo root:

    python cmd/braw-pack-gui/icon/make_icon.py
    go run github.com/tc-hib/go-winres@v0.3.3 make --in cmd/braw-pack-gui/icon/winres.json --out cmd/braw-pack-gui/rsrc --arch amd64
"""
from pathlib import Path

from PIL import Image, ImageDraw

HERE = Path(__file__).parent
MASTER = 1024
S = MASTER / 120  # the design is laid out on a 120-unit grid
K = 1.3  # the box is enlarged about its centre to fill the canvas
CX, CY = 60, 61

ACCENT = "#e8a13a"
CLEAR = (0, 0, 0, 0)  # holes and the play triangle are cut out, the background is transparent
SIZES = [256, 64, 48, 40, 32, 24, 20, 16]


def pt(x, y):
    return ((x - CX) * K + 60) * S, ((y - CY) * K + 60) * S


def box(x, y, w, h):
    return [*pt(x, y), *pt(x + w, y + h)]


def draw_master():
    img = Image.new("RGBA", (MASTER, MASTER), CLEAR)
    d = ImageDraw.Draw(img)
    u = K * S

    # lid, with sprocket holes
    d.rounded_rectangle(box(18, 24, 84, 18), radius=6 * u, fill=ACCENT)
    for x in (26, 41, 56, 71, 86):
        d.rounded_rectangle(box(x, 29, 8, 8), radius=2 * u, fill=CLEAR)

    # body
    d.rounded_rectangle(box(26, 46, 68, 52), radius=8 * u, fill=ACCENT)

    # play triangle with rounded corners
    tri = [pt(52, 58), pt(52, 86), pt(76, 72)]
    r = 2 * u
    d.polygon(tri, fill=CLEAR)
    d.line(tri + tri[:1], fill=CLEAR, width=round(2 * r))
    for x, y in tri:
        d.ellipse([x - r, y - r, x + r, y + r], fill=CLEAR)

    return img


def main():
    master = draw_master()
    frames = [master.resize((n, n), Image.LANCZOS) for n in SIZES]
    frames[0].save(HERE / "icon.png")
    frames[0].save(HERE / "icon.ico", sizes=[(n, n) for n in SIZES], append_images=frames[1:])


if __name__ == "__main__":
    main()

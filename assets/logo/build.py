#!/usr/bin/env python3
"""Build the chord logo set: wordmark (G1), icon (N), favicons, splash text, social card, preview sheet.

Letters are converted to outlines from JetBrains Mono SemiBold (OFL) so the SVGs
do not depend on installed fonts. The font is not checked in; fetch it first:

    curl -sL -o /tmp/JetBrainsMono-SemiBold.ttf \
      https://github.com/JetBrains/JetBrainsMono/raw/master/fonts/ttf/JetBrainsMono-SemiBold.ttf
    CHORD_LOGO_FONT=/tmp/JetBrainsMono-SemiBold.ttf uv run python assets/logo/build.py assets/logo

Requires fonttools, pillow and rsvg-convert.
"""
import atexit
import os
import shutil
import subprocess
import sys
import tempfile

from fontTools.pens.boundsPen import BoundsPen
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.ttLib import TTFont
from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
FONT_PATH = os.environ.get("CHORD_LOGO_FONT", os.path.join(HERE, "fonts", "JetBrainsMono-SemiBold.ttf"))
OUT = sys.argv[1] if len(sys.argv) > 1 else HERE
os.makedirs(OUT, exist_ok=True)

# Composite sources that only exist to be rasterised live here, not in OUT:
# they are inputs to the build, not part of the logo set.
SCRATCH = tempfile.mkdtemp(prefix="chord-logo-")
atexit.register(shutil.rmtree, SCRATCH, True)

AMBER = "#E8A33D"
INK_DARK_BG = "#F6F4EF"   # letters on dark backgrounds
INK_LIGHT_BG = "#1B1B1F"  # letters on light backgrounds

font = TTFont(FONT_PATH)
UPM = font["head"].unitsPerEm
GS = font.getGlyphSet()
CMAP = font.getBestCmap()
ADV = font["hmtx"][CMAP[ord("o")]][0]        # 600
XH = font["OS/2"].sxHeight                  # 550
ASC = font["OS/2"].sCapHeight               # 730 (ascender of d/h)


def glyph_path(ch):
    pen = SVGPathPen(GS)
    GS[CMAP[ord(ch)]].draw(pen)
    return pen.getCommands()


def glyph_bounds(ch):
    bp = BoundsPen(GS)
    GS[CMAP[ord(ch)]].draw(bp)
    return bp.bounds


def measure_stem_width():
    """Rasterise 'd' and measure the vertical stem thickness above the bowl."""
    size = 1000
    img = Image.new("L", (1200, 1600), 0)
    d = ImageDraw.Draw(img)
    f = ImageFont.truetype(FONT_PATH, size)
    d.text((100, 200), "d", font=f, fill=255)
    asc_px = f.getmetrics()[0]
    baseline = 200 + asc_px
    row = baseline - int(0.65 * size)  # 650 units above baseline: only the stem is there
    px = [x for x in range(img.width) if img.getpixel((x, row)) > 127]
    return (px[-1] - px[0] + 1) * UPM / size if px else 90


STEM_W = round(measure_stem_width())

# ---------------------------------------------------------------- wordmark (G1)
# Layout in font units, y up, baseline 0. Letters c h o r at 0..1800, note d at 2400.
o_b = glyph_bounds("o")
d_b = glyph_bounds("d")
NOTE_X = 4 * ADV
HEAD_CX = NOTE_X + 255
HEAD_CY = 235
HEAD_RX = 275
HEAD_RY = 185
HEAD_ROT = -20
STEM_X = NOTE_X + d_b[2] - STEM_W / 2          # stem's right edge aligns with d's right edge
STEM_TOP = ASC + 180                            # rises above the ascender line: it's a note, not a letter
SLUR_X0 = 0.15 * ADV
SLUR_X1 = HEAD_CX + 75                          # ends under the note head, never past the stem
SLUR_Y = -150
SLUR_DEPTH = 240
SLUR_THICK = 62

PAD_L, PAD_R = 120, 120
TOP = STEM_TOP + 90
BOTTOM = SLUR_Y - SLUR_DEPTH - SLUR_THICK - 60
VB_W = NOTE_X + ADV + PAD_L + PAD_R
VB_H = TOP - BOTTOM
BASE = TOP  # baseline y in SVG coords (y down): y_svg = TOP - y_font


def y(v):
    return BASE - v


def wordmark_svg(ink, note=AMBER, width=None):
    letters = "".join(
        f'<path transform="translate({PAD_L + i*ADV} {BASE}) scale(1 -1)" d="{glyph_path(ch)}"/>'
        for i, ch in enumerate("chor")
    )
    cx, cy = PAD_L + HEAD_CX, y(HEAD_CY)
    head = (f'<ellipse cx="{cx:.0f}" cy="{cy:.0f}" rx="{HEAD_RX}" ry="{HEAD_RY}" '
            f'transform="rotate({HEAD_ROT} {cx:.0f} {cy:.0f})"/>')
    stem = (f'<rect x="{PAD_L + STEM_X - STEM_W/2:.0f}" y="{y(STEM_TOP):.0f}" width="{STEM_W}" '
            f'height="{STEM_TOP - HEAD_CY:.0f}" rx="{STEM_W/2:.0f}"/>')
    x0, x1 = PAD_L + SLUR_X0, PAD_L + SLUR_X1
    mid = (x0 + x1) / 2
    sy = y(SLUR_Y)
    slur = (f'<path d="M{x0:.0f} {sy:.0f} Q{mid:.0f} {sy + SLUR_DEPTH + SLUR_THICK:.0f} {x1:.0f} {sy:.0f} '
            f'Q{mid:.0f} {sy + SLUR_DEPTH - SLUR_THICK:.0f} {x0:.0f} {sy:.0f} Z"/>')
    w_attr = f' width="{width}" height="{round(width * VB_H / VB_W)}"' if width else ""
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {VB_W:.0f} {VB_H:.0f}"{w_attr}>\n'
            f'  <title>chord</title>\n'
            f'  <g fill="{ink}">{letters}</g>\n'
            f'  <g fill="{note}">{head}{stem}{slur}</g>\n'
            f'</svg>\n')


# ---------------------------------------------------------------- icon (N)
def icon_svg(color="currentColor", bg=None, pad=0):
    """Beam (the line being written) with two rising notes and a block cursor as the next note."""
    vb = 64 + 2 * pad
    o = pad
    bg_rect = f'  <rect width="{vb}" height="{vb}" rx="{vb*0.22:.1f}" fill="{bg}"/>\n' if bg else ""
    body = (
        f'<rect x="{o+8}" y="{o+14}" width="48" height="6" rx="1"/>'
        f'<rect x="{o+17.3}" y="{o+17}" width="3.4" height="30" rx="1.7"/>'
        f'<rect x="{o+36.3}" y="{o+17}" width="3.4" height="22" rx="1.7"/>'
        f'<ellipse cx="{o+11.5}" cy="{o+47}" rx="8" ry="5.5" transform="rotate(-20 {o+11.5} {o+47})"/>'
        f'<ellipse cx="{o+30.5}" cy="{o+39}" rx="8" ry="5.5" transform="rotate(-20 {o+30.5} {o+39})"/>'
        f'<rect x="{o+48}" y="{o+22}" width="8" height="12"/>'
    )
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {vb} {vb}">\n'
            f'  <title>chord</title>\n{bg_rect}'
            f'  <g fill="{color}">{body}</g>\n</svg>\n')


def write(name, content):
    with open(os.path.join(OUT, name), "w") as f:
        f.write(content)


# wordmarks
write("chord-wordmark.svg", wordmark_svg("currentColor"))           # letters follow surrounding text colour
write("chord-wordmark-dark.svg", wordmark_svg(INK_DARK_BG))         # for dark backgrounds
write("chord-wordmark-light.svg", wordmark_svg(INK_LIGHT_BG))       # for light backgrounds
write("chord-wordmark-mono.svg", wordmark_svg("currentColor", "currentColor"))

# icons
write("chord-icon.svg", icon_svg(AMBER))
write("chord-icon-mono.svg", icon_svg("currentColor"))
write("chord-icon-tile.svg", icon_svg(AMBER, bg=INK_LIGHT_BG, pad=6))  # app-tile / social avatar
write("favicon.svg", icon_svg(AMBER))


def rsvg(src, dst, w, h=None):
    subprocess.run(["rsvg-convert", "-w", str(w), "-h", str(h or w), src, "-o", dst], check=True)


for size in (16, 32, 48):
    rsvg(os.path.join(OUT, "favicon.svg"), os.path.join(OUT, f"favicon-{size}.png"), size)
rsvg(os.path.join(OUT, "chord-icon-tile.svg"), os.path.join(OUT, "apple-touch-icon.png"), 180)
rsvg(os.path.join(OUT, "chord-icon-tile.svg"), os.path.join(OUT, "icon-512.png"), 512)
# largest first: Pillow drops ICO sizes bigger than the base image
ico = [Image.open(os.path.join(OUT, f"favicon-{s}.png")) for s in (48, 32, 16)]
ico[0].save(os.path.join(OUT, "favicon.ico"), sizes=[(48, 48), (32, 32), (16, 16)], append_images=ico[1:])

# splash text (what the TUI can print with real characters)
write("splash.txt",
      "chor♩\n"
      "╰───╯\n"
      "\n"
      "# single line:  chor♩\n"
      "# with cursor:  chor♩▎\n")

def inner(svg):
    """Strip the <svg> wrapper so a fragment can be placed on another canvas."""
    return svg.split("\n", 2)[2].rsplit("</svg>", 1)[0]


wm_dark = inner(wordmark_svg(INK_DARK_BG))
wm_light = inner(wordmark_svg(INK_LIGHT_BG))
ic = inner(icon_svg(AMBER))
ic_dark = inner(icon_svg(INK_LIGHT_BG))

# ---------------------------------------------------------------- social card
# og:image for link previews (Slack, Feishu, X, Discord, ...). Those crawlers
# do not render SVG, so the card is rasterised, and 1200x630 is the size they
# all crop to. The wordmark carries no background of its own; on a transparent
# PNG its near-white ink would vanish against a light-themed client, so the
# card puts the brand dark behind it - the same background chord-icon-tile.svg
# uses.
OG_W, OG_H = 1200, 630
OG_MARK_W = 900
og_sc = OG_MARK_W / VB_W
# Centre the artwork, not its viewBox: the wordmark reserves more room below
# the slur than above the stem, so centring the box alone sits the mark high.
ART_TOP = y(STEM_TOP)
ART_BOTTOM = y(SLUR_Y) + (SLUR_DEPTH + SLUR_THICK) / 2
OG_SRC = os.path.join(SCRATCH, "og.svg")
with open(OG_SRC, "w") as f:
    f.write(
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{OG_W}" height="{OG_H}" '
        f'viewBox="0 0 {OG_W} {OG_H}">\n'
        f'  <title>chord</title>\n'
        f'  <rect width="{OG_W}" height="{OG_H}" fill="{INK_LIGHT_BG}"/>\n'
        f'  <g transform="translate({(OG_W - OG_MARK_W) / 2:.0f} '
        f'{OG_H / 2 - (ART_TOP + ART_BOTTOM) / 2 * og_sc:.0f}) scale({og_sc})">{wm_dark}</g>\n'
        f'</svg>\n'
    )
rsvg(OG_SRC, os.path.join(OUT, "og.png"), OG_W, OG_H)

# ---------------------------------------------------------------- preview sheet
PREVIEW = os.path.join(SCRATCH, "preview.svg")
sc = 760 / VB_W
wm_h = VB_H * sc
P = 600  # panel height
with open(PREVIEW, "w") as f:
    f.write(f'''<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="{2*P}" viewBox="0 0 1200 {2*P}">
<rect width="1200" height="{P}" fill="#1b1b1f"/>
<rect y="{P}" width="1200" height="{P}" fill="#f6f4ef"/>
<g transform="translate(60 40) scale({sc})">{wm_dark}</g>
<g transform="translate(60 {P+40}) scale({sc})">{wm_light}</g>
<g transform="translate(60 {40+wm_h+20}) scale({sc*0.22})">{wm_dark}</g>
<g transform="translate(460 {40+wm_h+20}) scale({sc*0.11})">{wm_dark}</g>
<g transform="translate(660 {40+wm_h+20}) scale({sc*0.055})">{wm_dark}</g>
<g transform="translate(60 {P+40+wm_h+20}) scale({sc*0.22})">{wm_light}</g>
<g transform="translate(460 {P+40+wm_h+20}) scale({sc*0.11})">{wm_light}</g>
<g transform="translate(660 {P+40+wm_h+20}) scale({sc*0.055})">{wm_light}</g>
<g transform="translate(900 80) scale(3)">{ic}</g>
<g transform="translate(900 {P+80}) scale(3)">{ic_dark}</g>
<g transform="translate(900 320) scale(1)">{ic}</g>
<g transform="translate(980 320) scale(0.5)">{ic}</g>
<g transform="translate(1030 320) scale(0.25)">{ic}</g>
<g transform="translate(900 {P+320}) scale(1)">{ic_dark}</g>
<g transform="translate(980 {P+320}) scale(0.5)">{ic_dark}</g>
<g transform="translate(1030 {P+320}) scale(0.25)">{ic_dark}</g>
<text x="60" y="{P-30}" font-family="Menlo, monospace" font-size="20" fill="#888">$ chor♩▎</text>
<text x="900" y="{P-30}" font-family="Menlo, monospace" font-size="14" fill="#666">64 / 32 / 16</text>
</svg>''')
rsvg(PREVIEW, os.path.join(OUT, "preview.png"), 2400)

print(f"stem width {STEM_W} units; wordmark viewBox {VB_W:.0f}x{VB_H:.0f}; written to {OUT}")

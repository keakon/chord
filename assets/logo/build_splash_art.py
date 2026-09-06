#!/usr/bin/env python3
"""Render the chord wordmark into terminal half-block art.

The TUI splash cannot scale a font, so the large wordmark is pixel art derived
from the real SVG rather than hand-drawn ASCII: the shapes stay identical to
the logo. Each output cell packs two pixel rows via U+2580/U+2584/U+2588, so
the pixel grid is square and the SVG aspect ratio carries over unchanged.

Only the letters and the quarter note are rasterised. At this scale the swash
is a crescent barely one pixel thick, and rasterising it drops whole columns
wherever the curve crosses a pixel-row boundary, breaking it into segments.
The TUI synthesises the arc instead, which stays unbroken at any width.

Writes internal/tui/splash_art.txt, which internal/tui embeds.

Requires pillow and rsvg-convert:

    uv run --with pillow python assets/logo/build_splash_art.py [pixel-rows]
"""
import os
import re
import subprocess
import sys
import tempfile

from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
SOURCE = os.path.join(HERE, "chord-wordmark.svg")
# go:embed cannot reach outside its package directory, so the art lands next
# to its only consumer instead of in assets/logo/.
OUT = os.path.join(REPO, "internal", "tui", "splash_art.txt")

# Content bounding box of letters + note inside the wordmark's 3240x1512
# viewBox, so the art carries no dead padding. Letters start at x=201 and sit
# on the y=1000 baseline; the note stem tops out at y=90 and its head at x=3041.
VIEWBOX = (201, 90, 2840, 910)

PIXEL_ROWS = int(sys.argv[1]) if len(sys.argv) > 1 else 16

UPPER, LOWER, FULL, EMPTY = "▀", "▄", "█", " "
INK, ACCENT, NONE = "i", "a", "."


def read_groups():
    """Split the wordmark into its letter paths and note shapes."""
    svg = open(SOURCE, encoding="utf-8").read()
    groups = re.findall(r"<g fill=\"[^\"]*\">(.*?)</g>", svg, re.S)
    if len(groups) != 2:
        raise SystemExit(f"expected 2 <g> groups in {SOURCE}, found {len(groups)}")
    letters, accent = groups
    # The accent group is the note (ellipse head + stem rect) then the swash
    # path; only the note is rasterised here.
    note = "".join(re.findall(r"<(?:ellipse|rect)\b[^>]*/>", accent))
    return re.findall(r"<path\b[^>]*/>", letters), note


def rasterise(body, width, height):
    """Rasterise an SVG fragment to a boolean mask of covered pixels."""
    svg = (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{VIEWBOX[0]} {VIEWBOX[1]} '
        f'{VIEWBOX[2]} {VIEWBOX[3]}"><g fill="#ffffff">{body}</g></svg>'
    )
    with tempfile.TemporaryDirectory() as tmp:
        src = os.path.join(tmp, "frag.svg")
        png = os.path.join(tmp, "frag.png")
        open(src, "w", encoding="utf-8").write(svg)
        subprocess.run(
            ["rsvg-convert", "-w", str(width), "-h", str(height), src, "-o", png],
            check=True,
        )
        raw = Image.open(png).convert("LA").tobytes()
    # "LA" packs two bytes per pixel: luminance then alpha.
    return [
        [
            raw[(y * width + x) * 2 + 1] > 96 and raw[(y * width + x) * 2] > 96
            for x in range(width)
        ]
        for y in range(height)
    ]


def cell_glyph(top_set, bottom_set):
    if top_set and bottom_set:
        return FULL
    if top_set:
        return UPPER
    if bottom_set:
        return LOWER
    return EMPTY


def columns_of(mask, width, height):
    return [x for x in range(width) if any(mask[y][x] for y in range(height))]


def main():
    height = PIXEL_ROWS
    if height % 2:
        raise SystemExit("pixel-rows must be even (two pixel rows per cell)")
    width = round(height * VIEWBOX[2] / VIEWBOX[3])

    letter_paths, note = read_groups()
    ink = rasterise("".join(letter_paths), width, height)
    accent = rasterise(note, width, height)

    def layer_at(x, y):
        if ink[y][x]:
            return INK
        return ACCENT if accent[y][x] else NONE

    glyph_rows, fg_rows, bg_rows = [], [], []
    for row in range(height // 2):
        y0, y1 = row * 2, row * 2 + 1
        glyphs, fgs, bgs = [], [], []
        for x in range(width):
            top, bottom = layer_at(x, y0), layer_at(x, y1)
            glyphs.append(cell_glyph(top != NONE, bottom != NONE))
            # U+2580 paints its top half in the foreground over a background
            # bottom half, so the top pixel's layer is fg and the bottom's bg.
            fgs.append(top if top != NONE else bottom)
            bgs.append(bottom if top != NONE and bottom != NONE else NONE)
        glyph_rows.append("".join(glyphs))
        fg_rows.append("".join(fgs))
        bg_rows.append("".join(bgs))

    # Reveal units: one per letter, then the note. Column spans come from
    # rasterising each glyph alone so the animation pops whole letters.
    bounds = []
    for fragment in letter_paths + [note]:
        cols = columns_of(rasterise(fragment, width, height), width, height)
        bounds.append((min(cols), max(cols)))
    # Widen each span to the midpoint of the gap to its neighbour so that
    # revealing the units in order leaves no column behind.
    spans = []
    for i, (lo, hi) in enumerate(bounds):
        start = 0 if i == 0 else (bounds[i - 1][1] + lo + 1) // 2
        end = width - 1 if i == len(bounds) - 1 else (hi + bounds[i + 1][0]) // 2
        spans.append((start, end))

    split = sum(row.count(INK) + row.count(ACCENT) for row in bg_rows)

    with open(OUT, "w", encoding="utf-8") as fh:
        fh.write("# chord splash wordmark — generated by assets/logo/build_splash_art.py\n")
        fh.write("# Do not hand-edit; regenerate it from chord-wordmark.svg instead.\n")
        fh.write("# glyph: half-block art. fg/bg: per-cell layer, i=ink a=accent .=none.\n")
        fh.write("# units: inclusive column span per reveal step (c, h, o, r, note).\n")
        fh.write(f"size {width} {len(glyph_rows)}\n")
        fh.write("units " + " ".join(f"{a}-{b}" for a, b in spans) + "\n")
        for name, rows in (("glyph", glyph_rows), ("fg", fg_rows), ("bg", bg_rows)):
            fh.write(f"[{name}]\n")
            for row in rows:
                fh.write(row + "\n")

    print(f"wrote {OUT}: {width}x{len(glyph_rows)} cells, {split} split cells")
    for row in glyph_rows:
        print(row)


if __name__ == "__main__":
    main()

#!/usr/bin/env node
// Generate the site's root-served brand images from assets/logo/chord-wordmark.svg.
//
// chord-wordmark*.svg are the only brand sources kept in the repository; every
// derived image (favicon, touch icons, social card) is build output that lands
// in the gitignored website/public/. None of it belongs in the repository.
//
// sharp rasterises the SVG; the ICO container is assembled by hand because the
// docs toolchain has no ICO encoder.

import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import sharp from 'sharp';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const WORDMARK_PATH = path.join(REPO_ROOT, 'assets', 'logo', 'chord-wordmark.svg');
const DEFAULT_OUT_DIR = path.join(REPO_ROOT, 'website', 'public');

// The wordmark's letters use currentColor, which resolves to black inside a
// standalone image, so the generated images set their own ink.
const SURFACE = '#1b1b1f';
const INK_ON_DARK = '#f6f4ef'; // wordmark ink drawn over SURFACE
const ICON_PADDING = 0.08; // share of a square tile left around the wordmark
const FAVICON_PADDING = 0.04; // a transparent favicon carries the mark larger
const TILE_RADIUS = 0.22; // tile corner radius, as a share of the icon size
const ICO_SIZES = [16, 32, 48];
const OG_WIDTH = 1200;
const OG_HEIGHT = 630;
const OG_MARK_WIDTH = 900;

// Transform values need far more precision than the icon's geometry: rounding
// a scale like 0.018908 to two decimals stretches the mark by whole percents.
function fmt(value) {
  return Number(value.toFixed(6));
}

function parseViewBox(svg) {
  const m = svg.match(/viewBox="(-?[\d.]+) (-?[\d.]+) (-?[\d.]+) (-?[\d.]+)"/);
  if (!m) throw new Error(`${WORDMARK_PATH}: no viewBox`);
  return { x: Number(m[1]), y: Number(m[2]), width: Number(m[3]), height: Number(m[4]) };
}

function splitWordmark(svg) {
  const groups = [...svg.matchAll(/<g fill="([^"]+)"[^>]*>([\s\S]*?)<\/g>/g)];
  if (groups.length !== 2) {
    throw new Error(`${WORDMARK_PATH}: expected 2 <g> groups, found ${groups.length}`);
  }
  // First group is the letters, second the quarter-note accent (head, stem, swash).
  return { letters: groups[0][2], accent: groups[1][2], accentFill: groups[1][1] };
}

// Bounding box of the drawn mark inside the viewBox. It is measured from a
// downscaled raster instead of hardcoded so that centring follows the SVG.
async function contentBounds(viewBox) {
  const scanWidth = 800;
  const { data, info } = await sharp(WORDMARK_PATH)
    .resize({ width: scanWidth })
    .ensureAlpha()
    .raw()
    .toBuffer({ resolveWithObject: true });
  let minX = info.width;
  let minY = info.height;
  let maxX = -1;
  let maxY = -1;
  for (let y = 0; y < info.height; y++) {
    for (let x = 0; x < info.width; x++) {
      if (data[(y * info.width + x) * 4 + 3] > 8) {
        if (x < minX) minX = x;
        if (x > maxX) maxX = x;
        if (y < minY) minY = y;
        if (y > maxY) maxY = y;
      }
    }
  }
  if (maxX < 0) throw new Error(`${WORDMARK_PATH}: rendered to nothing`);
  const sx = viewBox.width / info.width;
  const sy = viewBox.height / info.height;
  return {
    x: viewBox.x + minX * sx,
    y: viewBox.y + minY * sy,
    width: (maxX - minX + 1) * sx,
    height: (maxY - minY + 1) * sy,
  };
}

// transform="translate(t) scale(s)" maps viewBox units onto (cx, cy)-centred art.
function centre(bounds, scale, cx, cy) {
  const tx = cx - scale * (bounds.x + bounds.width / 2);
  const ty = cy - scale * (bounds.y + bounds.height / 2);
  return `translate(${fmt(tx)} ${fmt(ty)}) scale(${fmt(scale)})`;
}

function mark(parts, ink, inkClass = '') {
  const cls = inkClass ? ` class="${inkClass}"` : '';
  return `<g${cls} fill="${ink}">${parts.letters}</g><g fill="${parts.accentFill}">${parts.accent}</g>`;
}

function iconSvg(size, parts, bounds) {
  const scale = (size * (1 - 2 * ICON_PADDING)) / bounds.width;
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 ${size} ${size}">
  <title>chord</title>
  <rect width="${size}" height="${size}" rx="${fmt(size * TILE_RADIUS)}" fill="${SURFACE}"/>
  <g transform="${centre(bounds, scale, size / 2, size / 2)}">${mark(parts, INK_ON_DARK)}</g>
</svg>
`;
}

// A favicon has no surface of its own, so the ink carries the contrast: brand
// dark for light tab bars, flipped to brand light where the browser honours the
// media query under a dark colour scheme.
function faviconSvg(size, parts, bounds) {
  const scale = (size * (1 - 2 * FAVICON_PADDING)) / bounds.width;
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 ${size} ${size}">
  <title>chord</title>
  <style>@media (prefers-color-scheme: dark) { .ink { fill: ${INK_ON_DARK} } }</style>
  <g transform="${centre(bounds, scale, size / 2, size / 2)}">${mark(parts, SURFACE, 'ink')}</g>
</svg>
`;
}

function socialCardSvg(parts, bounds) {
  const scale = OG_MARK_WIDTH / bounds.width;
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${OG_WIDTH}" height="${OG_HEIGHT}" viewBox="0 0 ${OG_WIDTH} ${OG_HEIGHT}">
  <title>chord</title>
  <rect width="${OG_WIDTH}" height="${OG_HEIGHT}" fill="${SURFACE}"/>
  <g transform="${centre(bounds, scale, OG_WIDTH / 2, OG_HEIGHT / 2)}">${mark(parts)}</g>
</svg>
`;
}

// ICO container with PNG-compressed entries, which every browser that honours
// the raster fallback understands.
function icoContainer(images) {
  const header = Buffer.alloc(6);
  header.writeUInt16LE(1, 2); // type: icon
  header.writeUInt16LE(images.length, 4);
  const entries = [];
  let offset = header.length + images.length * 16;
  for (const { size, data } of images) {
    const entry = Buffer.alloc(16);
    entry.writeUInt8(size < 256 ? size : 0, 0);
    entry.writeUInt8(size < 256 ? size : 0, 1);
    entry.writeUInt16LE(1, 4); // colour planes
    entry.writeUInt16LE(32, 6); // bits per pixel
    entry.writeUInt32LE(data.length, 8);
    entry.writeUInt32LE(offset, 12);
    offset += data.length;
    entries.push(entry);
  }
  return Buffer.concat([header, ...entries, ...images.map((image) => image.data)]);
}

export async function buildLogoAssets(outDir = DEFAULT_OUT_DIR) {
  const svg = await readFile(WORDMARK_PATH, 'utf8');
  const parts = splitWordmark(svg);
  const bounds = await contentBounds(parseViewBox(svg));
  const render = (source) => sharp(Buffer.from(source)).png().toBuffer();

  const favicons = await Promise.all(
    ICO_SIZES.map(async (size) => ({ size, data: await render(faviconSvg(size, parts, bounds)) })),
  );
  const outputs = {
    'favicon.svg': Buffer.from(faviconSvg(64, parts, bounds)),
    'favicon.ico': icoContainer(favicons),
    'apple-touch-icon.png': await render(iconSvg(180, parts, bounds)),
    'icon-512.png': await render(iconSvg(512, parts, bounds)),
    'og.png': await render(socialCardSvg(parts, bounds)),
  };

  await mkdir(outDir, { recursive: true });
  for (const [name, data] of Object.entries(outputs)) {
    await writeFile(path.join(outDir, name), data);
  }
  return Object.keys(outputs);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const outDir = process.argv[2] ? path.resolve(process.argv[2]) : DEFAULT_OUT_DIR;
  buildLogoAssets(outDir)
    .then((names) => console.log(`Generated ${names.join(', ')} in ${outDir}.`))
    .catch((err) => {
      console.error(err);
      process.exit(1);
    });
}

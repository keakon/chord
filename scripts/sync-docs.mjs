#!/usr/bin/env node
// Sync docs/*.md → website/src/content/docs/*.md (English root) and website/src/content/docs/zh/*.md.
//
// What it does for each source file:
//   1. Picks the target language from the *_CN.md suffix.
//   2. Strips the leading H1 (Starlight uses frontmatter title instead).
//   3. Adds Starlight frontmatter: title (from H1), description (first paragraph).
//   4. Rewrites relative ./xxx.md links to root /xxx/ slugs for English and /zh/xxx/ for Chinese.
//   5. Promotes "> **Note:** ...", "> **Important:** ..." block-quotes into
//      Starlight :::note / :::caution / :::danger admonitions.
//   6. Syncs docs/examples/*.md as ordinary markdown pages. Example YAML files
//      remain source assets and can still be linked from the docs or repository.
//
// Source of truth stays in docs/. The sync target markdown files are gitignored — never edit them by hand.

import { mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const repoRoot = path.resolve(__dirname, '..');
const docsDir = path.join(repoRoot, 'docs');
const websiteContentDir = path.join(repoRoot, 'website', 'src', 'content', 'docs');
const enDir = websiteContentDir;
const zhDir = path.join(websiteContentDir, 'zh');

// Pages we manage as hand-written Starlight (skip from sync to avoid clobbering).
const SKIP_FILES = new Set(['index.md', 'index_CN.md']);

// docs/<basename>.md lives at /<slug>/ on the site (en root) or /zh/<slug>/ (zh).
function deriveSlug(basename) {
  return basename.replace(/_CN$/, '');
}

function detectLanguage(filename) {
  return filename.endsWith('_CN.md') ? 'zh' : 'en';
}

// Extract H1 -> title; first non-heading paragraph -> description.
function extractTitleAndBody(markdown) {
  const lines = markdown.split('\n');
  let title = '';
  let bodyStart = 0;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.startsWith('# ')) {
      title = line.slice(2).trim();
      bodyStart = i + 1;
      // Skip a blank line after the H1, if present.
      while (bodyStart < lines.length && lines[bodyStart].trim() === '') {
        bodyStart++;
      }
      break;
    }
  }
  const body = lines.slice(bodyStart).join('\n');
  return { title, body };
}

function deriveDescription(body) {
  // Pull the first paragraph that isn't a heading, code fence, or admonition marker.
  const blocks = body.split(/\n{2,}/);
  for (const block of blocks) {
    const trimmed = block.trim();
    if (!trimmed) continue;
    if (trimmed.startsWith('#')) continue;
    if (trimmed.startsWith('```')) continue;
    if (trimmed.startsWith(':::')) continue;
    if (trimmed.startsWith('|')) continue; // table
    if (trimmed.startsWith('>')) continue; // quote / admonition
    return trimmed
      .replace(/\n+/g, ' ')
      .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '$1') // strip [label](url) → label
      .replace(/`([^`]+)`/g, '$1')
      .replace(/\s+/g, ' ')
      .slice(0, 180)
      .trim();
  }
  return '';
}

const SITE_BASE = '/chord';

function sitePath(lang, slug, anchor = '') {
  const langPrefix = lang === 'zh' ? '/zh' : '';
  return `${SITE_BASE}${langPrefix}/${slug}/${anchor}`;
}

function rewriteLinks(body, lang) {
  return body
    // ./examples/index.md  → /chord/examples/   (special-case the directory index)
    // ./examples/index_CN.md → /chord/zh/examples/
    .replace(/\(\.\/examples\/index(_CN)?\.md([^)]*)\)/g, (_m, cn, rest) => {
      const anchor = (rest || '').startsWith('#') ? rest : '';
      return `(${sitePath(cn ? 'zh' : lang, 'examples', anchor)})`;
    })
    // ./examples/<file>.yaml  → repo blob link (yaml not a content collection)
    .replace(/\(\.\/examples\/([\w.-]+)\.yaml\)/g, (_m, name) =>
      `(https://github.com/keakon/chord/blob/main/docs/examples/${name}.yaml)`,
    )
    // ../examples/<file>.yaml from inside the examples page
    .replace(/\(\.\.\/examples\/([\w.-]+)\.yaml\)/g, (_m, name) =>
      `(https://github.com/keakon/chord/blob/main/docs/examples/${name}.yaml)`,
    )
    // Sibling .md links: ./xxx_CN.md → /chord/zh/xxx/ ; ./xxx.md → /chord/xxx/
    .replace(/\(\.\/([\w-]+?)(_CN)?\.md([^)]*)\)/g, (_m, slug, cn, rest) => {
      const anchor = (rest || '').startsWith('#') ? rest : '';
      return `(${sitePath(cn ? 'zh' : lang, slug, anchor)})`;
    })
    // ../<page>.md from a subdir
    .replace(/\(\.\.\/([\w-]+?)(_CN)?\.md([^)]*)\)/g, (_m, slug, cn, rest) => {
      const anchor = (rest || '').startsWith('#') ? rest : '';
      return `(${sitePath(cn ? 'zh' : lang, slug, anchor)})`;
    });
}

// Promote the simple "> **Note:**" / "> **Important:** / **Warning:**" patterns
// into Starlight ::: admonitions.
function rewriteAdmonitions(body) {
  return body.replace(
    /(^|\n)>\s+\*\*(Note|Important|Warning|Tip|Caution|Danger)(?::|\*\*:)\*\*\s*([^\n]+(?:\n>\s+[^\n]+)*)/g,
    (_m, prefix, kind, content) => {
      const flavour = {
        Note: 'note',
        Tip: 'tip',
        Important: 'caution',
        Warning: 'caution',
        Caution: 'caution',
        Danger: 'danger',
      }[kind] || 'note';
      const inner = content.replace(/\n>\s?/g, '\n').trim();
      return `${prefix}:::${flavour}\n${inner}\n:::`;
    },
  );
}

function escapeYaml(value) {
  return value.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
}

function buildFrontmatter({ title, description }) {
  const lines = ['---'];
  if (title) lines.push(`title: "${escapeYaml(title)}"`);
  if (description) lines.push(`description: "${escapeYaml(description)}"`);
  lines.push('---', '');
  return lines.join('\n');
}

async function syncOne(srcPath, lang, targetSlug) {
  const raw = await readFile(srcPath, 'utf8');
  const { title, body } = extractTitleAndBody(raw);
  const description = deriveDescription(body);
  let processed = rewriteLinks(body, lang);
  processed = rewriteAdmonitions(processed);
  const out = buildFrontmatter({ title, description }) + processed.trimStart() + '\n';
  const targetDir = lang === 'zh' ? zhDir : enDir;
  const targetPath = path.join(targetDir, `${targetSlug}.md`);
  await mkdir(path.dirname(targetPath), { recursive: true });
  await writeFile(targetPath, out, 'utf8');
}

async function clean() {
  for (const dir of [enDir, zhDir]) {
    await mkdir(dir, { recursive: true });
    const entries = await readdir(dir, { withFileTypes: true });
    await Promise.all(
      entries
        .filter((entry) => entry.isFile() && entry.name.endsWith('.md'))
        .map((entry) => rm(path.join(dir, entry.name), { force: true })),
    );
  }
}

async function main() {
  await clean();

  const entries = await readdir(docsDir, { withFileTypes: true });
  for (const entry of entries) {
    if (!entry.isFile()) continue;
    if (!entry.name.endsWith('.md')) continue;
    if (SKIP_FILES.has(entry.name)) continue;
    const srcPath = path.join(docsDir, entry.name);
    const lang = detectLanguage(entry.name);
    const baseName = entry.name.replace(/\.md$/, '');
    const slug = deriveSlug(baseName);
    await syncOne(srcPath, lang, slug);
  }

  const exampleEntries = await readdir(path.join(docsDir, 'examples'), { withFileTypes: true });
  for (const entry of exampleEntries) {
    if (!entry.isFile()) continue;
    if (!entry.name.endsWith('.md')) continue;
    const srcPath = path.join(docsDir, 'examples', entry.name);
    const lang = detectLanguage(entry.name);
    const baseName = entry.name.replace(/\.md$/, '');
    const slug = deriveSlug(baseName);
    await syncOne(srcPath, lang, slug === 'index' ? 'examples' : slug);
  }

  console.log('Synced docs/ → website/src/content/docs/*.md and website/src/content/docs/zh/*.md.');
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});

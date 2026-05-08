#!/usr/bin/env node
// Sync docs/*.md → website/src/content/docs/{en,zh}/*.md for Starlight.
//
// What it does for each source file:
//   1. Picks the target language from the *_CN.md suffix.
//   2. Strips the leading H1 (Starlight uses frontmatter title instead).
//   3. Adds Starlight frontmatter: title (from H1), description (first paragraph).
//   4. Rewrites relative ./xxx.md links to absolute /xxx/ slugs (and zh/ for CN).
//   5. Promotes "> **Note:** ...", "> **Important:** ..." block-quotes into
//      Starlight :::note / :::caution / :::danger admonitions.
//   6. For docs/examples/index*.md, inlines the four yaml example files as
//      fenced code blocks so the rendered page is self-contained.
//
// Source of truth stays in docs/. The sync target (website/src/content/docs/{en,zh}/)
// is gitignored — never edit it by hand.

import { mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const repoRoot = path.resolve(__dirname, '..');
const docsDir = path.join(repoRoot, 'docs');
const examplesDir = path.join(docsDir, 'examples');
const websiteContentDir = path.join(repoRoot, 'website', 'src', 'content', 'docs');
const enDir = path.join(websiteContentDir, 'en');
const zhDir = path.join(websiteContentDir, 'zh');

// Pages we manage as hand-written Starlight (skip from sync to avoid clobbering).
const SKIP_FILES = new Set(['index.md', 'index_CN.md']);

// docs/<basename>.md lives at /<slug>/ on the site (en) or /zh/<slug>/ (zh).
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

function rewriteLinks(body, lang) {
  const langPrefix = lang === 'zh' ? '/zh' : '';
  return body
    // ./examples/index.md  → /examples/   (special-case the directory index)
    // ./examples/index_CN.md → /zh/examples/
    .replace(/\(\.\/examples\/index(_CN)?\.md([^)]*)\)/g, (_m, _cn, rest) => {
      const anchor = (rest || '').startsWith('#') ? rest : '';
      return `(${langPrefix}/examples/${anchor})`;
    })
    // ./examples/<file>.yaml  → repo blob link (yaml not a content collection)
    .replace(/\(\.\/examples\/([\w.-]+)\.yaml\)/g, (_m, name) =>
      `(https://github.com/keakon/chord/blob/main/docs/examples/${name}.yaml)`,
    )
    // ../examples/<file>.yaml from inside the examples page
    .replace(/\(\.\.\/examples\/([\w.-]+)\.yaml\)/g, (_m, name) =>
      `(https://github.com/keakon/chord/blob/main/docs/examples/${name}.yaml)`,
    )
    // Sibling .md links: ./xxx_CN.md → /zh/xxx/   ;  ./xxx.md → /<lang>/xxx/
    .replace(/\(\.\/([\w-]+)(_CN)?\.md(#[\w-]+)?\)/g, (_m, slug, cn, anchor) => {
      const targetLang = cn ? '/zh' : langPrefix;
      return `(${targetLang}/${slug}/${anchor || ''})`;
    })
    // ../<page>.md from a subdir
    .replace(/\(\.\.\/([\w-]+)(_CN)?\.md(#[\w-]+)?\)/g, (_m, slug, cn, anchor) => {
      const targetLang = cn ? '/zh' : langPrefix;
      return `(${targetLang}/${slug}/${anchor || ''})`;
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

async function syncExamples() {
  // Build a single page per language: index intro + each yaml as a fenced block.
  const yamlFiles = (await readdir(examplesDir))
    .filter((name) => name.endsWith('.yaml'))
    .sort();

  for (const lang of ['en', 'zh']) {
    const indexFile = lang === 'zh' ? 'index_CN.md' : 'index.md';
    const indexPath = path.join(examplesDir, indexFile);
    if (!existsSync(indexPath)) continue;

    const raw = await readFile(indexPath, 'utf8');
    const { title, body } = extractTitleAndBody(raw);
    const description = deriveDescription(body);
    let intro = rewriteLinks(body, lang);
    intro = rewriteAdmonitions(intro);

    let out = buildFrontmatter({ title, description });
    out += intro.trimStart();
    out += '\n\n---\n\n';
    out += lang === 'zh'
      ? '## 完整 YAML 内容\n\n下面是上表中每个示例文件的完整内容，可直接复制粘贴。\n\n'
      : '## Full YAML contents\n\nBelow is the verbatim content of each example file listed above, ready to copy.\n\n';
    for (const yaml of yamlFiles) {
      const yamlPath = path.join(examplesDir, yaml);
      const content = await readFile(yamlPath, 'utf8');
      out += `### \`${yaml}\`\n\n`;
      out += '```yaml\n';
      out += content.replace(/```/g, '` ` `'); // defensive escape
      out += '```\n\n';
    }

    const targetDir = lang === 'zh' ? zhDir : enDir;
    const targetPath = path.join(targetDir, 'examples.md');
    await mkdir(targetDir, { recursive: true });
    await writeFile(targetPath, out, 'utf8');
  }
}

async function clean() {
  for (const dir of [enDir, zhDir]) {
    if (existsSync(dir)) await rm(dir, { recursive: true, force: true });
    await mkdir(dir, { recursive: true });
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

  await syncExamples();

  console.log('Synced docs/ → website/src/content/docs/{en,zh}/.');
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});

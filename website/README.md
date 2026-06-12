# Chord docs site

Starlight (Astro) site that renders `docs/*.md` and `docs/*_CN.md` from the
repository root.

## Quick start

```bash
cd website
npm install
npm run dev      # http://localhost:4321/chord/
```

`npm run dev` (and `npm run build`) automatically run `npm run sync` first,
which converts the source markdown in `../docs/` into Starlight content
collections under `src/content/docs/{en,zh}/`. The synced output is
gitignored — never edit those files directly. Always edit the source under
`../docs/` and let `sync` regenerate the content collection.

## Layout

```
website/
├── astro.config.mjs        # Starlight config (sidebar, locales, base URL)
├── package.json
├── tsconfig.json
├── src/
│   ├── content.config.ts   # Astro content collection definition
│   ├── content/
│   │   └── docs/
│   │       ├── index.mdx       # Hand-written English landing page (Hero + CardGrid)
│   │       ├── zh/
│   │       │   └── index.mdx   # Hand-written Chinese landing page
│   │       ├── en/             # SYNCED — do not edit by hand
│   │       └── zh/             # SYNCED — do not edit by hand
│   └── styles/
│       └── custom.css      # Theme tweaks
└── public/
    └── screenshots/        # M2: drop screenshots here
```

## Authoring

- For new or updated docs content: **edit `docs/*.md` and `docs/*_CN.md`** at
  the repo root. They remain readable on GitHub directly.
- For navigation changes (sidebar groups, labels): edit
  `astro.config.mjs`'s `sidebar:` block.
- For landing-page hero / cards: edit `src/content/docs/index.mdx` and
  `src/content/docs/zh/index.mdx`.

## Build for production

```bash
npm run build           # writes static HTML to website/dist/
npm run preview         # serve the built site locally
```

The CI workflow at `.github/workflows/docs.yml` runs the same `build` step
and deploys `dist/` to GitHub Pages on every change to `docs/`, `website/`,
the docs sync/check scripts, or the README / CONTRIBUTING files.

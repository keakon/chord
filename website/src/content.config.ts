import { defineCollection } from 'astro:content';
import { glob } from 'astro/loaders';
import { docsSchema } from '@astrojs/starlight/schema';

function generateDocsId({ entry, data }: { entry: string; data: Record<string, unknown> }): string {
  if (typeof data.slug === 'string' && data.slug) return data.slug;
  return entry.replace(/\.(?:markdown|mdown|mkdn|mkd|mdwn|md|mdx)$/i, '').replace(/\/index$/, '');
}

export const collections = {
  docs: defineCollection({
    loader: glob({
      base: './src/content/docs',
      pattern: '**/[^_]*.{markdown,mdown,mkdn,mkd,mdwn,md,mdx}',
      generateId: generateDocsId,
    }),
    schema: docsSchema(),
  }),
};

// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

const repo = 'https://github.com/keakon/chord';
const site = 'https://keakon.github.io';
const base = '/chord';

export default defineConfig({
  site,
  base,
  trailingSlash: 'always',
  integrations: [
    starlight({
      title: 'Chord',
      description: 'A terminal coding agent that finishes coding tasks faster, runs cheaper, and stays small in memory.',
      social: [{ icon: 'github', label: 'GitHub', href: repo }],
      // assets/logo/chord-wordmark*.svg are the brand sources; scripts/sync-docs.mjs
      // regenerates the root-served images into website/public/ before every
      // dev/build run.
      logo: {
        light: '../assets/logo/chord-wordmark-light.svg',
        dark: '../assets/logo/chord-wordmark-dark.svg',
        replacesTitle: true,
      },
      favicon: '/favicon.svg',
      head: [
        // Chrome selects this .ico over the SVG even though it renders SVG
        // favicons, so the tab icon comes from here and must read on both
        // light and dark bars; Safari has no SVG favicons either.
        { tag: 'link', attrs: { rel: 'icon', href: `${base}/favicon.ico`, sizes: '16x16 32x32 48x48' } },
        { tag: 'link', attrs: { rel: 'apple-touch-icon', href: `${base}/apple-touch-icon.png`, sizes: '180x180' } },
        // Starlight already emits twitter:card=summary_large_image, which
        // renders an empty card without an image behind it. Crawlers fetch
        // these from another host, so the URL has to be absolute.
        { tag: 'meta', attrs: { property: 'og:image', content: `${site}${base}/og.png` } },
        { tag: 'meta', attrs: { property: 'og:image:width', content: '1200' } },
        { tag: 'meta', attrs: { property: 'og:image:height', content: '630' } },
        { tag: 'meta', attrs: { property: 'og:image:alt', content: 'Chord' } },
        { tag: 'meta', attrs: { name: 'twitter:image', content: `${site}${base}/og.png` } },
      ],
      defaultLocale: 'root',
      locales: {
        root: { label: 'English', lang: 'en' },
        zh: { label: '中文', lang: 'zh-CN' },
      },
      editLink: {
        baseUrl: `${repo}/edit/main/website/`,
      },
      lastUpdated: true,
      pagination: true,
      sidebar: [
        {
          label: 'Getting started',
          translations: { 'zh-CN': '开始使用' },
          items: [
            { slug: 'quickstart', translations: { 'zh-CN': '快速开始' } },
            { slug: 'usage', translations: { 'zh-CN': '使用指南' } },
            { slug: 'keybindings', translations: { 'zh-CN': '快捷键' } },
          ],
        },
        {
          label: 'Models and credentials',
          translations: { 'zh-CN': '配置模型' },
          items: [
            { slug: 'configuration', translations: { 'zh-CN': '配置与认证' } },
            { slug: 'model-configs', translations: { 'zh-CN': '模型配置速查' } },
            { slug: 'reasoning', translations: { 'zh-CN': '推理与思考' } },
            { slug: 'context-management', translations: { 'zh-CN': '上下文管理' } },
          ],
        },
        {
          label: 'Tools and safety',
          translations: { 'zh-CN': '工具与安全' },
          items: [
            { slug: 'permissions-and-safety', translations: { 'zh-CN': '权限与安全' } },
            { slug: 'tools', translations: { 'zh-CN': '内置工具' } },
            { slug: 'edit-tools', translations: { 'zh-CN': '编辑工具' } },
          ],
        },
        {
          label: 'Customization and integration',
          translations: { 'zh-CN': '扩展与集成' },
          items: [
            { slug: 'customization', translations: { 'zh-CN': '扩展与定制' } },
            { slug: 'hooks', translations: { 'zh-CN': 'Hooks' } },
            { slug: 'headless', translations: { 'zh-CN': 'Headless 集成' } },
          ],
        },
        {
          label: 'Configuration examples',
          translations: { 'zh-CN': '示例配置' },
          items: [
            { slug: 'examples', translations: { 'zh-CN': '示例配置库' } },
            { slug: 'examples-minimal', translations: { 'zh-CN': '最小可用' } },
            { slug: 'examples-codex-workstation', translations: { 'zh-CN': 'Codex + LSP' } },
            { slug: 'examples-openai-compat', translations: { 'zh-CN': 'OpenAI 兼容网关' } },
            { slug: 'examples-team', translations: { 'zh-CN': '团队方案' } },
          ],
        },
        {
          label: 'Reference and troubleshooting',
          translations: { 'zh-CN': '查阅与排障' },
          items: [
            { slug: 'cli', translations: { 'zh-CN': 'CLI 参考' } },
            { slug: 'paths', translations: { 'zh-CN': '目录与路径' } },
            { slug: 'environment', translations: { 'zh-CN': '环境变量' } },
            { slug: 'platforms', translations: { 'zh-CN': '平台支持' } },
            { slug: 'performance', translations: { 'zh-CN': '性能' } },
            { slug: 'troubleshooting', translations: { 'zh-CN': '常见问题排查' } },
            { slug: 'glossary', translations: { 'zh-CN': '术语表' } },
          ],
        },
      ],
      customCss: ['./src/styles/custom.css'],
    }),
  ],
});

// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

const repo = 'https://github.com/keakon/chord';

export default defineConfig({
  site: 'https://keakon.github.io',
  base: '/chord',
  trailingSlash: 'always',
  legacy: {
    collections: false,
  },
  integrations: [
    starlight({
      title: 'Chord',
      description: 'Calm AI coding in your terminal — a lightweight, local-first coding agent.',
      social: { github: repo },
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
          translations: { 'zh-CN': '入门' },
          items: [
            { slug: 'quickstart', translations: { 'zh-CN': '快速开始' } },
            { slug: 'usage', translations: { 'zh-CN': '使用指南' } },
            { slug: 'glossary', translations: { 'zh-CN': '术语表' } },
          ],
        },
        {
          label: 'Reference',
          translations: { 'zh-CN': '参考' },
          items: [
            { slug: 'cli', translations: { 'zh-CN': 'CLI' } },
            { slug: 'configuration', translations: { 'zh-CN': '配置与认证' } },
            { slug: 'tools', translations: { 'zh-CN': '内置工具' } },
            { slug: 'edit-tools', translations: { 'zh-CN': '编辑工具' } },
            { slug: 'keybindings', translations: { 'zh-CN': '快捷键' } },
            { slug: 'paths', translations: { 'zh-CN': '目录与路径' } },
            { slug: 'environment', translations: { 'zh-CN': '环境变量' } },
            { slug: 'platforms', translations: { 'zh-CN': '平台支持' } },
            { slug: 'performance', translations: { 'zh-CN': '性能' } },
          ],
        },
        {
          label: 'Going further',
          translations: { 'zh-CN': '进阶' },
          items: [
            { slug: 'customization', translations: { 'zh-CN': '扩展与定制' } },
            { slug: 'hooks', translations: { 'zh-CN': 'Hooks' } },
            { slug: 'examples', translations: { 'zh-CN': '示例配置库' } },
            { slug: 'examples-minimal', translations: { 'zh-CN': '最小可用示例' } },
            { slug: 'examples-codex-workstation', translations: { 'zh-CN': 'Codex + LSP 示例' } },
            { slug: 'examples-openai-compat', translations: { 'zh-CN': 'OpenAI 兼容网关示例' } },
            { slug: 'examples-team', translations: { 'zh-CN': '团队仓库示例' } },
          ],
        },
        {
          label: 'Integration',
          translations: { 'zh-CN': '集成' },
          items: [
            { slug: 'headless', translations: { 'zh-CN': 'Headless' } },
          ],
        },
        {
          label: 'Safety',
          translations: { 'zh-CN': '安全' },
          items: [
            { slug: 'permissions-and-safety', translations: { 'zh-CN': '权限与安全' } },
          ],
        },
        {
          label: 'Troubleshooting',
          translations: { 'zh-CN': '排障' },
          items: [
            { slug: 'troubleshooting', translations: { 'zh-CN': '常见问题排查' } },
          ],
        },
      ],
      customCss: ['./src/styles/custom.css'],
    }),
  ],
});

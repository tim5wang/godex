import { defineConfig } from 'vitepress'

const repository = 'https://github.com/tim5wang/godex'
const base = process.env.DOCS_BASE ?? (process.env.GITHUB_ACTIONS ? '/godex/' : '/')
const withBase = (path: string) => `${base}${path.replace(/^\//, '')}`

export default defineConfig({
  lang: 'zh-CN',
  title: 'GoDex',
  description: '本地优先的 AI Agent 工作台与运行时',
  base,
  cleanUrls: true,
  srcExclude: ['superpowers/**', 'node_modules/**', '.vitepress/**'],
  lastUpdated: true,
  sitemap: { hostname: 'https://tim5wang.github.io/godex/' },
  head: [
    ['link', { rel: 'icon', href: withBase('/brand/godex-icon.jpg') }],
    ['meta', { name: 'theme-color', content: '#3157d5' }]
  ],
  themeConfig: {
    logo: withBase('/brand/godex-icon.jpg'),
    siteTitle: 'GoDex 文档',
    nav: [
      { text: '入门', link: '/guide/' },
      { text: '开发', link: '/develop/' },
      { text: '参考', link: '/reference/' },
      { text: '部署', link: '/operations/' },
      { text: 'v1.4.0', items: [
        { text: '发布说明', link: '/release-notes-v1.4.0' },
        { text: '功能实现矩阵', link: '/feature-implementation-matrix' },
        { text: '完整文档索引', link: '/README' }
      ] }
    ],
    sidebar: {
      '/guide/': [
        {
          text: '开始使用',
          items: [
            { text: 'GoDex 是什么', link: '/guide/' },
            { text: '快速开始', link: '/guide/getting-started' },
            { text: '完整用户指南', link: '/user-guide' }
          ]
        },
        {
          text: '核心能力',
          items: [
            { text: '扩展运行时', link: '/extension-runtime-user-guide' },
            { text: 'Business Flow 画布', link: '/business-flow-canvas-guide' },
            { text: 'VS Code ACP', link: '/vscode-acp' },
            { text: '工作流集成', link: '/workflows-integration-guide' }
          ]
        }
      ],
      '/develop/': [
        {
          text: '开发指南',
          items: [
            { text: '参与开发', link: '/develop/' },
            { text: '项目结构', link: '/project-structure' },
            { text: '功能实现矩阵', link: '/feature-implementation-matrix' },
            { text: '自我认知与文档', link: '/self-knowledge-design' }
          ]
        },
        {
          text: '运行时',
          items: [
            { text: '架构 SPEC', link: '/architecture-v2-spec' },
            { text: 'Workflow Runtime', link: '/workflow-runtime' },
            { text: 'Scope 隔离', link: '/scope-isolation-design' },
            { text: 'Memory 设计', link: '/memory-design-principles' },
            { text: 'Agent Step SDK', link: '/agent-step-sdk' }
          ]
        }
      ],
      '/reference/': [
        {
          text: '概念',
          items: [
            { text: '架构', link: '/reference/' },
            { text: '项目结构', link: '/project-structure' },
            { text: '功能与实现', link: '/feature-implementation-matrix' }
          ]
        },
        {
          text: 'Agent 运行时',
          items: [
            { text: 'Agent 与工具', link: '/agent-role-and-bundle-design' },
            { text: 'Workflow', link: '/workflow-runtime' },
            { text: 'Business Flow', link: '/business-flow-runtime-design' },
            { text: 'Memory', link: '/memory-design-principles' },
            { text: 'Scope 与 Sandbox', link: '/scope-isolation-design' }
          ]
        },
        {
          text: '平台能力',
          items: [
            { text: 'Agent Step Platform', link: '/agent-step-platform-design' },
            { text: 'Business Agents', link: '/business-agents-console-design' },
            { text: 'TaskBoard', link: '/taskboard-plugin-design' },
            { text: 'Node Center Bridge', link: '/node-center-bridge-design' },
            { text: '扩展运行时', link: '/extension-runtime-user-guide' }
          ]
        },
        {
          text: '文档',
          items: [
            { text: '全部文档', link: '/README' },
            { text: '优化路线图', link: '/godex-optimization-roadmap' },
            { text: '发布说明', link: '/release-notes-v1.4.0' }
          ]
        }
      ],
      '/operations/': [
        {
          text: '部署与运维',
          items: [
            { text: '运维入口', link: '/operations/' },
            { text: '自部署', link: '/self-deploy' },
            { text: '节点接入', link: '/node-onboarding' },
            { text: 'Node Mesh', link: '/node-mesh-design' },
            { text: '桌面壳', link: '/desktop-shell' }
          ]
        },
        {
          text: '诊断',
          items: [
            { text: '用户指南：故障排查', link: '/user-guide#故障排查' },
            { text: '工具问题记录', link: '/tools_issues' }
          ]
        }
      ]
    },
    outline: { level: [2, 3], label: '本页目录' },
    docFooter: { prev: '上一页', next: '下一页' },
    lastUpdated: { text: '最后更新' },
    editLink: { pattern: `${repository}/edit/main/docs/:path`, text: '在 GitHub 上编辑此页' },
    socialLinks: [{ icon: 'github', link: repository }],
    search: { provider: 'local', options: { translations: {
      button: { buttonText: '搜索文档', buttonAriaLabel: '搜索文档' },
      modal: {
        noResultsText: '未找到相关结果',
        resetButtonTitle: '清除查询',
        footer: { selectText: '选择', navigateText: '切换', closeText: '关闭' }
      }
    } } },
    footer: { message: '本地优先，可审计，可扩展', copyright: 'GoDex' }
  },
  markdown: {
    html: false,
    lineNumbers: true,
    config: (md) => {
      md.options.linkify = false
      md.core.ruler.after('inline', 'escape-vue-interpolation', (state) => {
        for (const token of state.tokens) {
          if (token.type !== 'inline' || !token.children) continue
          for (const child of token.children) {
            if (child.type === 'text' || child.type === 'code_inline') {
              child.content = child.content.replaceAll('{{', '&#123;&#123;')
            }
          }
        }
      })
    }
  }
})

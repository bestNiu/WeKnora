// 运行时配置（本地开发默认值，Docker 环境会被 entrypoint 脚本覆盖）
window.__RUNTIME_CONFIG__ = {
  // 品牌显示名。Docker 部署时由 docker-entrypoint.sh 用 APP_NAME 覆盖。
  APP_NAME: '企业知识库',
  APP_TAGLINE: '内部资料智能问答',
  MAX_FILE_SIZE_MB: 50,
  MAX_SKILL_BUNDLE_SIZE_MB: 256,
  // Optional: serve embed on a dedicated origin, e.g. 'https://embed.example.com'
  EMBED_BASE_URL: '',
  // Optional: default UI locale for first-time visitors (zh-CN | zh-TW | en-US | ru-RU | ko-KR | ja-JP)
  DEFAULT_LOCALE: '',
};

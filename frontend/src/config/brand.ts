/**
 * 运行时品牌配置。
 *
 * 通过 `window.__RUNTIME_CONFIG__.APP_NAME` 注入（见 public/config.js 与
 * docker-entrypoint.sh），因此换显示名不需要重新构建前端镜像——改环境变量
 * `APP_NAME` 重启前端容器即可。
 *
 * 未配置时回落到中性默认值，避免出现上游开源项目的品牌名。
 */
type BrandRuntimeConfig = {
  APP_NAME?: string
  APP_TAGLINE?: string
}

// SAFETY: window.__RUNTIME_CONFIG__ is written by config.js / the nginx
// entrypoint before this module loads. TypeScript cannot see fields injected
// at runtime, so the cast narrows it to the shape we actually read; every
// field stays optional and is re-validated by readText below.
const runtime =
  (window as unknown as { __RUNTIME_CONFIG__?: BrandRuntimeConfig }).__RUNTIME_CONFIG__ || {}

function readText(value: unknown, fallback: string, maxLength: number): string {
  const text = typeof value === 'string' ? value.trim() : ''
  return text ? text.slice(0, maxLength) : fallback
}

/** 产品显示名：出现在页面标题、侧栏 wordmark、登录页。 */
export const APP_NAME = readText(runtime.APP_NAME, '企业知识库', 64)

/** 一句副标题：出现在登录页与页面 description。 */
export const APP_TAGLINE = readText(runtime.APP_TAGLINE, '内部资料智能问答', 96)
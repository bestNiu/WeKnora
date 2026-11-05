import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const agentStream = readFileSync(
  new URL('./AgentStreamDisplay.vue', import.meta.url),
  'utf8',
)
const zhLocale = readFileSync(new URL('../../../i18n/locales/zh-CN.ts', import.meta.url), 'utf8')

test('list_documents results become drawer references', () => {
  assert.match(
    agentStream,
    /if \(toolName === 'list_documents'\)[\s\S]*?toolData\.documents[\s\S]*?chunk_type: 'document'/,
  )
})

test('a plain click opens the reference drawer and modifiers keep the new tab', () => {
  assert.match(
    agentStream,
    /const openWeKnoraEntity = async \(el: HTMLElement, options: \{ newTab\?: boolean \} = \{\}\)/,
  )
  assert.match(
    agentStream,
    /if \(!options\.newTab && \(kind === 'doc' \|\| kind === 'chunk'\)\) \{\s*if \(await openWeKnoraEntityInDrawer\(kind, id\)\) return;/,
  )
  assert.match(agentStream, /referencesDrawer\.openSource\(\{[\s\S]*?knowledgeId: id/)
  assert.match(agentStream, /newTab: e\.ctrlKey \|\| e\.metaKey \|\| e\.shiftKey \|\| e\.button === 1/)
})

test('the entity chip tooltip explains both click behaviours', () => {
  assert.match(zhLocale, /doc: '文档：点击在右侧预览，Ctrl\/⌘\+点击新标签打开'/)
})
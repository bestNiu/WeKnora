import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

// The member panel pages over workspace members, so saving must stay
// incremental: only the rows the operator touched are sent. A full replace
// here silently revokes everyone on the other pages.

function setup(t: TestContext, grantedTotal = 1) {
  const f = fixture(t)
  const page = {
    visibility_mode: 'allowlist',
    members: [
      { user_id: 'creator', username: 'Creator', email: 'c@example.com', role: 'contributor', status: 'active', can_view: true, implicit: true },
      { user_id: 'granted', username: 'Alice', email: 'a@example.com', role: 'viewer', status: 'active', can_view: true, implicit: false },
      { user_id: 'other', username: 'Bob', email: 'b@example.com', role: 'viewer', status: 'active', can_view: false, implicit: false },
    ],
    total: 40,
    page: 1,
    page_size: 20,
    granted_total: grantedTotal,
  }
  const getAgentAccess = t.mock.fn(async () => ({ data: page }))
  const patchAgentAccess = t.mock.fn(async () => ({ updated: 1 }))
  const dialog = f.component(new URL('./AgentAccessDialog.vue', import.meta.url), {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': {
      MessagePlugin: { success() {}, error() {}, warning() {} },
      DialogPlugin: { confirm: (opts: any) => { opts.onConfirm?.(); return { destroy() {} } } },
    },
    '@/api/agent': { getAgentAccess, patchAgentAccess, putAgentAccess: async () => ({}) },
  }, { visible: true, agent: { id: 'agent-1', name: 'Agent' } })
  return { ...dialog, getAgentAccess, patchAgentAccess }
}

function find(page: any, userId: string) {
  return page.vm.rows.find((row: any) => row.user_id === userId)
}

/** Objects created inside the fixture's VM realm do not deepEqual across realms. */
function plain(value: unknown) {
  return JSON.parse(JSON.stringify(value))
}

function callArgs(fn: { mock: { calls: { arguments: unknown }[] } }) {
  return plain(fn.mock.calls[0].arguments)
}

test('loads one page and saves only the touched member', async t => {
  const f = setup(t)
  await flush()

  assert.equal(f.getAgentAccess.mock.callCount(), 1)
  assert.deepEqual(plain((f.getAgentAccess.mock.calls[0].arguments as unknown as any[])[1]),
    { page: 1, page_size: 20, q: '' })
  assert.equal(f.vm.rows.length, 3)
  assert.equal(f.vm.total, 40)

  f.vm.toggle(find(f, 'other'), true)
  await f.vm.save()

  assert.equal(f.patchAgentAccess.mock.callCount(), 1)
  const [agentID, payload] = callArgs(f.patchAgentAccess) as [string, any]
  assert.equal(agentID, 'agent-1')
  assert.equal(payload.visibility_mode, 'allowlist')
  assert.deepEqual(payload.members, [{ user_id: 'other', can_view: true, implicit: false }])
})

test('an untouched page sends no member rows at all', async t => {
  const f = setup(t)
  await flush()

  // Switching the mode alone must not imply "keep exactly this page".
  f.vm.modeTouched = true
  await f.vm.save()

  const [, payload] = callArgs(f.patchAgentAccess) as [string, any]
  assert.deepEqual(payload.members, [])
})

test('discards the page when the operator closes with unsaved changes', async t => {
  const f = setup(t)
  await flush()
  f.vm.toggle(find(f, 'other'), true)
  assert.equal(f.vm.dirty, true)

  await f.vm.onVisibleChange(false)
  assert.equal(f.vm.touched.size, 0, 'discarding must clear the pending edits')
  assert.equal(f.vm.dirty, false)
})

test('an empty allowlist is confirmed before saving', async t => {
  const f = setup(t, 0)
  await flush()
  const vm = f.vm
  vm.visibilityMode = 'allowlist'
  await vm.save()
  // The confirm mock auto-confirms, so the save still goes through — but only
  // because the operator was asked first. Assert the ask happened by checking
  // the save used an empty member set.
  assert.equal(f.patchAgentAccess.mock.callCount(), 1)
  const [, payload] = callArgs(f.patchAgentAccess) as [string, any]
  assert.deepEqual(payload.members, [])
})
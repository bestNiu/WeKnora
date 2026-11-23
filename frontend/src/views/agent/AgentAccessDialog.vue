<template>
  <t-dialog
    :visible="visible"
    :header="t('agentAccess.title', { name: agent?.name || '' })"
    width="680px"
    :confirm-btn="{ content: t('common.save'), loading: saving }"
    :cancel-btn="t('common.cancel')"
    @update:visible="onVisibleChange"
    @confirm="save"
  >
    <t-loading v-if="loading" size="small" />
    <template v-else>
      <p class="agent-access__hint">{{ t('agentAccess.hint') }}</p>
      <div class="agent-access__mode">
        <t-radio-group v-model="visibilityMode" variant="default-filled" @change="modeTouched = true">
          <t-radio-button value="tenant">{{ t('agentAccess.modeTenant') }}</t-radio-button>
          <t-radio-button value="allowlist">{{ t('agentAccess.modeAllowlist') }}</t-radio-button>
        </t-radio-group>
        <span class="agent-access__summary">
          {{ t('agentAccess.summary', { granted: grantedTotal, total: total }) }}
        </span>
      </div>

      <p v-if="visibilityMode === 'allowlist'" class="agent-access__warn">
        {{ t('agentAccess.allowlistHint') }}
      </p>

      <div v-if="visibilityMode === 'allowlist'" class="agent-access__members">
        <!-- 搜索 + 分页：成员按空间分页返回，不再一次性拉全空间的成员 -->
        <div class="agent-access__toolbar">
          <t-input
            v-model="query"
            size="small"
            clearable
            class="agent-access__search"
            :placeholder="t('agentAccess.searchPlaceholder')"
            @change="onSearch"
          />
          <span class="agent-access__page">{{ page }} / {{ pageCount }}</span>
          <t-button size="small" variant="text" :disabled="page <= 1" @click="goTo(page - 1)">‹</t-button>
          <t-button size="small" variant="text" :disabled="page >= pageCount" @click="goTo(page + 1)">›</t-button>
        </div>

        <div v-for="member in rows" :key="member.user_id" class="agent-access__row">
          <div class="agent-access__identity">
            <strong>{{ member.username || member.email || member.user_id }}</strong>
            <span v-if="member.username && member.email">{{ member.email }}</span>
            <t-tag v-if="member.status && member.status !== 'active'" theme="default" size="small" variant="light">
              {{ statusLabel(member.status) }}
            </t-tag>
          </div>
          <span v-if="member.implicit" class="agent-access__implicit">{{ t('agentAccess.implicit') }}</span>
          <t-checkbox
            v-else
            :checked="member.can_view"
            @change="(checked: boolean) => toggle(member, checked)"
          />
        </div>
        <p v-if="!rows.length" class="agent-access__empty">{{ t('agentAccess.empty') }}</p>
      </div>
    </template>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import {
  getAgentAccess,
  patchAgentAccess,
  type AgentAccessMember,
  type CustomAgent,
} from '@/api/agent'

const props = defineProps<{ visible: boolean; agent: CustomAgent | null }>()
const emit = defineEmits<{ (e: 'update:visible', value: boolean): void; (e: 'saved'): void }>()
const { t } = useI18n()

type Row = AgentAccessMember

const loading = ref(false)
const saving = ref(false)
const visibilityMode = ref<'tenant' | 'allowlist'>('tenant')
const modeTouched = ref(false)
const rows = ref<Row[]>([])
const grantedTotal = ref(0)
const total = ref(0)

// 分页 + 搜索。touched 记录被改过的成员：保存时只提交这些行，
// 整表提交在分页后会把当前页以外的人全部撤销。
const page = ref(1)
const pageSize = ref(20)
const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
const query = ref('')
const touched = ref(new Map<string, boolean>())

const dirty = computed(
  () => modeTouched.value || (visibilityMode.value === 'allowlist' && touched.value.size > 0),
)

function statusLabel(status: string): string {
  if (status === 'invited') return t('agentAccess.statusInvited')
  if (status === 'suspended') return t('agentAccess.statusSuspended')
  return status
}

async function load() {
  const agent = props.agent
  if (!agent) return
  loading.value = true
  try {
    const res = await getAgentAccess(agent.id, {
      page: page.value,
      page_size: pageSize.value,
      q: query.value,
    })
    const policy = res?.data
    visibilityMode.value = policy?.visibility_mode === 'allowlist' ? 'allowlist' : 'tenant'
    rows.value = policy?.members || []
    total.value = Number(policy?.total ?? rows.value.length)
    grantedTotal.value = Number(policy?.granted_total ?? 0)
    modeTouched.value = false
    touched.value = new Map()
  } catch (err) {
    console.error('Failed to load agent access:', err)
    MessagePlugin.error(t('agentAccess.loadFailed'))
  } finally {
    loading.value = false
  }
}

function goTo(target: number) {
  if (target < 1 || target > pageCount.value) return
  page.value = target
  void load()
}

function onSearch() {
  page.value = 1
  void load()
}

function toggle(member: Row, checked: boolean) {
  member.can_view = checked
  touched.value.set(member.user_id, checked)
  // 本地乐观更新「已指定 N 人」，保存后再以服务端为准。
  grantedTotal.value = Math.max(
    0,
    grantedTotal.value + (checked && !member.implicit ? 1 : 0) - (!checked ? 1 : 0),
  )
}

/** 增量提交：只发被改动过的成员。 */
async function applyChanges(): Promise<boolean> {
  const agent = props.agent
  if (!agent) return false
  const members: AgentAccessMember[] = [...touched.value.entries()].map(([userID, canView]) => ({
    user_id: userID,
    can_view: canView,
    implicit: false,
  }))
  const res = await patchAgentAccess(agent.id, {
    visibility_mode: visibilityMode.value,
    members,
  })
  const failed = Number((res as { failed?: number })?.failed || 0)
  if (failed > 0) {
    // 部分失败（成员已不是本空间成员等）：保留 touched，让操作者能重试。
    MessagePlugin.warning(t('agentAccess.partialFailed', { count: failed }))
    await load()
    return false
  }
  modeTouched.value = false
  touched.value = new Map()
  return true
}

async function save() {
  const agent = props.agent
  if (!agent) return
  if (visibilityMode.value === 'allowlist' && grantedTotal.value === 0) {
    // 空名单 = 只有创建者和管理员能用，保存前必须确认。
    const confirmed = await confirmDialog({
      header: t('agentAccess.confirmEmptyTitle'),
      body: t('agentAccess.confirmEmptyBody'),
    })
    if (!confirmed) return
  }
  saving.value = true
  try {
    if (await applyChanges()) {
      MessagePlugin.success(t('agentAccess.saved'))
      emit('saved')
      emit('update:visible', false)
    }
  } catch (err) {
    console.error('Failed to save agent access:', err)
    MessagePlugin.error(t('agentAccess.saveFailed'))
  } finally {
    saving.value = false
  }
}

/** 关闭前拦一道：有未保存改动时确认，避免"点了取消但以为已经保存"。 */
async function onVisibleChange(value: boolean) {
  if (value) {
    emit('update:visible', true)
    return
  }
  if (saving.value) return
  if (dirty.value) {
    const confirmed = await confirmDialog({
      header: t('agentAccess.unsavedTitle'),
      body: t('agentAccess.unsavedBody'),
      confirmText: t('agentAccess.discard'),
      cancelText: t('common.cancel'),
    })
    if (!confirmed) return
    touched.value = new Map()
    modeTouched.value = false
  }
  emit('update:visible', false)
}

function confirmDialog(options: {
  header: string
  body: string
  confirmText?: string
  cancelText?: string
}): Promise<boolean> {
  return new Promise((resolve) => {
    const dialog = DialogPlugin.confirm({
      header: options.header,
      body: options.body,
      confirmBtn: { content: options.confirmText || t('common.save') },
      cancelBtn: options.cancelText || t('common.cancel'),
      theme: 'warning',
      onConfirm: () => {
        resolve(true)
        dialog.destroy()
      },
      onCancel: () => {
        resolve(false)
        dialog.destroy()
      },
      onClose: () => {
        resolve(false)
        dialog.destroy()
      },
    })
  })
}

// 切换 agent 或重新打开都从第一页、无搜索词开始，避免沿用上一个智能体的分页状态。
watch(
  () => [props.visible, props.agent?.id] as const,
  ([visible]) => {
    if (!visible) return
    page.value = 1
    query.value = ''
    void load()
  },
  { immediate: true },
)
</script>

<style scoped>
.agent-access__hint {
  margin: 0 0 12px;
  color: var(--td-text-color-secondary);
  font-size: 13px;
}
.agent-access__mode {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.agent-access__summary {
  color: var(--td-text-color-placeholder);
  font-size: 12px;
}
.agent-access__warn {
  margin: 12px 0;
  padding: 8px 12px;
  border-radius: 6px;
  background: var(--td-warning-color-light);
  color: var(--td-warning-color);
  font-size: 13px;
}
.agent-access__members {
  margin-top: 4px;
}
.agent-access__toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0 8px;
  border-bottom: 1px solid var(--td-component-stroke);
}
.agent-access__search {
  flex: 1;
}
.agent-access__page {
  color: var(--td-text-color-secondary);
  font-size: 12px;
  white-space: nowrap;
}
.agent-access__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 4px;
  min-height: 44px;
  border-bottom: 1px solid var(--td-component-stroke);
}
.agent-access__identity {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  font-size: 13px;
}
.agent-access__identity span {
  color: var(--td-text-color-secondary);
  font-size: 12px;
}
.agent-access__implicit {
  color: var(--td-text-color-placeholder);
  font-size: 12px;
}
.agent-access__empty {
  padding: 16px 0;
  text-align: center;
  color: var(--td-text-color-placeholder);
  font-size: 13px;
}
</style>
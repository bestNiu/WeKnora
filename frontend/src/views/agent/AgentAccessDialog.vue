<template>
  <t-dialog
    :visible="visible"
    :header="t('agentAccess.title', { name: agent?.name || '' })"
    width="560px"
    :confirm-btn="{ content: t('common.save'), loading: saving }"
    :cancel-btn="t('common.cancel')"
    @update:visible="(v: boolean) => emit('update:visible', v)"
    @confirm="save"
  >
    <t-loading v-if="loading" size="small" />
    <template v-else>
      <p class="agent-access__hint">{{ t('agentAccess.hint') }}</p>
      <t-radio-group v-model="visibilityMode" variant="default-filled">
        <t-radio-button value="tenant">{{ t('agentAccess.modeTenant') }}</t-radio-button>
        <t-radio-button value="allowlist">{{ t('agentAccess.modeAllowlist') }}</t-radio-button>
      </t-radio-group>

      <p v-if="visibilityMode === 'allowlist'" class="agent-access__warn">
        {{ t('agentAccess.allowlistHint') }}
      </p>

      <div v-if="visibilityMode === 'allowlist'" class="agent-access__members">
        <div v-for="member in rows" :key="member.user_id" class="agent-access__row">
          <div class="agent-access__identity">
            <strong>{{ member.username || member.email }}</strong>
            <span v-if="member.username">{{ member.email }}</span>
          </div>
          <span v-if="member.implicit" class="agent-access__implicit">
            {{ t('agentAccess.implicit') }}
          </span>
          <t-checkbox v-else v-model="member.can_view" />
        </div>
        <p v-if="!rows.length" class="agent-access__empty">{{ t('agentAccess.empty') }}</p>
      </div>
    </template>
  </t-dialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import {
  getAgentAccess,
  putAgentAccess,
  type AgentAccessMember,
  type CustomAgent,
} from '@/api/agent'
import { fetchAllTenantMembers } from '@/api/tenant/members'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ visible: boolean; agent: CustomAgent | null }>()
const emit = defineEmits<{ (e: 'update:visible', value: boolean): void; (e: 'saved'): void }>()
const { t } = useI18n()
const authStore = useAuthStore()

type Row = {
  user_id: string
  username: string
  email: string
  can_view: boolean
  /** creator / Owner / Admin always see the agent; their grant is implicit. */
  implicit: boolean
}

const loading = ref(false)
const saving = ref(false)
const visibilityMode = ref<'tenant' | 'allowlist'>('tenant')
const rows = ref<Row[]>([])

async function load() {
  const agent = props.agent
  if (!agent) return
  loading.value = true
  try {
    const tenantId = agent.tenant_id || authStore.effectiveTenantId
    const [policyRes, members] = await Promise.all([
      getAgentAccess(agent.id),
      tenantId ? fetchAllTenantMembers(Number(tenantId)) : Promise.resolve([]),
    ])
    const policy = policyRes?.data
    visibilityMode.value = policy?.visibility_mode === 'allowlist' ? 'allowlist' : 'tenant'
    const granted = new Set((policy?.members || []).filter((m) => m.can_view).map((m) => m.user_id))
    rows.value = members
      .filter((m) => m.status === 'active')
      .map((m) => {
        const implicit =
          m.user_id === agent.created_by || m.role === 'owner' || m.role === 'admin'
        return {
          user_id: m.user_id,
          username: m.username || '',
          email: m.email || '',
          can_view: implicit || granted.has(m.user_id),
          implicit,
        }
      })
  } catch (err) {
    console.error('Failed to load agent access:', err)
    MessagePlugin.error(t('agentAccess.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function save() {
  const agent = props.agent
  if (!agent) return
  const members: AgentAccessMember[] = rows.value
    .filter((row) => !row.implicit && row.can_view)
    .map((row) => ({ user_id: row.user_id, can_view: true }))
  saving.value = true
  try {
    await putAgentAccess(agent.id, {
      visibility_mode: visibilityMode.value,
      members: visibilityMode.value === 'allowlist' ? members : [],
    })
    MessagePlugin.success(t('agentAccess.saved'))
    emit('saved')
    emit('update:visible', false)
  } catch (err) {
    console.error('Failed to save agent access:', err)
    MessagePlugin.error(t('agentAccess.saveFailed'))
  } finally {
    saving.value = false
  }
}

watch(
  () => [props.visible, props.agent?.id] as const,
  ([visible]) => {
    if (visible) load()
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
.agent-access__warn {
  margin: 12px 0;
  padding: 8px 12px;
  border-radius: 6px;
  background: var(--td-warning-color-light);
  color: var(--td-warning-color);
  font-size: 13px;
}
.agent-access__members {
  margin-top: 12px;
  max-height: 320px;
  overflow-y: auto;
  border-top: 1px solid var(--td-component-stroke);
}
.agent-access__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 4px;
  border-bottom: 1px solid var(--td-component-stroke);
}
.agent-access__identity {
  display: flex;
  flex-direction: column;
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
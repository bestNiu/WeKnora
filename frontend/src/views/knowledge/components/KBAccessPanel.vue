<template>
  <section class="kb-access-panel">
    <header class="kb-access-panel__header">
      <div>
        <h3>{{ t('knowledgeBase.access.title') }}</h3>
        <p class="kb-access-panel__hint">{{ t('knowledgeBase.access.hint') }}</p>
      </div>
      <t-button theme="primary" size="small" :loading="saving" :disabled="!dirty" @click="save">
        {{ t('common.save') }}
      </t-button>
    </header>

    <t-loading v-if="loading" size="small" />
    <template v-else>
      <div class="kb-access-panel__mode">
        <span>{{ t('knowledgeBase.access.visibility') }}</span>
        <t-radio-group v-model="visibilityMode" variant="default-filled">
          <t-radio-button value="tenant">{{ t('knowledgeBase.access.modeTenant') }}</t-radio-button>
          <t-radio-button value="restricted">{{ t('knowledgeBase.access.modeRestricted') }}</t-radio-button>
        </t-radio-group>
      </div>
      <p v-if="visibilityMode === 'restricted'" class="kb-access-panel__warn">
        {{ t('knowledgeBase.access.restrictedHint') }}
      </p>

      <div class="kb-access-panel__table">
        <div v-for="member in rows" :key="member.user_id" class="kb-access-panel__row">
          <div class="kb-access-panel__identity">
            <strong>{{ member.username || member.email }}</strong>
            <span>{{ member.email }}</span>
          </div>
          <label class="kb-access-panel__toggle">
            <t-checkbox v-model="member.can_view" :disabled="visibilityMode !== 'restricted'" />
            <span>{{ t('knowledgeBase.access.canView') }}</span>
          </label>
          <label class="kb-access-panel__toggle">
            <t-checkbox v-model="member.can_download" />
            <span>{{ t('knowledgeBase.access.canDownload') }}</span>
          </label>
        </div>
      </div>
      <p v-if="!rows.length" class="kb-access-panel__empty">{{ t('knowledgeBase.access.empty') }}</p>

      <div v-if="isAdmin" class="kb-access-panel__backfill">
        <div>
          <strong>{{ t('knowledgeBase.access.backfill') }}</strong>
          <p class="kb-access-panel__hint">
            {{ t('knowledgeBase.access.backfillStats', {
              ready: previewStats.ready || 0,
              pending: previewStats.pending || 0,
              failed: previewStats.failed || 0,
            }) }}
          </p>
        </div>
        <t-button size="small" :loading="backfilling" @click="startBackfill">
          {{ t('knowledgeBase.access.backfill') }}
        </t-button>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import {
  backfillKnowledgePreviews,
  getKnowledgeBaseAccess,
  getPreviewBackfillStatus,
  putKnowledgeBaseAccess,
} from '@/api/knowledge-base/index'
import { fetchAllTenantMembers } from '@/api/tenant/members'
import { useAuthStore } from '@/stores/auth'
import { getKnowledgeBaseById } from '@/api/knowledge-base/index'

const props = defineProps<{ kbId: string }>()
const { t } = useI18n()
const authStore = useAuthStore()

type Row = { user_id: string; username: string; email: string; can_view: boolean; can_download: boolean }

const loading = ref(true)
const saving = ref(false)
const visibilityMode = ref<'tenant' | 'restricted'>('tenant')
const rows = ref<Row[]>([])
const initial = ref('')

const dirty = computed(() => JSON.stringify(snapshot()) !== initial.value)

const isAdmin = computed(() => authStore.hasRole('admin'))
const backfilling = ref(false)
const previewStats = ref<Record<string, number>>({})

async function loadPreviewStats() {
  if (!isAdmin.value) return
  try {
    const res: any = await getPreviewBackfillStatus()
    previewStats.value = (res?.data || {}) as Record<string, number>
  } catch {
    // Reporting is best effort; the policy editor still works.
  }
}

async function startBackfill() {
  backfilling.value = true
  try {
    const res: any = await backfillKnowledgePreviews()
    const queued = res?.data?.queued ?? 0
    MessagePlugin.success(t('knowledgeBase.access.backfillQueued', { count: queued }))
    await loadPreviewStats()
  } catch (err) {
    console.error('Preview backfill failed:', err)
    MessagePlugin.error(t('knowledgeBase.access.saveFailed'))
  } finally {
    backfilling.value = false
  }
}

function snapshot() {
  return {
    visibility_mode: visibilityMode.value,
    members: rows.value
      .filter((row) => row.can_view || row.can_download)
      .map((row) => ({ user_id: row.user_id, can_view: row.can_view, can_download: row.can_download })),
  }
}

onMounted(async () => {
  try {
    // selectedTenantId is only set after an explicit workspace switch; the
    // active workspace is effectiveTenantId. Fall back to the KB's own tenant
    // when the store has not resolved one yet.
    let tenantId = authStore.effectiveTenantId
    if (!tenantId) {
      const kbRes: any = await getKnowledgeBaseById(props.kbId).catch(() => null)
      tenantId = kbRes?.data?.tenant_id ? Number(kbRes.data.tenant_id) : null
    }
    const [policyRes, members] = await Promise.all([
      getKnowledgeBaseAccess(props.kbId),
      tenantId ? fetchAllTenantMembers(tenantId) : Promise.resolve([]),
    ])
    const policy: any = (policyRes as any)?.data || {}
    visibilityMode.value = policy.visibility_mode === 'restricted' ? 'restricted' : 'tenant'
    const grants = new Map<string, { can_view: boolean; can_download: boolean }>()
    for (const member of policy.members || []) {
      grants.set(member.user_id, { can_view: member.can_view, can_download: member.can_download })
    }
    rows.value = (members || []).map((member: any) => {
      const grant = grants.get(member.user_id)
      return {
        user_id: member.user_id,
        username: member.username,
        email: member.email,
        can_view: grant?.can_view ?? false,
        can_download: grant?.can_download ?? false,
      }
    })
    initial.value = JSON.stringify(snapshot())
    void loadPreviewStats()
  } catch (err) {
    console.error('Failed to load KB access policy:', err)
    MessagePlugin.error(t('knowledgeBase.access.loadFailed'))
  } finally {
    loading.value = false
  }
})

async function save() {
  saving.value = true
  try {
    await putKnowledgeBaseAccess(props.kbId, snapshot())
    initial.value = JSON.stringify(snapshot())
    MessagePlugin.success(t('knowledgeBase.access.saved'))
  } catch (err) {
    console.error('Failed to save KB access policy:', err)
    MessagePlugin.error(t('knowledgeBase.access.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.kb-access-panel { max-width: 860px; margin: 0 auto; padding: 24px; }
.kb-access-panel__header { display: flex; justify-content: space-between; gap: 16px; align-items: flex-start; }
.kb-access-panel__hint { color: var(--td-text-color-secondary); margin: 6px 0 16px; }
.kb-access-panel__mode { display: flex; align-items: center; gap: 12px; margin: 16px 0; }
.kb-access-panel__warn { color: var(--td-warning-color); margin-bottom: 12px; }
.kb-access-panel__row { display: flex; align-items: center; gap: 24px; padding: 10px 0; border-top: 1px solid var(--td-component-stroke); }
.kb-access-panel__identity { display: flex; flex-direction: column; flex: 1; min-width: 0; }
.kb-access-panel__identity span { color: var(--td-text-color-secondary); font-size: var(--app-text-2xs); }
.kb-access-panel__toggle { display: flex; align-items: center; gap: 6px; }
.kb-access-panel__empty { color: var(--td-text-color-secondary); padding: 16px 0; }
</style>

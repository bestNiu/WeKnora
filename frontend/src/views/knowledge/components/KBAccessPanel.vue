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

    <div class="kb-access-panel__mode">
      <span>{{ t('knowledgeBase.access.visibility') }}</span>
      <t-radio-group v-model="visibilityMode" variant="default-filled" @change="modeTouched = true">
        <t-radio-button value="tenant">{{ t('knowledgeBase.access.modeTenant') }}</t-radio-button>
        <t-radio-button value="restricted">{{ t('knowledgeBase.access.modeRestricted') }}</t-radio-button>
      </t-radio-group>
    </div>
    <p v-if="visibilityMode === 'restricted'" class="kb-access-panel__warn">
      {{ t('knowledgeBase.access.restrictedHint') }}
    </p>

    <!-- 搜索 + 分页：成员列表按工作区分页，不再一次性拉全租户 -->
    <div class="kb-access-panel__toolbar">
      <t-input
        v-model="query"
        size="small"
        clearable
        :placeholder="t('knowledgeBase.access.searchPlaceholder')"
        class="kb-access-panel__search"
        @change="onSearch"
      />
      <t-select
        v-model="tagFilter"
        size="small"
        clearable
        class="kb-access-panel__tagfilter"
        :placeholder="t('knowledgeBase.access.tagFilter')"
        @change="onSearch"
      >
        <t-option v-for="opt in tagOptions" :key="opt.tag" :value="opt.tag"
          :label="`${opt.tag} (${opt.count})`" />
      </t-select>
      <span class="kb-access-panel__count">{{ page }} / {{ pageCount }}</span>
      <t-button size="small" variant="text" :disabled="page <= 1" @click="goTo(page - 1)">‹</t-button>
      <t-button size="small" variant="text" :disabled="page >= pageCount" @click="goTo(page + 1)">›</t-button>
    </div>

    <t-loading v-if="loading" size="small" />
    <template v-else>
      <div class="kb-access-panel__table">
        <div class="kb-access-panel__row kb-access-panel__row--head">
          <span class="kb-access-panel__identity">{{ t('knowledgeBase.access.title') }}</span>
          <span class="kb-access-panel__tags">{{ t('knowledgeBase.access.tags') }}</span>
          <span class="kb-access-panel__num">{{ t('knowledgeBase.access.views') }}</span>
          <span class="kb-access-panel__num">{{ t('knowledgeBase.access.downloads') }}</span>
          <span class="kb-access-panel__num">{{ t('knowledgeBase.access.downloadsToday') }}</span>
          <span class="kb-access-panel__toggle">{{ t('knowledgeBase.access.canView') }}</span>
          <span class="kb-access-panel__toggle">{{ t('knowledgeBase.access.canDownload') }}</span>
        </div>
        <div v-for="member in rows" :key="member.user_id" class="kb-access-panel__row">
          <div class="kb-access-panel__identity">
            <strong>{{ member.username || member.email || member.user_id }}</strong>
            <span>{{ member.email }}</span>
          </div>
          <div class="kb-access-panel__tags">
            <t-tag v-for="tag in member.tags || []" :key="tag" size="small" variant="light">{{ tag }}</t-tag>
            <t-input
              v-if="editingUser === member.user_id"
              v-model="editingTags"
              size="small"
              :placeholder="t('knowledgeBase.access.tagsHint')"
              @enter="saveTags(member.user_id)"
              @blur="editingUser = ''"
            />
            <t-button v-else size="small" variant="text" @click="startEditTags(member)">
              {{ (member.tags || []).length ? t('knowledgeBase.access.editTags') : '+' }}
            </t-button>
          </div>
          <span class="kb-access-panel__num">{{ member.views_total || 0 }}</span>
          <span class="kb-access-panel__num">{{ member.downloads_total || 0 }}</span>
          <span class="kb-access-panel__num">{{ member.downloads_today || 0 }}</span>
          <label class="kb-access-panel__toggle">
            <t-checkbox
              v-model="member.can_view"
              :disabled="visibilityMode !== 'restricted'"
              @change="touch(member)"
            />
          </label>
          <label class="kb-access-panel__toggle">
            <t-checkbox v-model="member.can_download" @change="touch(member)" />
            <span v-if="isSuspended(member)" class="kb-access-panel__badge">
              {{ t('knowledgeBase.access.risk') }}
            </span>
          </label>
        </div>
      </div>
      <p v-if="!rows.length" class="kb-access-panel__empty">{{ t('knowledgeBase.access.empty') }}</p>

      <!-- 风控名单：被下载守卫标记/冻结的成员，可在此解冻 -->
      <div class="kb-access-panel__risk">
        <div class="kb-access-panel__risk-head">
          <strong>{{ t('knowledgeBase.access.riskList') }}</strong>
          <t-button size="small" variant="text" @click="loadRisk">{{ t('common.refresh') }}</t-button>
        </div>
        <p v-if="!riskRows.length" class="kb-access-panel__hint">{{ t('knowledgeBase.access.riskEmpty') }}</p>
        <div v-for="row in riskRows" :key="row.user_id" class="kb-access-panel__risk-row">
          <div class="kb-access-panel__identity">
            <strong>{{ row.username || row.email || row.user_id }}</strong>
            <span>{{ row.risk_reason || row.email }}</span>
          </div>
          <span class="kb-access-panel__num">{{ row.downloads_today }}</span>
          <t-button size="small" variant="outline" @click="clearRisk(row.user_id)">
            {{ t('knowledgeBase.access.clearRisk') }}
          </t-button>
        </div>
      </div>

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
  clearKBAccessRisk,
  getKBAccessRisk,
  getKBAccessTags,
  putKBAccessTags,
  getKnowledgeBaseAccess,
  getPreviewBackfillStatus,
  patchKnowledgeBaseAccess,
  type KBAccessMember,
  type KBAccessRiskMember,
} from '@/api/knowledge-base/index'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ kbId: string }>()
const { t } = useI18n()
const authStore = useAuthStore()

const loading = ref(true)
const saving = ref(false)
const visibilityMode = ref<'tenant' | 'restricted'>('tenant')
const modeTouched = ref(false)
const rows = ref<KBAccessMember[]>([])
const riskRows = ref<KBAccessRiskMember[]>([])

// 分页 + 搜索状态。dirty 记录被改过的成员，保存时只提交这些行：
// 整表提交在分页后会把当前页以外的人全部撤销。
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
const query = ref('')
const dirty = ref(false)
const tagFilter = ref('')
const tagOptions = ref<{ tag: string; count: number }[]>([])
const editingUser = ref('')
const editingTags = ref('')
const touched = new Set<string>()

const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
const isAdmin = computed(() => authStore.hasRole('admin'))
const backfilling = ref(false)
const previewStats = ref<Record<string, number>>({})

function isSuspended(member: KBAccessMember): boolean {
  if (!member.download_suspended_until) return false
  return new Date(member.download_suspended_until).getTime() > Date.now()
}

function touch(member: KBAccessMember) {
  touched.add(member.user_id)
  dirty.value = true
}

async function load() {
  loading.value = true
  try {
    const res: any = await getKnowledgeBaseAccess(props.kbId, {
      q: query.value,
      page: page.value,
      page_size: pageSize.value,
      tag: tagFilter.value,
    } as any)
    const policy: any = res?.data || {}
    visibilityMode.value = policy.visibility_mode === 'restricted' ? 'restricted' : 'tenant'
    rows.value = (policy.members || []) as KBAccessMember[]
    total.value = Number(policy.total || rows.value.length)
    modeTouched.value = false
    touched.clear()
    dirty.value = false
  } catch (err) {
    console.error('Failed to load KB access policy:', err)
    MessagePlugin.error(t('knowledgeBase.access.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function loadTags() {
  try {
    const res: any = await getKBAccessTags(props.kbId)
    tagOptions.value = (res?.data || []) as { tag: string; count: number }[]
  } catch {
    tagOptions.value = []
  }
}

function startEditTags(member: KBAccessMember) {
  editingUser.value = member.user_id
  editingTags.value = (member.tags || []).join(', ')
}

async function saveTags(userId: string) {
  const tags = editingTags.value.split(',').map((v) => v.trim()).filter(Boolean)
  try {
    await putKBAccessTags(props.kbId, userId, tags)
    editingUser.value = ''
    MessagePlugin.success(t('knowledgeBase.access.saved'))
    await Promise.all([load(), loadTags()])
  } catch (err) {
    console.error('Failed to save member tags:', err)
    MessagePlugin.error(t('knowledgeBase.access.saveFailed'))
  }
}

async function loadRisk() {
  try {
    const res: any = await getKBAccessRisk(props.kbId)
    riskRows.value = (res?.data || []) as KBAccessRiskMember[]
  } catch {
    // 风控名单是附加信息，失败不影响权限编辑。
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

async function save() {
  saving.value = true
  try {
    const members = rows.value.filter((row) => touched.has(row.user_id))
    // 只提交改动过的成员：PATCH 是增量语义，其它页的授权不受影响。
    await patchKnowledgeBaseAccess(props.kbId, {
      visibility_mode: visibilityMode.value,
      members: members.map((row) => ({
        user_id: row.user_id,
        can_view: row.can_view,
        can_download: row.can_download,
      })),
    })
    touched.clear()
    dirty.value = false
    modeTouched.value = false
    MessagePlugin.success(t('knowledgeBase.access.saved'))
    await loadRisk()
  } catch (err) {
    console.error('Failed to save KB access policy:', err)
    MessagePlugin.error(t('knowledgeBase.access.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function clearRisk(userId: string) {
  try {
    await clearKBAccessRisk(props.kbId, userId)
    MessagePlugin.success(t('knowledgeBase.access.riskCleared'))
    await loadRisk()
    await load()
  } catch (err) {
    console.error('Failed to clear download freeze:', err)
    MessagePlugin.error(t('knowledgeBase.access.saveFailed'))
  }
}

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

onMounted(async () => {
  await load()
  void loadTags()
  void loadRisk()
  void loadPreviewStats()
})
</script>

<style scoped>
.kb-access-panel { max-width: 960px; margin: 0 auto; padding: 24px; }
.kb-access-panel__header { display: flex; justify-content: space-between; gap: 16px; align-items: flex-start; }
.kb-access-panel__hint { color: var(--td-text-color-secondary); margin: 6px 0 16px; }
.kb-access-panel__mode { display: flex; align-items: center; gap: 12px; margin: 16px 0; }
.kb-access-panel__warn { color: var(--td-warning-color); margin-bottom: 12px; }
.kb-access-panel__toolbar { display: flex; align-items: center; gap: 8px; margin: 12px 0; }
.kb-access-panel__search { max-width: 260px; }
.kb-access-panel__count { color: var(--td-text-color-secondary); font-size: var(--app-text-2xs); margin-left: auto; }
.kb-access-panel__row { display: flex; align-items: center; gap: 16px; padding: 8px 0; border-top: 1px solid var(--td-component-stroke); }
.kb-access-panel__row--head { border-top: none; color: var(--td-text-color-secondary); font-size: var(--app-text-2xs); }
.kb-access-panel__identity { display: flex; flex-direction: column; flex: 1; min-width: 0; }
.kb-access-panel__identity span { color: var(--td-text-color-secondary); font-size: var(--app-text-2xs); }
.kb-access-panel__tagfilter { width: 150px; }
.kb-access-panel__tags { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; width: 180px; }
.kb-access-panel__num { width: 64px; text-align: right; color: var(--td-text-color-secondary); }
.kb-access-panel__toggle { display: flex; align-items: center; gap: 6px; width: 92px; }
.kb-access-panel__badge { color: var(--td-error-color); font-size: var(--app-text-2xs); }
.kb-access-panel__empty { color: var(--td-text-color-secondary); padding: 16px 0; }
.kb-access-panel__risk { margin-top: 24px; border-top: 1px solid var(--td-component-stroke); padding-top: 12px; }
.kb-access-panel__risk-head { display: flex; justify-content: space-between; align-items: center; }
.kb-access-panel__risk-row { display: flex; align-items: center; gap: 16px; padding: 8px 0; border-top: 1px dashed var(--td-component-stroke); }
.kb-access-panel__backfill { margin-top: 24px; display: flex; justify-content: space-between; align-items: center; gap: 16px; }
</style>
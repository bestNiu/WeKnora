<template>
  <t-select
    :value="modelValue"
    :options="options"
    :loading="loading"
    clearable
    filterable
    :placeholder="placeholder"
    @change="onChange"
  />
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { fetchAllTenantMembers } from '@/api/tenant/members'
import { useAuthStore } from '@/stores/auth'

// Picks the member a machine principal (API key, IM bot, embed widget) acts as.
// Per-user knowledge-base visibility and download then apply to its traffic.
const props = defineProps<{ modelValue?: string; placeholder?: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const authStore = useAuthStore()
const options = ref<Array<{ label: string; value: string }>>([])
const loading = ref(false)

onMounted(async () => {
  const tenantId = authStore.selectedTenantId
  if (!tenantId) return
  loading.value = true
  try {
    const members = await fetchAllTenantMembers(tenantId)
    options.value = (members || []).map((member: any) => ({
      label: member.username ? `${member.username} (${member.email})` : member.email,
      value: member.user_id,
    }))
  } catch (err) {
    console.error('Failed to load workspace members:', err)
  } finally {
    loading.value = false
  }
})

function onChange(value: string) {
  emit('update:modelValue', value || '')
}
</script>

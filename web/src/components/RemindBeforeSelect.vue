<template>
  <select :value="modelValue" class="w-full" @change="onChange">
    <option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option>
  </select>
</template>

<script setup lang="ts">
// 提前提醒时间（分钟）。0 表示不提前，就在到期时刻提醒。
const props = defineProps<{ modelValue: number }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: number): void }>()

const options = [
  { value: 0, label: '准时提醒' },
  { value: 5, label: '提前 5 分钟' },
  { value: 10, label: '提前 10 分钟' },
  { value: 15, label: '提前 15 分钟（默认）' },
  { value: 30, label: '提前 30 分钟' },
  { value: 60, label: '提前 1 小时' },
  { value: 120, label: '提前 2 小时' },
  { value: 1440, label: '提前 1 天' },
]

function onChange(event: Event) {
  emit('update:modelValue', Number((event.target as HTMLSelectElement).value))
}
</script>

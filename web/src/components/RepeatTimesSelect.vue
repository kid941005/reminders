<template>
  <select :value="modelValue" class="w-full" @change="onChange">
    <option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option>
  </select>
</template>

<script setup lang="ts">
// 过期后最多重复提醒的次数。0 表示不限次数，直到用户处理为止（历史行为）。
const props = defineProps<{ modelValue: number }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: number): void }>()

const options = [
  { value: 0, label: '不限次数' },
  { value: 1, label: '最多 1 次' },
  { value: 2, label: '最多 2 次' },
  { value: 3, label: '最多 3 次' },
  { value: 5, label: '最多 5 次' },
  { value: 10, label: '最多 10 次' },
  { value: 20, label: '最多 20 次' },
]

function onChange(event: Event) {
  emit('update:modelValue', Number((event.target as HTMLSelectElement).value))
}
</script>

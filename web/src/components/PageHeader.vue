<template>
  <div class="flex flex-wrap items-start justify-between gap-3">
    <div class="min-w-0">
      <p v-if="eyebrow" class="type-label mb-1.5">{{ eyebrow }}</p>
      <h1 class="type-h1">{{ title }}</h1>
      <p v-if="description" class="type-body-sm mt-1.5 text-muted-foreground">{{ description }}</p>
    </div>
    <div v-if="$slots.actions" class="flex items-center gap-2 shrink-0">
      <slot name="actions" />
    </div>
  </div>
</template>

<script setup lang="ts">
withDefaults(
  defineProps<{
    title: string
    description?: string
    eyebrow?: string
  }>(),
  { description: '', eyebrow: '' },
)
</script>
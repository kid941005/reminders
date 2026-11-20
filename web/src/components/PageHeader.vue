<template>
  <div class="flex flex-wrap items-start justify-between gap-3">
    <div class="min-w-0">
      <p v-if="eyebrow" class="text-xs font-extrabold uppercase tracking-[.18em] text-brand-600 dark:text-brand-300 mb-1">{{ eyebrow }}</p>
      <h1 class="font-display text-2xl font-extrabold tracking-tight sm:text-3xl">{{ title }}</h1>
      <p v-if="description" class="text-[13px] sm:text-sm text-muted-foreground mt-1">{{ description }}</p>
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

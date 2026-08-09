<template>
  <UCard>
    <template #header>
      <div class="flex items-center gap-2 font-semibold">
        <UIcon name="i-lucide-printer" class="w-5 h-5" />
        打印机
      </div>
    </template>
    <UFormField label="选择打印机">
      <USelect
        :model-value="modelValue"
        :items="printerItems"
        value-key="value"
        label-key="label"
        class="w-full"
        @update:model-value="onSelect"
      />
    </UFormField>
  </UCard>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  modelValue: { type: String, default: '' },
  printers: { type: Array, default: () => [] }
})

const emit = defineEmits(['update:modelValue', 'change'])

const printerItems = computed(() =>
  props.printers.map(p => ({ label: `${p.name} — ${p.uri}`, value: p.uri }))
)

function onSelect(val) {
  emit('update:modelValue', val)
  emit('change')
}
</script>

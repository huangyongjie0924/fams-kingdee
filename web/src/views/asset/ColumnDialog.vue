<template>
  <el-dialog
    :model-value="modelValue"
    title="列配置"
    width="560px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="hint">勾选需要在列表中显示的列，配置保存在本地浏览器。</div>
    <el-checkbox-group :model-value="enabled" @update:model-value="emit('update:enabled', $event)">
      <el-checkbox v-for="col in columns" :key="col.prop" :value="col.prop" :label="col.label" />
    </el-checkbox-group>
    <template #footer>
      <el-button @click="restore">恢复默认</el-button>
      <el-button type="primary" @click="emit('update:modelValue', false)">确定</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ASSET_COLUMNS } from "../../api/meta";

defineProps<{ modelValue: boolean; enabled: string[] }>();
const emit = defineEmits(["update:modelValue", "update:enabled"]);

const columns = ASSET_COLUMNS;

function restore() {
  emit(
    "update:enabled",
    ASSET_COLUMNS.filter((c) => c.defaultOn).map((c) => c.prop),
  );
}
</script>

<style scoped>
.hint {
  margin-bottom: 12px;
  color: #909399;
  font-size: 13px;
}

:deep(.el-checkbox) {
  width: 160px;
  margin-right: 0;
}
</style>

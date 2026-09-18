<template>
  <!--
    主数据的来源标记。
    星瀚托管的行会被下一次同步覆盖，本地手工行不会被同步碰到 ——
    这两类在列表里必须能一眼分开，否则「为什么我改的又变回去了」没法解释。
  -->
  <el-tag v-if="source === 'kingdee'" type="success" size="small" effect="light">星瀚托管</el-tag>
  <el-tag v-else type="info" size="small" effect="plain">手工维护</el-tag>
</template>

<script setup lang="ts">
defineProps<{ source?: string }>();
</script>

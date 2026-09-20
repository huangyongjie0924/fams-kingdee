<template>
  <div class="page">
    <el-card>
      <div class="toolbar">
        <span class="hint">这里只显示你自己提交的报修单。</span>
        <span class="spacer" />
        <el-button :icon="Refresh" @click="load">刷新</el-button>
      </div>

      <el-table v-if="!isMobile" :data="rows" border v-loading="loading" @row-click="go" class="clickable">
        <el-table-column prop="code" label="单号" width="160" />
        <el-table-column prop="asset_name" label="资产" min-width="160" show-overflow-tooltip />
        <el-table-column prop="fault_desc" label="问题描述" min-width="180" show-overflow-tooltip />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="repairStatusTag(row.status)" size="small">{{ row.status_label }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="处理人" width="120">
          <template #default="{ row }">{{ handlerText(row) }}</template>
        </el-table-column>
        <el-table-column label="最近变更" width="170">
          <template #default="{ row }">{{ formatTime(row.updated_at) }}</template>
        </el-table-column>
      </el-table>

      <div v-else v-loading="loading" class="m-list">
        <div v-for="row in rows" :key="row.id" class="m-card" @click="go(row)">
          <div class="m-card-hd">
            <span class="m-title">{{ row.asset_name || row.asset_code }}</span>
            <el-tag :type="repairStatusTag(row.status)" size="small">{{ row.status_label }}</el-tag>
          </div>
          <div class="m-grid">
            <span class="k">单号</span><span>{{ row.code }}</span>
            <span class="k">问题</span><span>{{ row.fault_desc }}</span>
            <span class="k">处理人</span><span>{{ handlerText(row) }}</span>
            <span class="k">最近变更</span><span>{{ formatTime(row.updated_at) }}</span>
          </div>
        </div>
        <el-empty v-if="!rows.length && !loading" description="还没有报修记录" />
      </div>

      <div class="pager">
        <el-pagination
          :layout="isMobile ? 'prev, pager, next' : 'total, prev, pager, next'"
          :total="total"
          :current-page="page"
          :page-size="pageSize"
          :pager-count="isMobile ? 5 : 7"
          @current-change="onPage"
        />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { Refresh } from "@element-plus/icons-vue";
import http from "../../api/client";
import { repairStatusTag } from "../../api/meta";
import { useIsMobile } from "../../composables/useIsMobile";

const router = useRouter();
const isMobile = useIsMobile();
const rows = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = 20;
const loading = ref(false);

function handlerText(row: any): string {
  if (row.assignee_name) return row.assignee_name;
  if (row.vendor_name) return row.vendor_name;
  return "待指派";
}

function formatTime(t: string) {
  return (t || "").replace("T", " ").slice(0, 19);
}

async function load() {
  loading.value = true;
  try {
    const { data } = await http.get("/repairs", {
      params: { mine: 1, page: page.value, page_size: pageSize },
    });
    rows.value = data.items || [];
    total.value = data.total || 0;
  } finally {
    loading.value = false;
  }
}

function onPage(p: number) {
  page.value = p;
  load();
}

function go(row: any) {
  router.push({ name: "repair-detail", params: { id: row.id } });
}

onMounted(load);
</script>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.hint {
  color: #909399;
  font-size: 13px;
}

.spacer {
  flex: 1;
}

.clickable :deep(.el-table__row) {
  cursor: pointer;
}

.pager {
  margin-top: 12px;
  display: flex;
  justify-content: flex-end;
}
</style>

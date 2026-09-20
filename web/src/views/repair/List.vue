<template>
  <div class="page">
    <el-card>
      <div class="toolbar">
        <span class="title">{{ mine ? "我的维修" : "维修管理" }}</span>
        <span class="spacer" />
        <el-button :icon="Refresh" @click="load">刷新</el-button>
      </div>

      <div class="filters">
        <el-select
          v-model="query.status"
          multiple
          collapse-tags
          clearable
          placeholder="状态"
          style="width: 220px"
          @change="reload(1)"
        >
          <el-option v-for="s in statusOptions" :key="s.value" :label="s.label" :value="s.value" />
        </el-select>
        <el-select
          v-if="!mine"
          v-model="query.use_dept_id"
          clearable
          filterable
          placeholder="使用部门"
          style="width: 180px"
          @change="reload(1)"
        >
          <el-option v-for="d in departments" :key="d.id" :label="d.name" :value="d.id" />
        </el-select>
        <el-input
          v-model="query.asset_code"
          placeholder="资产编码"
          clearable
          style="width: 180px"
          @keyup.enter="reload(1)"
          @clear="reload(1)"
        />
        <el-date-picker
          v-model="dateRange"
          type="daterange"
          value-format="YYYY-MM-DD"
          start-placeholder="开始日期"
          end-placeholder="结束日期"
          style="width: 240px"
          @change="reload(1)"
        />
      </div>

      <el-table v-if="!isMobile" :data="rows" border v-loading="loading" @row-click="go" class="clickable">
        <el-table-column prop="code" label="单号" width="160" />
        <el-table-column label="资产" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">{{ row.asset_name }}（{{ row.asset_code }}）</template>
        </el-table-column>
        <el-table-column prop="fault_desc" label="问题描述" min-width="160" show-overflow-tooltip />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="repairStatusTag(row.status)" size="small">{{ row.status_label }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="报修人" width="100">
          <template #default="{ row }">{{ row.reporter_name || "-" }}</template>
        </el-table-column>
        <el-table-column label="处理人" width="120">
          <template #default="{ row }">{{ handlerText(row) }}</template>
        </el-table-column>
        <el-table-column label="使用部门" width="140">
          <template #default="{ row }">{{ row.use_dept_name || "-" }}</template>
        </el-table-column>
        <el-table-column label="提交时间" width="170">
          <template #default="{ row }">{{ formatTime(row.created_at) }}</template>
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
            <span class="k">报修人</span><span>{{ row.reporter_name || "-" }}</span>
            <span class="k">处理人</span><span>{{ handlerText(row) }}</span>
            <span class="k">提交时间</span><span>{{ formatTime(row.created_at) }}</span>
          </div>
        </div>
        <el-empty v-if="!rows.length && !loading" description="暂无维修单" />
      </div>

      <div class="pager">
        <el-pagination
          :layout="isMobile ? 'prev, pager, next' : 'total, sizes, prev, pager, next'"
          :total="total"
          :current-page="page"
          :page-size="pageSize"
          :page-sizes="[20, 50, 100]"
          :pager-count="isMobile ? 5 : 7"
          @current-change="onPage"
          @size-change="onSize"
        />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Refresh } from "@element-plus/icons-vue";
import http from "../../api/client";
import { REPAIR_STATUS_LABELS, repairStatusTag } from "../../api/meta";
import { useIsMobile } from "../../composables/useIsMobile";

const route = useRoute();
const router = useRouter();
const isMobile = useIsMobile();

const mine = computed(() => route.query.mine === "1");
const rows = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);
const loading = ref(false);
const departments = ref<any[]>([]);
const dateRange = ref<string[]>([]);

const query = ref<Record<string, any>>({
  status: [] as string[],
  use_dept_id: 0,
  asset_code: "",
});

// 状态下拉来自后端枚举，标签用中文白话（业务可预期，不暴露内部码）
const statusOptions = computed(() =>
  Object.entries(REPAIR_STATUS_LABELS).map(([value, label]) => ({ value, label })),
);

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
    const params: Record<string, any> = {
      page: page.value,
      page_size: pageSize.value,
      status: query.value.status.join(","),
      use_dept_id: query.value.use_dept_id || 0,
      asset_code: query.value.asset_code || "",
    };
    if (mine.value) params.mine = 1;
    if (dateRange.value && dateRange.value.length === 2) {
      params.from = dateRange.value[0];
      params.to = dateRange.value[1];
    }
    const { data } = await http.get("/repairs", { params });
    rows.value = data.items || [];
    total.value = data.total || 0;
  } finally {
    loading.value = false;
  }
}

function reload(p?: number) {
  if (p) page.value = p;
  load();
}

function onPage(p: number) {
  page.value = p;
  load();
}

function onSize(s: number) {
  pageSize.value = s;
  page.value = 1;
  load();
}

function go(row: any) {
  router.push({ name: "repair-detail", params: { id: row.id } });
}

onMounted(async () => {
  if (!mine.value) {
    const { data } = await http.get("/departments");
    departments.value = data || [];
  }
  await load();
});
</script>

<style scoped>
.title {
  font-size: 16px;
  font-weight: 600;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.spacer {
  flex: 1;
}

.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 12px;
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

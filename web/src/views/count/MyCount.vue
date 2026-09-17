<template>
  <div class="page">
    <el-card>
      <div class="toolbar">
        <span class="hint">这里只显示指派给你的盘点任务。</span>
        <span class="spacer" />
        <el-button @click="load">刷新</el-button>
      </div>

      <el-table v-if="!isMobile" :data="plans" border v-loading="loading">
        <el-table-column prop="code" label="计划编号" width="150" />
        <el-table-column prop="name" label="计划名称" min-width="180" />
        <el-table-column label="我的任务" width="140" align="right">
          <template #default="{ row }">{{ row.done }} / {{ row.mine }}</template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag v-if="row.mine && row.done === row.mine" type="success" size="small">已盘完</el-tag>
            <el-tag v-else-if="row.mine" type="warning" size="small">待盘点</el-tag>
            <el-tag v-else type="info" size="small">无指派</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="备注" min-width="140" show-overflow-tooltip />
        <el-table-column label="操作" width="120" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" :disabled="!row.mine" @click="go(row)">去盘点</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div v-else v-loading="loading" class="m-list">
        <div v-for="row in plans" :key="row.id" class="m-card">
          <div class="m-card-hd">
            <span class="m-title">{{ row.name }}</span>
            <el-tag v-if="row.mine && row.done === row.mine" type="success" size="small">已盘完</el-tag>
            <el-tag v-else-if="row.mine" type="warning" size="small">待盘点</el-tag>
            <el-tag v-else type="info" size="small">无指派</el-tag>
          </div>
          <div class="m-grid">
            <span class="k">计划编号</span><span>{{ row.code || "-" }}</span>
            <span class="k">我的任务</span><span>{{ row.done }} / {{ row.mine }}</span>
            <span v-if="row.remark" class="k">备注</span><span v-if="row.remark">{{ row.remark }}</span>
          </div>
          <div class="m-field">
            <el-button type="primary" :disabled="!row.mine" style="width: 100%" @click="go(row)">去盘点</el-button>
          </div>
        </div>
        <el-empty v-if="!plans.length && !loading" description="暂无指派给你的盘点任务" />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import http from "../../api/client";
import { useIsMobile } from "../../composables/useIsMobile";

const router = useRouter();
const isMobile = useIsMobile();
const plans = ref<any[]>([]);
const loading = ref(false);

async function load() {
  loading.value = true;
  try {
    const { data } = await http.get("/count/plans", { params: { page: 1, page_size: 200 } });
    const counting = (data.items || []).filter((p: any) => p.status === "counting");
    // 每个计划取一次「我的」明细，算出我自己的进度。
    // 单个计划失败（最常见的是账号没绑员工，后端返回 403）不能连累整页：
    // 否则 Promise.all 一拒，plans 留空，页面会误报成「暂无指派给你的盘点任务」。
    // 失败原因由 axios 拦截器统一弹提示，这里不再重复报一次。
    const withMine = await Promise.all(
      counting.map(async (p: any) => {
        try {
          const { data: d } = await http.get(`/count/plans/${p.id}/items`, { params: { mine: 1 } });
          const mine: any[] = d.items || [];
          return { ...p, mine: mine.length, done: mine.filter((it) => it.result).length };
        } catch {
          return { ...p, mine: 0, done: 0 };
        }
      }),
    );
    plans.value = withMine;
  } finally {
    loading.value = false;
  }
}

function go(row: any) {
  router.push({ name: "count-detail", params: { id: row.id } });
}

onMounted(load);
</script>

<style scoped>
.page {
  padding: 12px;
}

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
</style>

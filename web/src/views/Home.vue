<template>
  <div class="page home">
    <!-- 1. 「待我处理」：首屏最显眼位置，本页核心。待办项全部由服务端按角色算好，前端只渲染。 -->
    <el-card class="block" v-loading="loading">
      <template #header>
        <div class="block-hd">
          <span class="block-title">待我处理</span>
          <el-button text :icon="Refresh" :disabled="loading" @click="load">刷新</el-button>
        </div>
      </template>

      <el-alert
        v-if="loadError"
        type="error"
        :closable="false"
        show-icon
        title="首页数据加载失败，请稍后重试"
      />
      <div v-else-if="todo.length" class="todo-grid">
        <div
          v-for="t in todo"
          :key="t.key"
          class="todo-item"
          :class="{ 'is-zero': t.count <= 0 }"
          role="button"
          tabindex="0"
          @click="goTodo(t)"
          @keyup.enter="goTodo(t)"
        >
          <span class="todo-count">{{ t.count }}</span>
          <span class="todo-label">{{ t.label }}</span>
        </div>
      </div>
      <el-empty v-else :image-size="72" description="当前没有待你处理的事项" />
    </el-card>

    <!-- 2. 资产概况：总数 + 按状态分布（CSS 比例条，不引图表库）。资产列表对所有角色可见，故不设权限门。 -->
    <el-card class="block" v-loading="loading">
      <template #header>
        <span class="block-title">资产概况</span>
      </template>

      <div class="stat-row">
        <el-statistic title="资产总数" :value="assetTotal" />
      </div>

      <div v-if="assetStatus.length" class="bar-list">
        <div v-for="s in assetStatus" :key="s.status" class="bar-item">
          <div class="bar-hd">
            <span class="bar-label">{{ s.status }}</span>
            <span class="bar-count">{{ s.count }}</span>
          </div>
          <div class="bar-track">
            <div class="bar-fill" :style="{ width: pct(s.count, assetStatus) + '%' }" />
          </div>
        </div>
      </div>
      <el-empty v-else :image-size="60" description="暂无可统计的资产" />

      <template v-if="assetByCategory.length">
        <el-divider content-position="left">按类别</el-divider>
        <div class="bar-list">
          <div v-for="c in assetByCategory" :key="c.name" class="bar-item">
            <div class="bar-hd">
              <span class="bar-label">{{ c.name }}</span>
              <span class="bar-count">{{ c.count }}</span>
            </div>
            <div class="bar-track">
              <div class="bar-fill" :style="{ width: pct(c.count, assetByCategory) + '%' }" />
            </div>
          </div>
        </div>
      </template>
    </el-card>

    <!-- 3. 维修流程概况：各状态计数 + CSS 条。区块显隐沿用 SideMenu 的 auth.can 风格。 -->
    <el-card v-if="canSeeRepair" class="block" v-loading="loading">
      <template #header>
        <span class="block-title">维修流程概况</span>
      </template>

      <div v-if="repairStatus.length" class="bar-list">
        <div v-for="r in repairStatus" :key="r.status" class="bar-item">
          <div class="bar-hd">
            <span class="bar-label">{{ r.label || repairStatusLabel(r.status) }}</span>
            <span class="bar-count">{{ r.count }}</span>
          </div>
          <div class="bar-track">
            <div class="bar-fill is-repair" :style="{ width: pct(r.count, repairStatus) + '%' }" />
          </div>
        </div>
      </div>
      <el-empty v-else :image-size="60" description="暂无维修单" />
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { Refresh } from "@element-plus/icons-vue";
import {
  getDashboard,
  type Dashboard,
  type DashboardCategoryCount,
  type DashboardStatusCount,
  type DashboardTodo,
} from "../api/dashboard";
import { repairStatusLabel } from "../api/meta";
import { auth } from "../stores/auth";

const router = useRouter();

const loading = ref(false);
const loadError = ref(false);

// 空态数据：接口未返回或失败时用它兜底，保证模板不会读到 undefined 而抛错。
function emptyDashboard(): Dashboard {
  return {
    todo: [],
    overview: { asset_total: 0, asset_status: [], asset_by_category: [], repair_status: [] },
  };
}

const data = ref<Dashboard>(emptyDashboard());

const todo = computed<DashboardTodo[]>(() => data.value.todo ?? []);
const assetTotal = computed(() => data.value.overview?.asset_total ?? 0);
const assetStatus = computed<DashboardStatusCount[]>(() => data.value.overview?.asset_status ?? []);
const assetByCategory = computed<DashboardCategoryCount[]>(
  () => data.value.overview?.asset_by_category ?? [],
);
const repairStatus = computed<DashboardStatusCount[]>(
  () => data.value.overview?.repair_status ?? [],
);

// 流程概况区块的显隐，沿用 SideMenu.vue 的 auth.can 风格。
// 注意：只在这里做「区块级」判断；**待办项本身不按角色过滤**——哪些待办出现由服务端决定。
const canSeeRepair = computed(
  () =>
    auth.can("repair.report") ||
    auth.can("repair.handle") ||
    auth.can("repair.dispatch") ||
    auth.can("repair.approve") ||
    auth.can("repair.manage"),
);

// 比例条宽度：以当前分组内的最大值为 100%，纯 CSS，不引入图表库。
function pct(count: number, list: { count: number }[]): number {
  const max = Math.max(1, ...list.map((i) => i.count));
  return Math.max(0, Math.min(100, Math.round((count / max) * 100)));
}

// 待办项跳转：link 形如 "/repairs?status=pending"，由服务端拼好，前端直接 push。
function goTodo(t: DashboardTodo) {
  if (t.link) router.push(t.link);
}

async function load() {
  loading.value = true;
  loadError.value = false;
  try {
    data.value = await getDashboard();
  } catch {
    // client.ts 的响应拦截器已统一弹出错误提示，这里只标记状态，避免重复弹窗。
    loadError.value = true;
    data.value = emptyDashboard();
  } finally {
    loading.value = false;
  }
}

onMounted(load);
</script>

<style scoped>
.home {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.block {
  margin: 0;
}

.block-hd {
  display: flex;
  align-items: center;
  gap: 8px;
}

.block-title {
  flex: 1;
  font-size: 16px;
  font-weight: 600;
}

/* —— 待我处理 —— */
.todo-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 12px;
}

.todo-item {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  padding: 14px 16px;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  background: #f5f7fa;
  cursor: pointer;
  transition: border-color 0.2s, box-shadow 0.2s, transform 0.1s;
}

.todo-item:hover {
  border-color: #409eff;
  box-shadow: 0 2px 10px rgba(64, 158, 255, 0.15);
}

.todo-item:active {
  transform: translateY(1px);
}

.todo-item:focus-visible {
  outline: 2px solid #409eff;
  outline-offset: 1px;
}

.todo-count {
  font-size: 30px;
  font-weight: 700;
  line-height: 1.1;
  color: #409eff;
}

.todo-label {
  font-size: 13px;
  color: #606266;
}

/* count 为 0 的项仍然显示（让用户知道「这类事现在是 0」），但视觉上弱化。 */
.todo-item.is-zero {
  background: #fafafa;
  opacity: 0.6;
}

.todo-item.is-zero .todo-count {
  color: #c0c4cc;
}

/* —— 概览比例条 —— */
.stat-row {
  margin-bottom: 12px;
}

.bar-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.bar-hd {
  display: flex;
  justify-content: space-between;
  margin-bottom: 4px;
  font-size: 13px;
  color: #606266;
}

.bar-count {
  font-weight: 600;
  color: #303133;
}

.bar-track {
  height: 8px;
  background: #ebeef5;
  border-radius: 4px;
  overflow: hidden;
}

.bar-fill {
  height: 100%;
  background: #409eff;
  border-radius: 4px;
  transition: width 0.3s;
}

.bar-fill.is-repair {
  background: #e6a23c;
}

@media (max-width: 767px), (max-height: 520px) {
  .todo-grid {
    grid-template-columns: repeat(2, 1fr);
    gap: 8px;
  }

  .todo-count {
    font-size: 26px;
  }
}
</style>

<template>
  <div class="page home">
    <!-- 1. 问候条：先告诉用户「这是谁的工作台」，再往下才是具体的事。 -->
    <div class="greet">
      <div class="greet-main">
        <span class="greet-hi">{{ greeting }}，{{ displayName }}</span>
        <el-tag v-if="roleLabel" size="small" type="info">{{ roleLabel }}</el-tag>
        <span class="greet-date">{{ todayText }}</span>
      </div>
      <!-- 报修是首页唯一的主操作；repair_tech 没有 repair.report，强行展示会点出 403 -->
      <el-button
        v-if="auth.can('repair.report')"
        class="greet-act"
        type="primary"
        :icon="EditPen"
        @click="go('/repairs/new')"
      >
        报修
      </el-button>
    </div>

    <!-- 2. 「待我处理」：首屏最显眼位置，本页核心。待办项全部由服务端按角色算好，前端只渲染。 -->
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
      <div v-else-if="todo.length" class="todo-list">
        <div
          v-for="t in todo"
          :key="t.key"
          class="todo-row"
          :class="{ 'is-zero': t.count <= 0 }"
          :style="rowStyle(t)"
          role="button"
          tabindex="0"
          @click="goTodo(t)"
          @keyup.enter="goTodo(t)"
        >
          <span class="todo-bar" />
          <span class="todo-count">{{ t.count }}</span>
          <span class="todo-label">{{ t.label }}</span>
          <el-icon class="todo-arrow"><ArrowRight /></el-icon>
        </div>
      </div>
      <el-empty v-else :image-size="72" description="当前没有待你处理的事项" />
    </el-card>

    <!-- 3. 指标卡：服务端按角色下发的「与你相关的量」。后端 stats 字段未上线时整块不渲染。 -->
    <el-card v-if="stats.length" class="block" v-loading="loading">
      <template #header>
        <span class="block-title">概览</span>
      </template>

      <div class="stat-grid">
        <div
          v-for="s in stats"
          :key="s.key"
          class="stat-item"
          role="button"
          tabindex="0"
          @click="go(s.link)"
          @keyup.enter="go(s.link)"
        >
          <div class="stat-count">{{ s.count }}</div>
          <div class="stat-label">{{ s.label }}</div>
        </div>
      </div>
    </el-card>

    <!-- 4. 快速动作：4 格入口，按权限出现。路由 path 逐条对着 router/index.ts 核过，不留死链。 -->
    <el-card class="block">
      <template #header>
        <span class="block-title">快捷入口</span>
      </template>

      <div class="quick-grid">
        <div
          v-for="a in quickActions"
          :key="a.key"
          class="quick-item"
          role="button"
          tabindex="0"
          @click="go(a.path)"
          @keyup.enter="go(a.path)"
        >
          <el-icon class="quick-icon"><component :is="a.icon" /></el-icon>
          <span class="quick-label">{{ a.label }}</span>
        </div>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, type Component } from "vue";
import { useRouter } from "vue-router";
import { ArrowRight, EditPen, Refresh, Tickets, Tools } from "@element-plus/icons-vue";
import {
  getDashboard,
  type Dashboard,
  type DashboardStatCard,
  type DashboardTodo,
} from "../api/dashboard";
import { auth } from "../stores/auth";

const router = useRouter();

const loading = ref(false);
const loadError = ref(false);

// 空态数据：接口未返回或失败时用它兜底，保证模板不会读到 undefined 而抛错。
function emptyDashboard(): Dashboard {
  return {
    todo: [],
    stats: [],
    overview: { asset_total: 0, asset_status: [], asset_by_category: [], repair_status: [] },
  };
}

const data = ref<Dashboard>(emptyDashboard());

const todo = computed<DashboardTodo[]>(() => data.value.todo ?? []);
// stats 由后端并行开发中、此刻还没上线，必须防御：取不到就是空数组，整块不渲染。
const stats = computed<DashboardStatCard[]>(() => data.value.stats ?? []);

// —— 问候条 ——
const WEEK_DAYS = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

const greeting = computed(() => {
  const h = new Date().getHours();
  if (h < 12) return "早上好";
  if (h < 18) return "下午好";
  return "晚上好";
});

const displayName = computed(() => auth.user?.real_name || auth.user?.username || "同事");

const roleLabel = computed(() => auth.roleLabel || "");

const todayText = computed(() => {
  const d = new Date();
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${WEEK_DAYS[d.getDay()]}`;
});

// —— 待办配色：紧急度由服务端给，前端只做「档位 → 颜色」这一层映射 ——
const LEVEL_COLOR: Record<string, string> = {
  danger: "#f56c6c",
  warning: "#e6a23c",
  info: "#409eff",
};

function levelOf(t: DashboardTodo): string {
  const lv = (t.level || "").toLowerCase();
  return lv === "danger" || lv === "warning" ? lv : "info";
}

// 0 的项仍然显示（让用户知道「这类事现在是 0」），但灰掉，不再抢紧急度的色。
function rowStyle(t: DashboardTodo): Record<string, string> {
  return { "--lv": t.count > 0 ? LEVEL_COLOR[levelOf(t)] : "#c0c4cc" };
}

// —— 快速动作 ——
interface QuickAction {
  key: string;
  label: string;
  path: string;
  icon: Component;
}

// 流程入口的显隐，沿用 SideMenu.vue 的 auth.can 风格。
// 注意：只在这里做「区块/入口级」判断；待办项与指标卡本身不按角色过滤——给什么由服务端决定。
const canSeeRepair = computed(
  () =>
    auth.can("repair.report") ||
    auth.can("repair.handle") ||
    auth.can("repair.dispatch") ||
    auth.can("repair.approve") ||
    auth.can("repair.manage"),
);

// 维修单入口落在哪张列表，按权限选「看得见且有内容」的那张，与侧边菜单保持一致。
const repairEntry = computed<{ label: string; path: string }>(() => {
  if (auth.can("repair.dispatch") || auth.can("repair.manage")) {
    return { label: "维修管理", path: "/repairs" };
  }
  if (auth.can("repair.handle")) {
    return { label: "我的维修", path: "/repairs?mine=1" };
  }
  return { label: "我的报修", path: "/repairs/mine" };
});

const quickActions = computed<QuickAction[]>(() => {
  const list: QuickAction[] = [];
  if (auth.can("repair.report")) {
    list.push({ key: "report", label: "我要报修", path: "/repairs/new", icon: EditPen });
  }
  // 资产列表对所有角色可见，与侧边菜单一致，不加权限门。
  list.push({ key: "assets", label: "资产列表", path: "/assets", icon: Tickets });
  if (canSeeRepair.value) {
    list.push({ key: "repairs", label: repairEntry.value.label, path: repairEntry.value.path, icon: Tools });
  }
  if (auth.can("sync.manage")) {
    list.push({ key: "sync", label: "金蝶同步", path: "/sync", icon: Refresh });
  }
  return list;
});

// 待办项跳转：link 形如 "/repairs?status=pending"，由服务端拼好，前端直接 push。
function goTodo(t: DashboardTodo) {
  if (t.link) router.push(t.link);
}

function go(link: string) {
  if (link) router.push(link);
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
/* 限宽只加在首页自己身上：别的页面（列表、表单）需要吃满宽度，不能动全局 .page */
.page.home {
  max-width: 1080px;
  margin: 0 auto;
}

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

/* —— 1. 问候条 —— */
.greet {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 18px;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
}

.greet-main {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: 1;
  min-width: 0;
  flex-wrap: wrap;
}

.greet-hi {
  font-size: 18px;
  font-weight: 600;
  color: #303133;
}

.greet-date {
  font-size: 13px;
  color: #606266;
}

/* —— 2. 待我处理：横向列表，一屏看得完 —— */
.todo-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.todo-row {
  position: relative;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px 12px 20px;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  background: #fff;
  cursor: pointer;
  transition: border-color 0.2s, box-shadow 0.2s, transform 0.1s;
}

.todo-row:hover {
  border-color: var(--lv);
  box-shadow: 0 2px 10px rgba(0, 0, 0, 0.06);
}

.todo-row:active {
  transform: translateY(1px);
}

.todo-row:focus-visible {
  outline: 2px solid var(--lv);
  outline-offset: 1px;
}

/* 左侧竖条与数字同色：颜色只表达紧急度档位，由 --lv 一处控制 */
.todo-bar {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 4px;
  background: var(--lv);
  border-radius: 8px 0 0 8px;
}

.todo-count {
  flex: 0 0 56px;
  font-size: 26px;
  font-weight: 700;
  line-height: 1.1;
  color: var(--lv);
}

.todo-label {
  flex: 1;
  min-width: 0;
  font-size: 14px;
  color: #303133;
}

.todo-arrow {
  flex: 0 0 auto;
  color: #c0c4cc;
}

/* count 为 0：--lv 已在 rowStyle 里降成灰，这里只把底色压暗 */
.todo-row.is-zero {
  background: #fafafa;
}

/* —— 3. 指标卡 —— */
.stat-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}

.stat-item {
  padding: 14px 16px;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  background: #f5f7fa;
  cursor: pointer;
  transition: border-color 0.2s, box-shadow 0.2s;
}

.stat-item:hover {
  border-color: #409eff;
  box-shadow: 0 2px 10px rgba(64, 158, 255, 0.12);
}

.stat-item:focus-visible {
  outline: 2px solid #409eff;
  outline-offset: 1px;
}

.stat-count {
  font-size: 24px;
  font-weight: 700;
  line-height: 1.2;
  color: #303133;
}

.stat-label {
  margin-top: 2px;
  font-size: 13px;
  color: #606266;
}

/* —— 4. 快速动作 —— */
.quick-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}

.quick-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 16px 8px;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  background: #fff;
  cursor: pointer;
  transition: border-color 0.2s, box-shadow 0.2s, transform 0.1s;
}

.quick-item:hover {
  border-color: #409eff;
  box-shadow: 0 2px 10px rgba(64, 158, 255, 0.12);
}

.quick-item:active {
  transform: translateY(1px);
}

.quick-item:focus-visible {
  outline: 2px solid #409eff;
  outline-offset: 1px;
}

.quick-icon {
  font-size: 22px;
  color: #409eff;
}

.quick-label {
  font-size: 13px;
  color: #606266;
}

.quick-item:hover .quick-label {
  color: #409eff;
}

@media (max-width: 767px), (max-height: 520px) {
  .greet {
    flex-wrap: wrap;
  }

  /* 窄屏问候条换行后，主操作按钮独占一行更好点 */
  .greet-act {
    width: 100%;
    margin-left: 0;
  }

  .todo-count {
    flex-basis: 44px;
    font-size: 22px;
  }

  .stat-grid,
  .quick-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }
}
</style>

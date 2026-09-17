<template>
  <div class="page">
    <!-- 这个页面没做窄屏适配：跑同步和看日志都在电脑上做，给个占位比给个残局好 -->
    <el-card v-if="isMobile">
      <el-empty description="金蝶同步管理需要在电脑上操作，请用电脑打开本页。" />
    </el-card>

    <el-card v-if="!isMobile">
      <template #header>
        <div class="head">
          <span>金蝶同步状态</span>
          <span class="spacer" />
          <el-button :icon="Refresh" @click="loadAll">刷新</el-button>
          <el-button v-if="auth.can('sync.manage')" :loading="testing" @click="testConnect">测试连接</el-button>
          <el-button
            v-if="auth.can('sync.manage')"
            type="primary"
            :loading="running === 'incremental'"
            :disabled="!info.configured || running !== ''"
            @click="run('incremental')"
          >
            立即增量同步
          </el-button>
          <el-button
            v-if="auth.can('sync.manage')"
            type="warning"
            :loading="running === 'full'"
            :disabled="!info.configured || !info.allow_full || running !== ''"
            @click="run('full')"
          >
            全量同步
          </el-button>
        </div>
      </template>

      <el-alert
        v-if="!info.configured"
        type="warning"
        :closable="false"
        show-icon
        title="金蝶同步未配置"
        description="缺少金蝶连接参数，请在配置文件或环境变量中补齐后重启服务。"
      />
      <el-alert
        v-else-if="info.dry_run"
        type="info"
        :closable="false"
        show-icon
        title="演练模式已开启"
        :description="
          info.enabled
            ? '同步只比对、不写库 —— 定时调度也只会比对，不会真正更新台账。确认影响面后关闭 dry_run。'
            : '同步只比对、不写库，可先确认影响面，再关闭 dry_run 执行真实同步。'
        "
      />
      <el-alert
        v-if="sched.last_error"
        class="alert"
        type="error"
        :closable="false"
        show-icon
        title="上次定时同步失败"
        :description="sched.last_error"
      />

      <el-descriptions :column="3" border class="desc">
        <el-descriptions-item label="配置状态">
          <el-tag :type="info.configured ? 'success' : 'info'" size="small">
            {{ info.configured ? "已配置" : "未配置" }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="定时同步">
          <el-tag :type="info.enabled ? 'success' : 'info'" size="small">
            {{ info.enabled ? "已启用" : "未启用" }}
          </el-tag>
          <el-tag v-if="sched.running" type="warning" size="small" class="ml">运行中</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="演练模式">{{ info.dry_run ? "是" : "否" }}</el-descriptions-item>
        <el-descriptions-item label="允许全量同步">{{ info.allow_full ? "是" : "否" }}</el-descriptions-item>
        <el-descriptions-item label="增量游标">{{ info.state?.cursor_value || "—" }}</el-descriptions-item>
        <el-descriptions-item label="最近成功时间">{{ fmt(info.state?.last_success_at) }}</el-descriptions-item>
        <el-descriptions-item label="同步间隔">
          {{ sched.enabled ? `${sched.interval_minutes} 分钟` : "—" }}
        </el-descriptions-item>
        <el-descriptions-item label="下次自动同步">
          {{ sched.enabled ? fmt(sched.next_run_at) : "—" }}
        </el-descriptions-item>
        <el-descriptions-item label="上次调度">
          <template v-if="sched.enabled && sched.last_run_at">
            {{ fmt(sched.last_run_at) }}
            <el-tag :type="sched.last_error ? 'danger' : 'success'" size="small" class="ml">
              {{ sched.last_error ? "失败" : "正常" }}
            </el-tag>
          </template>
          <span v-else>—</span>
        </el-descriptions-item>
      </el-descriptions>

      <div v-if="latest" class="latest">
        <span class="muted">最近一次运行</span>
        <el-tag :type="statusTag(latest.status)" size="small">{{ latest.status }}</el-tag>
        <span class="muted">{{ modeLabel(latest.mode) }}</span>
        <span class="muted">触发人 {{ latest.triggered_by || "—" }}</span>
        <span class="muted">{{ fmt(latest.started_at) }}</span>
        <span class="summary">{{ latest.error_summary }}</span>
      </div>
    </el-card>

    <el-card v-if="!isMobile" class="mt">
      <template #header>
        <div class="head">
          <span>运行记录</span>
          <span class="spacer" />
          <el-button :icon="Refresh" @click="reloadRuns">刷新</el-button>
        </div>
      </template>

      <el-table :data="runs" border v-loading="loadingRuns" @row-click="openErrors">
        <el-table-column prop="id" label="批次" width="70" />
        <el-table-column label="模式" width="80">
          <template #default="{ row }">{{ modeLabel(row.mode) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="statusTag(row.status)" size="small">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="triggered_by" label="触发人" width="100" />
        <el-table-column prop="total_count" label="总" width="70" align="right" />
        <el-table-column prop="created_count" label="新增" width="70" align="right" />
        <el-table-column prop="updated_count" label="更新" width="70" align="right" />
        <el-table-column prop="skipped_count" label="跳过" width="70" align="right" />
        <el-table-column prop="deleted_count" label="删除" width="70" align="right" />
        <el-table-column label="失败" width="70" align="right">
          <template #default="{ row }">
            <span :class="{ bad: row.failed_count > 0 }">{{ row.failed_count }}</span>
          </template>
        </el-table-column>
        <el-table-column label="开始时间" width="170">
          <template #default="{ row }">{{ fmt(row.started_at) }}</template>
        </el-table-column>
        <el-table-column label="结束时间" width="170">
          <template #default="{ row }">{{ fmt(row.finished_at) }}</template>
        </el-table-column>
        <el-table-column prop="error_summary" label="摘要" min-width="260" show-overflow-tooltip />
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" :disabled="!row.failed_count" @click.stop="openErrors(row)">
              错误明细
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="more">
        <el-button v-if="runs.length >= PAGE" :loading="loadingRuns" @click="loadMore">加载更多</el-button>
      </div>
    </el-card>

    <el-dialog v-model="errDialog" :title="`批次 #${errRun?.id} 错误明细`" width="900px">
      <el-table :data="errors" border v-loading="loadingErrors" max-height="440">
        <el-table-column prop="external_id" label="外部ID" width="150" />
        <el-table-column prop="stage" label="阶段" width="110" />
        <el-table-column prop="error_message" label="错误信息" min-width="320" show-overflow-tooltip />
        <el-table-column label="可重试" width="90">
          <template #default="{ row }">{{ row.retryable ? "是" : "否" }}</template>
        </el-table-column>
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ fmt(row.created_at) }}</template>
        </el-table-column>
      </el-table>
      <template #footer>
        <el-button @click="errDialog = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { ElMessage } from "element-plus";
import { Refresh } from "@element-plus/icons-vue";
import http from "../../api/client";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";

const isMobile = useIsMobile();
const PAGE = 50;
// 同步接口是同步阻塞的，最长可能跑几分钟，必须单次覆盖 axios 默认的 30s 超时
const RUN_TIMEOUT = 600000;

const info = ref<any>({
  configured: false,
  enabled: false,
  dry_run: false,
  allow_full: false,
  state: null,
  scheduler: null,
});
const runs = ref<any[]>([]);
const errors = ref<any[]>([]);
const errRun = ref<any>(null);
const errDialog = ref(false);
const testing = ref(false);
const running = ref("");
const loadingRuns = ref(false);
const loadingErrors = ref(false);

const latest = computed(() => (runs.value.length ? runs.value[0] : null));
// 未启用定时同步时后端给的是 { enabled: false }，这里兜住 null 的情况
const sched = computed(() => info.value.scheduler || { enabled: false });

async function loadStatus() {
  const { data } = await http.get("/sync/status");
  info.value = data || info.value;
}

async function reloadRuns() {
  loadingRuns.value = true;
  try {
    const { data } = await http.get("/sync/runs", { params: { limit: PAGE, offset: 0 } });
    runs.value = data || [];
  } finally {
    loadingRuns.value = false;
  }
}

async function loadMore() {
  loadingRuns.value = true;
  try {
    const { data } = await http.get("/sync/runs", { params: { limit: PAGE, offset: runs.value.length } });
    runs.value = runs.value.concat(data || []);
  } finally {
    loadingRuns.value = false;
  }
}

async function loadAll() {
  await Promise.all([loadStatus(), reloadRuns()]);
}

async function testConnect() {
  testing.value = true;
  try {
    await http.post("/sync/test-connect");
    ElMessage.success("金蝶连接正常");
  } finally {
    testing.value = false;
  }
}

async function run(mode: "incremental" | "full") {
  const label = mode === "full" ? "全量" : "增量";
  running.value = mode;
  try {
    const { data } = await http.post("/sync/run", { mode }, { timeout: RUN_TIMEOUT });
    ElMessage.success(
      `${label}同步完成：新增 ${data.created_count}、更新 ${data.updated_count}、跳过 ${data.skipped_count}、失败 ${data.failed_count}`,
    );
  } finally {
    running.value = "";
    await loadAll();
  }
}

async function openErrors(row: any) {
  if (!row?.failed_count) return;
  errRun.value = row;
  errDialog.value = true;
  loadingErrors.value = true;
  try {
    const { data } = await http.get(`/sync/runs/${row.id}/errors`, { params: { limit: 200, offset: 0 } });
    errors.value = data || [];
  } finally {
    loadingErrors.value = false;
  }
}

function statusTag(s: string) {
  if (s === "success") return "success";
  if (s === "partial") return "warning";
  if (s === "failed") return "danger";
  return "info";
}

function modeLabel(m: string) {
  return m === "full" ? "全量" : m === "incremental" ? "增量" : m || "—";
}

function fmt(v?: string | null) {
  if (!v) return "—";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

onMounted(loadAll);
</script>

<style scoped>
.head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.head .spacer {
  flex: 1;
}

.desc {
  margin-top: 12px;
}

.latest {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid #ebeef5;
}

.muted {
  color: #909399;
  font-size: 13px;
}

.summary {
  font-size: 13px;
  color: #606266;
}

.bad {
  color: #f56c6c;
  font-weight: 600;
}

.ml {
  margin-left: 6px;
}

.alert {
  margin-top: 12px;
}

.mt {
  margin-top: 12px;
}

.more {
  margin-top: 12px;
  text-align: center;
}
</style>

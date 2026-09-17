<template>
  <div class="page">
    <!-- 这个页面没做窄屏适配：文件读写和整批预检在手机上没法用，给个占位比给个残局好 -->
    <el-card v-if="isMobile">
      <el-empty description="批量导入需要在电脑上操作，请用电脑打开本页。" />
    </el-card>

    <el-card v-else>
      <template #header>
        <span class="title">Excel 批量导入</span>
      </template>

      <el-tabs v-model="tab">
        <!-- ==================== 新增资产 ==================== -->
        <el-tab-pane label="新增资产" name="create">
          <div class="toolbar">
            <span class="spacer" />
            <el-button :icon="Download" @click="downloadTemplate">下载模板</el-button>
          </div>

          <el-alert type="info" :closable="false" style="margin-bottom: 16px">
            <div>1. 先下载模板，按表头填写。资产名称与资产类别必填，资产编码留空则自动生成。</div>
            <div>2. 公司、部门、员工、区域、供应商按<b>名称</b>填写，系统里必须已存在，否则会报错。</div>
            <div>3. 校验采用全量预检：只要有一行不合格，整批都不会写入。</div>
            <div>4. 本页只<b>新增</b>：资产编码已存在的行会被拒绝。要改已有资产，请用「批量更新财务信息」。</div>
          </el-alert>

          <el-upload
            drag
            :http-request="doImport"
            accept=".xlsx"
            :show-file-list="false"
            :disabled="!auth.can('asset.manage')"
          >
            <el-icon class="el-icon--upload"><UploadFilled /></el-icon>
            <div class="el-upload__text">把 .xlsx 拖到此处，或<em>点击选择文件</em></div>
          </el-upload>

          <el-result
            v-if="result"
            icon="success"
            :title="`导入成功 ${result} 条`"
            sub-title="可到资产列表查看"
          >
            <template #extra>
              <el-button type="primary" @click="router.push({ name: 'assets' })">去资产列表</el-button>
            </template>
          </el-result>

          <div v-if="errors.length" class="errors">
            <el-alert type="error" :closable="false" :title="summary" style="margin-bottom: 8px" />
            <el-table :data="errors" border max-height="420">
              <el-table-column prop="row" label="行号" width="90" />
              <el-table-column prop="column" label="字段" width="160" />
              <el-table-column prop="message" label="问题" />
            </el-table>
          </div>
        </el-tab-pane>

        <!-- ==================== 批量更新财务信息 ==================== -->
        <el-tab-pane label="批量更新财务信息" name="finance">
          <el-alert type="warning" :closable="false" style="margin-bottom: 16px">
            <div>
              <b>只更新财务列</b>：原值、累计折旧、残值率(%)、财务使用期限(月)。
              文件里其他列（名称、部门、状态、数量…）一律不读，不会覆盖。
            </div>
            <div>
              <b>净值不用填</b>：这次改了原值或累计折旧的卡，系统按「原值 − 累计折旧」重算净值；
              没改的卡净值不动。
            </div>
            <div>单元格<b>留空 = 这一列不动</b>；填 0 才是「改成 0」。</div>
            <div>资产编码必须已在系统里存在 —— 本页只更新，不新增。</div>
          </el-alert>

          <div class="fin-step">
            <el-button :icon="Download" @click="downloadExport">① 导出当前资产清单</el-button>
            <span class="hint">在导出的表里改「原值 / 累计折旧」，保存后从下面上传</span>
          </div>

          <el-upload
            drag
            :http-request="doFinanceUpload"
            accept=".xlsx"
            :show-file-list="false"
            :disabled="!auth.can('asset.manage')"
          >
            <el-icon class="el-icon--upload"><UploadFilled /></el-icon>
            <div class="el-upload__text">② 把改好的 .xlsx 拖到此处，或<em>点击选择文件</em>（先预检，不直接写库）</div>
          </el-upload>

          <div v-if="finErrors.length" class="errors">
            <el-alert type="error" :closable="false" :title="finSummary" style="margin-bottom: 8px" />
            <el-table :data="finErrors" border max-height="420">
              <el-table-column prop="row" label="行号" width="90" />
              <el-table-column prop="column" label="字段" width="160" />
              <el-table-column prop="message" label="问题" />
            </el-table>
          </div>

          <div v-if="finPreview" class="errors">
            <el-alert
              :type="finPreview.updated ? 'info' : 'success'"
              :closable="false"
              :title="previewTitle"
              style="margin-bottom: 8px"
            />
            <el-table v-if="finPreview.changes.length" :data="finPreview.changes" border max-height="420">
              <el-table-column prop="asset_code" label="资产编码" width="190" />
              <el-table-column prop="name" label="资产名称" min-width="180" show-overflow-tooltip />
              <el-table-column label="将变更" min-width="340">
                <template #default="{ row }">
                  <div v-for="c in row.changes" :key="c.field" class="chg">
                    <span class="chg-field">{{ c.field }}</span>
                    <span class="chg-old">{{ c.old || "（空）" }}</span>
                    <span class="chg-arrow">→</span>
                    <span class="chg-new">{{ c.new || "（空）" }}</span>
                  </div>
                </template>
              </el-table-column>
            </el-table>
            <div v-if="finPreview.updated" class="confirm-bar">
              <el-button type="primary" :loading="finBusy" @click="confirmFinance">
                确认更新 {{ finPreview.updated }} 张卡
              </el-button>
              <el-button @click="resetFinance">取消</el-button>
            </div>
          </div>

          <el-result
            v-if="finResult > 0"
            icon="success"
            :title="`已更新 ${finResult} 张卡的财务信息`"
            sub-title="净值已按「原值 − 累计折旧」重算，变更逐字段记入履历"
          >
            <template #extra>
              <el-button type="primary" @click="router.push({ name: 'assets' })">去资产列表</el-button>
              <el-button @click="resetFinance">再传一批</el-button>
            </template>
          </el-result>
        </el-tab-pane>
      </el-tabs>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import { Download, UploadFilled } from "@element-plus/icons-vue";
import http, { download } from "../../api/client";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";

const isMobile = useIsMobile();
const router = useRouter();
const tab = ref("create");

/* ---------------- 新增导入 ---------------- */
const errors = ref<any[]>([]);
const summary = ref("");
const result = ref(0);

function downloadTemplate() {
  return download("/assets/import-template");
}

async function doImport(options: any) {
  errors.value = [];
  summary.value = "";
  result.value = 0;

  const fd = new FormData();
  fd.append("file", options.file);
  try {
    const { data } = await http.post("/assets/import", fd);
    result.value = data.imported;
  } catch (err: any) {
    const body = err.response?.data;
    if (body?.errors) {
      summary.value = body.error;
      errors.value = body.errors;
    }
  }
}

/* ---------------- 批量更新财务信息 ---------------- */
const finErrors = ref<any[]>([]);
const finSummary = ref("");
const finPreview = ref<any>(null);
const finResult = ref(0);
const finBusy = ref(false);
// 预检通过后原始文件要留着：确认时重传同一份，避免前端自己缓存解析结果
// 与后端实际落库的内容出现偏差（预览和落库必须是同一份输入）。
const finFile = ref<File | null>(null);

function downloadExport() {
  return download("/assets/export");
}

const previewTitle = computed(() => {
  const p = finPreview.value;
  if (!p) return "";
  if (!p.updated) return `共 ${p.rows} 行，没有需要变更的内容（当前值已经和文件一致）`;
  const parts = [`共 ${p.rows} 行，将更新 ${p.updated} 张卡的财务信息`];
  if (p.skipped) parts.push(`${p.skipped} 张无变化`);
  parts.push("确认后才会写库");
  return parts.join("，");
});

function resetFinance() {
  finErrors.value = [];
  finSummary.value = "";
  finPreview.value = null;
  finResult.value = 0;
  finFile.value = null;
}

async function doFinanceUpload(options: any) {
  resetFinance();
  finFile.value = options.file;

  const fd = new FormData();
  fd.append("file", options.file);
  try {
    const { data } = await http.post("/assets/import-fin", fd, { params: { dry_run: 1 } });
    finPreview.value = data;
  } catch (err: any) {
    finFile.value = null;
    const body = err.response?.data;
    if (body?.errors) {
      finSummary.value = body.error;
      finErrors.value = body.errors;
    }
  }
}

async function confirmFinance() {
  if (!finFile.value) return;
  finBusy.value = true;
  const fd = new FormData();
  fd.append("file", finFile.value);
  try {
    const { data } = await http.post("/assets/import-fin", fd);
    finResult.value = data.updated;
    finPreview.value = null;
    finFile.value = null;
    ElMessage.success(`已更新 ${data.updated} 张卡的财务信息`);
  } catch (err: any) {
    const body = err.response?.data;
    if (body?.errors) {
      finSummary.value = body.error;
      finErrors.value = body.errors;
    }
  } finally {
    finBusy.value = false;
  }
}
</script>

<style scoped>
.title {
  font-weight: 600;
}

.errors {
  margin-top: 16px;
}

.fin-step {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
}

.hint {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

.chg {
  display: flex;
  align-items: baseline;
  gap: 6px;
  line-height: 1.7;
}

.chg-field {
  color: var(--el-text-color-secondary);
  min-width: 116px;
}

.chg-old {
  color: var(--el-text-color-placeholder);
  text-decoration: line-through;
}

.chg-arrow {
  color: var(--el-text-color-placeholder);
}

.chg-new {
  color: var(--el-color-primary);
  font-weight: 600;
}

.confirm-bar {
  margin-top: 12px;
}
</style>

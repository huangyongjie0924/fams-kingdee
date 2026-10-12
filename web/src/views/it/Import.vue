<template>
  <div class="page">
    <!-- 与资产导入页同一条约定：文件读写和整批预检在手机上没法用，给占位比给残局好 -->
    <el-card v-if="isMobile">
      <el-empty description="批量导入需要在电脑上操作，请用电脑打开本页。" />
    </el-card>

    <el-card v-else>
      <template #header>
        <span class="title">IT 设备台账 · Excel 批量导入</span>
      </template>

      <div class="toolbar">
        <span class="spacer" />
        <el-button :icon="Download" @click="downloadTemplate">下载模板</el-button>
      </div>

      <el-alert type="info" :closable="false" style="margin-bottom: 16px">
        <div>1. 先下载模板，按表头填写。<b>资产编码必填</b> —— 导入靠它判重，留空会导致同一份文件导两次就重复一遍。</div>
        <div>2. 资产台账里<b>没有</b>的设备（外购路由器、自购配件等）：编码列填一个自有编码（如 IT-2026-0007），页面表单可以留空，但导入必须填。</div>
        <div>3. 编码在资产台账里存在时，会自动<b>关联</b>那张资产卡，并使用卡上的资产编码。</div>
        <div>4. 编码已经登记过 IT 信息的行会被<b>更新</b>（不是新增）—— 所以「导出 → 改 → 导回」可以反复执行。</div>
        <div>5. 使用人 / 部门按<b>名称</b>填写，系统里必须已存在，否则会报错。</div>
        <div>6. 校验采用全量预检：只要有一行不合格，整批都不会写入。</div>
      </el-alert>

      <el-upload
        drag
        :http-request="doImport"
        accept=".xlsx"
        :show-file-list="false"
        :disabled="!canManage"
      >
        <el-icon class="el-icon--upload"><UploadFilled /></el-icon>
        <div class="el-upload__text">把 .xlsx 拖到此处，或<em>点击选择文件</em></div>
      </el-upload>

      <el-result
        v-if="result"
        icon="success"
        :title="`导入完成：新增 ${result.created} 条，更新 ${result.updated} 条`"
        sub-title="可到 IT 设备台账查看"
      >
        <template #extra>
          <el-button type="primary" @click="router.push({ name: 'it-equipment' })">去 IT 设备台账</el-button>
          <el-button @click="reset">再传一批</el-button>
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

const router = useRouter();
const isMobile = useIsMobile();
const canManage = computed(() => auth.can("it.manage"));

const result = ref<{ created: number; updated: number } | null>(null);
const errors = ref<any[]>([]);

const summary = computed(() => `共 ${errors.value.length} 处问题，未写入任何数据`);

function downloadTemplate() {
  return download("/it-equipment/import-template");
}

function reset() {
  result.value = null;
  errors.value = [];
}

// el-upload 的 http-request 钩子：自己发请求，才能拿到后端返回的 errors[] 明细。
// 与 asset/Import.vue 的 doImport 同一写法（那边也是为了展示逐行错误）。
async function doImport(opt: any) {
  const fd = new FormData();
  fd.append("file", opt.file);
  reset();
  try {
    const { data } = await http.post("/it-equipment/import", fd);
    result.value = { created: data.created || 0, updated: data.updated || 0 };
    ElMessage.success("导入完成");
  } catch (e: any) {
    // 拦截器已经弹过概要，这里把逐行明细摊开给用户改。
    const detail = e?.response?.data?.errors;
    if (Array.isArray(detail)) errors.value = detail;
  }
}
</script>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  margin-bottom: 12px;
}
.spacer {
  flex: 1;
}
.errors {
  margin-top: 16px;
}
</style>

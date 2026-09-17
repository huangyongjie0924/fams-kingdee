<template>
  <div class="page">
    <!-- 这个页面没做窄屏适配：文件读写和整批预检在手机上没法用，给个占位比给个残局好 -->
    <el-card v-if="isMobile">
      <el-empty description="批量导入需要在电脑上操作，请用电脑打开本页。" />
    </el-card>

    <el-card v-else>
      <template #header>
        <div class="toolbar" style="margin: 0">
          <span class="title">Excel 批量导入资产</span>
          <span class="spacer" />
          <el-button :icon="Download" @click="downloadTemplate">下载模板</el-button>
        </div>
      </template>

      <el-alert type="info" :closable="false" style="margin-bottom: 16px">
        <div>1. 先下载模板，按表头填写。资产名称与资产类别必填，资产编码留空则自动生成。</div>
        <div>2. 公司、部门、员工、区域、供应商按<b>名称</b>填写，系统里必须已存在，否则会报错。</div>
        <div>3. 校验采用全量预检：只要有一行不合格，整批都不会写入。</div>
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
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { useRouter } from "vue-router";
import { Download, UploadFilled } from "@element-plus/icons-vue";
import http, { download } from "../../api/client";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";

const isMobile = useIsMobile();
const router = useRouter();
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
</script>

<style scoped>
.title {
  font-weight: 600;
}

.errors {
  margin-top: 16px;
}
</style>

<template>
  <div class="page">
    <div class="toolbar">
      <span class="title">资产分类</span>
      <span class="spacer" />
      <el-button v-if="auth.can('master.manage')" type="primary" :icon="Plus" @click="openNew()">新增分类</el-button>
    </div>

    <el-card>
      <el-table v-if="!isMobile" :data="tree" row-key="id" border default-expand-all>
        <el-table-column prop="name" label="分类名称" min-width="220" />
        <el-table-column prop="code" label="编码" width="120" />
        <el-table-column label="可维修" width="100">
          <template #default="{ row }">
            <el-tag :type="row.repairable ? 'success' : 'info'" size="small">
              {{ row.repairable ? "可维修" : "不可维修" }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="use_months" label="默认使用期限(月)" width="160" align="right" />
        <el-table-column prop="residual_rate" label="默认残值率(%)" width="140" align="right" />
        <el-table-column prop="sort_index" label="排序" width="90" align="right" />
        <el-table-column v-if="auth.can('master.manage')" label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openNew(row)">新增子级</el-button>
            <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <!-- 窄屏用 el-tree：树形能保留父子层级又可折叠，比自己渲染缩进省事 -->
      <el-tree
        v-else
        :data="tree"
        node-key="id"
        :props="{ label: 'name', children: 'children' }"
        :expand-on-click-node="false"
      >
        <template #default="{ data }">
          <div class="m-tree-node">
            <div class="m-tree-line">
              <span class="m-title">{{ data.name }}</span>
              <span class="m-muted">{{ data.code }}</span>
            </div>
            <div class="m-tree-line m-muted">
              期限 {{ data.use_months ?? "-" }} 月 · 残值 {{ data.residual_rate ?? "-" }}% · 排序 {{ data.sort_index }} · {{ data.repairable ? "可维修" : "不可维修" }}
            </div>
            <div v-if="auth.can('master.manage')" class="m-tree-ops">
              <el-button link type="primary" size="small" @click.stop="openNew(data)">新增子级</el-button>
              <el-button link type="primary" size="small" @click.stop="openEdit(data)">编辑</el-button>
              <el-button link type="danger" size="small" @click.stop="remove(data)">删除</el-button>
            </div>
          </div>
        </template>
      </el-tree>
    </el-card>

    <el-dialog
      v-model="dialog"
      :title="editing.id ? '编辑分类' : '新增分类'"
      width="480px"
      :fullscreen="isMobile"
    >
      <el-form :model="editing" :label-width="isMobile ? 'auto' : '130px'" :label-position="isMobile ? 'top' : 'right'">
        <el-form-item label="分类名称">
          <el-input v-model="editing.name" />
        </el-form-item>
        <el-form-item label="编码">
          <el-input v-model="editing.code" />
        </el-form-item>
        <el-form-item label="默认使用期限(月)">
          <el-input-number v-model="editing.use_months" :min="0" :controls="false" style="width: 100%" />
        </el-form-item>
        <el-form-item label="默认残值率(%)">
          <el-input-number v-model="editing.residual_rate" :min="0" :max="100" :precision="2" :controls="false" style="width: 100%" />
        </el-form-item>
        <el-form-item label="排序">
          <el-input-number v-model="editing.sort_index" :min="0" :controls="false" style="width: 100%" />
        </el-form-item>
        <el-form-item label="是否可维修">
          <el-switch v-model="editing.repairable" active-text="可维修" inactive-text="不可维修" />
          <div class="form-hint">勾选后，该分类下的资产才能提交维修单。</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Plus } from "@element-plus/icons-vue";
import http from "../../api/client";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";

const isMobile = useIsMobile();
const tree = ref<any[]>([]);
const dialog = ref(false);
const editing = ref<any>({});

async function load() {
  const { data } = await http.get("/categories");
  tree.value = data || [];
}

function openNew(parent?: any) {
  // 新分类默认不可维修（保守，与 asset_category.repairable 列 DEFAULT 0 一致）：
  // 宁可管理员显式勾选，也不让新分类默认可报修。
  editing.value = { id: 0, parent_id: parent?.id || 0, name: "", code: "", use_months: 0, residual_rate: 5, sort_index: 0, repairable: false };
  dialog.value = true;
}

function openEdit(row: any) {
  editing.value = { ...row, children: undefined };
  dialog.value = true;
}

async function save() {
  const { children, ...payload } = editing.value;
  await http.post("/categories", payload);
  dialog.value = false;
  ElMessage.success("已保存");
  load();
}

async function remove(row: any) {
  await ElMessageBox.confirm(`确认删除分类「${row.name}」？被资产引用的分类不允许删除。`, "提示", { type: "warning" });
  await http.delete(`/categories/${row.id}`);
  ElMessage.success("已删除");
  load();
}

onMounted(load);
</script>

<style scoped>
.title {
  font-size: 15px;
  font-weight: 600;
}

.form-hint {
  color: #909399;
  font-size: 12px;
  line-height: 1.4;
}
</style>

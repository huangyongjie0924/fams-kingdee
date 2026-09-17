<template>
  <div class="page">
    <el-card>
      <div class="toolbar">
        <span class="hint">制定盘点计划 → 生成盘点表 → 分配盘点人 → 录入结果 → 打印报表</span>
        <span class="spacer" />
        <el-button v-if="canManage" type="primary" :icon="Plus" @click="open()">新建计划</el-button>
      </div>

      <el-table v-if="!isMobile" :data="plans" border v-loading="loading">
        <el-table-column prop="code" label="计划编号" width="150" />
        <el-table-column prop="name" label="计划名称" min-width="180" />
        <el-table-column label="盘点范围" min-width="220">
          <template #default="{ row }">{{ scopeText(row.scope) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">{{ statusLabel(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="进度" width="110" align="right">
          <template #default="{ row }">{{ row.counted_count || 0 }} / {{ row.item_count || 0 }}</template>
        </el-table-column>
        <el-table-column prop="created_by" label="创建人" width="110" />
        <el-table-column label="操作" width="260" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="detail(row)">明细</el-button>
            <template v-if="canManage">
              <el-button link type="primary" @click="generate(row)">生成盘点表</el-button>
              <el-button link type="primary" @click="open(row)">编辑</el-button>
              <el-button link type="danger" @click="remove(row)">删除</el-button>
            </template>
          </template>
        </el-table-column>
      </el-table>

      <div v-else v-loading="loading" class="m-list">
        <div v-for="row in plans" :key="row.id" class="m-card">
          <div class="m-card-hd">
            <span class="m-muted">{{ row.code }}</span>
            <span class="spacer" />
            <el-tag :type="statusType(row.status)" size="small">{{ statusLabel(row.status) }}</el-tag>
          </div>
          <div class="m-name">{{ row.name }}</div>
          <div class="m-grid">
            <span class="k">盘点范围</span><span>{{ scopeText(row.scope) }}</span>
            <span class="k">进度</span><span>{{ row.counted_count || 0 }} / {{ row.item_count || 0 }}</span>
            <span class="k">创建人</span><span>{{ row.created_by || "-" }}</span>
          </div>
          <div class="m-tree-ops">
            <el-button link type="primary" size="small" @click="detail(row)">明细</el-button>
            <template v-if="canManage">
              <el-button link type="primary" size="small" @click="generate(row)">生成盘点表</el-button>
              <el-button link type="primary" size="small" @click="open(row)">编辑</el-button>
              <el-button link type="danger" size="small" @click="remove(row)">删除</el-button>
            </template>
          </div>
        </div>
        <el-empty v-if="!plans.length && !loading" description="暂无盘点计划" />
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

    <el-dialog v-model="dialog" :title="form.id ? '编辑盘点计划' : '新建盘点计划'" width="520px" :fullscreen="isMobile">
      <el-form :model="form" :label-width="isMobile ? 'auto' : '90px'" :label-position="isMobile ? 'top' : 'right'">
        <el-form-item label="计划名称"><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="使用公司">
          <el-select v-model="form.scope.use_company_id" clearable placeholder="全部" style="width: 100%">
            <el-option v-for="c in companies" :key="c.id" :label="c.name" :value="c.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="使用部门">
          <el-select v-model="form.scope.use_dept_id" clearable placeholder="全部" style="width: 100%">
            <el-option v-for="d in departments" :key="d.id" :label="d.name" :value="d.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="资产分类">
          <el-tree-select
            v-model="form.scope.category_id"
            :data="categories"
            :props="treeProps"
            node-key="id"
            check-strictly
            clearable
            placeholder="全部"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="区域">
          <el-tree-select
            v-model="form.scope.area_id"
            :data="areas"
            :props="treeProps"
            node-key="id"
            check-strictly
            clearable
            placeholder="全部"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="备注"><el-input v-model="form.remark" type="textarea" :rows="2" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import { Plus } from "@element-plus/icons-vue";
import http from "../../api/client";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";

const isMobile = useIsMobile();
const router = useRouter();
const plans = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = 20;
const loading = ref(false);
const dialog = ref(false);
const form = ref<any>({});

const categories = ref<any[]>([]);
const areas = ref<any[]>([]);
const companies = ref<any[]>([]);
const departments = ref<any[]>([]);
const treeProps = { label: "name", children: "children" };

const canManage = computed(() => auth.can("count.manage"));

const scopeText = (s: any) => {
  if (!s) return "全部资产";
  const parts: string[] = [];
  const pick = (list: any[], id: number, field = "name") => list.find((x) => x.id === id)?.[field];
  if (s.use_company_id) parts.push(`公司：${pick(companies.value, s.use_company_id) ?? s.use_company_id}`);
  if (s.use_dept_id) parts.push(`部门：${pick(departments.value, s.use_dept_id) ?? s.use_dept_id}`);
  if (s.category_id) parts.push(`分类：${findName(categories.value, s.category_id) ?? s.category_id}`);
  if (s.area_id) parts.push(`区域：${findName(areas.value, s.area_id) ?? s.area_id}`);
  return parts.length ? parts.join("、") : "全部资产";
};

// 分类/区域是树，名称要递归找
function findName(nodes: any[], id: number): string | undefined {
  for (const n of nodes) {
    if (n.id === id) return n.name;
    const hit = n.children ? findName(n.children, id) : undefined;
    if (hit) return hit;
  }
  return undefined;
}

const statusLabel = (v: string) =>
  ({ draft: "草稿", counting: "盘点中", done: "已完成", cancelled: "已取消" } as Record<string, string>)[v] ?? v;

const statusType = (v: string) =>
  ({ draft: "info", counting: "warning", done: "success", cancelled: "info" } as Record<string, string>)[v] ?? "info";

async function load() {
  loading.value = true;
  try {
    const { data } = await http.get("/count/plans", { params: { page: page.value, page_size: pageSize } });
    plans.value = data.items || [];
    total.value = data.total || 0;
  } finally {
    loading.value = false;
  }
}

function onPage(p: number) {
  page.value = p;
  load();
}

function open(row?: any) {
  form.value = row
    ? { id: row.id, name: row.name, remark: row.remark, scope: { ...row.scope } }
    : {
        id: 0,
        name: "",
        remark: "",
        scope: { use_company_id: 0, use_dept_id: 0, category_id: 0, area_id: 0 },
      };
  dialog.value = true;
}

async function save() {
  const { id, name, remark, scope } = form.value;
  const payload = { name, remark, scope: cleanScope(scope) };
  if (id) {
    await http.put(`/count/plans/${id}`, payload);
  } else {
    await http.post("/count/plans", payload);
  }
  dialog.value = false;
  ElMessage.success("已保存");
  await load();
}

// 空值统一成 0，后端按「零值=不限」判断
function cleanScope(s: any) {
  return {
    use_company_id: s.use_company_id || 0,
    use_dept_id: s.use_dept_id || 0,
    category_id: s.category_id || 0,
    area_id: s.area_id || 0,
  };
}

async function generate(row: any) {
  await ElMessageBox.confirm(
    `按当前范围重新生成「${row.name}」的盘点表？已录入的盘点结果会被清空。`,
    "提示",
    { type: "warning" },
  );
  const { data } = await http.post(`/count/plans/${row.id}/generate`);
  ElMessage.success(`已生成 ${data.count} 条盘点明细`);
  await load();
}

async function remove(row: any) {
  await ElMessageBox.confirm(`确认删除盘点计划「${row.name}」？`, "提示", { type: "warning" });
  await http.delete(`/count/plans/${row.id}`);
  ElMessage.success("已删除");
  await load();
}

function detail(row: any) {
  router.push({ name: "count-detail", params: { id: row.id } });
}

onMounted(async () => {
  const [c, a, co, d] = await Promise.all([
    http.get("/categories"),
    http.get("/areas"),
    http.get("/companies"),
    http.get("/departments"),
  ]);
  categories.value = c.data || [];
  areas.value = a.data || [];
  companies.value = co.data || [];
  departments.value = d.data || [];
  await load();
});
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

.pager {
  margin-top: 12px;
  display: flex;
  justify-content: flex-end;
}

/* 卡片第一行放编号+状态，名称单独一行，要和下面的字段拉开距离 */
.m-name {
  font-size: 15px;
  font-weight: 600;
  margin-bottom: 8px;
}
</style>

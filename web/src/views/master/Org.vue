<template>
  <div class="page">
    <el-card>
      <el-tabs v-model="tab">
        <el-tab-pane label="公司" name="company">
          <div class="toolbar">
            <span class="spacer" />
            <el-button v-if="auth.can('master.manage')" type="primary" :icon="Plus" @click="openCompany()">新增公司</el-button>
          </div>
          <el-table v-if="!isMobile" :data="companies" border>
            <el-table-column prop="name" label="公司名称" min-width="200" />
            <el-table-column prop="code" label="编码" width="120" />
            <el-table-column prop="tax_no" label="税号" width="200" />
            <el-table-column prop="sort_index" label="排序" width="90" align="right" />
            <el-table-column v-if="auth.can('master.manage')" label="操作" width="140" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openCompany(row)">编辑</el-button>
                <el-button link type="danger" @click="remove('companies', row, loadCompanies)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>

          <div v-else class="m-list">
            <div v-for="row in companies" :key="row.id" class="m-card">
              <div class="m-card-hd">
                <span class="m-title">{{ row.name }}</span>
              </div>
              <div class="m-grid">
                <span class="k">编码</span><span>{{ row.code || "-" }}</span>
                <span class="k">税号</span><span>{{ row.tax_no || "-" }}</span>
                <span class="k">排序</span><span>{{ row.sort_index }}</span>
              </div>
              <div v-if="auth.can('master.manage')" class="m-tree-ops">
                <el-button link type="primary" size="small" @click="openCompany(row)">编辑</el-button>
                <el-button link type="danger" size="small" @click="remove('companies', row, loadCompanies)">删除</el-button>
              </div>
            </div>
            <el-empty v-if="!companies.length" description="暂无公司" />
          </div>
        </el-tab-pane>

        <el-tab-pane label="部门" name="dept">
          <div class="toolbar">
            <span class="spacer" />
            <el-button v-if="auth.can('master.manage')" type="primary" :icon="Plus" @click="openDept()">新增部门</el-button>
          </div>
          <el-table v-if="!isMobile" :data="departments" border>
            <el-table-column prop="name" label="部门名称" min-width="200" />
            <el-table-column prop="code" label="编码" width="120" />
            <el-table-column prop="company_name" label="所属公司" width="180" />
            <el-table-column prop="sort_index" label="排序" width="90" align="right" />
            <el-table-column v-if="auth.can('master.manage')" label="操作" width="140" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openDept(row)">编辑</el-button>
                <el-button link type="danger" @click="remove('departments', row, loadDepartments)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>

          <div v-else class="m-list">
            <div v-for="row in departments" :key="row.id" class="m-card">
              <div class="m-card-hd">
                <span class="m-title">{{ row.name }}</span>
              </div>
              <div class="m-grid">
                <span class="k">编码</span><span>{{ row.code || "-" }}</span>
                <span class="k">所属公司</span><span>{{ row.company_name || "-" }}</span>
                <span class="k">排序</span><span>{{ row.sort_index }}</span>
              </div>
              <div v-if="auth.can('master.manage')" class="m-tree-ops">
                <el-button link type="primary" size="small" @click="openDept(row)">编辑</el-button>
                <el-button link type="danger" size="small" @click="remove('departments', row, loadDepartments)">删除</el-button>
              </div>
            </div>
            <el-empty v-if="!departments.length" description="暂无部门" />
          </div>
        </el-tab-pane>

        <el-tab-pane label="员工" name="emp">
          <div class="toolbar">
            <el-input v-model="empKeyword" placeholder="姓名或工号" clearable :style="{ width: isMobile ? '100%' : '220px' }" @keyup.enter="loadEmployees" @clear="loadEmployees" />
            <el-button @click="loadEmployees">查询</el-button>
            <span class="spacer" />
            <el-button v-if="auth.can('master.manage')" type="primary" :icon="Plus" @click="openEmp()">新增员工</el-button>
          </div>
          <el-table v-if="!isMobile" :data="employees" border>
            <el-table-column prop="emp_no" label="工号" width="120" />
            <el-table-column prop="name" label="姓名" width="120" />
            <el-table-column prop="dept_name" label="部门" width="180" />
            <el-table-column prop="company_name" label="公司" width="180" />
            <el-table-column prop="phone" label="联系方式" width="150" />
            <el-table-column label="状态" width="90">
              <template #default="{ row }">
                <el-tag :type="row.active ? 'success' : 'info'" size="small">{{ row.active ? "在职" : "离职" }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column v-if="auth.can('master.manage')" label="操作" width="140" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openEmp(row)">编辑</el-button>
                <el-button link type="danger" @click="remove('employees', row, loadEmployees)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>

          <div v-else class="m-list">
            <div v-for="row in employees" :key="row.id" class="m-card">
              <div class="m-card-hd">
                <span class="m-title">{{ row.name }}</span>
                <el-tag :type="row.active ? 'success' : 'info'" size="small">{{ row.active ? "在职" : "离职" }}</el-tag>
              </div>
              <div class="m-grid">
                <span class="k">工号</span><span>{{ row.emp_no || "-" }}</span>
                <span class="k">部门</span><span>{{ row.dept_name || "-" }}</span>
                <span class="k">公司</span><span>{{ row.company_name || "-" }}</span>
                <span class="k">联系方式</span><span>{{ row.phone || "-" }}</span>
              </div>
              <div v-if="auth.can('master.manage')" class="m-tree-ops">
                <el-button link type="primary" size="small" @click="openEmp(row)">编辑</el-button>
                <el-button link type="danger" size="small" @click="remove('employees', row, loadEmployees)">删除</el-button>
              </div>
            </div>
            <el-empty v-if="!employees.length" description="暂无员工" />
          </div>
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-dialog v-model="companyDialog" :title="company.id ? '编辑公司' : '新增公司'" width="440px" :fullscreen="isMobile">
      <el-form :model="company" :label-width="isMobile ? 'auto' : '90px'" :label-position="isMobile ? 'top' : 'right'">
        <el-form-item label="公司名称"><el-input v-model="company.name" /></el-form-item>
        <el-form-item label="编码"><el-input v-model="company.code" /></el-form-item>
        <el-form-item label="税号"><el-input v-model="company.tax_no" /></el-form-item>
        <el-form-item label="排序">
          <el-input-number v-model="company.sort_index" :min="0" :controls="false" style="width: 100%" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="companyDialog = false">取消</el-button>
        <el-button type="primary" @click="saveCompany">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="deptDialog" :title="dept.id ? '编辑部门' : '新增部门'" width="440px" :fullscreen="isMobile">
      <el-form :model="dept" :label-width="isMobile ? 'auto' : '90px'" :label-position="isMobile ? 'top' : 'right'">
        <el-form-item label="部门名称"><el-input v-model="dept.name" /></el-form-item>
        <el-form-item label="编码"><el-input v-model="dept.code" /></el-form-item>
        <el-form-item label="所属公司">
          <el-select v-model="dept.company_id" clearable style="width: 100%">
            <el-option v-for="c in companies" :key="c.id" :label="c.name" :value="c.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="上级部门">
          <el-select v-model="dept.parent_id" clearable style="width: 100%">
            <el-option :value="0" label="无" />
            <el-option v-for="d in departments" :key="d.id" :label="d.name" :value="d.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="排序">
          <el-input-number v-model="dept.sort_index" :min="0" :controls="false" style="width: 100%" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="deptDialog = false">取消</el-button>
        <el-button type="primary" @click="saveDept">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="empDialog" :title="emp.id ? '编辑员工' : '新增员工'" width="440px" :fullscreen="isMobile">
      <el-form :model="emp" :label-width="isMobile ? 'auto' : '90px'" :label-position="isMobile ? 'top' : 'right'">
        <el-form-item label="姓名"><el-input v-model="emp.name" /></el-form-item>
        <el-form-item label="工号"><el-input v-model="emp.emp_no" /></el-form-item>
        <el-form-item label="部门">
          <el-select v-model="emp.dept_id" clearable style="width: 100%">
            <el-option v-for="d in departments" :key="d.id" :label="d.name" :value="d.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="公司">
          <el-select v-model="emp.company_id" clearable style="width: 100%">
            <el-option v-for="c in companies" :key="c.id" :label="c.name" :value="c.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="联系方式"><el-input v-model="emp.phone" /></el-form-item>
        <el-form-item label="在职"><el-switch v-model="emp.active" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="empDialog = false">取消</el-button>
        <el-button type="primary" @click="saveEmp">保存</el-button>
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
const tab = ref("company");
const companies = ref<any[]>([]);
const departments = ref<any[]>([]);
const employees = ref<any[]>([]);
const empKeyword = ref("");

const companyDialog = ref(false);
const deptDialog = ref(false);
const empDialog = ref(false);
const company = ref<any>({});
const dept = ref<any>({});
const emp = ref<any>({});

async function loadCompanies() {
  const { data } = await http.get("/companies");
  companies.value = data || [];
}

async function loadDepartments() {
  const { data } = await http.get("/departments");
  departments.value = data || [];
}

async function loadEmployees() {
  const { data } = await http.get("/employees", { params: { keyword: empKeyword.value } });
  employees.value = data || [];
}

function openCompany(row?: any) {
  company.value = row ? { ...row } : { id: 0, name: "", code: "", tax_no: "", sort_index: 0 };
  companyDialog.value = true;
}

function openDept(row?: any) {
  dept.value = row
    ? { ...row, company_name: undefined }
    : { id: 0, name: "", code: "", parent_id: 0, company_id: null, sort_index: 0 };
  deptDialog.value = true;
}

function openEmp(row?: any) {
  emp.value = row
    ? { ...row, dept_name: undefined, company_name: undefined }
    : { id: 0, emp_no: "", name: "", dept_id: null, company_id: null, phone: "", active: true };
  empDialog.value = true;
}

async function saveCompany() {
  await http.post("/companies", company.value);
  companyDialog.value = false;
  ElMessage.success("已保存");
  loadCompanies();
}

async function saveDept() {
  const { company_name, ...payload } = dept.value;
  await http.post("/departments", payload);
  deptDialog.value = false;
  ElMessage.success("已保存");
  loadDepartments();
}

async function saveEmp() {
  const { dept_name, company_name, ...payload } = emp.value;
  await http.post("/employees", payload);
  empDialog.value = false;
  ElMessage.success("已保存");
  loadEmployees();
}

async function remove(path: string, row: any, reload: () => Promise<void>) {
  await ElMessageBox.confirm(`确认删除「${row.name}」？被资产引用的记录不允许删除。`, "提示", { type: "warning" });
  await http.delete(`/${path}/${row.id}`);
  ElMessage.success("已删除");
  await reload();
}

onMounted(async () => {
  await Promise.all([loadCompanies(), loadDepartments(), loadEmployees()]);
});
</script>

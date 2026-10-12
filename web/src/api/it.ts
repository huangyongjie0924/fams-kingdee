// IT 信息设备台账的前端元数据（2026-10-10 需求）。
//
// 与 api/meta.ts 的分工：meta.ts 放资产台账的东西，本文件放 IT 台账的，
// 避免两个模块的列配置/枚举混在一个文件里互相干扰。

export interface ITColumnMeta {
  prop: string;
  label: string;
  width?: number;
  align?: "left" | "right" | "center";
  defaultOn: boolean;
}

// 列顺序与用户提出的字段清单一致（资产编码 使用人 部门 电脑类型 使用地点 用途 状态
// 是否信创 品牌.型号 CPU型号 内存容量(GB) 硬盘类型及容量 系统版本 备注）。
//
// 默认显示的只有 7 列：14 列全开会把表格挤到横向滚动，而「信创 / 硬盘 / 系统版本」
// 这类细节多数时候不需要常驻视野。全部可在「列配置」里打开。
export const IT_COLUMNS: ITColumnMeta[] = [
  { prop: "asset_code", label: "资产编码", width: 150, defaultOn: true },
  { prop: "user_emp_name", label: "使用人", width: 100, defaultOn: true },
  { prop: "dept_name", label: "部门", width: 130, defaultOn: true },
  { prop: "device_type", label: "电脑类型", width: 100, defaultOn: true },
  { prop: "location", label: "使用地点", width: 140, defaultOn: true },
  { prop: "purpose", label: "用途", width: 90, defaultOn: true },
  { prop: "status", label: "状态", width: 90, defaultOn: true },
  { prop: "is_xinchuang", label: "是否信创", width: 90, defaultOn: true },
  { prop: "brand_model", label: "品牌.型号", width: 160, defaultOn: true },
  { prop: "cpu", label: "CPU型号", width: 150, defaultOn: false },
  { prop: "memory_gb", label: "内存容量(GB)", width: 110, align: "right", defaultOn: false },
  { prop: "disk", label: "硬盘类型及容量", width: 140, defaultOn: false },
  { prop: "os_version", label: "系统版本", width: 130, defaultOn: false },
  { prop: "remark", label: "备注", width: 160, defaultOn: false },
];

// 提交给后端的字段白名单：后端开了 DisallowUnknownFields，多一个键就 400。
//
// ⚠️ 只放**可写**字段：
//   - id 是自增主键；
//   - asset_name / user_emp_name / dept_name 是 LEFT JOIN 派生字段，不落库
//     （见 model/it.go 的 ITEquipment 注释）；
//   - created_by / created_at / updated_at 由服务端维护。
// asset_code 在内：无卡设备的编码由用户手工填（用户已确认「可留空，也支持手工编码」）；
// 有关联卡时后端会**以资产台账为准**覆盖它（见 api/it.go 的 resolveITCardLink），
// 所以这里传什么都不影响正确性。
export const IT_SUBMIT_FIELDS = [
  "card_id", "asset_code", "device_type", "user_emp_id", "dept_id", "location",
  "purpose", "status", "is_xinchuang", "brand_model", "cpu", "memory_gb",
  "disk", "os_version", "remark",
];

export function pickITSubmit(form: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = {};
  for (const k of IT_SUBMIT_FIELDS) {
    if (form[k] !== undefined && form[k] !== null) out[k] = form[k];
  }
  return out;
}

// IT 设备状态 → el-tag 类型。取值与后端 model.ITStatuses 对应。
export const IT_STATUS_TAG: Record<string, string> = {
  在用: "primary",
  闲置: "success",
  维修中: "warning",
  借用: "warning",
  报废: "danger",
  待分配: "info",
};

export function itStatusTag(s: string): string {
  return IT_STATUS_TAG[s] || "info";
}

// 「是否信创」是布尔，展示成中文标签而不是 true/false：
// 现场表格里这一列写的就是「是/否」，导出/导入也按中文往返（见 api/it_importexport.go 的 boolCN）。
export function boolLabel(v: any): string {
  return v ? "是" : "否";
}

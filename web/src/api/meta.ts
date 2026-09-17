export interface ColumnMeta {
  prop: string;
  label: string;
  width?: number;
  align?: "left" | "right" | "center";
  money?: boolean;
  // 数量列用 qty 而不是 money：数量是 4 位小数的计量值（194.52 平方米、1 台），
  // 套金额的两位小数会把「1 台」显示成「1.00」
  qty?: boolean;
  defaultOn: boolean;
}

// 列顺序对齐参考系统的资产列表
export const ASSET_COLUMNS: ColumnMeta[] = [
  { prop: "status", label: "状态", width: 90, defaultOn: true },
  { prop: "asset_code", label: "资产编码", width: 130, defaultOn: true },
  { prop: "name", label: "资产名称", width: 180, defaultOn: true },
  { prop: "category_name", label: "资产类别", width: 170, defaultOn: true },
  { prop: "spec", label: "规格型号", width: 150, defaultOn: true },
  { prop: "serial_no", label: "设备序列号", width: 140, defaultOn: false },
  { prop: "unit", label: "计量单位", width: 90, defaultOn: false },
  { prop: "quantity", label: "数量", width: 110, align: "right", qty: true, defaultOn: true },
  { prop: "amount", label: "金额", width: 120, align: "right", money: true, defaultOn: true },
  { prop: "use_company_name", label: "使用公司", width: 140, defaultOn: true },
  { prop: "use_dept_name", label: "使用部门", width: 140, defaultOn: true },
  { prop: "user_emp_name", label: "使用人", width: 100, defaultOn: true },
  { prop: "use_status", label: "使用状态", width: 110, defaultOn: false },
  { prop: "area_name", label: "区域", width: 110, defaultOn: true },
  { prop: "location", label: "存放地点", width: 140, defaultOn: false },
  { prop: "manager_emp_name", label: "管理人", width: 100, defaultOn: false },
  { prop: "owner_company_name", label: "所属公司", width: 140, defaultOn: false },
  { prop: "purchase_date", label: "购入日期", width: 120, defaultOn: false },
  { prop: "card_created_at", label: "建卡时间", width: 160, defaultOn: false },
  { prop: "vendor_name", label: "供应商", width: 140, defaultOn: false },
  { prop: "use_months", label: "使用期限(月)", width: 120, align: "right", defaultOn: false },
  { prop: "source", label: "来源", width: 90, defaultOn: false },
  { prop: "rfid", label: "RFID", width: 120, defaultOn: false },
  { prop: "fin_original_value", label: "原值", width: 120, align: "right", money: true, defaultOn: false },
  { prop: "fin_net_value", label: "净值", width: 120, align: "right", money: true, defaultOn: false },
  { prop: "fin_status", label: "财务状态", width: 110, defaultOn: false },
  { prop: "created_by", label: "创建人", width: 100, defaultOn: false },
];

export const STATUS_TAG: Record<string, string> = {
  闲置: "success",
  在用: "primary",
  借用: "warning",
  维修中: "warning",
  调拨中: "info",
  报废: "danger",
};

export function money(v: number | string | null | undefined): string {
  const n = Number(v || 0);
  return n.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

// 数量最多 4 位小数（对齐星瀚），末尾多余的 0 去掉：
// 194.52 平方米 → 194.52，1 台 → 1，而不是 194.5200 / 1.0000
export function qty(v: number | string | null | undefined): string {
  const n = Number(v || 0);
  if (!Number.isFinite(n)) return "0";
  return n.toLocaleString("zh-CN", { maximumFractionDigits: 4 });
}

// 提交给后端的字段白名单：后端开了 DisallowUnknownFields，多一个键就 400
// quantity 在内：手工新建的卡要能自己填数量。金蝶同步来的卡由前端置灰（见
// CardForm 的 form.synced），且后端同步按「星瀚 >0 才覆盖」处理，改了也会被纠正回来。
// synced 是派生字段，故意不在白名单里。
export const CARD_SUBMIT_FIELDS = [
  "asset_code", "name", "category_id", "spec", "serial_no", "unit", "status", "amount",
  "quantity",
  "use_company_id", "use_dept_id", "user_emp_id", "use_status", "manager_emp_id",
  "owner_company_id", "area_id", "location", "purchase_date", "use_months",
  "source", "in_stock_no", "rfid", "remark",
  "fin_asset_type", "fin_share_dept_id", "vendor_id", "fin_amount_with_tax", "fin_tax",
  "fin_original_value", "fin_net_value", "fin_accum_depreciation", "fin_residual_rate",
  "fin_use_months", "fin_period", "fin_entry_date", "fin_status",
  "mt_vendor_id", "mt_contact", "mt_phone", "mt_owner_emp_id", "mt_expire_date", "mt_remark",
  "tag_ids", "attachment_ids",
];

export function pickSubmit(form: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = {};
  for (const k of CARD_SUBMIT_FIELDS) {
    if (form[k] !== undefined && form[k] !== null) out[k] = form[k];
  }
  return out;
}

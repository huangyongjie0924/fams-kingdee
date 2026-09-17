import axios from "axios";
import { ElMessage } from "element-plus";
import { auth } from "../stores/auth";
import router from "../router";

const http = axios.create({ baseURL: "/api", timeout: 30000 });

http.interceptors.request.use((cfg) => {
  if (auth.token) {
    cfg.headers.Authorization = `Bearer ${auth.token}`;
  }
  return cfg;
});

http.interceptors.response.use(
  (res) => res,
  (err) => {
    const status = err.response?.status;
    if (status === 401) {
      auth.logout();
      router.push({ name: "login" });
      ElMessage.warning("登录状态已失效，请重新登录");
      return Promise.reject(err);
    }
    const msg = extractError(err);
    if (msg) ElMessage.error(msg);
    return Promise.reject(err);
  },
);

// 导入接口的错误体是 { error, errors[] }，交由调用方展示明细，这里只取概要
function extractError(err: any): string {
  const data = err.response?.data;
  if (data instanceof Blob) return "请求失败";
  return data?.error || err.message || "请求失败";
}

export default http;

export async function download(url: string, params: Record<string, any> = {}) {
  const res = await http.get(url, { params, responseType: "blob" });
  const disposition = res.headers["content-disposition"] || "";
  let filename = "download.xlsx";
  const m = /filename\*?=(?:UTF-8'')?([^;]+)/i.exec(disposition);
  if (m) filename = decodeURIComponent(m[1].replace(/"/g, ""));

  const link = document.createElement("a");
  link.href = URL.createObjectURL(res.data);
  link.download = filename;
  link.click();
  URL.revokeObjectURL(link.href);
}

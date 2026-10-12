// 云之家（云之家）免登链路。
//
// 权威依据：docs/云之家待办通知集成方案.md §5（降级链）。
// 用户点击 云之家待办卡片跳进 FAMS 时是未登录状态，需要一条免登链路把人自动登进来。
// 逐层尝试、任一层成功即止：
//
//   第 0 层  URL 里 APP 注入的 ticket → POST /api/sso/login            （云之家 APP 内，完整可写会话）
//   第 2 层  URL 里暂存的 yzj_token    → POST /api/sso/deeplink-login    （网页端门户唯一入口，只读会话）
//   第 3 层  什么都不做，交给路由守卫跳登录页（带 ?redirect= 回原单）        （最终兜底）
//
// 层与层之间必须静默：中间层失败绝不弹错，只有「本来带了 ticket 却所有层都失败」才提示，
// 否则 token 过期这种预期内的高频情况会让用户以为系统坏了（§5.4 缺陷 3）。
//
// ⚠️ 原「第 1 层 JS 桥（qing.call('getTicket')）」已**整段删除**，不是注释掉。
//   它依赖后端签名接口 /api/yzj/jsapi-sign 去取 jsapiticket，而该接口要的是 **AppAuth2** 凭据
//   —— AppAuth2 的签发端点在 yzj.example.com 上全部 404、规范所在 wiki 对本网 IP 403
//   （docs/云之家待办通知集成方案.md §5.3 取证三连）。即这条路径**永远不可能成功**。
//
//   更要紧的是它原先排在 yzj_token **之前**，而网页端门户点待办时只有 yzj_token、没有 ticket
//   ⇒ 主路径每次都要先跨域拉一次 static.yunzhijia.com/qing.js 并等到超时，
//   才轮到真正可用的第 2 层。**一个已被证伪的机制留在热路径上，代价不是零，
//   而是每次都给用户加一次白等** —— 这是「删除而非注释掉」的直接原因。

import { ElMessage } from "element-plus";
import { auth } from "../stores/auth";

/** 启动时从 URL 读取并暂存的免登凭据。 */
export interface YzjCredentials {
  /** 云之家 APP 注入的一次性 ticket。 */
  ticket: string;
  /** 深链里的一次性免登 token（?yzj_token=）。 */
  yzjToken: string;
}

/** 判断凭据是否至少带了一个，用于「普通用户直接打开首页」时短路整条链。 */
export function hasAnyCredential(creds: YzjCredentials): boolean {
  return Boolean(creds.ticket || creds.yzjToken);
}

/**
 * 用 ticket 换会话（现有链路，完全复用，不回归）。
 *
 * @returns ok=true 表示已 auth.login；ok=false 时 error 为可展示的失败原因。
 */
async function exchangeTicket(ticket: string): Promise<{ ok: boolean; error: string }> {
  try {
    // 用 fetch 直接调，绕开 axios 拦截器：SSO 失败的 401 不是「会话过期」，需要展示真实原因。
    const res = await fetch("/api/sso/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ticket }),
    });
    const data = await res.json();
    if (!res.ok) {
      return { ok: false, error: data?.error || "云之家登录失败" };
    }
    // 第 0 层走的是 /api/sso/login，是完整权限会话，明确传 false。
    auth.login(data.token, data.user, false);
    return { ok: true, error: "" };
  } catch {
    return { ok: false, error: "云之家登录失败" };
  }
}

/**
 * 用深链免登 token 换会话（第 2 层）。
 *
 * 响应形状与 /api/sso/login 类似，但**多一个 `readonly: true`** —— 第 2 层是收窄的只读会话。
 * 失败一律静默（返回 false），绝不弹错。
 */
async function exchangeDeeplinkToken(token: string): Promise<boolean> {
  try {
    const res = await fetch("/api/sso/deeplink-login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token }),
    });
    if (!res.ok) return false;
    const data = await res.json();
    if (!data?.token || !data?.user) return false;
    // 第 2 层是收窄的只读会话：后端回 readonly:true；无该键视为 false（普通登录会话）。
    // 前端据此隐藏写按钮、改显示「登录后处理」入口（§12-Q16 方案 D）。
    auth.login(data.token, data.user, data.readonly === true);
    return true;
  } catch {
    return false;
  }
}

/**
 * 执行免登降级链（⓪ → ② → ③）。必须在 app.use(router) 之前 await 完成。
 *
 * @param creds 启动时从 URL 读取并暂存的凭据（ticket / yzj_token）。
 * @returns true 表示已成功登录（auth.login 已调用）；false 表示未登录（交给路由守卫）。
 */
export async function tryYunzhijiaLogin(creds: YzjCredentials): Promise<boolean> {
  // 普通用户直接打开首页：无任何凭据，什么都不做（也不发任何请求）。
  if (!hasAnyCredential(creds)) {
    return false;
  }

  let ticketError = "";

  // 第 0 层：URL 里 APP 注入的 ticket（保留现有行为，不需要外部脚本，优先尝试）。
  if (creds.ticket) {
    const r = await exchangeTicket(creds.ticket);
    if (r.ok) return true;
    ticketError = r.error;
  }

  // 第 2 层（兜底）：URL 里暂存的 yzj_token。失败必须静默。
  // 网页端门户点待办走的就是这一层（网页端不注入 ticket），拿到的是**只读**收窄会话。
  if (creds.yzjToken) {
    if (await exchangeDeeplinkToken(creds.yzjToken)) return true;
  }

  // 第 3 层：什么都不做，交给路由守卫跳登录页。
  // 仅当用户本来带了 ticket（APP 注入）却全部失败时才提示，便于排查 APP 集成问题。
  if (creds.ticket) {
    ElMessage.error(ticketError || "云之家登录失败");
  }
  return false;
}

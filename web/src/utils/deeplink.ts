// 扫码深链的 asset_code 兜底存储。
// token 过期时 router 守卫会把人踢到登录页并重写 hash，query 里的编码就丢了；
// 挂载前存一份，登录后资产列表页再取回来。
const KEY = "asset_deep_link";

function codeFromLocation(): string {
  const h = window.location.hash;
  const i = h.indexOf("?");
  return (
    new URLSearchParams(window.location.search).get("asset_code") ||
    (i >= 0 ? new URLSearchParams(h.slice(i + 1)).get("asset_code") : "") ||
    ""
  );
}

/** 应用挂载前调用，把当前 URL 上的资产编码留下来。 */
export function stashAssetCode() {
  const code = codeFromLocation();
  if (code) sessionStorage.setItem(KEY, code);
}

/** 读取并清空兜底编码，只消费一次。 */
export function takeAssetCode(): string {
  const code = sessionStorage.getItem(KEY) || "";
  if (code) sessionStorage.removeItem(KEY);
  return code;
}

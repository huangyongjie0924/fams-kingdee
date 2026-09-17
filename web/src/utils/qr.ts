import { encode } from "uqr";

/**
 * 二维码内容 = 完整链接，任何扫码工具（系统相机、微信、PDA）都能直接打开，
 * 不依赖应用内的摄像头权限。裸编码也一并支持，方便手工造的标签。
 */
export function assetQrUrl(code: string, base: string): string {
  const b = (base || "").trim().replace(/\/+$/, "");
  return `${b}/#/assets?asset_code=${encodeURIComponent(code)}`;
}

/** 从查询串形态的字符串里取 asset_code，入参可带可不带开头的 ?。 */
function paramFromQuery(q: string): string {
  const i = q.indexOf("?");
  if (i < 0) return "";
  return new URLSearchParams(q.slice(i + 1)).get("asset_code") || "";
}

/**
 * 从扫码结果里取出资产编码。
 * 坑：new URL() 不解析 fragment 内部的查询串 ——
 * new URL("https://h/#/assets?asset_code=X").searchParams.get("asset_code") 是 null，
 * 因为 searchParams 只看 `?` 之前的那段。本项目的 hash 路由深链正好是这种形态，
 * 所以必须把 u.hash 单独再解析一次。
 */
export function extractAssetCode(payload: string): string {
  const raw = (payload || "").trim();
  if (!raw) return "";

  let u: URL | null = null;
  try {
    u = new URL(raw);
  } catch {
    u = null;
  }

  if (u) {
    const direct = u.searchParams.get("asset_code");
    if (direct) return direct.trim();
    const fromHash = paramFromQuery(u.hash);
    if (fromHash) return fromHash.trim();
    // 是合法 URL 但没有 asset_code：不是本系统的标签，交给调用方报错
    return "";
  }

  // 不像 URL：可能是只带 hash 的 "#/assets?asset_code=X"，否则按裸编码看待
  const fromHash = paramFromQuery(raw);
  return (fromHash || raw).trim();
}

// 批量打印时改一下字段勾选就会整批重算。编码本身跟这些选项无关，缓存住不重复编码。
const svgCache = new Map<string, string>();

/**
 * 生成内联 SVG 字符串。矢量输出在 20mm 标签上仍然锐利，且不经过 canvas/base64。
 *
 * 不用 uqr 自带的 renderSVG：它每个模块吐一条 `M..h..v..z` 片段，一张 33 模块的码
 * 就有 1300 多条，200 张标签合计约 2MB DOM。这里把每行连续的黑模块合并成一条横线，
 * 实测片段数降到约 1/5、体积降到 1/2.65（782KB / 200 张），解码矩阵与 uqr 输出逐位一致。
 */
export function renderQrSvg(text: string): string {
  const hit = svgCache.get(text);
  if (hit) return hit;

  // ecc 提到 M（默认 L）：标签会被搬动、摩擦、沾灰，纠错等级要留余量。
  // border 给 2 个模块静区，绝不能是 0 —— 静区没了大多数扫码器直接识别不出来。
  const r = encode(text, { ecc: "M", border: 2 });
  const runs: string[] = [];
  for (let y = 0; y < r.size; y++) {
    let x = 0;
    while (x < r.size) {
      if (!r.data[y][x]) {
        x++;
        continue;
      }
      let w = 1;
      while (x + w < r.size && r.data[y][x + w]) w++;
      runs.push(`M${x} ${y}h${w}v1h-${w}z`);
      x += w;
    }
  }
  // viewBox 用模块坐标（1 单位 = 1 模块），靠 width/height 100% 撑满容器。
  // 白底 rect 兼作静区：万一打在有色纸上，静区仍是白的。
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${r.size} ${r.size}" width="100%" height="100%">` +
    `<rect width="${r.size}" height="${r.size}" fill="#ffffff"/>` +
    `<path fill="#000000" d="${runs.join("")}"/></svg>`;

  svgCache.set(text, svg);
  return svg;
}

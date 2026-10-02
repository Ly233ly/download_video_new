# 站点适配器

本目录是「特定抓取方法」的**唯一事实源**。

规范：[docs/14-SITE-ADAPTERS.md](../docs/14-SITE-ADAPTERS.md)。**加新站点前先读规范 §11 的八步流程。**

---

## 索引

| 站点 | ID | 层 | 定制原因 | 最后核对 | 文档 |
| --- | --- | --- | --- | --- | --- |
| 通用兜底 | `generic` | L0 | 无匹配时生效，不针对任何站点 | — | — |
| 抖音 | `douyin` | L1 | 需从多个播放器中选主播放器（`primarySelection: current-player`）；需放宽通用路径「必须有 blob 源」的约束；ID 有路径/查询参数/类名三种来源 | **尚未核对** | [douyin](douyin/README.md) |
| 微信视频号 | `wechat-channels` | L2 | 需代理捕获与专用解密；媒体 CDN 必须绕过代理 | **尚未移植** | [docs/06-WECHAT.md](../docs/06-WECHAT.md) |

「最后核对」指**按真实页面人工验证过**的时间。旧实现还原出来的适配器一律为「尚未核对」，因为旧行为不等于新架构下已验证的行为。

层次按 **A-101** 从严判定：能用声明表达的**禁止**写 L2。抖音原先按旧实现登记为 L2，核对后确认它需要的每一项都能声明，因此改为 **L1**（[14 §10](../docs/14-SITE-ADAPTERS.md)），其行为由扩展侧的通用声明式引擎执行（`extension/js/eagle-bridge-candidate-logic.js` 里的 `adapter*` 函数），离线测试在 `tests/js/test_adapter_douyin.js`。

---

## 目录约定

```
adapters/
  README.md              本文件
  generic/               通用兜底
  <site-id>/
    adapter.json         声明（唯一事实源，必填）
    README.md            站点文档（必填）
    extension.js         扩展侧代码（仅 L2，可选）
    fixtures/            离线测试夹具
```

**只允许出现以上文件名。** 草稿、截图、抓包、笔记一律不得留在这里——放 `fixtures/` 或移出仓库。

---

## 命名

| 对象 | 规则 | 示例 |
| --- | --- | --- |
| 目录名 | 主域名去公共后缀；全小写；只允许 `[a-z0-9-]` | `douyin.com` → `douyin` |
| 多词 | 用连字符，不用下划线 | `wechat-channels` |
| 声明文件 | 固定 `adapter.json` | — |
| 站点文档 | 固定 `README.md` | — |
| 扩展代码 | 固定 `extension.js` | — |
| 夹具 | `fixtures/<场景>.<ext>` | `feed-item.html` |

---

## 两端如何取用

| 端 | 来源 |
| --- | --- |
| 桌面端（Go） | 直接读本目录 |
| 扩展（MV3） | 读包内 `extension/site-adapters.json` |

`extension/site-adapters.json` 是**构建生成的**，从本目录抽取扩展所需字段（`match` / `identity` / `capture` / `title`）。**禁止手工编辑**——改了也会被下次构建覆盖。

加站点或改声明后必须重新构建，否则扩展侧不生效。

---

## 禁止

1. 在 `content-script.js`、`media.py` 等公共文件里写站点名判断——站点逻辑只属于本目录。
2. 在声明里加入与安全边界有关的字段（跳过校验、允许绝对路径等）。
3. 在声明或站点文档里写入真实 Cookie、签名 URL、账号信息、抓包原文。
4. 为了让某个站点通过而修改公共发现代码的分支——那些分支属于所有站点。

---

## 与 `site_rules` 的区别

| | 本目录 | `site_rules` 表 |
| --- | --- | --- |
| 归属 | 随程序分发的内置内容 | 用户在设置里的开关 |
| 内容 | **怎么抓** | **抓不抓** |
| 存储 | 磁盘文件 | SQLite |

用户禁用某站点时，在进入发现流程**之前**就已拦截，与本目录无关。

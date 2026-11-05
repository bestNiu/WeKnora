# P0 取证：PPT/文档 → PDF 预览可行性（docreader + LibreOffice）

> 目的：在实现"入库预生成 PDF 预览"（P5）前，确认转换是否可行、字体是否够用、
> 隐藏页是否破坏 `slide N == page N`，并量化耗时/内存/体积。
> 结论：**可行**，但必须带三项修正：补 CJK 字体、强制导出隐藏页、限制并发。

## 1. 环境事实

- 全环境只有 **docreader** 容器装了 LibreOffice：`LibreOffice 7.4.7.2`（`/usr/bin/soffice`）。
  app / frontend / sandbox 容器均无 `soffice`。
- docreader 的 Python 环境在 `/app/.venv/bin/python`，已含 `pypdf 6.14.2`、`pypdfium2`、
  `python-pptx 1.0.2`、`markitdown[pptx]`。
- 镜像内**没有** `pdftoppm` / `pdftocairo` / `gs`。若后续要做"逐页图片"，
  需要在镜像加 `poppler-utils`；当前用 `pypdfium2` 也能渲染页图。
- 现有 CJK 字体只有 `WenQuanYi Zen Hei`（wqy-zenhei）+ `Unifont`。

## 2. 字体结论（需要改镜像）

`fc-match` 现状（Debian 12）：

| 请求字体 | 实际命中 |
| --- | --- |
| Microsoft YaHei / 微软雅黑 | Noto Sans **Regular（拉丁）** |
| SimSun / 等线 / DengXian | Noto Sans Regular（拉丁） |
| 宋体 | Noto Sans Regular（拉丁） |
| Calibri | Carlito（正确） |

即：拉丁字符用 Noto Sans，中文在渲染时逐字回退到 WenQuanYi Zen Hei。
文本可提取（实测第 1 页含 29 个 CJK 字符、0 个 U+FFFD），所以不会出现"豆腐块"，
但字宽/行距与原稿（雅黑/宋体）有差异，个别生僻字会落到 `Unifont`（位图感）。

**修正**：
1. 镜像安装 `fonts-noto-cjk`（Debian 12 候选版本 `1:20220127+repack1-1` 可用）；
2. 增加 fontconfig 别名：`Microsoft YaHei/微软雅黑/等线/DengXian → Noto Sans CJK SC`，
   `SimSun/宋体 → Noto Serif CJK SC`，避免回退到拉丁 Noto Sans。

## 3. 隐藏页结论（必须强制导出）

用 3 个真实文件对比 `python-pptx` 的 slide 数与默认导出的 PDF 页数：

| 样本 | 大小 | slides | 默认导出 pages | 隐藏页数 |
| --- | --- | --- | --- | --- |
| 6d870aa2… | 4.3 MB | 53 | 51 | 2 |
| a1375de5… | 41.0 MB | 113 | 110 | 3 |
| f02c9d56… | 55.8 MB | 147 | 144 | 3 |

**三份真实文件都有隐藏页**，默认导出会丢页 → 引用定位会整体错位。

已验证可用的导出参数（LO 7.4 支持 JSON filter options）：

```bash
soffice --headless --norestore -env:UserInstallation=file:///tmp/lo-profile-N \
  --convert-to 'pdf:impress_pdf_Export:{"ExportHiddenSlides":{"type":"boolean","value":"true"}}' \
  --outdir <dir> <file.pptx>
```

实测：同一份 53 页文件，加该参数后导出 **53 页**，`slide N == page N` 成立。
因此 P5 不需要额外持久化 slide→page 映射表（若后续发现个别文件仍不一致，
再退回"记录 visible slide 列表 + 映射"的方案）。

## 4. 耗时 / 内存 / 体积（实测）

| 样本 | 大小 | 耗时 | 峰值 RSS | PDF 大小 | 比例 |
| --- | --- | --- | --- | --- | --- |
| 6d870aa2… | 4.3 MB | 31.8 s | — | 2.6 MB | 60% |
| a1375de5… | 41.0 MB | 79.0 s（冷） / 77.7 s（热） | ~1013 MB | 7.0 MB | 17% |
| f02c9d56… | 55.8 MB | 61.4 s | — | 11.0 MB | 20% |

要点：
- **耗时与体积不线性**，主要由幻灯片数量与图片复杂度决定；40MB/110 页 ≈ 78s。
- 冷/热差异很小（79.0s vs 77.7s）→ **按需转换会让用户干等，必须入库预生成**。
- 单次转换峰值内存约 **1 GB**；库内还有 123–129 MB 的文件，需按 1.5–2 GB 预留。
- PDF 体积约为源文件的 **13–20%**（本次样本），9.2 GB 语料预估新增 **1.5–3 GB**，
  远低于先前 3–8 GB 的估计。

## 5. 对 P5 实现的硬约束

1. **预生成时机**：解析成功后入队 `preview:render`（独立队列），失败不影响解析状态。
2. **并发**：全局并发 **1–2**（峰值 RSS ≈1 GB/任务），单任务超时 **300 s**；
   每个任务独立 `-env:UserInstallation`（现有 `ppt_convert.py` 已是该模式）。
3. **大小上限**：建议 >150 MB 直接标 `failed`，前端提示"请下载原文件查看"。
4. **导出参数**：必须带 `ExportHiddenSlides=true`，否则引用跳页错位。
5. **字体**：镜像加 `fonts-noto-cjk` + fontconfig 别名；否则中文版式与原件有可见差异。
6. **产物**：docreader 通过流式 RPC 返回 PDF（对齐现有 `ReadStream` 分块模式），
   app 写入对象存储，并在 `knowledge_preview_artifact` 记录
   `status/page_count/content_hash`。
7. **预览接口**：`/knowledge/:id/preview` 只返回派生 PDF，不再返回原始文件；
   原始文件仅走 `/download`（受 ACL 控制）。
8. **兜底**：`pending` → 占位；`failed` → 按需转换（限时 60s）→ 小文件客户端渲染 →
   提示下载（有权限者给按钮）。

## 6. 复现步骤

```bash
# 1) 取样本（viewer 账号即可，preview 返回原始文件）
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"zhangzy@unionclin.com","password":"75554406"}' | jq -r .token)
curl -H "Authorization: Bearer $TOKEN" -o /tmp/probe/s.pptx \
  "http://localhost:8080/api/v1/knowledge/<knowledge_id>/preview"

# 2) 拷入 docreader 并转换
docker cp /tmp/probe/s.pptx WeKnora-docreader:/tmp/probe/
docker exec WeKnora-docreader sh -lc 'soffice --headless --norestore \
  -env:UserInstallation=file:///tmp/lo-p1 \
  --convert-to "pdf:impress_pdf_Export:{\"ExportHiddenSlides\":{\"type\":\"boolean\",\"value\":\"true\"}}" \
  --outdir /tmp/probe /tmp/probe/s.pptx'

# 3) 校验页数/字体/文本
docker exec -i WeKnora-docreader /app/.venv/bin/python -c "
import pypdf; r=pypdf.PdfReader('/tmp/probe/s.pdf')
print('pages', len(r.pages)); print((r.pages[0].extract_text() or '')[:80])"
```

## 7. 结论

P0 通过，P5 按第 5 节约束实现。唯一必须在编码前落地的镜像改动是
**CJK 字体 + 隐藏页导出参数**，其余均在应用层完成。
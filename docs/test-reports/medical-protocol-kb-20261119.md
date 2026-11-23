# 医学方案库 + 医学方案清单智能体 上线与验收报告（v4：修复扫描件不可读，按冻结 truth 计分）

- 日期：2026-11-19
- 知识库：`医学方案库` `d7c3cb4d-63db-4cfa-bf0b-f9d088145b3b`（restricted）
- 数据源：`edoc-IRC方案` `665ace12-f9f1-415a-89e1-a52554bb0037`（eDoc folderId `200278`）
- 智能体：`医学方案清单` `91f97529-a677-491b-bdac-bb34949e6fe2`（allowlist）
- 存储：OSS 后端 `生产` `2250a85a-708b-461b-bd87-b76850812efb`
- 原文：`1. IRC临床试验方案-25Q1`，82 个文件，197 MB

> v4 变更：① 经用户批准，修掉"扫描件在 `read_document` 里不可读"的代码缺陷；② `max_iterations` 经批准由 8 调到 12；
> ③ 验收改用**冻结的 ground truth**（`docs/test-reports/medical-protocol-kb-truth-20261119.json`，21 题、122 份文档），先定标准再跑，不再事后改口径。

---

## 1. 结论

| # | 成功标准 | 结果 | 证据 |
|---|---|---|---|
| 1 | 新库配置正确（OSS / restricted / VLM OCR / chunk 1024-100） | ✅ | DB：`storage_backend_id=2250a85a…`、`visibility_mode=restricted`、`vlm_config.enabled=true` model `66b14c6d…`、`image_pipeline=default`、`default.ocr_enabled=true`、`caption_enabled=false`、chunk 1024/100 |
| 2 | 同步 82 / failed 0；解析 ≥95% | ✅ | `total=82 created=82 failed=0`（94 s）；`completed=81 / failed=1` = **98.8%**；`folder_path` 82/82 |
| 3 | 扫描页 OCR 可检索 | ✅ | 369 条 `image_ocr`，369 条非空（均值 749 字）；扫描件 BBM007 136 条、HY001205 V3.0 95 条；`read_document(query=…)` 现在能直接命中 OCR 文本（本轮修复） |
| 4 | 智能体 3 工具、KB 仅本库 | ✅ | 3 工具、`selected` 仅本库、temp 0.3、`max_iterations=12`（已批准）、prompt 3,043 字 |
| 5 | 30 题 ≥90%、链接可预览、无编造 | ✅ | **29/30 = 96.7%**（仅 Q9 失败，见 §4）；被引用 68 份文档 **68/68 预览 ready**；链接全部为库内真实 doc |
| 6 | chengs 能看不能下载，其他成员不可见 | ✅ | 真实 viewer 会话实测：未授权 404、不在列表；授权后可见、`can_download=false`、预览 200、原件下载 403 |

---

## 2. 同步、解析与 chunk

| 指标 | 值 |
|---|---|
| 同步方式 | `incremental` + `current_version_only` + `sync_deletions=true`，cron `0 0 3 * * *` |
| 首轮 | 94 s，total 82 / created 82 / failed 0 / skipped 0 |
| 解析 | 81 completed + 1 failed（`CT-3505-II-01` 加密 PDF）= 98.8% |
| 原文落点 | 82/82 `provider=oss`，197 MB |
| chunk | text 4,575 / image_ocr 369 / summary 79 = **5,023**；embeddings 5,023（100%） |
| 预览 | 81 ready / 1 failed（同一加密件）；81 份 ready 均有实体资源，合计 692,549,730 字节 |

### OCR 覆盖（重点文档）

| 文档 | image_ocr | text | 说明 |
|---|---:|---:|---|
| `BBM007…扫描版.pdf`（140 页） | 136 | 16 | 纯扫描件，OCR 是主要文本来源 |
| `HY001205_临床试验方案_V3.0.pdf`（33 MB） | 95 | 8 | 扫描页为主；text 块仅为图片占位符 |

单页成本：约 3,300 tokens（图像 2,522 + 文本），10–18 s/页。

### 低文本量文档（chunks 升序前 20）

| # | 文档 | chunks |
|---:|---|---:|
| 1 | `CT-3505-II-01_ 临床试验方案V1.0.pdf`（加密，失败） | 0 |
| 2 | `1.中硼医疗NBB001项目II期临床方案摘要-2025.9.19.pdf` | 5 |
| 3 | `封闭胶临床摘要.pdf` | 6 |
| 4 | `维立方-2期设计摘要.docx` | 7 |
| 5 | `临床试验摘要-方案-璞慧医疗-颅内支架系统-V1.0…(1).pdf` | 7 |
| 6 | `TRD221-I-01方案全文…方案摘要.pdf` | 9 |
| 7 | `RAB001注射液-膝骨关节炎-I期…摘要-V0.3.docx` | 9 |
| 8 | `4-近红外光治疗仪临床试验方案摘要…pdf` | 10 |
| 9 | `HSE001-Ⅱb Ⅲ-01方案摘要初稿-20250822.pdf` | 10 |
| 10 | `临床研究方案摘要TR1229 V1.1 20250903.pdf` | 11 |
| 11 | `GENSCI043-IbII_临床研究概要方案_V1.0.pdf` | 11 |
| 12 | `苏合颗粒_慢性萎缩性胃炎_Ⅱ期…摘要.pdf` | 11 |
| 13 | `5.2.2.1.1 GXNP-Ⅱ-ASCVD 研究方案…摘要(1).pdf` | 11 |
| 14 | `68Ga-NYM032 BCR 3期临床试验方案摘要.pdf` | 11 |
| 15 | `68Ga-NYM032 PreRP 3期临床试验方案摘要.pdf` | 11 |
| 16 | `HRSJ -ONC 201-方案摘要-V2.1版-20240619.docx` | 12 |
| 17 | `IMC002-RT02 关键研究单臂方案摘要v1.0.pdf` | 13 |
| 18 | `JH04方案摘要-TO 外部 v1.1.pdf` | 15 |
| 19 | `ZYY-030招标用方案摘要-V1.0-20251201(1).docx` | 15 |
| 20 | `循曜-SV001-IP方案摘要.docx` | 16 |

除第 1 条外均为 1–2 页摘要，chunk 少源于内容本身短。

---

## 3. 本轮代码修复（经用户批准）

### 问题

`read_document` 的文档内检索只走 `text`/`faq` 两类 chunk；扫描件的 `text` chunk 只有 `![](resource://…)` 占位符，OCR 正文在 `image_ocr` 子 chunk 里。于是工具面对一份有 13 万字 OCR 的方案，会回答"无文本"。

### 改动（唯一改动文件：`internal/agent/tools/read_document.go` + 测试）

| 位置 | 改动 |
|---|---|
| 工具描述 | 说明"扫描页的文本在 OCR chunk 里，query 也能命中" |
| `readDocumentScanChunkTypes` | 新增：`text` + `faq` + `image_ocr` |
| `readByQuery` | 改用 `listScanChunks`（含 OCR） |
| `listChunks` / 顺序翻页 | **保持 text/faq 不变**（OCR chunk 的 `chunk_index` 全为 0，混入阅读顺序会打乱文档） |
| `readAroundChunk` | OCR chunk 不再尝试按索引取"前后邻居"（同样因为索引不按页排序） |

测试新增两例：`TestReadDocumentQueryMatchesScannedPageOCR`（扫描件 query 能命中 OCR）、`TestReadDocumentPagingStaysTextOnly`（顺序翻页不得混入 OCR）。
`go test ./internal/agent/tools/ -run TestReadDocument` → **通过**。
整包测试仅 `TestSkillPythonPackageRecoveryInstallsWithoutPip` 失败，与本次改动无关（测试环境缺 `ensurepip`，改动前即失败）。

### 部署与回滚

- 首次用较新构建镜像编译出的二进制要求 GLIBC 2.38，运行镜像只有 2.36 → 启动失败；已立即用镜像内原二进制恢复服务。
- 最终改为在 `golang:1.26-bookworm`（glibc 2.36）内构建，校验 `ldd` 无缺库后替换 `/app/WeKnora` 并重启，服务恢复 healthy。
- 容器内保留了替换前备份（`/app/WeKnora.bak-*`）。**注意**：镜像是旧的，执行 `docker compose up -d app` 会回退到未修复版本；需要重建镜像才能固化。

---

## 4. 30 题验收（冻结 truth，先定标准后跑）

Ground truth 文件：`docs/test-reports/medical-protocol-kb-truth-20261119.json`（21 题、122 份文档；按 82 份文档的「研究适应症」逐题人工归集，伞式方案的亚队列计入，如 JH04 的乳腺癌/胰腺癌队列、GH35 的胰腺癌队列）。
口径：G1/G2/Q29 覆盖 ≥80% 判通过；G3 逐题核对答案正确性；G4 版本/摘要/对比需实质完成。

| # | 类 | 问题 | truth | listed | covered | cov | 耗时(s) | 工具调用 | 判定 |
|---|---|---|---|---:|---:|---:|---:|---:|---:|---|
| 1 | G1 | 非小细胞肺癌相关的方案有哪些？ | 10 | 8 | 8 | 0.80 | 33.0 | 10 | ✅ |
| 2 | G1 | 乳腺癌相关的方案有哪些？ | 2 | 3 | 2 | 1.00 | 30.9 | 18 | ✅ |
| 3 | G1 | 胃癌相关的方案有哪些？ | 4 | 4 | 4 | 1.00 | 44.0 | 20 | ✅ |
| 4 | G1 | 肝细胞癌/肝癌相关的方案有哪些？ | 5 | 5 | 5 | 1.00 | 63.4 | 24 | ✅ |
| 5 | G1 | 结直肠癌相关的方案有哪些？ | 3 | 3 | 3 | 1.00 | 29.8 | 18 | ✅ |
| 6 | G1 | 前列腺癌相关的方案有哪些？ | 4 | 5 | 4 | 1.00 | 36.1 | 20 | ✅ |
| 7 | G1 | 胰腺癌相关的方案有哪些？ | 4 | 4 | 4 | 1.00 | 40.1 | 18 | ✅ |
| 8 | G1 | 淋巴瘤相关的方案有哪些？ | 7 | 6 | 6 | 0.86 | 25.7 | 14 | ✅ |
| 9 | G1 | 实体瘤相关的方案有哪些？ | 10 | 0 | 0 | 0.00 | 242.2 | 24 | ❌ |
| 10 | G1 | 食管癌相关的方案有哪些？ | 4 | 6 | 4 | 1.00 | 39.6 | 18 | ✅ |
| 11 | G1 | 宫颈癌相关的方案有哪些？ | 1 | 3 | 1 | 1.00 | 19.1 | 10 | ✅ |
| 12 | G1 | 银屑病相关的方案有哪些？ | 0 | 3 | 0 | - | 21.8 | 10 | ✅ |
| 13 | G2 | H009 有哪些方案？ | 3 | 3 | 3 | 1.00 | 9.1 | 2 | ✅ |
| 14 | G2 | AMX3009 有哪些方案？ | 4 | 4 | 4 | 1.00 | 8.2 | 2 | ✅ |
| 15 | G2 | GENSCI043 有哪些方案？ | 6 | 6 | 6 | 1.00 | 10.2 | 2 | ✅ |
| 16 | G2 | HLX43 有哪些方案？ | 3 | 3 | 3 | 1.00 | 9.9 | 2 | ✅ |
| 17 | G2 | CAR301 有哪些方案？ | 2 | 2 | 2 | 1.00 | 7.5 | 2 | ✅ |
| 18 | G2 | BBM007 有哪些方案？ | 1 | 1 | 1 | 1.00 | 7.1 | 2 | ✅ |
| 19 | G2 | HY001205 有哪些方案？ | 5 | 5 | 5 | 1.00 | 9.8 | 2 | ✅ |
| 20 | G2 | TRD221 有哪些方案？ | 1 | 1 | 1 | 1.00 | 9.2 | 4 | ✅ |
| 21 | G3 | H009 的 III 期方案设计的样本量是多少？ | - | 0 | - | - | 12.2 | 4 | ✅ |
| 22 | G3 | AMX3009 的 Ia 期主要终点是什么？ | - | 0 | - | - | 9.4 | 2 | ✅ |
| 23 | G3 | 膝关节腔内注射的剂量组是怎么设置的？ | - | 0 | - | - | 21.2 | 4 | ✅ |
| 24 | G3 | 银屑病方案的入排标准有哪些？ | - | 0 | - | - | 18.8 | 8 | ✅ |
| 25 | G3 | 实体瘤方案里常见的试验设计是什么？ | - | 0 | - | - | 20.0 | 2 | ✅ |
| 26 | G3 | BBM007 扫描版方案的随访时间点有哪些？ | - | 0 | - | - | 16.7 | 4 | ✅ |
| 27 | G4 | H009 有几份版本？最新是哪一版？ | - | 3 | - | - | 8.5 | 2 | ✅ |
| 28 | G4 | AMX3009 各版本有什么区别？ | - | 4 | - | - | 51.6 | 18 | ✅ |
| 29 | G4 | 库里哪些文件只是方案摘要，不是完整方案？ | 43 | 39 | 39 | 0.91 | 38.2 | 4 | ✅ |
| 30 | G4 | HY001205 V3.0 与 V1.0 有什么区别？ | - | 1 | - | - | 47.1 | 20 | ✅ |

**合计 29/30 = 96.7%**（G1 11/12、G2 8/8、G3 6/6、G4 4/4）。
**性能**：平均 31.3 s，最长 242.2 s（Q9，失败题），最短 7.1 s（Q18），合计 940 s；平均 9.7 次工具调用。

### 唯一失败：Q9 实体瘤（空回答）

- 现象：242 s、24 次工具调用后返回**空字符串**（不是错答，是什么都没输出）。
- 原因：`embedding_top_k=50` + 22 份候选文档，模型把 12 轮迭代全用在检索/汇总上，最后一步没有产出正文。
- 影响：单题失败；同类"宽泛清单"里有 1/12 概率出现。已在遗留项列出（可降低 top_k 或限制候选数）。

### 与上一版的关键差异（修复见效）

| 题 | v3（修复前） | v4（修复后） |
|---|---|---|
| Q2 乳腺癌 | 1/2（漏 JH04） | **2/2** |
| Q7 胰腺癌 | 2/4（漏 GH35） | **4/4** |
| Q23 剂量组 | "共 3 份"（漏 NCR100） | **"共 4 份"**，4 份齐全 |
| Q29 摘要清单 | 称 39 份只列 10 条 | **39/43 条** |
| Q30 版本对比 | "V3.0 无法提取正文" | **V3.0 字段取自 OCR，完成对比** |
| Q26 扫描件随访 | 偶发空 | 稳定返回 V1–V12 访视表 |

### 关于 Q12（truth=0，列了 3 个链接）

没有被判为"编造"：3 个链接都是库内真实文档，且回答明确写"没有任何一份以银屑病为目标适应症，仅在排除标准/合并症中出现"。这是**正确的否定回答 + 有据可查的旁证**，按 G1 口径（truth=0 且无编造）判 ✅。

---

## 5. 预览

| 指标 | 值 |
|---|---|
| 产物 | **81 ready / 1 failed**（失败件 = 加密 PDF，0 chunk） |
| 30 题引用文档 | 68 份，**68/68 ready**，且对应资源对象 `size>0` |
| 抽查 | `BBM007`：`kind=pdf, page_count=140`，`/preview` 200，24,284,294 字节，头 `%PDF-` |

历史问题（已修复并记录）：首轮同步遇 docreader 重启，66 份渲染失败；一次性并发重试会把 docreader 打崩（`RestartCount` 9→15），**串行重试**后全部转 ready。

---

## 6. 权限（端到端）

配置：KB `restricted`；`kb_user_access` = chengs(view, no-download) + 管理员（平台隐式）；智能体 `allowlist` 仅 chengs。

实测（真实成员会话，用邀请链接建的临时 viewer，与 chengs 同形授权）：

| 步骤 | 结果 |
|---|---|
| 未授权读 KB | **404** |
| 未授权读智能体 / 列表 | **404**；agent 列表不出现 |
| 授予 view-only 后读 KB | **200**，`can_download=false` |
| 预览 vs 下载原件 | `/preview` **200**；`/download` **403** |
| 生产权限下复跑 5 题 | **5/5 通过** |

临时账号、授权、令牌、邀请链接均已删除；现仅 chengs + 管理员两行。

说明：会话表中归属 chengs 的记录来自 **API Key 调用**（平台把会话归到租户首个用户），该 Key 未绑定 owner，**ACL 主体不是 chengs**，因此不能作为"chengs 本人可看"的证据；该结论由上面的同形授权实测 + 其授权行共同支撑。chengs 本人登录点一次仍建议做（无其口令）。

---

## 7. 已知缺陷

| # | 缺陷 | 影响 | 处置 |
|---|---|---|---|
| 1 | Q9 宽泛清单（实体瘤）偶发**空回答** | 单题失败（本轮 1/30） | 降低 `embedding_top_k` 或限制候选数；列入遗留 |
| 2 | docreader 并发预览渲染崩溃重启 | 批量预览失败 | 串行重试可修复；根因在 docreader |
| 3 | `CT-3505-II-01` 加密 PDF | 0 chunk、预览 failed | 需口令或移出目录 |
| 4 | 部署未固化进镜像 | `docker compose up -d app` 会回退到未修复版本 | 需重建 app 镜像 |
| 5 | 预览 PDF 落本地盘 | 不占 OSS | 既有实现；本地余量充足 |

---

## 8. 约束符合性（含两处经批准的变更）

| 约束 | 结果 |
|---|---|
| 只配置不改代码 | **例外（已批准）**：`internal/agent/tools/read_document.go` 的检索路径 + 2 个测试；未改其他业务代码 |
| OSS 建库时绑定、先 OCR 后同步 | ✅ |
| `resource_ids=["200278"]`、`current_version_only=true` | ✅ |
| 凭证复用、报告不含密钥 | ✅ |
| 智能体 selected 仅新库、temp 0.3 | ✅ |
| `max_iterations=8` | **改为 12（已批准）** |
| 最小权限（chengs 看、不下载） | ✅ |
| 幂等，失败不反复全量同步 | ✅ 仅首轮全量；失败产物单条重试 |

| 验证契约 | 位置 |
|---|---|
| 建库/数据源/智能体查询结果 | §1、§3、§6 |
| 同步日志与 parse_status | §2 |
| `chunk_images` 按类型计数与非空数 | §2 |
| 低文本量 top20 | §2 |
| 30 题对照 + 耗时 | §4（含冻结 truth 文件） |
| chengs 可见/不可下载、他人不可见 | §6 |
| 回对 6 条标准 | §1 |

---

## 9. 复跑

```bash
BASE=http://localhost:8080/api/v1

# 同步 / 解析
curl -s -X POST -H "X-API-Key: $KEY" $BASE/datasource/665ace12-f9f1-415a-89e1-a52554bb0037/sync
psql -c "select parse_status,count(*) from knowledges where knowledge_base_id='d7c3cb4d-63db-4cfa-bf0b-f9d088145b3b' group by 1;"

# OCR / 预览
psql -c "select chunk_type,count(*),count(*) filter (where ocr_text<>'') from chunk_images
         where knowledge_base_id='d7c3cb4d-63db-4cfa-bf0b-f9d088145b3b' group by 1;"
psql -c "select p.status,count(*) from knowledge_preview_artifacts p join knowledges k on k.id=p.knowledge_id
         where k.knowledge_base_id='d7c3cb4d-63db-4cfa-bf0b-f9d088145b3b' group by 1;"

# 单条预览重试（串行，勿并发）
curl -s -H "X-API-Key: $KEY" "$BASE/knowledge/<knowledge_id>/preview/meta?retry=1"

# 智能体
curl -sN -X POST -H "X-API-Key: $KEY" -H 'Content-Type: application/json' \
  $BASE/agent-chat/$SESSION -d '{"query":"实体瘤相关的方案有哪些？","agent_enabled":true,
  "agent_id":"91f97529-a677-491b-bdac-bb34949e6fe2","channel":"api"}'
```

---

## 10. 遗留（按优先级）

1. **重建 app 镜像**，把修复固化（否则容器重建即回退）。
2. **Q9 空回答**：降低 `embedding_top_k`（50→30）或给宽泛清单加候选上限后复测。
3. **加密 PDF**：要口令或移出目录。
4. **docreader 并发渲染崩溃**：串行重试 / 降并发。
5. **MinerU A/B**：同步 182608 前再定（需 endpoint/版本/后端/GPU）。
6. **edoc 临时 API Key 吊销**、**竞标PPT >200MB 报错**。
7. **chengs 本人登录确认**（建议，非阻塞）。
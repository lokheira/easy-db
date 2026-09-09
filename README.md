# easy-db — 从零编写教学型数据库

> 本项目旨在通过**从零手写一个数据库**来学习数据库的底层构造原理：
> 文件存储格式 → 缓冲池 → 记录层 → B+ 树索引 → SQL 执行器 → 事务并发 → WAL 崩溃恢复。
> 每个阶段都有可独立验证的成果，边学边做。

---

## 1. 项目定位与范围

- **形态**：单机、单进程、单用户、可持久化的教学型命令行数据库 `easy-db`。
- **语言**：Go（建议 Go ≥ 1.21），仅使用标准库（`os`、`encoding/binary`、`bufio`、`sync`、`container/list`），**零第三方依赖**。
- **验证方式**：`go build` / `go vet` / `go test`，并发代码用 `go test -race`。
- **数据布局**：单文件多页布局（页大小 4096 字节，类似 SQLite 的思路），便于用 `xxd` 或自写 dump 工具观察二进制格式，强化底层理解。
- **交付物**：以学习为主，每个阶段由学习者亲手编写代码；每阶段配有验收标准与测试用例。

> ⚠️ 注意：开始前需自行安装 Go 工具链（当前开发机尚未安装）。
> Windows 可用 `winget install GoLang.Go`，或前往 <https://go.dev/dl> 下载。

---

## 2. 目录结构设计

`go.mod` 模块名可自定（如 `easydb`）。

```
easy-db/
├─ go.mod                     # module easydb；go >= 1.21
├─ cmd/easydb/main.go         # REPL 入口（命令循环）
├─ internal/storage/          # 页、堆文件、记录编码
├─ internal/buffer/           # 缓冲池、LRU
├─ internal/record/           # Schema、Tuple、表、扫描器
├─ internal/index/            # B+ 树
├─ internal/sql/              # lexer / parser / AST / 执行器 / 表达式 / catalog
├─ internal/tx/               # 事务、锁、死锁检测
├─ internal/wal/              # WAL 与崩溃恢复
├─ README.md                  # 架构说明 + 每阶段验收命令
└─ 各包内 *_test.go           # go test ./...
```

包依赖方向（自底向上）：

```
cmd/easydb
    └─ sql ── tx ── wal
       │  └── index
       └─ record ── buffer ── storage
```

---

## 3. 学习路线（分阶段计划）

### 阶段 0：环境与骨架（约 1 天）

- 安装 Go，执行 `go mod init`，搭建 `cmd/easydb` 空 REPL（读一行、回显、支持 `EXIT`）。
- 约定构建 / 测试命令：
  - `go build ./cmd/easydb`
  - `go vet ./...`
  - `go test ./...`
  - `go test -race ./internal/tx/...`
- **里程碑**：任意目录下都能编译并运行 REPL。

---

### 阶段 1：磁盘存储与文件格式（1–2 周）

**学习点**：页（Page）、页头、slot 目录（堆表经典布局）、定长/变长记录、文件末尾追加页、删除标记与空闲空间回收、持久化。

- `Page`：固定 `[]byte`（4096 字节），提供读写头部字段（页类型、记录数、空闲偏移等）的辅助。
- `HeapFile`：对单个 `os.File` 的页级读写抽象；PageID → 文件位置换算。
- 记录编码：定长字段直接拼接；变长字段用长度前缀；DELETE 用 slot 标记。
- 命令 / API：`CreateTable / AppendRecord / GetRecord / DeleteRecord / Scan`；关闭文件重开数据仍在。
- **测试**：写入 1 万条 → 关闭 → 重开 → 逐条校验；用 `xxd` 或自写 dump 观察页二进制。
- **里程碑**：不依赖内存缓存的“直读直写”存储层可增、删、查。

---

### 阶段 2：缓冲池与页缓存（约 1 周）

**学习点**：Buffer Pool、页表映射、Pin/Unpin 引用计数、LRU/CLOCK 淘汰、脏页与刷盘。

- `BufferPool`：定长 frame 数组 + `map[PageID]*frame`；提供 `Fetch / Unpin / Flush / FlushAll / NewPage`。
- 驱逐策略：LRU（`container/list`）或 CLOCK；脏页先落盘；frame 复用避免频繁分配。
- 单用户阶段先加互斥锁保证线程安全；把存储层改为“一切页访问经缓冲池”。
- **测试**：随机访问 10 万次，断言内存中页数 ≤ 池容量；脏页淘汰后磁盘内容正确。
- **里程碑**：页面读写统一走缓冲池，内存占用有上限。

---

### 阶段 3：记录层与 REPL（1–2 周）

**学习点**：Schema、Tuple 序列化、NULL 位图、RID = (page_id, slot)、UPDATE 实现策略、迭代器。

- `Schema`：列名 / 类型（INT、VARCHAR(n)、BOOL 可选）/ 是否可空。
- `Tuple`：字节流编码 / 解码（含 NULL 位图）；按 `RID` 随机访问。
- 表扫描迭代器：`func (t *Table) Scan() Iterator[Tuple]`，配套 `Next() (Tuple, error)`。
- REPL 命令：

  ```
  INSERT <id> <name> <age>
  SCAN
  UPDATE <id> <name> <age>
  DELETE <id>
  SHOW TABLES
  EXIT
  ```

- **测试**：10 万条插入 → 重启 → 扫描校验；空表 / 超长值 / 非法命令报错清晰、进程不崩。
- **里程碑**：通过 REPL 完成完整增删改查，且重启后数据不丢。

---

### 阶段 4：索引 — B+ 树（1–2 周）

**学习点**：B+ 树结构、节点即页（复用阶段 1/2 的存储层）、插入分裂、查找与范围扫描、删除（借位与合并为选做）。

- 页布局：内部节点存分隔键 + 子页号；叶子节点存 `(key, RID)` 列表；根页号持久化。
- API：`Insert(key, rid)`、`Lookup(key)`、`RangeScan(low, high)`；建议直接落盘实现。
- 命令扩展：`CREATE INDEX ON <table>(<col>)`；`WHERE col = v` / `col BETWEEN a AND b` 走索引。
- **测试**：10 万随机键插入后点查全部正确；与全表扫描对比耗时并记录到 README。
- **里程碑**：随机数据下点查 O(log n) 有实测对比数据。

---

### 阶段 5：SQL 子集与执行器（2–3 周）

**学习点**：tokenize、递归下降解析、AST、执行器、表达式求值与 NULL 三值逻辑、系统目录（catalog）。

- 支持的 SQL 子集：

  ```sql
  CREATE TABLE t (id INT PRIMARY KEY, name VARCHAR(32), age INT NULL);
  INSERT INTO t VALUES (1, 'Alice', 20);
  INSERT INTO t (id, name) VALUES (2, 'Bob');      -- age 为 NULL
  SELECT * FROM t WHERE age >= 18;
  UPDATE t SET age = 21 WHERE name = 'Alice';
  DELETE FROM t WHERE id = 1;
  DROP TABLE t;
  SHOW TABLES;
  ```

- 表达式：比较（`= < > <= >= <>`）、算术（`+ - * /`）、`AND / OR / NOT`、NULL 三值逻辑（结果为 UNKNOWN）。
- Catalog 设计为普通表（存放 schema 行），新建表时写回数据文件。
- **测试**：与阶段 3 命令对同一批数据做结果等价性用例；错误 SQL 返回明确报错、不崩溃。
- **里程碑**：能用标准 SQL 子集完成建表、增删改查。

---

### 阶段 6：事务与并发控制（1–2 周）

**学习点**：事务抽象、原子性 / 隔离性、两阶段锁（2PL）、死锁检测、隔离级别。

- `BEGIN / COMMIT / ROLLBACK`；写操作经事务上下文执行。
- 锁管理器：先表级、后行级；采用严格 2PL（锁持有到 COMMIT 后再释放）。
- 死锁：等待图检测或锁超时；被选为牺牲者的事务自动 ROLLBACK 并返回错误。
- Go 并发注意：goroutine + channel / 共享锁管理器；`go test -race` 全绿。
- 隔离级别：READ COMMITTED；时间充裕再实现 REPEATABLE READ。
- **测试**：多 goroutine 并发转账式读写——不丢更新、回滚可见、无死锁悬挂。
- **里程碑**：并发下数据一致，事务可回滚。

---

### 阶段 7：WAL 与崩溃恢复（1–2 周）

**学习点**：Write-Ahead Logging、日志记录格式、STEAL+NO-FORCE、redo/undo（简化 ARIES）、LSN 与检查点（可选）。

- WAL 文件独立于数据文件；每次变更**先追加日志再改页**，必要时 `fsync`（或 group commit）。
- 日志记录：`BEGIN(TXNID)`、`INSERT / UPDATE / DELETE（前后镜像）`、`COMMIT(TXNID)`。
- 恢复流程：打开时扫描日志 → redo 已提交事务 → undo 未提交事务 → 启动完成。
- **崩溃注入测试**：子进程写数据途中被测试脚本 kill，重启后断言：已提交不丢、未提交不留半写。
- **里程碑**：kill -9 场景下数据库一致性有自动化测试覆盖。

---

### 阶段 8（可选扩展，任选其一深耕）

- MVCC 多版本（undo 链 / 版本页）与 SNAPSHOT ISOLATION；
- 哈希索引（静态 / 可扩展哈希）；
- 查询优化：谓词下推、索引选择、简单代价估算；
- JOIN（Nested Loop / Hash Join）与 GROUP BY / 聚合；
- B+ 树自身的崩溃恢复（页级 LSN）或检查点（checkpoint）机制。

---

## 4. 每阶段通用验收标准

- `go build ./...` 与 `go vet ./...` 零错误；相关包 `go test` 全绿；并发包加 `-race`。
- 重启持久化用例：关闭进程后重开，数据 / 索引完整。
- 边界用例：空表、NULL、超长记录、满页分裂、非法 SQL——报错清晰、进程不崩。
- README 每阶段记录：结构图、数据文件布局说明、验收命令、实测结果。

---

## 5. 学习资源对照（按需取用）

| 主题 | 资源 |
|---|---|
| 存储 / 缓冲池 / B+ 树 / 并发 / 恢复 | CMU 15-445 / 15-721 课程讲义与 Project |
| 存储与检索、事务 | 《DDIA》(Designing Data-Intensive Applications) 第 3、7 章 |
| 页 / 日志 / B-tree 工程化范例 | SQLite 架构文档（对照学习，不照抄） |
| B 树与日志深入 | 《Database Internals》— A. Petrov |

---

## 6. 时间预估

| 阶段 | 内容 | 预计投入（业余节奏） |
|---|---|---|
| 0 | 环境与骨架 | 1 天 |
| 1 | 磁盘存储与文件格式 | 1–2 周 |
| 2 | 缓冲池与页缓存 | 1 周 |
| 3 | 记录层与 REPL | 1–2 周 |
| 4 | B+ 树索引 | 1–2 周 |
| 5 | SQL 子集与执行器 | 2–3 周 |
| 6 | 事务与并发控制 | 1–2 周 |
| 7 | WAL 与崩溃恢复 | 1–2 周 |

**总计约 8–14 周**，任何阶段可随时暂停；时间紧时：
- 第 6 阶段可先做“单事务 + ROLLBACK”简化版；
- 第 7 阶段可先做 redo-only 恢复。

---

## 7. 假设与约定

- 按阶段顺序推进，每阶段验收通过后再进入下一阶段。
- 本文件为**学习规划与项目说明**；各阶段代码由学习者亲手编写，逐步替换本文中的占位设计。
- 阶段内新增命令、格式变更时，同步更新本文“目录结构 / 数据布局 / 验收命令”三节。

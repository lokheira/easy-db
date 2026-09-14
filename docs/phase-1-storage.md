# 阶段 1：磁盘存储与文件格式 — 详细实施文档

> 对应 `README.md` 第 3 节「阶段 1：磁盘存储与文件格式（1–2 周）」
>
> 本文是该阶段的**施工图**：给出可直接落地的页布局、记录编码、Go 接口签名、分步路线、
> 测试清单与验收命令。所有设计都以「单文件 + 4096 字节定长页」为前提，与后续
> 阶段 2（缓冲池）、阶段 4（B+ 树）、阶段 7（WAL）保持兼容。

---

## 目录

1. [本阶段目标与产出](#1-本阶段目标与产出)
2. [前置条件](#2-前置条件)
3. [核心概念速览](#3-核心概念速览)
4. [设计一：文件级布局](#4-设计一文件级布局)
5. [设计二：页内布局（页头 + slot 目录 + 记录区）](#5-设计二页内布局页头--slot-目录--记录区)
6. [设计三：记录编码](#6-设计三记录编码)
7. [设计四：Go 接口签名](#7-设计四go-接口签名)
8. [关键算法（伪代码）](#8-关键算法伪代码) — 含 8.5 墓碑与空间回收策略、8.6 slot 表容量损失（🟢 低优先级）
9. [分步实现路线](#9-分步实现路线)
10. [测试用例清单](#10-测试用例清单)
11. [手工验证：dump 工具与 xxd](#11-手工验证dump-工具与-xxd)
12. [常见坑与避雷](#12-常见坑与避雷)
13. [验收命令](#13-验收命令)
14. [学习资源（B 站优先）](#14-学习资源b-站优先)
15. [完成检查清单](#15-完成检查清单)

---

## 1. 本阶段目标与产出

### 1.1 一句话目标

写出一层**不依赖任何内存缓存**的「直读直写」存储：把记录写进磁盘文件的页里，
关掉进程再打开，数据一条不少、一条不错。

### 1.2 明确的产出物

| 产出 | 路径 | 说明 |
|---|---|---|
| 页抽象 | `internal/storage/page.go` | 页头读写、slot 目录、记录增删查、整理 |
| 页常量 | `internal/storage/const.go` | 页大小、页头大小、页类型、错误值 |
| 堆文件 | `internal/storage/heapfile.go` | `os.File` 页级读写、PageID↔偏移换算、追加页 |
| 记录编码 | `internal/storage/record.go` | 定长拼接 + 变长长度前缀的编解码 |
| 记录文件 | `internal/storage/recordfile.go` | `Append/Get/Delete/Scan` + `RID` |
| dump 工具 | `cmd/easydb/main.go`（子命令） | 把页打印成可读十六进制，肉眼验证格式 |
| 测试 | `internal/storage/*_test.go` | 见第 10 节清单 |

### 1.3 里程碑（Definition of Done）

- [ ] 能向文件追加记录，`Close()` 后重新 `Open()`，**1 万条记录逐条校验一致**
- [ ] 能按 `RID` 随机读取、能标记删除且删除状态可持久化
- [ ] 页写满时返回明确错误（`ErrPageFull`），**不 panic、不损坏已有数据**
- [ ] 能用 dump 工具 / `xxd` 指出页头每个字段的字节位置

### 1.4 本阶段**不做**什么

- 不做缓冲池（阶段 2）——每次读写都真实落到 `os.File`
- 不做 Schema / NULL 位图 / UPDATE 的完整语义（阶段 3）
- 不做 B+ 树（阶段 4）、SQL（阶段 5）、事务（阶段 6）、WAL（阶段 7）

> 但页头要**预留** `LSN` 字段，避免阶段 7 改格式。

---

## 2. 前置条件

```bash
go version          # 需要 >= 1.21，本仓库 go.mod 声明 go 1.27.1
go env GOOS GOARCH  # Windows 下应为 windows/amd64
```

依赖：**仅标准库**——`os`、`encoding/binary`、`errors`、`fmt`、`hash/crc32`（可选）、`testing`。

构建 / 校验入口（`Makefile` 已就绪）：

```bash
make build      # go build -o easydb.exe ./cmd/easydb
make vet        # go vet ./...
make test       # go test ./...
make test-race  # go test -race ./...
make fmt-check  # 校验 gofmt
```

---

## 3. 核心概念速览

在看视频之前，先用一段话把阶段 1 的五个概念串起来：

1. **页（Page）**：磁盘 I/O 的最小单位。数据库不会一次读写一个字段，而是整页读写。
   本项目的页固定 **4096 字节**，与常见磁盘块 / 文件系统块对齐，避免读放大。
2. **文件 = 页的数组**：第 `N` 页在文件中的偏移恒为 `N * 4096`。
   这个「定长页 + 算术换算」的约定，是后面所有层（缓冲池、B+ 树）能简化的根因。
3. **页头（Page Header）**：页开头的固定区域，记录这页是什么类型、有几个记录、
   空闲区在哪。没有它，页就是一堆无法解释的字节。
4. **slot 目录（Slot Directory）**：页头后面的定长表，第 `i` 项记录「第 i 条记录
   从哪里开始、有多长」。有了它，**页内移动记录不需要改外部引用**——
   外部只需要记住 `(页号, slot 号)`，也就是 `RID`。
5. **记录区从页尾向页头方向增长，slot 目录从页头向页尾方向增长**，中间是空闲区。
   两者相向而行，空闲区被夹在中间；记录长度不一时，删除会在记录区留下**空洞**，
   需要**碎片整理（compaction）**回收。

> 这套布局就是教科书里的 **slotted page（分槽页）**，也是 SQLite / PostgreSQL / MySQL
> 堆表组织的共同思路内核。

### 3.1 概念 → 视频对照

| 概念 | 建议看的资源（详见第 14 节） |
|---|---|
| 页 / 页管理 / 元数据 / 缓冲区 | 「自己动手编写数据库系统 [2025新版]」第 1 讲 |
| 辅助存储管理与索引结构 | 「数据库系统实现串讲」D1S2 |
| 单文件数据库整体直觉 | 「9 分钟带你了解 SQLite 底层原理」 |
| 工业级页与缓冲池对照 | 「MySQL 核心技术原理之 InnoDB 缓冲池底层解析」 |

---

## 4. 设计一：文件级布局

### 4.1 常量

```go
const (
    PageSize       = 4096 // 每页字节数，固定
    PageHeaderSize = 32   // 页头字节数
    SlotSize       = 4    // 每个 slot 4 字节（offset uint16 + length uint16）
)
```

### 4.2 PageID ↔ 文件偏移

```
offset = PageID * PageSize
PageID = offset / PageSize
```

因为所有页等长，**不需要索引表**就能定位任意页——只要文件足够长。
文件长度必然是 `PageSize` 的整数倍（最后一页写不满也要补零写满整页）。

### 4.3 页类型

```go
type PageType uint8

const (
    PageTypeData  PageType = 1 // 数据页：存放用户记录
    PageTypeFree  PageType = 2 // 空闲页：内容已废弃，可被重新分配
    PageTypeMeta  PageType = 3 // 元数据页：第 0 页，记录文件级信息
    PageTypeIndex PageType = 4 // 索引页：阶段 4 使用，本阶段只占位
)
```

### 4.4 第 0 页放什么

阶段 1 只需要最朴素的元数据页（真正把它当「表目录」用是阶段 3 的 catalog）：

```
magic       uint32  // 0x45415359  "EASY" —— 用于识别文件类型
version     uint32  // 格式版本，当前 1；将来改布局时靠它做兼容判断
pageSize    uint32  // 冗余存储页大小，防止用错页大小打开旧文件
pageCount   uint32  // 当前总页数（可作为 HeapFile 的内存副本校验用）
```

- 打开文件时读第 0 页校验 `magic` + `version` + `pageSize`；
  不匹配就直接报错，**绝不猜测**。
- 文件大小 < 1 页 ⇒ 判定为「新文件」，写入元数据页。

---

## 5. 设计二：页内布局（页头 + slot 目录 + 记录区）

### 5.1 一张图看懂

```
 偏移 0
+--------------------------------------+
|            Page Header (32 B)        |  ← 定长，字段见 5.2
+--------------------------------------+ 偏移 32
|  slot[0] | slot[1] | slot[2] | ...   |  ← 每项 4 B，向下（高地址）增长
+--------------------------------------+ 偏移 FreeStart
|                                      |
|              Free Space              |  ← 空闲区，被两侧夹住
|                                      |
+--------------------------------------+ 偏移 FreeEnd
|   record N  ...  record 1  record 0  |  ← 记录区，向上（低地址）增长
+--------------------------------------+ 偏移 4096
```

**关键不变式（每个测试都要能断言）：**

```
0 < PageHeaderSize <= FreeStart <= FreeEnd <= PageSize
FreeStart = PageHeaderSize + SlotCount * SlotSize     // 无碎片整理时
空闲字节数 = FreeEnd - FreeStart
```

### 5.2 页头字段表（32 字节，全部小端）

| 偏移 | 长度 | 字段 | 类型 | 说明 |
|---|---|---|---|---|
| 0 | 1 | `PageType` | uint8 | 页类型，见 4.3 |
| 1 | 1 | `Flags` | uint8 | 位标志，bit0 = 页内存在墓碑 slot（含义见下方说明） |
| 2 | 2 | `SlotCount` | uint16 | slot 总数，**含**已删除的墓碑 slot |
| 4 | 2 | `FreeStart` | uint16 | 空闲区起始 = 页头 + slot 数组末尾 |
| 6 | 2 | `FreeEnd` | uint16 | 空闲区结束 = 记录区第一个字节 |
| 8 | 4 | `NextPageID` | uint32 | 同类型页链表指针；`0xFFFFFFFF` 表示「无」 |
| 12 | 4 | `PageID` | uint32 | 本页页号，用于自校验与调试（对不上说明写错位置了） |
| 16 | 8 | `LSN` | uint64 | **预留**给阶段 7 WAL 的日志序列号，本阶段恒为 0 |
| 24 | 4 | `Checksum` | uint32 | 可选 CRC32；本阶段可恒为 0，阶段 7 启用 |
| 28 | 4 | `Reserved` | uint32 | 补齐到 32，保持 8 字节对齐，恒为 0 |

> **为什么预留 LSN 和 Checksum？** 页布局一旦落盘，改字段就要写格式迁移代码。
> 反正现在空着不花钱，先把位置占住是最省事的工程决策。

**⚠️ `Flags` bit0 的确切语义（不要搞混两种含义）：**

`BitsDeleted = 1 << 0` 表示「**本页存在至少一个墓碑 slot**」，它回答的问题是
「插入时能否复用墓碑、从而不必新增 4 字节 slot」——**不是**「页内存在碎片空洞」。

两者的区别很关键：

| | 含义 | 何时置位 | 何时清除 |
|---|---|---|---|
| ✅ 正确语义 | 存在墓碑 slot | `DeleteRecord` 把某个 slot 置为 `{0,0}` 时 | 仅当**所有**墓碑都被复用掉时 |
| ❌ 错误语义 | 存在碎片空洞 | —— | —— |

**为什么不能用「碎片空洞」做语义：** `Compact()` 整理后记录区不再有空洞，
但**墓碑 slot 依然保留**（见 8.3），此时页内仍然存在墓碑。
若整理时清除该 bit，下一次插入就会误判「无墓碑可复用」→ 不触发整理、
不复用 slot → **对一个明明放得下的记录误报 `ErrPageFull`**。

因此约定：`Compact()` **不得无条件清除**该 bit，必须按「整理后是否仍有墓碑」重新计算。
推荐写成一个统一的重算函数，在每次增删改后调用，杜绝手工置位/清位不一致：

```go
// refreshDeletedFlag 依据当前 slot 数组重算 Flags，避免手工置位遗漏。
func (p *Page) refreshDeletedFlag() {
    if p.hasTombstone() {
        p.setFlag(bitsDeleted)
    } else {
        p.clearFlag(bitsDeleted)
    }
}
```

### 5.3 slot 项（4 字节）

| 偏移 | 长度 | 字段 | 类型 | 说明 |
|---|---|---|---|---|
| 0 | 2 | `Offset` | uint16 | 记录在页内的起始偏移 |
| 2 | 2 | `Length` | uint16 | 记录字节数 |

**删除哨兵约定：`Offset == 0 && Length == 0` 表示该 slot 已删除。**

为什么 `Offset == 0` 是安全的哨兵？因为偏移 0–31 是页头区，
记录**永远不可能**落在那里，所以 0 不可能是合法记录偏移。

> 📌 **一条记录 = 恰好一个 slot**（容易误读，务必记住）。
> 记录再长也只占 / 复用**一个** slot——`Length` 字段本身就是为变长服务的，
> 不存在"长记录吃掉多个槽位"的情况。详见 8.6。
>
> ⚠️ 由此带来一个后果：**未被复用的墓碑会永久占据 slot 表空间**
> （`4 × SlotCount` 字节），而 `Compact()` **回收不了**这部分。
> 这会让"删空的页"容量反而小于全新页，具体影响见 8.6（🟢 低优先级，可暂不处理）。

### 5.4 单页能放多少条记录？

页可用空间（未放任何记录时）：

```
PageSize - PageHeaderSize = 4096 - 32 = 4064 字节
```

设记录长度为 `r`，一条记录要额外占 4 字节 slot，则：

```
n * (r + 4) <= 4064
```

**举例**：`r = 100` ⇒ `n * 104 <= 4064` ⇒ `n <= 39.07` ⇒ 每页 39 条。
写 1 万条 100 字节记录需要 `ceil(10000 / 39) = 257` 页，
文件约 `257 * 4096 = 1,052,672` 字节 ≈ 1.03 MB。

> 把这两个数字记下来，第 10 节的测试会用它做断言。

### 5.5 一条记录的最大长度

```
MaxRecordSize = PageSize - PageHeaderSize - SlotSize = 4096 - 32 - 4 = 4060 字节
```

超过就返回 `ErrRecordTooBig`（注意：这是**单页实现**的上限；
将来支持跨页记录（overflow page）时再放开，本阶段明确不支持）。

---

## 6. 设计三：记录编码

阶段 1 还没有 Schema（阶段 3 才有），所以这里只做**最基础的编码原语**：
把「一组字段」编码成字节流、再原样解回来。

### 6.1 编码规则

| 字段种类 | 编码方式 |
|---|---|
| 定长字段（INT / BIGINT / BOOL / 定长 CHAR） | 直接拼接原始字节，小端 |
| 变长字段（VARCHAR） | `uint16 长度前缀` + 原始字节 |
| 阶段 1 的整条记录 | 上述字段依次拼接，**记录之间不共享格式** |

### 6.2 具体例子

Schema 假定为 `(id INT, name VARCHAR(32), age INT)`，其中 `INT` 取 **int64（8 字节）**：

记录 `(1, "Alice", 20)` 的字节流：

```
偏移  字节                                含义
0     01 00 00 00 00 00 00 00             id = 1 (int64 小端)
8     05 00                               name 长度前缀 = 5
10    41 6C 69 63 65                      "Alice"
15    14 00 00 00 00 00 00 00             age = 20 (int64 小端)
--------------------------------------------------------------
总计 23 字节
```

### 6.3 字节序

**全项目统一小端（little-endian）**，统一用 `binary.LittleEndian`。

> 不要在页里混用大端，也不要依赖 `unsafe.Pointer` 直接类型转换——
> 那会引入对齐和可移植性问题，且 `go vet` 会不高兴。

### 6.4 编解码接口

```go
// EncodeFields 把若干字段按「定长直接拼接 / 变长长度前缀」的约定拼成一条记录。
// fixed[i] == true 表示第 i 个字段是定长字段。
func EncodeFields(fields [][]byte, fixed []bool) []byte

// DecodeFields 按照同样的 fixed 布局把记录拆回字段。
// 数据不足时返回 error，绝不越界读。
func DecodeFields(rec []byte, fixed []bool) ([][]byte, error)
```

> `fixed` 布局本身就是 Schema 的雏形，阶段 3 会把它正式化为 `Schema` 结构。

---

## 7. 设计四：Go 接口签名

### 7.1 常量与错误

```go
package storage

import "errors"

const (
    PageSize       = 4096
    PageHeaderSize = 32
    SlotSize       = 4
    MaxRecordSize  = PageSize - PageHeaderSize - SlotSize // 4060
    InvalidPageID  = ^PageID(0)                           // 0xFFFFFFFF
)

var (
    ErrPageFull     = errors.New("storage: page is full")
    ErrSlotNotFound = errors.New("storage: slot number out of range")
    ErrSlotDeleted  = errors.New("storage: slot has been deleted")
    ErrRecordTooBig = errors.New("storage: record exceeds MaxRecordSize")
    ErrBadMagic     = errors.New("storage: not an easy-db file")
    ErrBadVersion   = errors.New("storage: unsupported format version")
)
```

### 7.2 Page

```go
type PageID uint32

// RID 是记录的全局地址：第 PageID 页的第 SlotNo 个 slot。
// 注意：只要记录还在同一个 slot 里，页内移动（整理）不会让 RID 失效。
type RID struct {
    PageID PageID
    SlotNo uint16
}

type Page struct {
    id    PageID
    data  [PageSize]byte // 定长数组而非 slice：避免额外分配，值语义清晰
    dirty bool
}

func NewPage(id PageID, t PageType) *Page

// --- 页头存取 ---
func (p *Page) ID() PageID
func (p *Page) Type() PageType
func (p *Page) SetType(t PageType)
func (p *Page) SlotCount() uint16
func (p *Page) FreeSpace() int   // = FreeEnd - FreeStart
func (p *Page) Dirty() bool
func (p *Page) MarkClean()

// --- 记录操作 ---
// InsertRecord 写入一条记录并返回其 slot 号。
// 空间不足时返回 ErrPageFull（调用方应换页或先尝试 Compact）。
func (p *Page) InsertRecord(rec []byte) (slotNo uint16, err error)

// GetRecord 返回记录的副本（切片为拷贝，调用方随意持有）。
func (p *Page) GetRecord(slotNo uint16) ([]byte, error)

// DeleteRecord 把 slot 标记为墓碑，不移动其他记录、不立即回收空间。
// 记录字节会变成页内"空洞"，需靠 Compact() 回收；墓碑 slot 本身永不删除，
// 只会在后续 InsertRecord 时被复用（详见 8.5）。
func (p *Page) DeleteRecord(slotNo uint16) error

// UpdateRecord 更新记录：长度不变则原地覆盖，否则删除后重插（slot 号可能变化）。
func (p *Page) UpdateRecord(slotNo uint16, rec []byte) (newSlot uint16, err error)

// Compact 碎片整理：把所有存活记录压到页尾，重建 slot 目录。
// 整理后 slot 号保持不变（墓碑保留），因此 RID 依然有效。
// 注意：本方法只回收记录区空洞，不会减少 SlotCount，也不会清除 bitsDeleted 标志。
func (p *Page) Compact()

// Iterate 顺序遍历所有存活记录；fn 返回 false 时提前结束。
func (p *Page) Iterate(fn func(slotNo uint16, rec []byte) bool)

// --- 序列化 ---
func (p *Page) Bytes() []byte              // 返回内部数组的切片，只读使用
func DecodePage(id PageID, buf []byte) (*Page, error)
```

### 7.3 HeapFile

```go
// HeapFile 是对单个 os.File 的「页数组」抽象。
// 本阶段刻意不做缓存：每次 Read/Write 都是真实的系统调用，
// 这样才能测得准、看得清（阶段 2 再引入缓冲池）。
type HeapFile struct {
    f         *os.File
    path      string
    pageCount uint32
    freePages []PageID // 内存中的空闲页列表（可选：阶段 1 简化实现，重开时重建）
}

// OpenHeapFile 打开或创建文件；文件不存在或小于一页时按新库初始化第 0 页。
func OpenHeapFile(path string) (*HeapFile, error)

func (h *HeapFile) Close() error          // 刷盘 + 关闭；Close 之后任何操作都返回错误
func (h *HeapFile) Sync() error           // 显式 fsync，崩溃测试用

func (h *HeapFile) PageCount() uint32
func (h *HeapFile) ReadPage(id PageID) (*Page, error)
func (h *HeapFile) WritePage(p *Page) error

// AllocatePage 优先复用空闲页，否则在文件末尾追加一页。
func (h *HeapFile) AllocatePage(t PageType) (*Page, error)

// FreePage 把页标记为 PageTypeFree 并加入空闲列表。
func (h *HeapFile) FreePage(id PageID) error
```

### 7.4 RecordFile（阶段 1 的对外 API）

```go
// RecordFile 是「页」之上的一层薄封装，提供面向记录的增删查扫。
type RecordFile struct {
    hf *HeapFile
}

func CreateRecordFile(path string) (*RecordFile, error)
func OpenRecordFile(path string) (*RecordFile, error)
func (r *RecordFile) Close() error

// Append 追加一条记录。依次尝试：当前「可写页」→ 其他半满页 → 新页。
func (r *RecordFile) Append(rec []byte) (RID, error)

func (r *RecordFile) Get(rid RID) ([]byte, error)
func (r *RecordFile) Delete(rid RID) error

// Scan 全表顺序扫描所有存活记录。
func (r *RecordFile) Scan(fn func(rid RID, rec []byte) bool) error
```

> **注意 `Append` 的策略**：找到第一页放得下就往里塞，是「尽量填满」的做法；
> 简单实现可以只在「最后一页」尝试，满了就开新页。两种都行，
> 但要在 README 里写清你选了哪种——这直接影响空间利用率。

---

## 8. 关键算法（伪代码）

### 8.1 插入记录

```
InsertRecord(rec):
    if len(rec) > MaxRecordSize:  return ErrRecordTooBig

    # 先判断能否复用墓碑：能复用就省下 4 字节的 slot 开销。
    # 注意 need 必须按这条规则算，否则会把放得下的记录误判为页满（见下方说明）。
    hasTomb = 页内存在墓碑 slot          # 即 Flags 的 bitsDeleted 位
    need = len(rec) + (hasTomb ? 0 : SlotSize)

    if need > FreeSpace():
        if hasTomb:
            Compact()                     # 回收记录区空洞；墓碑仍保留，可复用
            need = len(rec)               # 整理后仍复用墓碑，故不再 + SlotSize
        if need > FreeSpace():
            return ErrPageFull

    # 1) 记录区从高位向低位生长
    FreeEnd -= len(rec)
    copy(data[FreeEnd : FreeEnd+len(rec)], rec)

    # 2) 找一个可用 slot：优先复用墓碑，否则在数组末尾新增
    slotNo = 第一个 Offset==0 的 slot 下标；若没有:
        slotNo = SlotCount
        SlotCount += 1
        FreeStart += SlotSize

    # 3) 写 slot
    slot[slotNo] = {Offset: FreeEnd, Length: len(rec)}
    refreshDeletedFlag()   # 由 slot 数组重算，而不是无条件 clear
    dirty = true
    return slotNo
```

> **⚠️ `need` 记账陷阱（已验证）**
>
> 曾经写成「无论是否复用墓碑，一律 `need = len(rec) + SlotSize`」，这会让
> **合法的插入被误拒**。反例：一条 4060 字节（= `MaxRecordSize`，页内能容纳的最大记录）
> 的记录被删除并 `Compact()` 后，`FreeSpace()` 恰好恢复到 4060 字节，完全放得下；
> 但保守算法算出 `need = 4060 + 4 = 4064 > 4060` → **误报 `ErrPageFull`**。
>
> ```
> 记录长 4060，删除 → Compact() → FreeSpace = 4060
>   保守算法（错误）: need = 4064  → ErrPageFull   ← 明明放得下
>   复用算法（正确）: need = 4060  → 插入成功
> ```
>
> 凡是「记录长度接近 MaxRecordSize」的负载，都会踩到这个坑。

### 8.2 删除记录

```
DeleteRecord(slotNo):
    s = slot[slotNo]
    if 越界:            return ErrSlotNotFound
    if s.Offset == 0:   return ErrSlotDeleted     # 重复删除要报错，便于发现逻辑 bug

    slot[slotNo] = {Offset: 0, Length: 0}         # 墓碑，空间不回收
    setFlag(Flags, deletedBit)
    dirty = true
```

**为什么删除不立即移动数据？** 因为移动会改变其他记录的位置，
如果有外部引用直接指向偏移就会全部失效。墓碑 + 延迟整理是标准做法。

### 8.3 碎片整理

```
Compact():
    buf = 临时缓冲区（长度 = 页大小）
    write = PageSize                            # 从页尾往前写
    newSlots = 空数组

    for slotNo 从 SlotCount-1 递减到 0:          # 逆序，让 slot[0] 落在最低地址
        s = slot[slotNo]
        if s.Offset == 0:                        # 墓碑原样保留，保持 slot 编号稳定
            newSlots[slotNo] = {0, 0}
            continue
        write -= s.Length
        copy(buf[write:write+s.Length], data[s.Offset : s.Offset+s.Length])
        newSlots[slotNo] = {Offset: write, Length: s.Length}

    # 一次性写回：页头 + slot 数组 + 记录区
    FreeStart = PageHeaderSize + SlotCount*SlotSize
    FreeEnd   = write
    copy(data[PageHeaderSize:FreeStart], newSlots 序列化)
    copy(data[FreeEnd:PageSize], buf[FreeEnd:PageSize])
    refreshDeletedFlag()                         # 墓碑仍在，故不能无条件清除该位
    dirty = true
```

> **整理后 slot 编号不变**，所以 `RID` 依然有效——这正是 slotted page 的核心价值。
>
> ⚠️ `Compact()` 只回收**记录区的空洞**，**不删除墓碑 slot**（slot 编号必须稳定）。
> 因此整理后页内通常仍有墓碑，`Flags` 的 `bitsDeleted` 位**应保持置位**。
> 早期草稿曾在这里写 `clearFlag(...)`，那会让后续插入误判「无墓碑可复用」而误报页满。

### 8.4 分配页

```
AllocatePage(t):
    if len(freePages) > 0:
        id = freePages.pop()
        p = ReadPage(id)
        p.SetType(t); p.重置页内布局()   # SlotCount=0, FreeStart=页头大小, FreeEnd=PageSize
        p.PageID = id
        WritePage(p)
        return p

    id = PageCount
    p = NewPage(id, t)                   # 全零 + 正确页头
    WritePage(p)                         # 顺序写在文件末尾；短写要循环补齐
    PageCount += 1
    return p
```

### 8.5 墓碑与空间回收策略（重要）

删除记录时实际发生两件**不同**的事，必须分开理解，否则很容易设计错：

```
删除前:  slot[3] = {off=0x0fec, len=100}  →  记录区 0x0fec..0x1050 的 100 字节
删除后:  slot[3] = {off=0,      len=0}    →  那 100 字节变成"空洞"，仍留在页里
         └──────── (A) ────────┘             └────────── (B) ──────────┘
         墓碑 slot 项：固定 4 字节            记录字节空洞：原记录长度
```

| | (A) 墓碑 slot 项 | (B) 记录字节空洞 |
|---|---|---|
| 大小 | 固定 4 字节（`SlotSize`） | 等于原记录长度 |
| 何时产生 | `DeleteRecord` 置 `{0,0}` | 同左 |
| 回收方式 | **从不删除，只被复用** | `Compact()` 回收 |
| 是否增长 | 否，**循环复用** | 否，整理后归零 |

#### (A) 墓碑 slot 项的三种归宿

**1. 复用（主要机制，见 8.1 步骤 2）**

```
slotNo = 第一个 Offset==0 的 slot 下标；若没有: 新增
```

任何插入都会优先填进墓碑。这是**防止 slot 数组无限增长的唯一机制**。

**2. 尾部截断（可选优化）**

若**末尾连续**若干 slot 均为墓碑，可安全缩回：

```
k = 从末尾往前数的连续墓碑个数
SlotCount -= k
FreeStart -= k * SlotSize        # 回收 4k 字节
```

> 🚫 **只能截断「尾部」**。移除中间墓碑会让其后所有 slot 编号前移，
> 导致**所有 RID 全部失效**——索引项（阶段 4）、事务状态（阶段 6）
> 都会指向错误记录。这是绝对红线。
>
> ⚠️ 被截断的 RID，语义会从 `ErrSlotDeleted` 变为 `ErrSlotNotFound`，
> 影响 10.1 节的 `TestDeleteTwice`。要不要做这一步，先想清楚 RID 的语义承诺。

**3. 整页回收（见 8.4 `FreePage`）**

当一页**全部是墓碑**（无任何存活记录）时，把整页置为 `PageTypeFree` 并加入空闲列表。
一次回收 4064 字节，粒度最粗但收益最大。

> **推荐组合：1 + 3。** 中间墓碑靠复用，整页空了就归还。
> 尾部截断属于锦上添花，教学项目可以先不做。
>
> 📎 尾部截断能救什么、救不了什么（场景 C），以及 slot 表的容量损失量化，
> 见下方 **8.6 节**（🟢 低优先级，有余力再处理）。

#### (B) 记录区空洞：`Compact()` 的触发时机

文档采用的策略是**惰性整理（lazy compaction）——页满才打扫**：

```
if 插入空间不足:  Compact()
```

三种可选策略对比：

| 策略 | 触发点 | 优点 | 缺点 |
|---|---|---|---|
| **页满时（本方案）** | 插入前 `need > FreeSpace` | 实现最简、无额外开销 | 空洞可能长期滞留 |
| 删除时立即整理 | 每次 `DeleteRecord` | 无碎片 | 每次删除 O(页内记录数) 内存拷贝，代价高 |
| 阈值触发 | 碎片字节 > 页大小 × 20% | 均衡 | 页头需加 `DeadBytes` 字段（**要改格式**） |

> 📌 **必须知道的行为特征**：只要页面一直不满，空洞就**永不回收**。
> 例如反复「插一条、删一条」的负载，页内会积累大量空洞，
> 但因每次都能放下新记录而始终不触发整理——空间利用率持续下降。
> 这是本方案的已知取舍，接受它，但要在 README 里写清楚。

#### 退化场景与对策

| 场景 | 现象 | 对策 |
|---|---|---|
| 反复插删，页面始终未满 | 空洞堆积，利用率下降 | 定期手动 `Compact()`，或改用阈值触发 |
| 大量删除后页面全空 | 整页 4064 字节浪费 | 检查「全墓碑」并 `FreePage`（归宿 3） |
| 长记录与短记录混插 | 空洞尺寸不匹配，难以复用 | `Compact()` 会被触发；极端情况换页 |
| 记录长度接近 `MaxRecordSize` | 见 8.1 的 `need` 记账陷阱 | 按 8.1 的复用算法算 `need` |

#### 实测数据（验证上述行为）

以 100 字节记录为例，单页容量 39 条：

```
首次插满:  39 条, SlotCount=39, FreeSpace=8
全删后:    SlotCount=39, FreeSpace=8        ← 墓碑不释放空间，符合 8.2
Compact(): SlotCount=39, FreeSpace=3908     ← 回收记录区 3900 字节，slot 区 156 字节保留
重插 39 条: SlotCount=39, FreeSpace=8        ← 墓碑被复用，slot 数组"零增长"
```

三条结论：

1. **删除 39 条 100 字节记录，整理后永久留下 156 字节 slot 占用**（`39 × 4`），直到被复用；
2. **复用后 slot 数组完全不增长**（`39 → 39`，`FreeSpace` 精确回到初始的 8 字节）
   —— 墓碑是**周转**而非**堆积**；
3. `SlotCount` 大致等于「**历史上同时存活记录数的峰值**」，这是容量规划时该记住的数字。

#### 一句话总结

> **墓碑 slot 项：一直不清理**（从中间移除会摧毁所有 RID），
> 但会被后续插入**复用**，因此不会无限增长；页全空时随整页归还。
>
> **记录字节空洞：由 `Compact()` 清理**，惰性触发（插入空间不足时）。

### 8.6 slot 表导致的容量损失 🟢 低优先级（有余力再处理）

> **⏭️ 可以先跳过本节。**
>
> 本节的结论是**「当前设计没有问题，只是存在一个可优化的空间损失」**。
> 8.1–8.5 的方案本身是正确且可用的，**不影响任何功能正确性，不影响测试通过**。
> 只有在阶段 1–8 全部走完、确实有余力时，再回头考虑这里的优化项。
>
> **实现时唯一必须做的**，是把下面「1 条记录 = 1 个 slot」这个认知搞清楚——
> 它是个容易误读的概念点，但不产生任何额外代码。

#### 先澄清一个常见误读

> ❌ 「一条长记录会复用**几个** slot，可能刚好差几个墓碑的空间」

**不存在"一条记录复用多个 slot"这回事。** 一条记录无论多长，
永远只占用 / 复用**恰好一个** slot——slot 里存的就是 `{offset, length}` ，
`length` 字段本来就是为记录的**变长**服务的。

实测（5 个墓碑，插入 1 条 3000 字节记录）：

```
SlotCount 仍为 5，只复用了 slot=0，其余 4 个墓碑继续闲置
```

所以真正的问题**不是**"长记录吃多个槽"，而是下面这个。

#### 真正的原因：未被复用的墓碑永久占据 slot 表

`Compact()` 只回收**记录区空洞**，对 slot 表的 `4 × SlotCount` 字节
**完全无能为力**（`FreeStart` 在整理前后一模一样）。

于是得到一个很反直觉的结果：**一张"数据已全部删光"的页，容量反而比全新页小。**

实测（36 字节短记录，单页上限 101 条）：

```
【场景 A】插满 101 条 → 全删
  全删后       FreeSpace = 24     (未整理)
  Compact() 后 FreeSpace = 3660   ← 记录区 3636 字节回来了
  Slot 表占用  = 404 字节          ← 101 × 4，Compact 一点都回收不了

  插 4000B 记录 → ❌ ErrPageFull
  而同尺寸的全新页 FreeSpace = 4064 → ✅ 插入成功
```

**同样"没有数据"，两张页差了 404 字节。** 这才是"空间不足"的真实来源。

#### 代价：空间放大而非功能缺陷

`RecordFile.Append` 换页是正常流程，`ErrPageFull` 也是**预期行为**，不是 bug。
真正的损失是**文件虚胖**：

| | 旧页 | 新页 | 合计 |
|---|---|---|---|
| 分配字节 | 4096 | +4096 | 8192 |
| 实际可用 | 0（3660 用不上） | 4060 | —— |

旧页仍占着 3660 空闲字节却放不下一条 4000 字节记录，只能再开一页。

#### 量级与触发门槛

| 指标 | 数值 |
|---|---|
| 浪费量 | ≈ `SlotCount × 4` 字节 |
| `SlotCount` | ≈ 该页**历史上同时存活记录数的峰值** |
| 上界 | **有上界**，不是无底洞（墓碑被复用后不再新增） |
| 短记录示例 | 36B 记录 → 101 槽 → **404 字节 ≈ 9.9% 页大小** |
| 记录更短时 | 字节/槽比更差，浪费占比可逼近整页 |

> 触发门槛**不高**：只要「记录长度变化大 + 有删除」就会遇到。
> 但它只影响空间利用率，**不影响正确性**——所以是优化项，不是 bug。

#### 两种缓解手段及其边界（场景 C 最刁钻）

| 场景 | 布局 | 尾部截断 | 整页回收 | 结果 |
|---|---|---|---|---|
| **A** | 101 个墓碑，无存活 | ✅ 可截到 0 | ✅ 适用 | 都能救 |
| **B** | 1 存活在头 + 100 墓碑在尾 | ✅ 截掉尾部 100 个 | ❌ 有存活 | 截断能救 |
| **C** | **100 墓碑在头 + 1 存活在尾** | ❌ 尾部是存活，截不动 | ❌ 有存活 | **两者皆失效** |

实测场景 C：

```
100 墓碑在头 + 1 条 36B 存活在尾
  tailTrunc=false: SlotCount=101 FreeSpace=3624 -> 插 3990B ❌ ErrPageFull
  tailTrunc=true : SlotCount=101 FreeSpace=3624 -> 插 3990B ❌ ErrPageFull  ← 截断无效
  对照理想页（同样 1 条 36B 存活、无墓碑）: FreeSpace=4024 -> 插 3990B ✅ 成功
```

**存活记录恰好卡在尾部**时，两种手段都够不着。

> 🚫 **不建议做的"优化"**：把尾部存活记录**搬**进前面的墓碑槽，腾空尾部再截断。
> 那条记录的 RID 会从 `slot[100]` 变成 `slot[0]`，**所有外部引用**
> （阶段 4 索引项、阶段 6 事务状态）**全部失效**。
> 要么维护重定向表，要么迁移时同步改索引——复杂度陡增。
> PostgreSQL 的 line-pointer 重定向就是干这个的，**教学项目明确不做**。

#### 小结

> ❌ 「复用几个 slot 导致空间不足」
>
> ✅ **「未被复用的墓碑永久占据 slot 表空间，使页的有效容量低于全新页；
> 且 `Compact()` 无法回收这部分，最终被迫跨页，造成空间放大」**

本节的**唯一必读结论**：slot 表是独立于记录区的**第二类浪费源**，
`Compact()` 管不着它。**知道即可，先不处理。**

---

## 9. 分步实现路线

按顺序做，**每一步结束都能编译、能跑测试**。不要一口气写完再调。

| 步骤 | 内容 | 本步验收 |
|---|---|---|
| **1** | `const.go`：页常量、页类型、错误值 | `go build ./...` 通过 |
| **2** | 页头读写：`NewPage` / `DecodePage` + 各 getter/setter | 单元测试：新建页写字段 → `Bytes()` → `DecodePage` 往返一致 |
| **3** | slot 目录读写（纯内存操作，不涉及记录） | 能设置 / 读取 slot 项；越界返回 `ErrSlotNotFound` |
| **4** | `InsertRecord` + `GetRecord` | 单页插 3 条、读回内容一致；`FreeSpace()` 按公式递减 |
| **5** | `DeleteRecord` 墓碑 | 删除后 `GetRecord` 返回 `ErrSlotDeleted`；`FreeSpace` **不变** |
| **6** | `Compact` 碎片整理 | 整理后 `FreeEnd` 前移，空闲空间回收；存活记录内容不变、slot 号不变 |
| **7** | `HeapFile`：`ReadPage` / `WritePage` + 偏移换算 | 写第 0、1、5 页，读回一致；文件长度 == 6 * 4096 |
| **8** | `AllocatePage` + 元数据页（magic / version / pageSize） | 打开非本格式文件返回 `ErrBadMagic` |
| **9** | `RecordFile`：`Append` / `Get` / `Delete` / `Scan` | 内存态下 100 条增删查正确 |
| **10** | 持久化：`Sync` / `Close` / 重开 | **1 万条 → Close → Open → 逐条校验通过** |
| **11** | 记录编码 `EncodeFields` / `DecodeFields` | 变长字段往返一致；截断数据返回 error 而非 panic |
| **12** | dump 子命令 + `xxd` 对照 | 人工核对页头 32 字节与字段表一致 |

**每完成一步就 `git commit` 一次**，把「能工作的最小增量」固定下来。

---

## 10. 测试用例清单

放在 `internal/storage/page_test.go`、`heapfile_test.go`、`record_test.go`。

### 10.1 页级测试

| 用例 | 断言要点 |
|---|---|
| `TestNewPageHeader` | 新页 `SlotCount==0`、`FreeStart==32`、`FreeEnd==4096`、`FreeSpace()==4064` |
| `TestPageHeaderRoundTrip` | 写满页头字段 → `Bytes()` → `DecodePage` → 字段全部相等 |
| `TestPageIDMismatch` | `DecodePage` 传入的 id 与页内 `PageID` 不符时返回错误 |
| `TestInsertAndGet` | 连续插 3 条变长记录，逐条 `GetRecord` 内容一致；`FreeStart` 每次 +4 |
| `TestInsertTooBig` | 插入 4061 字节 → `ErrRecordTooBig`，且**页状态未被修改** |
| `TestPageFull` | 持续插入直到 `ErrPageFull`；断言页头不变式仍成立、已存记录仍可读 |
| `TestDeleteTwice` | 第一次删除成功，第二次返回 `ErrSlotDeleted` |
| `TestCompact` | 插 10 条删中间 5 条 → `Compact()` → `FreeSpace` 恢复；存活记录内容与 slot 号不变 |
| `TestIterateOrder` | `Iterate` 按 slot 升序、跳过墓碑 |
| `TestUpdateSameSize` | 等长更新后 slot 号不变、内容已变 |
| `TestUpdateGrowShrink` | 变长更新返回新 slot 号，旧 slot 成为墓碑 |

#### 墓碑与空间回收专项（对应 8.5 节）

| 用例 | 断言要点 |
|---|---|
| `TestTombstoneReuse` | 插入 39 条 → 全删 → `Compact()` → 再插 39 条，**`SlotCount` 仍为 39 不增长**，`FreeSpace` 回到初值 |
| `TestCompactKeepsTombstone` | 整理后墓碑 slot 仍为 `{0,0}`、`SlotCount` 不变、存活记录 slot 号不变 |
| `TestDeletedFlagAfterCompact` | **回归测试**：删一条 → `Compact()` → 断言 `bitsDeleted` **仍置位**，随后插入能复用墓碑而非新增 slot（防 8.3 那个矛盾写法回归） |
| `TestMaxRecordAfterDelete` | **回归测试**：插入 `MaxRecordSize`(4060) 字节记录 → 删除 → `Compact()` → **重插同长记录成功**（防 8.1 的 `need` 记账陷阱回归） |
| `TestCompactNoShrinkSlotTable` | 整理只回收记录区：`FreeStart` 不变、`FreeEnd` 前移，二者差值正确 |
| `TestAllTombstonePageReclaim` | 页内全部墓碑 → 触发整页回收 → 该 PageID 进入空闲列表，可被 `AllocatePage` 复用 |
| `TestTailTruncation`（选做） | 尾部墓碑可截断：`SlotCount` 减少、`FreeStart` 相应回退；**中间墓碑绝不能被移除** |
| `TestSlotTableCapacityLoss`（🟢 低优先级，选做） | 固化 8.6 场景 A：短记录插满 → 全删 → `Compact()` → 插长记录失败（`ErrPageFull`），而同尺寸全新页成功；断言"删空的页容量 < 全新页"这一**已知取舍**，防止将来被误当 bug |
| `TestRepeatedInsertDelete` | 反复「插一条删一条」1000 次：页面不触发整理时 `FreeSpace` 不下降（固化惰性策略的已知行为） |

### 10.2 文件级测试

| 用例 | 断言要点 |
|---|---|
| `TestOffsetMapping` | 第 5 页写标记 → 文件偏移 `5*4096` 处能读到该标记 |
| `TestFileExactlyPages` | 每次 `AllocatePage` 后文件长度是 4096 整数倍 |
| `TestReadBeyondEOF` | 读不存在的页返回 error，不是零页 |
| `TestBadMagic` | 打开一个随机内容的文件 → `ErrBadMagic` |
| `TestFreePageReuse` | `FreePage` 后再 `AllocatePage` 拿到同一个 PageID |
| `TestCloseIdempotent` | `Close()` 两次不 panic；Close 后 `ReadPage` 返回错误 |

### 10.3 持久化测试（**本阶段的灵魂**）

```go
func TestReopenPersist(t *testing.T) {
    const n = 10000
    path := filepath.Join(t.TempDir(), "persist.db")

    // 阶段一：写入
    rf, err := CreateRecordFile(path)
    if err != nil { t.Fatal(err) }
    rids := make([]RID, 0, n)
    for i := 0; i < n; i++ {
        rid, err := rf.Append([]byte(fmt.Sprintf("record-%06d-payload", i)))
        if err != nil { t.Fatalf("append %d: %v", i, err) }
        rids = append(rids, rid)
    }
    if err := rf.Close(); err != nil { t.Fatal(err) } // 必须真正 Close，模拟进程退出

    // 阶段二：重开校验
    rf2, err := OpenRecordFile(path)
    if err != nil { t.Fatal(err) }
    defer rf2.Close()

    for i, rid := range rids {
        got, err := rf2.Get(rid)
        if err != nil { t.Fatalf("get %d (%v): %v", i, rid, err) }
        want := fmt.Sprintf("record-%06d-payload", i)
        if string(got) != want {
            t.Fatalf("record %d mismatch: got %q want %q", i, got, want)
        }
    }
    // 顺便断言页数符合 5.4 节的算术预期
    t.Logf("pages = %d, file bytes = %d", rf2.hf.PageCount(),
        int64(rf2.hf.PageCount())*int64(PageSize))
}
```

配套用例：

| 用例 | 断言要点 |
|---|---|
| `TestReopenAfterDelete` | 删除一半 → Close → Open → 已删的返回 `ErrSlotDeleted`、未删的仍可读 |
| `TestScanAfterReopen` | `Scan` 条数 == 存活条数，且 RID 不重复 |
| `TestLargeValues` | 混合 1 字节与 4000 字节记录各若干，重开后全部一致 |
| `TestEmptyFile` | 建库后立刻 Close → Open，`Scan` 零条、不报错 |

### 10.4 记录编码测试

| 用例 | 断言要点 |
|---|---|
| `TestEncodeDecodeFixed` | 全定长字段往返一致 |
| `TestEncodeDecodeVarLen` | 含空字符串、含 4000 字节长字段，往返一致 |
| `TestDecodeTruncated` | 故意截断长度前缀 / 内容 → error，**不 panic** |
| `TestDecodeExtraBytes` | 尾部多余字节的处理策略明确（报错或忽略，测试固化下来） |

### 10.5 边界与健壮性

| 用例 | 断言要点 |
|---|---|
| `TestManyPages` | 插入足够多数据跨越 50+ 页，全部可读 |
| `TestCaseInsensitivePath` | Windows 下同一路径不同大小写不产生两个文件 |
| `TestNoGoroutineLeak` | （若用了 goroutine）`goleak` 不可用则用 `runtime.NumGoroutine` 粗查 |

---

## 11. 手工验证：dump 工具与 xxd

自动化测试证明「对」，dump 工具让你看见「为什么对」。

### 11.1 加一个 dump 子命令

在 `cmd/easydb/main.go` 里加：

```
> dump <file> <pageID>      # 打印第 pageID 页的解析结果
> dump <file>              # 打印第 0 页（元数据页）
```

输出形如：

```
page 0 @ offset 0x00000000
  type        = 1 (data)
  flags       = 0x01
  slotCount   = 3
  freeStart   = 0x002c (44)
  freeEnd     = 0x0fc4 (4036)
  freeSpace   = 3992 bytes
  nextPageID  = 0xffffffff (none)
  pageID      = 0
  lsn         = 0
  checksum    = 0x00000000
  slots:
    [0] off=0x0fec len=20
    [1] off=0x0fd8 len=20
    [2] off=0x0000 len=0   (deleted)
  records:
    [0] "record-000000-paylo"
    [1] "record-000001-paylo"
```

> 校验一下算术：3 个 slot ⇒ `FreeStart = 32 + 3*4 = 44 = 0x2c` ✓；
> 两条 20 字节记录 ⇒ `FreeEnd = 4096 - 40 = 4036 = 0x0fc4` ✓。

### 11.2 用 xxd 对照原始字节

```bash
# 看第 0 页的前 64 字节（页头 + 前 8 个 slot 位置）
xxd -l 64 -g 1 test.db

# 看第 3 页（偏移 3*4096 = 12288）
xxd -s 12288 -l 64 -g 1 test.db

# 只看记录区开头
xxd -s 12288 -l 32 -g 1 -e test.db   # -e 小端分组显示
```

Windows 若没有 `xxd`（Git Bash 通常自带），可用 dump 命令或 PowerShell：

```powershell
Format-Hex -Path .\test.db -Count 64
```

**练习**：手工把 `xxd` 输出的第 0 页前 32 字节，逐字段对回 5.2 节的字段表。
对不上就说明你的写入或读取有问题——这是最快的定位手段。

---

## 12. 常见坑与避雷

| # | 坑 | 现象 | 对策 |
|---|---|---|---|
| 1 | **字节序不统一** | 自己写自己读没问题，一换工具就全乱 | 全项目只用 `binary.LittleEndian`，写成一个 helper |
| 2 | **用 `unsafe` 直接转结构体** | 对齐 / 可移植性 / `go vet` 报错 | 老老实实 `binary.Read/Write` 或手写 get/set |
| 3 | **短写（short write）** | 大文件偶发数据损坏 | `WriteAt` 返回 `n < len` 时循环补齐；或断言 `n == len` |
| 4 | **文件最后一页不满** | 读最后一页时 `io.ErrUnexpectedEOF` | 分配页时**总是写满 4096 字节**（尾部补零），保证长度是页整数倍 |
| 5 | **`Close` 前进程退出** | 数据丢失 | 阶段 1 用 `WriteAt`（无缓冲）+ 关键路径 `Sync()`；不要在句柄上 `defer Close` |
| 6 | **删除标记搞反** | 删除的记录又读出来了 | 明确哨兵是 `Offset==0 && Length==0`，写测试固化 |
| 6b | **以为墓碑会被自动清理** | slot 数组只增不减，或以为删除能立刻回收空间 | 墓碑**永不删除、只被复用**；回收的是记录区空洞，且需 `Compact()`（见 8.5）。用 `TestTombstoneReuse` 固化 |
| 6c | **从中间移除墓碑来"清理"** | 所有 RID 静默失效，读取到错误记录 | 只允许截断**尾部**连续墓碑，中间的一律保留（红线，见 8.5） |
| 6d | **`Compact()` 里无条件清除 `bitsDeleted`** | 整理后插入误报 `ErrPageFull` | 整理保留墓碑 ⇒ 该位应保持置位；用 `refreshDeletedFlag()` 按 slot 数组重算（见 5.2 / 8.3） |
| 6e | **`need` 忘了复用墓碑省下的 4 字节** | 4060 字节记录删后重插被误拒 | 有墓碑时 `need = len(rec)`，无墓碑时 `need = len(rec) + SlotSize`（见 8.1） |
| 6f | **以为一条记录会占用多个 slot** | 按"长记录吃多个槽"去设计回收逻辑，越算越乱 | **1 条记录 = 恰好 1 个 slot**，`Length` 字段即为变长服务（见 5.3 / 8.6） |
| 6g | **以为 `Compact()` 能回收 slot 表** | 删空的页仍放不下长记录，困惑为什么"没数据却没空间" | `Compact()` 只回收记录区空洞；`4 × SlotCount` 字节回收不了。这是**已知取舍**，见 8.6（🟢 低优先级，不必修） |
| 7 | **整理后 RID 失效** | 外部持有偏移而非 slot 号 | **对外只暴露 `RID{PageID, SlotNo}`**，页内偏移不出页 |
| 8 | **`FreeSpace` 算错** | 明明有空间却报 `ErrPageFull`，或反过来写越界 | 每次操作后断言不变式 `FreeStart <= FreeEnd`，写个 `verifyInvariants()` 在测试里调 |
| 9 | **`uint16` 溢出** | 记录长度或偏移 > 65535 时静默截断 | `MaxRecordSize = 4060` 远小于 65535，但仍要在入口显式校验 |
| 10 | **多文件句柄未关** | Windows 上文件被占用，删不掉 | 测试一律用 `t.TempDir()`，并 `defer Close()` |
| 11 | **用 `offset==0` 当「空记录」** | 误判 | 页头区 0–31 永不存记录，0 只表示墓碑 |
| 12 | **`Scan` 里修改页** | 迭代器失效 | `Scan` 传副本（`GetRecord` 已返回拷贝），回调内不要调 `DeleteRecord` |
| 13 | **页头 `PageID` 不对** | 写错页、覆盖数据 | `WritePage` 前断言 `p.ID() == 目标 id` |
| 14 | **REPL 用 `fmt.Scan`** | 一行多个词只读到第一个 | 阶段 1 不影响；改 REPL 时换成 `bufio.Scanner` 按行读再分词 |

---

## 13. 验收命令

阶段 1 的验收**必须全部通过**才算完成：

```bash
# 1. 编译与静态检查
go build ./...
go vet ./...

# 2. 格式检查
make fmt-check

# 3. 单元测试（详细输出）
go test ./internal/storage/... -v

# 4. 持久化大用例（1 万条）
go test ./internal/storage/... -run TestReopenPersist -v

# 5. 竞态检测（阶段 1 虽单线程，但提前养成习惯）
go test -race ./...

# 6. 手工验证
go run ./cmd/easydb dump ./.build/test.db 0
xxd -l 64 -g 1 ./.build/test.db
```

### 13.1 达成判据

- [ ] 上面 6 条命令**零错误**，测试**全绿**
- [ ] `TestReopenPersist` 打印的页数与第 5.4 节算术推算**吻合**
- [ ] dump 输出与 `xxd` 原始字节**逐字段对得上**
- [ ] 在 `README.md` 第 3 节补上本阶段的**实测结果**（页数、文件大小、记录数）

---

## 14. 学习资源（B 站优先）

> 先看 B 站，B 站直接讲「从零写存储层」的成体系课程不多，
> 因此搭配下列文章/官方文档补齐实现细节。

### 14.1 B 站 — 最贴合本阶段

| 资源 | 链接 | 对应本阶段的哪部分 |
|---|---|---|
| **自己动手编写数据库系统 [2025新版]** | <https://www.bilibili.com/video/BV1sJETztEjS/> | **首推**。25 讲，含「1. 存储管理：页管理、元数据、缓冲区管理」——直接对应第 4、5 节 |
| **从零编写一个数据库系统 DB** | <https://www.bilibili.com/video/BV192BsYwEYr/> | 同主题另一合集（25 讲），含「6.5 存储管理：页管理、元数据」；与上一条互补、交叉验证 |
| **数据库系统实现串讲** | <https://www.bilibili.com/video/BV1ce4y1R7Mh/> | 15 讲，**D1S2「辅助存储管理与索引结构」**——磁盘页与存储布局的理论框架 |
| **9 分钟带你了解 SQLite 底层原理** | <https://www.bilibili.com/video/BV1ymKt6iEKG/> | 快速建立「单文件多页数据库」的整体直觉，正是本项目的形态 |
| **了解 SQLite 底层原理** | <https://www.bilibili.com/video/BV1ssgy6cEDc/> | 同上，稍长版本，可当背景音反复听 |

### 14.2 B 站 — 对照工业实现（理解「为什么这样设计」）

| 资源 | 链接 | 用途 |
|---|---|---|
| **MySQL 核心技术原理之 InnoDB 缓冲池底层解析** | <https://www.bilibili.com/video/BV19z4y1C7iN/> | 页与页缓存的真实工程形态，为阶段 2 预热 |
| **MySQL 数据库全套教程（2025 版）** | <https://www.bilibili.com/video/BV19A3EzpECj/> | 第 1、2 讲「索引的本质」「B+Tree 结构」，理解页在索引中的角色（阶段 4 铺垫） |
| **【中文配音】C + Linux 进阶项目，手写 DB 数据库** | <https://www.bilibili.com/video/BV1F15r69EiV/> | 7 讲，C 语言从零写内存 DB。语言不同，但**数据结构组织与工程分层思路**可借鉴 |

### 14.3 其他平台 — 补齐实现细节（B 站不足时）

| 资源 | 链接 | 用途 |
|---|---|---|
| **手把手教你从零开始实现一个数据库系统**（CSDN） | <https://blog.csdn.net/m2l0zgssvc7r69efdtj/article/details/105001892> | 基于 SQLite 的单文件数据库逐行实现，**最贴近本项目形态** |
| **自己动手写数据库：缓存管理的设计**（CSDN，tyler_download 系列） | <https://blog.csdn.net/tyler_download/article/details/124143120> | 页缓存 / 缓冲池的完整设计与实现细节，阶段 1 末 + 阶段 2 都能用 |
| **SQLite 架构概览** | <https://www.sqlite.org/arch.html> | 官方架构图，理解「单文件 + 分页 + B 树」的整体拼装 |
| **SQLite 数据库文件格式** | <https://www.sqlite.org/fileformat2.html> | **页头字段、cell 指针数组、freeblock、freelist 的权威定义**——对照自己设计的字段表 |
| **CMU 15-445 Database Systems** | <https://15445.courses.cs.cmu.edu/> | 存储层讲义与 Project，系统化补齐理论 |

### 14.4 建议的观看顺序

1. 「自己动手编写数据库系统」**存储管理那 1–2 讲** → 建立心智模型
2. 「9 分钟带你了解 SQLite 底层原理」→ 看清成品长什么样
3. 动手实现第 9 节的步骤 1–6
4. 卡住时看 SQLite `fileformat2.html` 的页头与 cell 部分 → 对照自己的字段表
5. 实现步骤 7–12 时，配合 CSDN「手把手」那篇对照 slot 与变长编码
6. 收尾时看「数据库系统实现串讲」D1S2 → 把零散知识连成体系

---

## 15. 完成检查清单

复制到你的 `README.md` 或 issue 里逐项打勾：

**功能**

- [ ] 页头 32 字节字段全部按表实现，dump 可读
- [ ] slot 目录支持新增 / 复用墓碑 / 越界报错
- [ ] `InsertRecord` / `GetRecord` / `DeleteRecord` / `Scan` 全部可用
- [ ] `Compact` 碎片整理正确，且整理后 `RID` 不变
- [ ] 墓碑被正确复用（`TestTombstoneReuse`：删光重插后 `SlotCount` 不增长）
- [ ] `bitsDeleted` 标志在 `Compact()` 后仍正确置位（`TestDeletedFlagAfterCompact`）
- [ ] `MaxRecordSize` 记录删后重插成功（`TestMaxRecordAfterDelete`）

**🟢 低优先级（有余力再做，不做不影响验收）**

- [ ] 理解「1 条记录 = 1 个 slot」与 slot 表的容量损失（8.6，**只需理解，无需写代码**）
- [ ] （选做）尾部墓碑截断
- [ ] （选做）`TestSlotTableCapacityLoss` 固化已知取舍
- [ ] `HeapFile` 页级读写 + 追加页 + 空闲页复用
- [ ] 元数据页 magic/version 校验生效
- [ ] 变长记录长度前缀编码往返正确

**质量**

- [ ] `go build ./...`、`go vet ./...`、`make fmt-check` 零错误
- [ ] `go test ./...` 与 `go test -race ./...` 全绿
- [ ] 第 10 节所有测试用例均已实现
- [ ] 大记录 / 满页 / 截断数据 / 非法文件均可控报错，无 panic

**验证**

- [ ] 1 万条写入 → Close → 重开 → 逐条校验通过
- [ ] 删除状态在重开后依然生效
- [ ] dump 输出与 `xxd` 原始字节逐字段对得上
- [ ] README 已补上本阶段实测数据（页数 / 文件大小 / 耗时）

**衔接**

- [ ] 页头已预留 `LSN`、`Checksum` 字段（阶段 7 用）
- [ ] 所有页访问都走 `HeapFile`，没有绕过它直接 `os.File` 的地方（阶段 2 才能整体替换为缓冲池）
- [ ] `RID` 已在对外 API 中定型，页内偏移没有泄漏到 storage 包之外

---

> 下一步：完成本阶段后进入 `README.md` 的「阶段 2：缓冲池与页缓存」，
> 把「一切页访问经缓冲池」这条约束落地。

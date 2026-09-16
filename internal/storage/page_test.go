package storage

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

// =============================================================================
// 测试辅助
// =============================================================================

// guard 隔离被测实现的 panic。
//
// Go 中未捕获的 panic 会终止整个测试二进制，导致其后的用例全部无法执行，
// 只能看到第一个问题。在每个用例开头 defer guard(t) 可把 panic 降级为
// 该用例的一次失败，从而让全套用例跑完并各自报告结果。
// 对应 README 验收标准：「边界用例——报错清晰、进程不崩」。
func guard(t *testing.T) {
	t.Helper()
	if r := recover(); r != nil {
		t.Errorf("被测代码发生 panic（按验收标准应返回 error 而非崩溃）: %v", r)
	}
}

func newTestPage() *Page {
	return NewPage(0, PageTypeData)
}

// mustInsert 插入成功则返回 slot 号，失败直接终止该用例。
func mustInsert(t *testing.T, p *Page, rec []byte) uint16 {
	t.Helper()
	slotNo, err := p.InsertRecord(rec)
	if err != nil {
		t.Fatalf("InsertRecord(%d bytes) 意外失败: %v", len(rec), err)
	}
	return slotNo
}

// mustGet 读取成功则返回记录副本，失败直接终止该用例。
func mustGet(t *testing.T, p *Page, slotNo uint16) []byte {
	t.Helper()
	rec, err := p.GetRecord(slotNo)
	if err != nil {
		t.Fatalf("GetRecord(%d) 意外失败: %v", slotNo, err)
	}
	return rec
}

// mustNotPanic 在局部包裹可能 panic 的调用，转为测试失败后仍继续后续断言。
func mustNotPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s 发生 panic（应返回 error 或不崩）: %v", what, r)
		}
	}()
	fn()
}

// decodedHeader 独立按小端从 Bytes() 解出页头，用于交叉验证序列化结果。
func decodedHeader(t *testing.T, p *Page) PageHeader {
	t.Helper()
	b := p.Bytes()
	if len(b) != PageSize {
		t.Fatalf("Bytes() 长度 = %d, 期望 %d", len(b), PageSize)
	}
	return PageHeader{
		PageType:   PageType(b[0]),
		Flags:      b[1],
		SlotCount:  binary.LittleEndian.Uint16(b[2:4]),
		FreeEnd:    binary.LittleEndian.Uint16(b[4:6]),
		FressSpace: binary.LittleEndian.Uint16(b[6:8]),
		NextPageId: PageID(binary.LittleEndian.Uint32(b[8:12])),
		PageId:     PageID(binary.LittleEndian.Uint32(b[12:16])),
		LSN:        binary.LittleEndian.Uint64(b[16:24]),
		CheckSum:   binary.LittleEndian.Uint32(b[24:28]),
		Reserved:   binary.LittleEndian.Uint32(b[28:32]),
	}
}

// trueFreeSpace 依「页头 + slot 表 + 记录区连续」模型算出的空闲字节数。
// 不含页内碎片（hole），仅代表 FreeEnd 之上的连续空闲区。
func trueFreeSpace(p *Page) int {
	return int(p.FressSpace)
}

// recomputedFreeSpace 独立重算页的真实剩余容量（不读页头字段）：
//
//	真实剩余 = 页大小 - 页头 - slot 表 - 存活记录总字节
//
// FressSpace 语义即为「当前页剩余空间（含墓碑回收的碎片）」，
// 所以两者必须相等；不等即说明页头字段与页内容脱节。
func recomputedFreeSpace(p *Page) int {
	live := 0
	for i := uint16(0); i < p.SlotCount; i++ {
		s := p.getSlot(i)
		if !s.isDeleted() {
			live += int(s.Length)
		}
	}
	return PageSize - PageHeaderSize - int(p.SlotCount)*SlotSize - live
}

// assertFreeSpaceAccurate 断言页头 FressSpace 与独立重算值一致。
func assertFreeSpaceAccurate(t *testing.T, p *Page, ctx string) {
	t.Helper()
	if got, want := int(p.FressSpace), recomputedFreeSpace(p); got != want {
		t.Errorf("%s: FressSpace=%d 与真实剩余空间 %d 不一致（偏差 %d 字节）",
			ctx, got, want, want-got)
	}
}

// assertFieldsRoundTrip 校验页头各字段经 Bytes()/DecodePage 后完全还原。
func assertFieldsRoundTrip(t *testing.T, want, got PageHeader, ctx string) {
	t.Helper()
	if got.PageType != want.PageType {
		t.Errorf("%s: PageType = %d, 期望 %d", ctx, got.PageType, want.PageType)
	}
	if got.Flags != want.Flags {
		t.Errorf("%s: Flags = %#x, 期望 %#x", ctx, got.Flags, want.Flags)
	}
	if got.SlotCount != want.SlotCount {
		t.Errorf("%s: SlotCount = %d, 期望 %d", ctx, got.SlotCount, want.SlotCount)
	}
	if got.FreeEnd != want.FreeEnd {
		t.Errorf("%s: FreeEnd = %d, 期望 %d", ctx, got.FreeEnd, want.FreeEnd)
	}
	if got.FressSpace != want.FressSpace {
		t.Errorf("%s: FressSpace = %d, 期望 %d", ctx, got.FressSpace, want.FressSpace)
	}
	if got.NextPageId != want.NextPageId {
		t.Errorf("%s: NextPageId = %d, 期望 %d", ctx, got.NextPageId, want.NextPageId)
	}
	if got.PageId != want.PageId {
		t.Errorf("%s: PageId = %d, 期望 %d", ctx, got.PageId, want.PageId)
	}
	if got.LSN != want.LSN {
		t.Errorf("%s: LSN = %#x, 期望 %#x", ctx, got.LSN, want.LSN)
	}
	if got.CheckSum != want.CheckSum {
		t.Errorf("%s: CheckSum = %#x, 期望 %#x", ctx, got.CheckSum, want.CheckSum)
	}
	if got.Reserved != want.Reserved {
		t.Errorf("%s: Reserved = %#x, 期望 %#x", ctx, got.Reserved, want.Reserved)
	}
}

// liveSlotNos 返回所有存活（非墓碑）slot 号，升序。
func liveSlotNos(p *Page) []uint16 {
	var out []uint16
	p.Iterate(func(slotNo uint16, _ []byte) bool {
		out = append(out, slotNo)
		return true
	})
	return out
}

// assertInvariants 校验页结构的基础不变式。
func assertInvariants(t *testing.T, p *Page, ctx string) {
	t.Helper()
	if got := PageHeaderSize + int(p.SlotCount)*SlotSize; got > int(p.FreeEnd) {
		t.Errorf("%s: slot 表越过 FreeEnd（slotTableEnd=%d > FreeEnd=%d）", ctx, got, p.FreeEnd)
	}
	if p.FreeEnd > PageSize {
		t.Errorf("%s: FreeEnd=%d 超出页大小", ctx, p.FreeEnd)
	}
	for i := uint16(0); i < p.SlotCount; i++ {
		s := p.getSlot(i)
		if s.isDeleted() {
			continue
		}
		if int(s.Offset) < PageHeaderSize+int(p.SlotCount)*SlotSize {
			t.Errorf("%s: slot %d 记录偏移 %d 落在页头/slot 表区域", ctx, i, s.Offset)
		}
		if int(s.end()) > PageSize {
			t.Errorf("%s: slot %d 记录末端 %d 超出页大小", ctx, i, s.end())
		}
	}
}

// =============================================================================
// InsertRecord
// =============================================================================

func TestInsertRecord_SequentialSlots(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	recs := [][]byte{[]byte("aaaa"), []byte("bbbbbb"), []byte("cc")}

	for i, rec := range recs {
		slotNo, err := p.InsertRecord(rec)
		if err != nil {
			t.Fatalf("第 %d 次插入失败: %v", i, err)
		}
		if slotNo != uint16(i) {
			t.Errorf("第 %d 次插入返回 slot=%d, 期望 %d", i, slotNo, i)
		}
	}

	if p.SlotCount != uint16(len(recs)) {
		t.Errorf("SlotCount = %d, 期望 %d", p.SlotCount, len(recs))
	}
	for i, want := range recs {
		if got := mustGet(t, p, uint16(i)); !bytes.Equal(got, want) {
			t.Errorf("GetRecord(%d) = %q, 期望 %q", i, got, want)
		}
	}
	assertInvariants(t, p, "顺序插入后")
}

func TestInsertRecord_EmptyRecordAllowed(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	slotNo, err := p.InsertRecord([]byte{})
	if err != nil {
		t.Fatalf("插入空记录应成功, 得到: %v", err)
	}
	got, err := p.GetRecord(slotNo)
	if err != nil {
		t.Fatalf("读取空记录失败: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空记录读回长度 = %d, 期望 0", len(got))
	}
}

func TestInsertRecord_ReusesLowestTombstone(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	mustInsert(t, p, []byte("aaaa"))
	mustInsert(t, p, []byte("bbbb"))
	mustInsert(t, p, []byte("cccc"))

	if err := p.DeleteRecord(0); err != nil {
		t.Fatalf("DeleteRecord(0) 失败: %v", err)
	}
	if err := p.DeleteRecord(2); err != nil {
		t.Fatalf("DeleteRecord(2) 失败: %v", err)
	}

	slotCountBefore := p.SlotCount
	slotNo, err := p.InsertRecord([]byte("ZZ"))
	if err != nil {
		t.Fatalf("复用墓碑插入失败: %v", err)
	}
	if slotNo != 0 {
		t.Errorf("应复用编号最小的墓碑 slot 0, 实际得到 %d", slotNo)
	}
	if p.SlotCount != slotCountBefore {
		t.Errorf("复用墓碑不应增长 SlotCount: %d -> %d", slotCountBefore, p.SlotCount)
	}
	if got := mustGet(t, p, slotNo); string(got) != "ZZ" {
		t.Errorf("复用 slot 内容 = %q, 期望 \"ZZ\"", got)
	}
	if _, err := p.GetRecord(1); err != nil {
		t.Errorf("slot 1 应仍可读, 得到: %v", err)
	}
	assertInvariants(t, p, "复用墓碑后")
}

func TestInsertRecord_MaxSizeSucceeds(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	rec := bytes.Repeat([]byte{0xAB}, MaxRecordSize)
	if _, err := p.InsertRecord(rec); err != nil {
		t.Fatalf("插入 MaxRecordSize(%d) 字节应成功, 得到: %v", MaxRecordSize, err)
	}
	if got := mustGet(t, p, 0); !bytes.Equal(got, rec) {
		t.Errorf("MaxRecordSize 记录内容往返不一致")
	}
}

// 超过单页上限的记录必须返回 ErrRecordTooBig，且不得留下副作用。
func TestInsertRecord_TooBigReturnsErrRecordTooBig(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	_, err := p.InsertRecord(bytes.Repeat([]byte{1}, MaxRecordSize+1))
	if err != ErrRecordTooBig {
		t.Errorf("插入 %d 字节应返回 ErrRecordTooBig, 实际: %v", MaxRecordSize+1, err)
	}
	if p.SlotCount != 0 {
		t.Errorf("插入失败后不应留下 slot, SlotCount = %d", p.SlotCount)
	}
}

// 长度超过 uint16 的记录若被静默截断，会写入错误数据而不报错。
func TestInsertRecord_HugeRecordNotSilentlyTruncated(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	_, err := p.InsertRecord(make([]byte, 70000)) // uint16 截断后为 4464
	if err != ErrRecordTooBig {
		t.Errorf("70000 字节记录应返回 ErrRecordTooBig（不得被 uint16 截断）, 实际: %v", err)
	}
}

func TestInsertRecord_CapacityFor100ByteRecords(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	rec := make([]byte, 100)

	n := 0
	stopped := false
	mustNotPanic(t, "插入至页满", func() {
		for {
			if _, err := p.InsertRecord(rec); err != nil {
				if err != ErrPageFull {
					t.Errorf("页满时应返回 ErrPageFull, 实际: %v", err)
				}
				stopped = true
				break
			}
			n++
			if n > 200 {
				t.Errorf("插入未收敛（n=%d），可能陷入死循环", n)
				break
			}
		}
	})

	// 设计容量：n*(100+4) <= 4096-32 = 4064  =>  n = 39
	const wantCapacity = 39
	if !stopped {
		t.Errorf("页满路径未触发（已插入 %d 条仍未停下）", n)
	}
	if n != wantCapacity {
		t.Errorf("100 字节记录单页容量 = %d, 期望 %d", n, wantCapacity)
	}
	assertInvariants(t, p, "填满后")

	for i := 0; i < n; i++ {
		if _, err := p.GetRecord(uint16(i)); err != nil {
			t.Errorf("填满后 GetRecord(%d) 失败: %v", i, err)
			break
		}
	}
}

func TestInsertRecord_PageFullDoesNotCorruptExisting(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	const existing = 5
	for i := 0; i < existing; i++ {
		mustInsert(t, p, bytes.Repeat([]byte{byte('A' + i)}, 500))
	}

	mustNotPanic(t, "空间不足时插入", func() {
		if _, err := p.InsertRecord(bytes.Repeat([]byte{'Z'}, 2000)); err == nil {
			t.Errorf("空间不足时应返回错误")
		}
	})

	for i := 0; i < existing; i++ {
		want := bytes.Repeat([]byte{byte('A' + i)}, 500)
		if got := mustGet(t, p, uint16(i)); !bytes.Equal(got, want) {
			t.Errorf("插入失败后 slot %d 内容被破坏", i)
		}
	}
	assertInvariants(t, p, "插入失败后")
}

// =============================================================================
// GetRecord
// =============================================================================

func TestGetRecord_RoundTripVariousLengths(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	payloads := [][]byte{
		{},
		[]byte("x"),
		bytes.Repeat([]byte{0x00}, 17),
		bytes.Repeat([]byte{0xFF}, 256),
		[]byte("边界-中文-字符串"),
		bytes.Repeat([]byte{0x5A}, 1000),
	}
	for i, want := range payloads {
		slotNo := mustInsert(t, p, want)
		got := mustGet(t, p, slotNo)
		if !bytes.Equal(got, want) {
			t.Errorf("第 %d 条记录往返不一致: 长度 got=%d want=%d", i, len(got), len(want))
		}
	}
}

func TestGetRecord_ReturnsIndependentCopy(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	slotNo := mustInsert(t, p, []byte("original"))

	got := mustGet(t, p, slotNo)
	for i := range got {
		got[i] = 'X'
	}

	again := mustGet(t, p, slotNo)
	if string(again) != "original" {
		t.Errorf("GetRecord 返回的切片被修改后影响了页内数据: %q", again)
	}
}

func TestGetRecord_OutOfRange(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	mustInsert(t, p, []byte("aaaa"))
	mustInsert(t, p, []byte("bbbb"))
	// SlotCount == 2，合法 slot 号为 0、1
	for _, slotNo := range []uint16{p.SlotCount, p.SlotCount + 1, 100, 65535} {
		_, err := p.GetRecord(slotNo)
		if err != ErrSlotNotFound {
			t.Errorf("GetRecord(%d) 应返回 ErrSlotNotFound, 实际: %v", slotNo, err)
		}
	}
}

// 删除后的 slot 语义分两种（取决于它当时是否为尾部 slot）：
//   - 非尾部：保留墓碑，读取返回 ErrSlotDeleted
//   - 尾部  ：被即时截断以归还空间，该 RID 变为越界 ErrSlotNotFound
//
// 两种都必须报错（绝不能静默返回数据），但错误值不同。
func TestGetRecord_DeletedSlot(t *testing.T) {
	defer guard(t)

	t.Run("中间墓碑_ErrSlotDeleted", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		for i := 0; i < 3; i++ {
			mustInsert(t, p, []byte("aaaa"))
		}
		// slot 1 非尾部（slot 2 仍存活），墓碑保留
		if err := p.DeleteRecord(1); err != nil {
			t.Fatalf("DeleteRecord(1) 失败: %v", err)
		}
		if _, err := p.GetRecord(1); err != ErrSlotDeleted {
			t.Errorf("读取中间墓碑应返回 ErrSlotDeleted, 实际: %v", err)
		}
	})

	t.Run("尾部截断_ErrSlotNotFound", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		slotNo := mustInsert(t, p, []byte("aaaa"))
		if err := p.DeleteRecord(slotNo); err != nil {
			t.Fatalf("DeleteRecord 失败: %v", err)
		}
		if _, err := p.GetRecord(slotNo); err != ErrSlotNotFound {
			t.Errorf("读取已截断的尾部 slot 应返回 ErrSlotNotFound, 实际: %v", err)
		}
	})
}

// =============================================================================
// DeleteRecord
// =============================================================================

func TestDeleteRecord_MarksTombstoneWithoutMovingOthers(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	slots := make([]uint16, 3)
	for i := range slots {
		slots[i] = mustInsert(t, p, fmt.Appendf(nil, "rec-%d", i))
	}
	freeEndBefore := p.FreeEnd
	slotCountBefore := p.SlotCount

	if err := p.DeleteRecord(1); err != nil {
		t.Fatalf("DeleteRecord(1) 失败: %v", err)
	}

	if p.SlotCount != slotCountBefore {
		t.Errorf("删除不应改变 SlotCount: %d -> %d", slotCountBefore, p.SlotCount)
	}
	if p.FreeEnd != freeEndBefore {
		t.Errorf("删除不应移动记录区边界: FreeEnd %d -> %d", freeEndBefore, p.FreeEnd)
	}
	if s := p.getSlot(1); !s.isDeleted() {
		t.Errorf("slot 1 未被标记为墓碑: %+v", *s)
	}
	if got := mustGet(t, p, 0); string(got) != "rec-0" {
		t.Errorf("slot 0 被影响: %q", got)
	}
	if got := mustGet(t, p, 2); string(got) != "rec-2" {
		t.Errorf("slot 2 被影响: %q", got)
	}
}

// 重复删除的错误值同样取决于 slot 是否已被截断。
func TestDeleteRecord_TwiceReturnsErrSlotDeleted(t *testing.T) {
	defer guard(t)

	t.Run("中间墓碑_ErrSlotDeleted", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		mustInsert(t, p, []byte("aaaa"))
		mustInsert(t, p, []byte("bbbb")) // 保证 slot 0 非尾部
		if err := p.DeleteRecord(0); err != nil {
			t.Fatalf("首次删除失败: %v", err)
		}
		if err := p.DeleteRecord(0); err != ErrSlotDeleted {
			t.Errorf("重复删除中间墓碑应返回 ErrSlotDeleted, 实际: %v", err)
		}
	})

	t.Run("尾部截断_ErrSlotNotFound", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		slotNo := mustInsert(t, p, []byte("aaaa"))
		if err := p.DeleteRecord(slotNo); err != nil {
			t.Fatalf("首次删除失败: %v", err)
		}
		// 尾部 slot 已被即时截断，该编号不再存在
		if err := p.DeleteRecord(slotNo); err != ErrSlotNotFound {
			t.Errorf("重复删除已截断的尾部 slot 应返回 ErrSlotNotFound, 实际: %v", err)
		}
	})
}

func TestDeleteRecord_OutOfRange(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	mustInsert(t, p, []byte("aaaa"))
	for _, slotNo := range []uint16{p.SlotCount, p.SlotCount + 1, 9999} {
		if err := p.DeleteRecord(slotNo); err != ErrSlotNotFound {
			t.Errorf("DeleteRecord(%d) 应返回 ErrSlotNotFound, 实际: %v", slotNo, err)
		}
	}
}

// =============================================================================
// UpdateRecord
// =============================================================================

func TestUpdateRecord_SameLengthInPlace(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	slotNo := mustInsert(t, p, []byte("AAAAAAAAAA"))
	freeEndBefore := p.FreeEnd

	newSlot, err := p.UpdateRecord(slotNo, []byte("BBBBBBBBBB"))
	if err != nil {
		t.Fatalf("等长更新失败: %v", err)
	}
	if newSlot != slotNo {
		t.Errorf("等长更新不应改变 slot 号: %d -> %d", slotNo, newSlot)
	}
	if got := mustGet(t, p, slotNo); string(got) != "BBBBBBBBBB" {
		t.Errorf("等长更新后内容 = %q", got)
	}
	if p.FreeEnd != freeEndBefore {
		t.Errorf("等长更新不应改变 FreeEnd: %d -> %d", freeEndBefore, p.FreeEnd)
	}
}

func TestUpdateRecord_ShrinkKeepsSlotAndReclaimsSpace(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	slotNo := mustInsert(t, p, bytes.Repeat([]byte{'A'}, 200))
	mustInsert(t, p, bytes.Repeat([]byte{'B'}, 200))
	freeBefore := trueFreeSpace(p)

	newSlot, err := p.UpdateRecord(slotNo, bytes.Repeat([]byte{'C'}, 50))
	if err != nil {
		t.Fatalf("变短更新失败: %v", err)
	}
	if newSlot != slotNo {
		t.Errorf("变短更新应保持 slot 号不变: %d -> %d", slotNo, newSlot)
	}
	if got := mustGet(t, p, slotNo); len(got) != 50 || got[0] != 'C' {
		t.Errorf("变短更新后内容异常: len=%d", len(got))
	}
	if s := p.getSlot(slotNo); s.Length != 50 {
		t.Errorf("slot.Length = %d, 期望 50", s.Length)
	}
	if freeAfter := trueFreeSpace(p); freeAfter <= freeBefore {
		t.Errorf("变短更新应归还空间: 空闲 %d -> %d", freeBefore, freeAfter)
	}
	if got := mustGet(t, p, 1); len(got) != 200 || got[0] != 'B' {
		t.Errorf("邻居记录被影响: len=%d", len(got))
	}
}

func TestUpdateRecord_GrowKeepsSlotNumber(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	// 保留一条后续记录，避免被更新的 slot 处于尾部（尾部 slot 会被删除并截断）
	slotNo := mustInsert(t, p, bytes.Repeat([]byte{'A'}, 20))
	mustInsert(t, p, bytes.Repeat([]byte{'T'}, 20)) // 尾部哨兵

	newSlot, err := p.UpdateRecord(slotNo, bytes.Repeat([]byte{'B'}, 300))
	if err != nil {
		t.Fatalf("变长更新失败: %v", err)
	}
	if newSlot != slotNo {
		t.Errorf("变长更新应保持 RID 稳定（slot 号不变）: %d -> %d", slotNo, newSlot)
	}
	if got := mustGet(t, p, newSlot); len(got) != 300 || got[0] != 'B' {
		t.Errorf("变长更新后内容异常: len=%d", len(got))
	}
}

// RID 稳定性不得依赖「页内没有编号更小的墓碑」这种偶然条件：
// 否则调用方拿到的 slot 号不可预测，索引项 / 事务状态会指向错误记录。
func TestUpdateRecord_GrowWithEarlierTombstoneKeepsSlotNumber(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	mustInsert(t, p, bytes.Repeat([]byte{'A'}, 20))           // slot 0
	mustInsert(t, p, bytes.Repeat([]byte{'B'}, 20))           // slot 1
	slotNo := mustInsert(t, p, bytes.Repeat([]byte{'C'}, 20)) // slot 2
	mustInsert(t, p, bytes.Repeat([]byte{'T'}, 20))           // slot 3：尾部哨兵

	if err := p.DeleteRecord(0); err != nil { // 制造编号更小的墓碑
		t.Fatalf("DeleteRecord(0) 失败: %v", err)
	}

	newSlot, err := p.UpdateRecord(slotNo, bytes.Repeat([]byte{'D'}, 200))
	if err != nil {
		t.Fatalf("变长更新失败: %v", err)
	}
	if newSlot != slotNo {
		t.Errorf("存在更小墓碑时 RID 发生漂移: slot %d -> %d（调用方无法安全使用该 RID）", slotNo, newSlot)
	}
	if got := mustGet(t, p, newSlot); len(got) != 200 || got[0] != 'D' {
		t.Errorf("更新后内容异常: len=%d", len(got))
	}
}

func TestUpdateRecord_OutOfRange(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	mustInsert(t, p, []byte("aaaa"))
	for _, slotNo := range []uint16{1, 5, 65535} {
		if _, err := p.UpdateRecord(slotNo, []byte("xy")); err != ErrSlotNotFound {
			t.Errorf("UpdateRecord(%d) 应返回 ErrSlotNotFound, 实际: %v", slotNo, err)
		}
	}
	if p.SlotCount != 1 {
		t.Errorf("越界更新不应创建新 slot, SlotCount = %d", p.SlotCount)
	}
}

// 同上：更新已删除 slot 的错误值取决于该 slot 是否仍作为墓碑存在。
func TestUpdateRecord_DeletedSlot(t *testing.T) {
	defer guard(t)

	t.Run("中间墓碑_ErrSlotDeleted", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		mustInsert(t, p, []byte("aaaa"))
		mustInsert(t, p, []byte("bbbb")) // 保证 slot 0 非尾部
		if err := p.DeleteRecord(0); err != nil {
			t.Fatalf("DeleteRecord 失败: %v", err)
		}
		if _, err := p.UpdateRecord(0, []byte("cccc")); err != ErrSlotDeleted {
			t.Errorf("更新中间墓碑应返回 ErrSlotDeleted, 实际: %v", err)
		}
	})

	t.Run("尾部截断_ErrSlotNotFound", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		slotNo := mustInsert(t, p, []byte("aaaa"))
		if err := p.DeleteRecord(slotNo); err != nil {
			t.Fatalf("DeleteRecord 失败: %v", err)
		}
		if _, err := p.UpdateRecord(slotNo, []byte("bbbb")); err != ErrSlotNotFound {
			t.Errorf("更新已截断的尾部 slot 应返回 ErrSlotNotFound, 实际: %v", err)
		}
	})
}

func TestUpdateRecord_TooBig(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	slotNo := mustInsert(t, p, []byte("aaaa"))
	_, err := p.UpdateRecord(slotNo, make([]byte, MaxRecordSize+1))
	if err != ErrRecordTooBig {
		t.Errorf("更新为超大记录应返回 ErrRecordTooBig, 实际: %v", err)
	}
}

// 更新因空间不足失败时，原记录必须保持原样（不得先删后插导致数据丢失）。
func TestUpdateRecord_NoSpaceLeavesOriginalIntact(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	original := bytes.Repeat([]byte{'O'}, 100)
	slotNo := mustInsert(t, p, original)
	for i := 0; i < 200; i++ {
		if _, err := p.InsertRecord(bytes.Repeat([]byte{'F'}, 30)); err != nil {
			break
		}
	}

	var newSlot uint16
	var err error
	mustNotPanic(t, "空间不足时的变长更新", func() {
		newSlot, err = p.UpdateRecord(slotNo, bytes.Repeat([]byte{'N'}, 4000))
	})
	if err == nil {
		t.Fatalf("空间不足时应返回错误, 却成功返回 slot=%d", newSlot)
	}
	if got, gerr := p.GetRecord(slotNo); gerr == nil {
		if !bytes.Equal(got, original) {
			t.Errorf("更新失败后原记录被破坏: got %d 字节, want %d 字节", len(got), len(original))
		}
	}
	assertInvariants(t, p, "变长更新失败后")
}

func TestUpdateRecord_DoesNotAffectSiblings(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	slots := make([]uint16, 5)
	for i := range slots {
		slots[i] = mustInsert(t, p, bytes.Repeat([]byte{byte('A' + i)}, 40))
	}

	if _, err := p.UpdateRecord(slots[2], bytes.Repeat([]byte{'Z'}, 400)); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	for i, s := range slots {
		if i == 2 {
			continue
		}
		want := bytes.Repeat([]byte{byte('A' + i)}, 40)
		got, err := p.GetRecord(s)
		if err != nil {
			t.Errorf("slot %d 更新后不可读: %v", s, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("slot %d 内容被更新操作破坏", s)
		}
	}
}

// =============================================================================
// Compact
// =============================================================================

func TestCompact_NoDeletesIsNoop(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := 0; i < 5; i++ {
		mustInsert(t, p, bytes.Repeat([]byte{byte('A' + i)}, 20))
	}
	freeEndBefore := p.FreeEnd
	slotCountBefore := p.SlotCount

	mustNotPanic(t, "无删除时 Compact", func() { p.Compact() })

	if p.FreeEnd != freeEndBefore {
		t.Errorf("无删除时 Compact 不应改变 FreeEnd: %d -> %d", freeEndBefore, p.FreeEnd)
	}
	if p.SlotCount != slotCountBefore {
		t.Errorf("无删除时 Compact 不应改变 SlotCount: %d -> %d", slotCountBefore, p.SlotCount)
	}
	for i := 0; i < 5; i++ {
		want := bytes.Repeat([]byte{byte('A' + i)}, 20)
		if got := mustGet(t, p, uint16(i)); !bytes.Equal(got, want) {
			t.Errorf("Compact 后 slot %d 内容变化", i)
		}
	}
}

func TestCompact_EmptyPage(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	mustNotPanic(t, "空页 Compact", func() { p.Compact() })
	if p.SlotCount != 0 {
		t.Errorf("空页 Compact 后 SlotCount = %d, 期望 0", p.SlotCount)
	}
	if p.FreeEnd != PageSize {
		t.Errorf("空页 Compact 后 FreeEnd = %d, 期望 %d", p.FreeEnd, PageSize)
	}
}

// 契约：Compact 后 slot 编号保持不变，存活记录仍可按原 RID 读到原内容。
func TestCompact_InteriorDeletePreservesSurvivors(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	const n = 10
	for i := 0; i < n; i++ {
		mustInsert(t, p, fmt.Appendf(nil, "record-%03d", i))
	}
	deleted := map[uint16]bool{3: true, 4: true, 5: true}
	for slotNo := range deleted {
		if err := p.DeleteRecord(slotNo); err != nil {
			t.Fatalf("DeleteRecord(%d) 失败: %v", slotNo, err)
		}
	}

	mustNotPanic(t, "中间删除后 Compact", func() { p.Compact() })

	for i := 0; i < n; i++ {
		slotNo := uint16(i)
		got, err := p.GetRecord(slotNo)
		if deleted[slotNo] {
			if err != ErrSlotDeleted {
				t.Errorf("已删除 slot %d 应返回 ErrSlotDeleted, 实际: %v (内容 %q)", slotNo, err, got)
			}
			continue
		}
		want := fmt.Sprintf("record-%03d", i)
		if err != nil {
			t.Errorf("Compact 后 slot %d 不可读: %v", slotNo, err)
			continue
		}
		if string(got) != want {
			t.Errorf("Compact 后 slot %d = %q, 期望 %q", slotNo, got, want)
		}
	}
	assertInvariants(t, p, "中间删除 Compact 后")
}

func TestCompact_ReclaimsSpace(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := 0; i < 10; i++ {
		mustInsert(t, p, bytes.Repeat([]byte{'A'}, 300))
	}
	for i := 0; i < 6; i++ {
		if err := p.DeleteRecord(uint16(i)); err != nil {
			t.Fatalf("DeleteRecord(%d) 失败: %v", i, err)
		}
	}
	freeBefore := p.freeBlockSize()

	mustNotPanic(t, "回收碎片 Compact", func() { p.Compact() })

	if freeAfter := p.freeBlockSize(); freeAfter <= freeBefore {
		t.Errorf("Compact 未回收空间: 空闲 %d -> %d", freeBefore, freeAfter)
	}

	mustNotPanic(t, "Compact 后插入大记录", func() {
		if _, err := p.InsertRecord(bytes.Repeat([]byte{'Z'}, 1500)); err != nil {
			t.Errorf("Compact 回收的空间无法使用: %v", err)
		}
	})
	assertInvariants(t, p, "回收碎片 Compact 后")
}

func TestCompact_AllDeleted(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := 0; i < 5; i++ {
		mustInsert(t, p, fmt.Appendf(nil, "r%d", i))
	}
	for i := uint16(0); i < 5; i++ {
		if err := p.DeleteRecord(i); err != nil {
			t.Fatalf("DeleteRecord(%d) 失败: %v", i, err)
		}
	}

	mustNotPanic(t, "全部删除后 Compact", func() { p.Compact() })

	if p.SlotCount != 0 {
		t.Errorf("全部删除后 Compact: SlotCount = %d, 期望 0", p.SlotCount)
	}
	// 已知缺陷：Compact() 开头的 `if p.SlotCount == 0 { return }` 提前返回，
	// 而 DeleteRecord 的尾部截断已把 SlotCount 归零，于是恢复分支
	// （FreeEnd = PageSize）永远走不到，FreeEnd 停在最后一条记录的起始偏移。
	if p.FreeEnd != PageSize {
		t.Errorf("全部删除后 Compact: FreeEnd = %d, 期望 %d（页内已无数据，应回到页尾）",
			p.FreeEnd, PageSize)
	}
	if got := liveSlotNos(p); len(got) != 0 {
		t.Errorf("全部删除后不应有存活记录, 实际: %v", got)
	}
	slotNo, err := p.InsertRecord([]byte("fresh"))
	if err != nil {
		t.Fatalf("Compact 后插入失败: %v", err)
	}
	if got := mustGet(t, p, slotNo); string(got) != "fresh" {
		t.Errorf("Compact 后新记录内容 = %q", got)
	}
	assertInvariants(t, p, "全部删除 Compact 后")
}

func TestCompact_TruncatesTrailingTombstones(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := 0; i < 5; i++ {
		mustInsert(t, p, fmt.Appendf(nil, "r%d", i))
	}
	if err := p.DeleteRecord(4); err != nil {
		t.Fatalf("DeleteRecord(4) 失败: %v", err)
	}

	mustNotPanic(t, "尾部墓碑 Compact", func() { p.Compact() })

	if p.SlotCount != 4 {
		t.Errorf("尾部墓碑应被截断: SlotCount = %d, 期望 4", p.SlotCount)
	}
	for i := uint16(0); i < 4; i++ {
		want := fmt.Sprintf("r%d", i)
		if got := mustGet(t, p, i); string(got) != want {
			t.Errorf("slot %d = %q, 期望 %q", i, got, want)
		}
	}
	assertInvariants(t, p, "尾部墓碑 Compact 后")
}

func TestCompact_Idempotent(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := 0; i < 8; i++ {
		mustInsert(t, p, fmt.Appendf(nil, "record-%02d", i))
	}
	if err := p.DeleteRecord(2); err != nil {
		t.Fatalf("DeleteRecord(2) 失败: %v", err)
	}

	mustNotPanic(t, "首次 Compact", func() { p.Compact() })
	slotCount1, freeEnd1 := p.SlotCount, p.FreeEnd

	mustNotPanic(t, "二次 Compact", func() { p.Compact() })
	if p.SlotCount != slotCount1 || p.FreeEnd != freeEnd1 {
		t.Errorf("Compact 非幂等: (SlotCount,FreeEnd) (%d,%d) -> (%d,%d)",
			slotCount1, freeEnd1, p.SlotCount, p.FreeEnd)
	}
	for i := uint16(0); i < p.SlotCount; i++ {
		if i == 2 {
			continue
		}
		want := fmt.Sprintf("record-%02d", i)
		if got, err := p.GetRecord(i); err != nil || string(got) != want {
			t.Errorf("二次 Compact 后 slot %d 异常: %q, %v", i, got, err)
		}
	}
}

// =============================================================================
// Iterate
// =============================================================================

func TestIterate_VisitsLiveRecordsInSlotOrder(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := 0; i < 5; i++ {
		mustInsert(t, p, fmt.Appendf(nil, "rec%d", i))
	}
	if err := p.DeleteRecord(1); err != nil {
		t.Fatalf("DeleteRecord(1) 失败: %v", err)
	}
	if err := p.DeleteRecord(3); err != nil {
		t.Fatalf("DeleteRecord(3) 失败: %v", err)
	}

	var gotSlots []uint16
	var gotRecs []string
	p.Iterate(func(slotNo uint16, rec []byte) bool {
		gotSlots = append(gotSlots, slotNo)
		gotRecs = append(gotRecs, string(rec))
		return true
	})

	wantSlots := []uint16{0, 2, 4}
	wantRecs := []string{"rec0", "rec2", "rec4"}
	if fmt.Sprint(gotSlots) != fmt.Sprint(wantSlots) {
		t.Errorf("Iterate slot 序列 = %v, 期望 %v", gotSlots, wantSlots)
	}
	if fmt.Sprint(gotRecs) != fmt.Sprint(wantRecs) {
		t.Errorf("Iterate 记录序列 = %v, 期望 %v", gotRecs, wantRecs)
	}
}

// 回调返回 false 必须终止遍历（方法注释约定：fn 返回 false 时提前结束）。
func TestIterate_EarlyStop(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := 0; i < 5; i++ {
		mustInsert(t, p, fmt.Appendf(nil, "rec%d", i))
	}

	count := 0
	p.Iterate(func(_ uint16, _ []byte) bool {
		count++
		return count < 2
	})
	if count != 2 {
		t.Errorf("fn 返回 false 应提前结束遍历: 回调次数 = %d, 期望 2", count)
	}
}

func TestIterate_EmptyPage(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	count := 0
	p.Iterate(func(_ uint16, _ []byte) bool { count++; return true })
	if count != 0 {
		t.Errorf("空页 Iterate 回调次数 = %d, 期望 0", count)
	}
}

func TestIterate_AfterCompact(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := 0; i < 6; i++ {
		mustInsert(t, p, fmt.Appendf(nil, "rec%d", i))
	}
	if err := p.DeleteRecord(2); err != nil {
		t.Fatalf("DeleteRecord(2) 失败: %v", err)
	}
	mustNotPanic(t, "Compact", func() { p.Compact() })

	want := []string{"rec0", "rec1", "rec3", "rec4", "rec5"}
	var got []string
	p.Iterate(func(_ uint16, rec []byte) bool {
		got = append(got, string(rec))
		return true
	})
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Compact 后 Iterate = %v, 期望 %v", got, want)
	}
}

// =============================================================================
// Bytes
// =============================================================================

func TestBytes_LengthIsPageSize(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	if got := len(p.Bytes()); got != PageSize {
		t.Errorf("Bytes() 长度 = %d, 期望 %d", got, PageSize)
	}
}

func TestBytes_HeaderFieldsMatchState(t *testing.T) {
	defer guard(t)
	p := NewPage(7, PageTypeData)
	mustInsert(t, p, []byte("xyz"))

	h := decodedHeader(t, p)
	if h.PageType != PageTypeData {
		t.Errorf("PageType = %d, 期望 %d", h.PageType, PageTypeData)
	}
	if h.SlotCount != 1 {
		t.Errorf("SlotCount = %d, 期望 1", h.SlotCount)
	}
	if h.PageId != 7 {
		t.Errorf("PageId = %d, 期望 7", h.PageId)
	}
	if h.FreeEnd != PageSize-3 {
		t.Errorf("FreeEnd = %d, 期望 %d", h.FreeEnd, PageSize-3)
	}
	if h.NextPageId != InvalidPageID {
		t.Errorf("NextPageId = %d, 期望 InvalidPageID(%d)", h.NextPageId, InvalidPageID)
	}
}

func TestBytes_ReflectsInserts(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	mustInsert(t, p, bytes.Repeat([]byte{'A'}, 100))
	h := decodedHeader(t, p)

	if h.SlotCount != 1 {
		t.Errorf("插入后 SlotCount = %d, 期望 1", h.SlotCount)
	}
	if int(h.FreeEnd) != PageSize-100 {
		t.Errorf("插入后 FreeEnd = %d, 期望 %d", h.FreeEnd, PageSize-100)
	}
}

// 页头的空闲空间字段必须随操作更新：InsertRecord / UpdateRecord 都依赖它做容量判断，
// 若不维护会误判「还有空间」而写出页外。
func TestBytes_FreeSpaceFieldStaysAccurate(t *testing.T) {
	defer guard(t)
	p := newTestPage()

	if h := decodedHeader(t, p); int(h.FressSpace) != trueFreeSpace(p) {
		t.Errorf("新页空闲字段 = %d, 实际 %d", h.FressSpace, trueFreeSpace(p))
	}

	mustInsert(t, p, bytes.Repeat([]byte{'A'}, 1000))
	if h := decodedHeader(t, p); int(h.FressSpace) != trueFreeSpace(p) {
		t.Errorf("插入 1000 字节后空闲字段 = %d, 实际 %d（字段未随操作更新）",
			h.FressSpace, trueFreeSpace(p))
	}
}

func TestBytes_StableAcrossRepeatedCalls(t *testing.T) {
	defer guard(t)
	p := NewPage(3, PageTypeData)
	mustInsert(t, p, []byte("hello"))

	first := append([]byte(nil), p.Bytes()...)
	second := p.Bytes()
	if !bytes.Equal(first, second) {
		t.Errorf("连续两次 Bytes() 结果不一致（页头写回可能覆盖数据）")
	}
}

func TestBytes_DoesNotClobberRecords(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	want := bytes.Repeat([]byte{0x7E}, 200)
	mustInsert(t, p, want)
	_ = p.Bytes() // 触发页头写回
	if got := mustGet(t, p, 0); !bytes.Equal(got, want) {
		t.Errorf("调用 Bytes() 后记录被破坏")
	}
}

// =============================================================================
// 综合：混合操作序列的健壮性与不变式
// =============================================================================

func TestMixedOperations_NoPanicAndInvariantsHold(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	alive := map[uint16][]byte{}

	mustNotPanic(t, "混合操作序列", func() {
		for i := 0; i < 200; i++ {
			switch i % 5 {
			case 0: // 插入
				rec := bytes.Repeat([]byte{byte(i)}, 10+i%40)
				if slotNo, err := p.InsertRecord(rec); err == nil {
					alive[slotNo] = rec
				}
			case 1: // 删除
				for slotNo := range alive {
					if err := p.DeleteRecord(slotNo); err == nil {
						delete(alive, slotNo)
					}
					break
				}
			case 2: // 等长更新
				for slotNo, rec := range alive {
					_, _ = p.UpdateRecord(slotNo, rec)
					break
				}
			case 3: // 变短更新
				for slotNo, rec := range alive {
					if len(rec) < 4 {
						break
					}
					short := rec[:len(rec)/2]
					if newSlot, err := p.UpdateRecord(slotNo, short); err == nil {
						delete(alive, slotNo)
						alive[newSlot] = short
					}
					break
				}
			case 4: // 整理
				p.Compact()
			}
		}
	})

	assertInvariants(t, p, "混合操作后")

	// 所有「跟踪为存活」的记录都必须能按 RID 原样读回。
	for slotNo, want := range alive {
		got, err := p.GetRecord(slotNo)
		if err != nil {
			t.Errorf("混合操作后 RID{%d} 不可读: %v", slotNo, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("混合操作后 RID{%d} 内容不一致: got %d 字节, want %d 字节",
				slotNo, len(got), len(want))
		}
	}
}

// =============================================================================
// Dirty / MarkClean
// =============================================================================

func TestDirty_NewPageIsClean(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	if p.Dirty() {
		t.Errorf("NewPage 返回的页应为干净状态")
	}
}

func TestDirty_MarkClean(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	mustInsert(t, p, []byte("aaaa"))
	if !p.Dirty() {
		t.Fatalf("插入后应为脏页")
	}
	p.MarkClean()
	if p.Dirty() {
		t.Errorf("MarkClean 后应为干净状态")
	}
	p.MarkClean() // 重复调用应幂等
	if p.Dirty() {
		t.Errorf("重复 MarkClean 后应仍为干净状态")
	}
}

// 所有会改变页内容的写操作都必须置脏，否则缓冲池会漏刷盘。
func TestDirty_MutatingOpsSetDirty(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, p *Page)
	}{
		{"InsertRecord", func(t *testing.T, p *Page) {
			if _, err := p.InsertRecord([]byte("aaaa")); err != nil {
				t.Fatalf("InsertRecord 失败: %v", err)
			}
		}},
		{"DeleteRecord", func(t *testing.T, p *Page) {
			mustInsert(t, p, []byte("aaaa"))
			p.MarkClean()
			if err := p.DeleteRecord(0); err != nil {
				t.Fatalf("DeleteRecord 失败: %v", err)
			}
		}},
		{"UpdateRecord_等长", func(t *testing.T, p *Page) {
			mustInsert(t, p, []byte("aaaa"))
			p.MarkClean()
			if _, err := p.UpdateRecord(0, []byte("bbbb")); err != nil {
				t.Fatalf("UpdateRecord 失败: %v", err)
			}
		}},
		{"UpdateRecord_变短", func(t *testing.T, p *Page) {
			mustInsert(t, p, []byte("aaaaaaaa"))
			p.MarkClean()
			if _, err := p.UpdateRecord(0, []byte("bb")); err != nil {
				t.Fatalf("UpdateRecord 失败: %v", err)
			}
		}},
		{"UpdateRecord_变长", func(t *testing.T, p *Page) {
			mustInsert(t, p, []byte("aa"))
			p.MarkClean()
			if _, err := p.UpdateRecord(0, []byte("bbbbbbbb")); err != nil {
				t.Fatalf("UpdateRecord 失败: %v", err)
			}
		}},
		{"Compact", func(t *testing.T, p *Page) {
			for i := 0; i < 5; i++ {
				mustInsert(t, p, []byte("aaaa"))
			}
			p.MarkClean()
			p.Compact()
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer guard(t)
			p := newTestPage()
			tc.run(t, p)
			if !p.Dirty() {
				t.Errorf("%s 后页应为脏状态", tc.name)
			}
		})
	}
}

// 只读操作不得改变脏标记，否则缓冲池会做无谓刷盘。
func TestDirty_ReadOnlyOpsKeepClean(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := range 3 {
		mustInsert(t, p, fmt.Appendf(nil, "rec%d", i))
	}

	cases := []struct {
		name string
		run  func()
	}{
		{"GetRecord", func() { _, _ = p.GetRecord(0) }},
		{"GetRecord_越界", func() { _, _ = p.GetRecord(99) }},
		{"Iterate", func() { p.Iterate(func(uint16, []byte) bool { return true }) }},
		{"Bytes", func() { _ = p.Bytes() }},
		{"Dirty", func() { _ = p.Dirty() }},
		{"freeBlock", func() { _ = p.freeBlockSize() }},
	}

	for _, tc := range cases {
		p.MarkClean()
		tc.run()
		if p.Dirty() {
			t.Errorf("%s 是只读操作，不应置脏", tc.name)
		}
	}
}

// 失败的写操作不应置脏：否则会触发一次内容并未变化的刷盘。
func TestDirty_FailedOpsDoNotSetDirty(t *testing.T) {
	t.Run("InsertRecord_ErrPageFull", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		for {
			if _, err := p.InsertRecord(make([]byte, 100)); err != nil {
				break
			}
		}
		p.MarkClean()
		if _, err := p.InsertRecord(make([]byte, 500)); err != ErrPageFull {
			t.Fatalf("期望 ErrPageFull, 实际: %v", err)
		}
		if p.Dirty() {
			t.Errorf("插入失败后不应置脏")
		}
	})

	t.Run("InsertRecord_ErrRecordTooBig", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		p.MarkClean()
		if _, err := p.InsertRecord(make([]byte, MaxRecordSize+1)); err != ErrRecordTooBig {
			t.Fatalf("期望 ErrRecordTooBig, 实际: %v", err)
		}
		if p.Dirty() {
			t.Errorf("插入失败后不应置脏")
		}
	})

	t.Run("DeleteRecord_ErrSlotNotFound", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		mustInsert(t, p, []byte("aaaa"))
		p.MarkClean()
		if err := p.DeleteRecord(99); err != ErrSlotNotFound {
			t.Fatalf("期望 ErrSlotNotFound, 实际: %v", err)
		}
		if p.Dirty() {
			t.Errorf("删除失败后不应置脏")
		}
	})

	t.Run("UpdateRecord_ErrPageFull", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		mustInsert(t, p, []byte("aaaa"))
		for {
			if _, err := p.InsertRecord(make([]byte, 100)); err != nil {
				break
			}
		}
		p.MarkClean()
		if _, err := p.UpdateRecord(0, make([]byte, 4000)); err != ErrPageFull {
			t.Fatalf("期望 ErrPageFull, 实际: %v", err)
		}
		if p.Dirty() {
			t.Errorf("更新失败后不应置脏")
		}
	})

	t.Run("GetRecord_ErrSlotNotFound", func(t *testing.T) {
		defer guard(t)
		p := newTestPage()
		mustInsert(t, p, []byte("aaaa"))
		p.MarkClean()
		if _, err := p.GetRecord(99); err != ErrSlotNotFound {
			t.Fatalf("期望 ErrSlotNotFound, 实际: %v", err)
		}
		if p.Dirty() {
			t.Errorf("读取失败后不应置脏")
		}
	})
}

// =============================================================================
// DecodePage
// =============================================================================

func TestDecodePage_AllHeaderFieldsRoundTrip(t *testing.T) {
	defer guard(t)
	src := NewPage(0, PageTypeData)
	// 各字段取互不相同的非零值，确保不会因字段串位而「碰巧」通过。
	src.PageType = PageTypeIndex
	src.Flags = 0x5A
	src.NextPageId = 12345
	src.PageId = 4242
	src.LSN = 0x1122334455667788
	src.CheckSum = 0xDEADBEEF
	src.Reserved = 0x12345678

	got, err := DecodePage(src.PageId, src.Bytes())
	if err != nil {
		t.Fatalf("DecodePage 失败: %v", err)
	}
	assertFieldsRoundTrip(t, src.PageHeader, got.PageHeader, "页头全字段往返")
}

func TestDecodePage_WrongLength(t *testing.T) {
	defer guard(t)
	for _, n := range []int{0, 1, 31, 32, 4095, 4097, 8192} {
		if _, err := DecodePage(0, make([]byte, n)); err != ErrNotPage {
			t.Errorf("DecodePage(len=%d) 应返回 ErrNotPage, 实际: %v", n, err)
		}
	}
	if _, err := DecodePage(0, nil); err != ErrNotPage {
		t.Errorf("DecodePage(nil) 应返回 ErrNotPage, 实际: %v", err)
	}
}

func TestDecodePage_UnrecognizedPageType(t *testing.T) {
	defer guard(t)
	for _, pt := range []PageType{0, 5, 6, 200, 255} {
		buf := make([]byte, PageSize)
		buf[0] = byte(pt)
		if _, err := DecodePage(0, buf); err == nil {
			t.Errorf("PageType=%d 应被拒绝, 却成功解码", pt)
		}
	}
	// 合法页类型应全部接受
	for _, pt := range []PageType{PageTypeData, PageTypeFree, PageTypeMeta, PageTypeIndex} {
		buf := make([]byte, PageSize)
		buf[0] = byte(pt)
		page, err := DecodePage(0, buf)
		if err != nil {
			t.Errorf("PageType=%d 应被接受, 却报错: %v", pt, err)
			continue
		}
		if page.PageType != pt {
			t.Errorf("PageType 解码错误: got %d, want %d", page.PageType, pt)
		}
	}
}

func TestDecodePage_DecodedPageIsClean(t *testing.T) {
	defer guard(t)
	src := newTestPage()
	mustInsert(t, src, []byte("hello"))
	if !src.Dirty() {
		t.Fatalf("插入后源页应为脏")
	}

	got, err := DecodePage(0, src.Bytes())
	if err != nil {
		t.Fatalf("DecodePage 失败: %v", err)
	}
	if got.Dirty() {
		t.Errorf("刚解码的页不应是脏页（内容与磁盘一致）")
	}
}

// 解码必须复制字节：若与调用方缓冲区共享底层数组，
// 调用方复用缓冲区会静默破坏页内容（缓冲池场景下的典型 bug）。
func TestDecodePage_CopiesBuffer(t *testing.T) {
	defer guard(t)
	src := newTestPage()
	mustInsert(t, src, []byte("original"))

	buf := append([]byte(nil), src.Bytes()...)
	got, err := DecodePage(0, buf)
	if err != nil {
		t.Fatalf("DecodePage 失败: %v", err)
	}

	// 调用方回收并复用缓冲区
	for i := range buf {
		buf[i] = 0xFF
	}

	if got.PageType != PageTypeData {
		t.Errorf("缓冲区被复写后页头受影响: PageType = %d", got.PageType)
	}
	rec, err := got.GetRecord(0)
	if err != nil {
		t.Fatalf("缓冲区被复写后记录不可读: %v", err)
	}
	if string(rec) != "original" {
		t.Errorf("缓冲区被复写后记录被破坏: %q", rec)
	}
}

// 完整往返：编码 → 解码 → 记录仍可正常读取。
func TestDecodePage_PreservesRecords(t *testing.T) {
	defer guard(t)
	src := NewPage(0, PageTypeData)
	payloads := [][]byte{
		[]byte("hello"),
		[]byte("world!"),
		{},
		bytes.Repeat([]byte{0xA5}, 500),
	}
	for _, rec := range payloads {
		mustInsert(t, src, rec)
	}

	got, err := DecodePage(0, src.Bytes())
	if err != nil {
		t.Fatalf("DecodePage 失败: %v", err)
	}
	if got.SlotCount != uint16(len(payloads)) {
		t.Errorf("SlotCount = %d, 期望 %d", got.SlotCount, len(payloads))
	}
	for i, want := range payloads {
		rec, err := got.GetRecord(uint16(i))
		if err != nil {
			t.Errorf("解码后 GetRecord(%d) 失败: %v", i, err)
			continue
		}
		if !bytes.Equal(rec, want) {
			t.Errorf("解码后 slot %d 内容不一致: got %d 字节, want %d 字节", i, len(rec), len(want))
		}
	}
	assertFieldsRoundTrip(t, src.PageHeader, got.PageHeader, "记录往返")
	assertFreeSpaceAccurate(t, got, "解码后")
}

// 解码后应能继续增删改查，且行为与原始页一致。
func TestDecodePage_UsableAfterDecode(t *testing.T) {
	defer guard(t)
	src := NewPage(0, PageTypeData)
	mustInsert(t, src, []byte("alpha"))
	mustInsert(t, src, []byte("beta"))
	mustInsert(t, src, []byte("tail")) // 尾部哨兵，保证后续删除/更新不落在尾部
	if err := src.DeleteRecord(0); err != nil {
		t.Fatalf("DeleteRecord 失败: %v", err)
	}

	got, err := DecodePage(0, src.Bytes())
	if err != nil {
		t.Fatalf("DecodePage 失败: %v", err)
	}

	// 墓碑状态应被保留
	if _, err := got.GetRecord(0); err != ErrSlotDeleted {
		t.Errorf("解码后 slot 0 应为墓碑, 实际: %v", err)
	}

	// 插入：应复用墓碑 slot 0
	slotNo, err := got.InsertRecord([]byte("gamma"))
	if err != nil {
		t.Fatalf("解码后插入失败: %v", err)
	}
	if slotNo != 0 {
		t.Errorf("解码后插入应复用墓碑 slot 0, 实际: %d", slotNo)
	}
	if !got.Dirty() {
		t.Errorf("解码后插入应置脏")
	}

	// 更新与删除
	if _, err := got.UpdateRecord(1, []byte("BETA!")); err != nil {
		t.Fatalf("解码后更新失败: %v", err)
	}
	// 删除 slot 1（非尾部，slot 2 仍存活），避开尾部截断路径
	if err := got.DeleteRecord(1); err != nil {
		t.Fatalf("解码后删除失败: %v", err)
	}

	// 遍历：slot2 的 "tail" 仍存活
	var live []string
	got.Iterate(func(_ uint16, rec []byte) bool {
		live = append(live, string(rec))
		return true
	})
	want := []string{"gamma", "tail"}
	if fmt.Sprint(live) != fmt.Sprint(want) {
		t.Errorf("解码后 Iterate = %v, 期望 %v", live, want)
	}
	assertFreeSpaceAccurate(t, got, "解码后多次操作")
}

// 二次往返必须稳定（幂等），防止编码/解码一侧不对称导致字段漂移。
func TestDecodePage_SecondRoundTripStable(t *testing.T) {
	defer guard(t)
	src := NewPage(9, PageTypeData)
	mustInsert(t, src, []byte("one"))
	mustInsert(t, src, []byte("two"))
	if err := src.DeleteRecord(0); err != nil {
		t.Fatalf("DeleteRecord 失败: %v", err)
	}

	first, err := DecodePage(9, src.Bytes())
	if err != nil {
		t.Fatalf("首次 DecodePage 失败: %v", err)
	}
	second, err := DecodePage(9, first.Bytes())
	if err != nil {
		t.Fatalf("二次 DecodePage 失败: %v", err)
	}

	assertFieldsRoundTrip(t, first.PageHeader, second.PageHeader, "二次往返")
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Errorf("二次往返后整页字节不一致")
	}
}

// =============================================================================
// FressSpace：剩余空间的准确性与维护
// =============================================================================

// FressSpace 是「当前页剩余空间」的唯一权威来源，InsertRecord / UpdateRecord
// 都靠它在压缩前判定能否放下新记录，因此每一步操作后都必须准确。
func TestFreeSpace_StaysAccurateAcrossOperations(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	assertFreeSpaceAccurate(t, p, "新建")

	for i := range 5 {
		mustInsert(t, p, fmt.Appendf(nil, "rec-%02d", i))
		assertFreeSpaceAccurate(t, p, fmt.Sprintf("插入第 %d 条后", i))
	}

	// 中间删除：墓碑空间立即归还
	if err := p.DeleteRecord(2); err != nil {
		t.Fatalf("DeleteRecord(2) 失败: %v", err)
	}
	assertFreeSpaceAccurate(t, p, "删除中间 slot 后")

	// 变短：差额归还
	if _, err := p.UpdateRecord(0, []byte("r")); err != nil {
		t.Fatalf("变短更新失败: %v", err)
	}
	assertFreeSpaceAccurate(t, p, "变短更新后")

	// 变长：占用更多
	if _, err := p.UpdateRecord(1, bytes.Repeat([]byte{'X'}, 200)); err != nil {
		t.Fatalf("变长更新失败: %v", err)
	}
	assertFreeSpaceAccurate(t, p, "变长更新后")

	// 等长：不变
	before := p.FressSpace
	if _, err := p.UpdateRecord(3, bytes.Repeat([]byte{'Y'}, 6)); err != nil {
		t.Fatalf("等长更新失败: %v", err)
	}
	if p.FressSpace != before {
		t.Errorf("等长更新不应改变 FressSpace: %d -> %d", before, p.FressSpace)
	}
}

// 删除应立即归还记录空间（FressSpace 是「剩余空间」而非「连续空闲块」）。
func TestFreeSpace_DeleteReturnsSpaceImmediately(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	const recLen = 100
	slots := make([]uint16, 5)
	for i := range slots {
		slots[i] = mustInsert(t, p, bytes.Repeat([]byte{'A'}, recLen))
	}
	before := int(p.FressSpace)

	if err := p.DeleteRecord(2); err != nil {
		t.Fatalf("DeleteRecord 失败: %v", err)
	}
	if got, want := int(p.FressSpace), before+recLen; got != want {
		t.Errorf("删除后 FressSpace = %d, 期望 %d（应立即归还记录空间）", got, want)
	}
	assertFreeSpaceAccurate(t, p, "删除后")
}

// Compact 截断尾部墓碑后，被截断 slot 所占的 4 字节也应归还。
func TestFreeSpace_CompactTruncationReturnsSlotSpace(t *testing.T) {
	defer guard(t)
	for k := 1; k <= 3; k++ {
		p := newTestPage()
		for i := 0; i < 6; i++ {
			mustInsert(t, p, []byte("r0"))
		}
		for i := 0; i < k; i++ {
			if err := p.DeleteRecord(uint16(5 - i)); err != nil {
				t.Fatalf("DeleteRecord 失败: %v", err)
			}
		}
		p.Compact()
		assertFreeSpaceAccurate(t, p, fmt.Sprintf("截断 %d 个尾部墓碑后 Compact", k))
	}
}

// FressSpace 必须能作为「能否放下新记录」的判据：
// 它若偏小，会导致本可放下的记录被误判为页满而被迫跨页。
func TestFreeSpace_NotUnderestimatedAfterCompact(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := 0; i < 5; i++ {
		mustInsert(t, p, []byte("r0"))
	}
	// 删除尾部 slot 后再 Compact（会截断墓碑并回收其 slot 空间）
	if err := p.DeleteRecord(4); err != nil {
		t.Fatalf("DeleteRecord(4) 失败: %v", err)
	}
	p.Compact()

	real := recomputedFreeSpace(p)
	if int(p.FressSpace) != real {
		t.Errorf("Compact 后 FressSpace=%d 与真实剩余空间 %d 不一致（偏差 %d 字节）",
			p.FressSpace, real, real-int(p.FressSpace))
	}

	// 实证：不触发 Compact 时，能放下的最大长度 = 连续空闲块 - SlotSize。
	//
	// 必须减掉 SlotSize：此处尾部墓碑已被截断，页内没有可复用的墓碑，
	// 所以 InsertRecord 走「追加 slot」分支，需要额外 4 字节存放 slot 项。
	// 直接按 real 去插会超出上限而被正确拒绝（real 是「剩余空间」口径，
	// 含页内碎片；能立即写入的字节数受连续空闲块 freeBlock 限制）。
	freeSlots := int(p.SlotCount) - len(liveSlotNos(p))
	if freeSlots != 0 {
		t.Fatalf("本用例前提是页内无可复用墓碑, 实际有 %d 个", freeSlots)
	}
	size := real - SlotSize
	if blk := int(p.freeBlockSize()) - SlotSize; size > blk {
		size = blk // 收窄到「无需 Compact 即可写入」的范围，本用例只验证 FressSpace 口径
	}
	if size <= 0 {
		t.Fatalf("可插入长度算出 %d，用例前提不成立", size)
	}
	slotNo, err := p.InsertRecord(make([]byte, size))
	if err != nil {
		t.Errorf("真实剩余 %d 字节、连续空闲 %d 字节，应能插入 %d 字节记录，却失败: %v",
			real, p.freeBlockSize(), size, err)
		return
	}
	if got := mustGet(t, p, slotNo); len(got) != size {
		t.Errorf("插入记录长度 = %d, 期望 %d", len(got), size)
	}
	assertFreeSpaceAccurate(t, p, "插入后")
}

// InsertRecord 会先 SlotCount++ 预留 slot，再在需要时调用 Compact()。
// 此时预留 slot 的字节仍是 {0,0}，与墓碑的哨兵形式相同，
// 若 Compact 按墓碑把它截断，SlotCount 就会回退，
// 导致记录字节已写入页内却无法按 RID 读回 —— 静默数据丢失。
func TestInsertRecord_ReservedSlotNotEatenByCompact(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	for i := 0; i < 5; i++ {
		mustInsert(t, p, []byte("r0"))
	}
	// 删掉尾部 slot 并整理，得到一个「无墓碑但 FreeEnd 未回到页尾」的页
	if err := p.DeleteRecord(4); err != nil {
		t.Fatalf("DeleteRecord(4) 失败: %v", err)
	}
	p.Compact()

	// 触发 Compact 需要：追加 slot 后 freeBlock < size，即 size > freeBlock-SlotSize。
	// 同时必须通过容量检查：size <= FressSpace-SlotSize。
	// 故可行区间为 [freeBlock-SlotSize+1, FressSpace-SlotSize]，取其上界最稳妥。
	upper := int(p.FressSpace) - SlotSize
	lower := int(p.freeBlockSize()) - SlotSize + 1
	if upper > MaxRecordSize {
		upper = MaxRecordSize
	}
	if lower > upper {
		t.Skipf("剩余空间不足以构造该场景（lower=%d upper=%d）", lower, upper)
	}
	size := upper

	slotCountBefore := p.SlotCount
	slotNo, err := p.InsertRecord(make([]byte, size))
	if err != nil {
		t.Fatalf("插入 %d 字节应成功（FressSpace=%d）, 却失败: %v", size, p.FressSpace, err)
	}
	if p.SlotCount <= slotCountBefore {
		t.Errorf("插入后 SlotCount 未增长（%d -> %d）：预留的 slot 被 Compact 当作墓碑截断，"+
			"记录将无法按 RID 读回", slotCountBefore, p.SlotCount)
	}
	got, err := p.GetRecord(slotNo)
	if err != nil {
		t.Errorf("插入返回 slot=%d，但按该 RID 读取失败: %v（SlotCount=%d）—— 静默数据丢失",
			slotNo, err, p.SlotCount)
		return
	}
	if len(got) != size {
		t.Errorf("读回长度 = %d, 期望 %d", len(got), size)
	}
	assertFreeSpaceAccurate(t, p, "插入后")
}

// FressSpace 是 uint16；页满时做减法不得下溢成巨大值。
func TestFreeSpace_NoUnderflowAtCapacity(t *testing.T) {
	defer guard(t)
	p := newTestPage()
	filled := 0
	for {
		if _, err := p.InsertRecord(make([]byte, 100)); err != nil {
			if err != ErrPageFull {
				t.Fatalf("期望 ErrPageFull, 实际: %v", err)
			}
			break
		}
		filled++
	}
	if filled != 39 {
		t.Errorf("100 字节记录容量 = %d, 期望 39", filled)
	}
	if p.FressSpace > PageSize {
		t.Errorf("页满后 FressSpace = %d，疑似 uint16 下溢", p.FressSpace)
	}
	assertFreeSpaceAccurate(t, p, "页满后")

	// 页满后仍应能正常删除并回收空间
	if err := p.DeleteRecord(0); err != nil {
		t.Fatalf("页满后删除失败: %v", err)
	}
	assertFreeSpaceAccurate(t, p, "页满后删除")
	if _, err := p.InsertRecord(make([]byte, 100)); err != nil {
		t.Errorf("删除归还空间后应能重新插入: %v", err)
	}
}

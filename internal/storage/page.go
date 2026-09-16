package storage

import (
	"easydb/utils"
	"encoding/binary"
	"sort"
)

type PageID uint32

// RID 是记录的全局地址：第 PageID 页的第 SlotNo 个 slot。
// 注意：只要记录还在同一个 slot 里，页内移动（整理）不会让 RID 失效。
type RID struct {
	PageID PageID
	SlotNo uint16
}

type Page struct {
	PageHeader
	id    PageID
	data  [PageSize]byte // 定长数组而非 slice：避免额外分配，值语义清晰
	dirty bool
}

func NewPage(id PageID, t PageType) *Page {
	data := [PageSize]byte{}
	pageHeader := PageHeader{
		PageType:   t,
		Flags:      0,
		SlotCount:  0,
		FreeEnd:    PageSize,
		FressSpace: PageSize - PageHeaderSize,
		NextPageId: InvalidPageID,
		PageId:     id,
		LSN:        0,
		CheckSum:   0,
		Reserved:   0,
	}
	return &Page{pageHeader, id, data, false}
}

// --- 页头存取 ---
// func (p *Page) ID() PageID {
// 	// return PageID(binary.LittleEndian.Uint32(p.data[12:16]))
// 	return p.PageId
// }
// func (p *Page) Type() PageType {
// 	return PageType(p.data[0])
// }
// func (p *Page) SetType(t PageType) {
// 	p.data[0] = byte(t)
// }
// func (p *Page) SlotCount() uint16 {
// 	return binary.LittleEndian.Uint16(p.data[2:4])
// }

// func (p *Page) Dirty() bool {
// 	return false
// }
// func (p *Page) MarkClean()

// --- 记录操作 ---
// InsertRecord 写入一条记录并返回其 slot 号。
// 空间不足时返回 ErrPageFull（调用方应换页或先尝试 Compact）。
func (p *Page) InsertRecord(rec []byte) (uint16, error) {

	recordLength := uint16(len(rec))
	if recordLength > MaxRecordSize {
		return 0, ErrRecordTooBig
	}

	// 查找空slot
	slotNo, err := p.nextBlankSlotNo()
	if err != nil {
		// 追加slot
		if err == ErrSlotNotFound {
			slotNo = p.SlotCount
			if recordLength+SlotSize > p.FressSpace {
				return 0, ErrPageFull
			}
			p.SlotCount++
			p.FressSpace -= SlotSize
		} else {
			return 0, err
		}
	} else {
		// 复用slot
		if recordLength > p.FressSpace {
			return 0, ErrPageFull
		}
	}

	// 连续空余空间不足，压缩
	if p.freeBlock() < recordLength {
		p.Compact()
	}

	newSlot := Slot{
		Offset: p.FreeEnd - recordLength,
		Length: recordLength,
	}
	// 写入slot
	p.writeSlot(slotNo, newSlot)
	// 写入record
	copy(p.data[newSlot.Offset:], rec)
	// 更新FreeEnd
	p.FreeEnd = newSlot.Offset
	// 更新FreeSpace
	p.FressSpace -= recordLength
	return slotNo, nil
}

// GetRecord 返回记录的副本（切片为拷贝，调用方随意持有）。
func (p *Page) GetRecord(slotNo uint16) ([]byte, error) {
	if slotNo >= p.SlotCount {
		return nil, ErrSlotNotFound
	}

	slot := p.getSlot(slotNo)
	if slot.isDeleted() {
		return nil, ErrSlotDeleted
	}
	b := make([]byte, slot.Length)
	copy(b, p.sliceBySlot(slot))
	return b, nil
}

// DeleteRecord 把 slot 标记为墓碑，不移动其他记录、不立即回收空间。
func (p *Page) DeleteRecord(slotNo uint16) error {
	if slotNo >= p.SlotCount {
		return ErrSlotNotFound
	}
	s := p.getSlot(slotNo)
	if s.isDeleted() {
		return ErrSlotDeleted
	}
	p.writeSlot(slotNo, Slot{Offset: 0, Length: 0})
	p.FressSpace += s.Length
	return nil
}

// UpdateRecord 更新记录：长度不变则原地覆盖，否则删除后重插（slot 号可能变化）。
func (p *Page) UpdateRecord(slotNo uint16, rec []byte) (uint16, error) {
	if slotNo >= p.SlotCount {
		return 0, ErrSlotNotFound
	}
	newRecordLength := uint16(len(rec))
	if newRecordLength > MaxRecordSize {
		return 0, ErrRecordTooBig
	}
	orignalSlot := p.getSlot(slotNo)
	if orignalSlot.isDeleted() {
		return 0, ErrSlotDeleted
	}

	if orignalSlot.Length == newRecordLength { // 更新长度与原长度相等
		copy(p.data[orignalSlot.Offset:], rec)
		return slotNo, nil
	} else if newRecordLength < orignalSlot.Length { // 更新长度变小
		// 新record写入
		copy(p.data[orignalSlot.Offset:], rec)
		// 新slot信息写入
		newSlot := Slot{Offset: orignalSlot.Offset, Length: newRecordLength}
		copy(p.data[slotNo*SlotSize+PageHeaderSize:], newSlot.chunk())

		// 空间碎片
		forwardOffset := orignalSlot.Length - newRecordLength
		p.FressSpace += forwardOffset
		return slotNo, nil
	} else { // 更新长度变大
		// 增量大于空闲空间
		if newRecordLength-orignalSlot.Length > p.FressSpace {
			return 0, ErrPageFull
		}
		// 删除原有记录
		p.DeleteRecord(slotNo)
		// 连续空余空间不足，压缩
		if p.freeBlock() < newRecordLength {
			p.Compact()
		}
		newSlot := Slot{
			Offset: p.FreeEnd - newRecordLength,
			Length: newRecordLength,
		}
		// 写入slot
		p.writeSlot(slotNo, newSlot)
		// 写入record
		copy(p.data[newSlot.Offset:], rec)
		// 更新FreeEnd
		p.FreeEnd = newSlot.Offset
		// 更新FreeSpace
		p.FressSpace -= newRecordLength
		return slotNo, nil
	}
}

// Compact 碎片整理：把所有存活记录压到页尾，重建 slot 目录。
// 整理后 slot 号保持不变（墓碑保留），因此 RID 依然有效。
func (p *Page) Compact() {
	// 从后向前清理slot墓碑
	if p.SlotCount == 0 {
		return
	}
	for i := p.SlotCount - 1; ; i-- {
		if p.getSlot(i).isDeleted() {
			p.SlotCount--
		} else {
			break
		}
		if i == 0 {
			break
		}
	}
	if p.SlotCount == 0 {
		p.FreeEnd = PageSize
		p.FressSpace = PageSize - PageHeaderSize
		return
	}
	// 清理碎片
	slots := make([]Slot, p.SlotCount)
	idx := 0
	for i := uint16(0); i < p.SlotCount; i++ {
		s := *p.getSlot(uint16(i))
		if s.isDeleted() {
			continue
		}
		slots[idx] = s
		idx++
	}
	slots = slots[0:idx]
	sort.Slice(slots, func(i, j int) bool {
		return slots[i].Offset > slots[j].Offset
	})
	tailIndex, runningOffset := uint16(PageSize), uint16(0)
	// 偏移量记录：[当前偏移，碎片偏移]
	offsetRecord := make([]struct {
		Slot
		runningOffset uint16
	}, 0)
	for i := 0; i < len(slots); i++ {
		slot := slots[i]
		diff := tailIndex - slot.end()
		runningOffset += diff
		if diff != 0 {
			offsetRecord = append(offsetRecord, struct {
				Slot
				runningOffset uint16
			}{slot, runningOffset})
		}
		tailIndex = slot.Offset
		slots[i].Offset += runningOffset
	}
	// 处理偏移
	for i := 0; i < len(offsetRecord); i++ {
		var fromStart, fromEnd uint16
		currRecord := offsetRecord[i]
		if i == len(offsetRecord)-1 {
			fromStart = p.FreeEnd
			fromEnd = currRecord.Offset + currRecord.Length
		} else {
			fromStart = offsetRecord[i+1].end()
			fromEnd = currRecord.Offset + currRecord.Length
		}
		copy(p.data[fromStart+currRecord.runningOffset:], p.data[fromStart:fromEnd])
	}
	for _, s := range slots {
		p.writeSlot(s.slotNo, s)
	}
	p.FreeEnd += runningOffset
}

// Iterate 顺序遍历所有存活记录；fn 返回 false 时提前结束。
func (p *Page) Iterate(fn func(slotNo uint16, rec []byte) bool) {
	for i := uint16(0); i < p.SlotCount; i++ {
		s := p.getSlot(i)
		if s.isDeleted() {
			continue
		}
		if !fn(i, p.sliceBySlot(s)) {
			return
		}
	}
}

// --- 序列化 ---
// 返回内部数组的切片，只读使用
func (p *Page) Bytes() []byte {
	headerBytes := p.PageHeader.Bytes()
	copy(p.data[:], headerBytes[:])
	return p.data[:]
}

func DecodePage(id PageID, buf []byte) (*Page, error) {
	// TODO
	return nil, nil
}
func (p *Page) getSlot(slotNo uint16) *Slot {
	slotStart := PageHeaderSize + slotNo*SlotSize
	slotBytes := p.data[slotStart : slotStart+SlotSize]
	s := NewSlot([4]byte(slotBytes))
	s.slotNo = slotNo
	return s
}

// 向指定slot写入信息
func (p *Page) writeSlot(slotNo uint16, slot Slot) {
	copy(p.data[PageHeaderSize+slotNo*SlotSize:], slot.chunk())
}

// 追加slot
func (p *Page) appendSlot(s Slot) uint16 {
	slotNo := p.SlotCount
	p.writeSlot(slotNo, s)
	p.SlotCount++
	p.FressSpace -= SlotSize
	return slotNo
}

// 遍历查找slot墓碑，返回slotNo
func (p *Page) nextBlankSlotNo() (uint16, error) {
	for i := uint16(0); i < p.SlotCount; i++ {
		if s := p.getSlot(i); s.isDeleted() {
			return i, nil
		}
	}
	return uint16(0), ErrSlotNotFound
}

func (p *Page) sliceBySlot(s *Slot) []byte {
	return p.data[s.Offset : s.Offset+s.Length]
}

func (p *Page) freeBlock() uint16 {
	return p.FreeEnd - p.SlotCount*SlotSize - PageHeaderSize
}

// 32 bytes
type PageHeader struct {
	PageType   PageType // 页类型
	Flags      uint8    // 位标志，bit0 = 页内存在已删除 slot（供整理判断）
	SlotCount  uint16   // slot 总数，含已删除的墓碑 slot
	FreeEnd    uint16   // 空闲区结束 = 记录区第一个字节
	FressSpace uint16   // 空闲空间
	NextPageId PageID   // 同类型页链表指针；0xFFFFFFFF 表示「无」
	PageId     PageID   // 本页页号，用于自校验与调试
	LSN        uint64
	CheckSum   uint32
	Reserved   uint32 // 补齐到 32，保持 8 字节对齐，恒为 0
}

func newPageHeader(buf []byte) *PageHeader {
	return &PageHeader{
		PageType:   PageType(buf[0]),
		Flags:      uint8(buf[1]),
		SlotCount:  binary.LittleEndian.Uint16(buf[2:4]),
		FreeEnd:    binary.LittleEndian.Uint16(buf[4:6]),
		FressSpace: binary.LittleEndian.Uint16(buf[6:8]),
		NextPageId: PageID(binary.LittleEndian.Uint32(buf[8:12])),
		PageId:     PageID(binary.LittleEndian.Uint32(buf[12:16])),
		LSN:        binary.LittleEndian.Uint64(buf[16:24]),
		CheckSum:   binary.LittleEndian.Uint32(buf[24:28]),
		Reserved:   binary.LittleEndian.Uint32(buf[24:32]),
	}
}

func (h *PageHeader) Bytes() [32]byte {
	byteArr := [][]byte{
		utils.MakeChunk(h.PageType),
		utils.MakeChunk(h.Flags),
		utils.MakeChunk(h.SlotCount),
		utils.MakeChunk(h.FreeEnd),
		utils.MakeChunk(h.FressSpace),
		utils.MakeChunk(h.NextPageId),
		utils.MakeChunk(h.PageId),
		utils.MakeChunk(h.LSN),
		utils.MakeChunk(h.CheckSum),
		utils.MakeChunk(h.Reserved),
	}
	bytes := [32]byte{}
	index := 0
	for _, chunk := range byteArr {
		copy(bytes[index:], chunk)
		index += len(chunk)
	}
	return bytes
}

// 4 bytes
type Slot struct {
	slotNo uint16
	Offset uint16 // 记录在页内的起始偏移
	Length uint16 // 记录字节数
}

func NewSlot(bytes [4]byte) *Slot {
	return &Slot{
		Offset: binary.LittleEndian.Uint16(bytes[0:2]),
		Length: binary.LittleEndian.Uint16(bytes[2:4]),
	}
}

func (s *Slot) isDeleted() bool {
	return s.Offset == 0 && s.Length == 0
}

func (s *Slot) chunk() []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint16(b[0:], s.Offset)
	binary.LittleEndian.PutUint16(b[2:], s.Length)
	return b
}

func (s *Slot) end() uint16 {
	return s.Offset + s.Length
}

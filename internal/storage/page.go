package storage

import "encoding/binary"

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
	// header := [][]byte{
	// 	utils.MakeChunk(t),
	// 	utils.MakeChunk(uint8(1)),
	// 	utils.MakeChunk(uint16(0)),              // TODO SlotCount
	// 	utils.MakeChunk(uint16(PageHeaderSize)), // FreeStart
	// 	utils.MakeChunk(uint16(PageSize)),       // FreeEnd
	// 	utils.MakeChunk(InvalidPageID),          // NextPageID
	// 	utils.MakeChunk(id),                     // PageID
	// 	utils.MakeChunk(uint64(0)),              // LSN
	// 	utils.MakeChunk(uint32(0)),              // Checksum
	// 	utils.MakeChunk(uint32(0)),              // Reserved
	// }
	// offset := 0
	// for _, c := range header {
	// 	n := copy(data[offset:], c)
	// 	offset += n
	// }
	pageHeader := PageHeader{
		PageType:   t,
		Flags:      0,
		SlotCount:  0,
		FreeStart:  PageHeaderSize,
		FreeEnd:    PageSize,
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

// = FreeEnd - FreeStart
func (p *Page) FreeSpace() int {
	freeStart := binary.LittleEndian.Uint16(p.data[4:6])
	freeEnd := binary.LittleEndian.Uint16(p.data[6:8])
	return int(freeEnd - freeStart)
}

// func (p *Page) Dirty() bool {
// 	return false
// }
// func (p *Page) MarkClean()

// --- 记录操作 ---
// InsertRecord 写入一条记录并返回其 slot 号。
// 空间不足时返回 ErrPageFull（调用方应换页或先尝试 Compact）。
func (p *Page) InsertRecord(rec []byte) (slotNo uint16, err error) {
	size := uint16(len(rec))
	if size > p.FreeEnd-p.FreeStart {
		return 0, ErrPageFull
	}

	newSlot := Slot{
		Offset: p.FreeEnd - size,
		Length: size,
	}
	copy(newSlot.sliceFrom(p.data[:]), rec)
	for i := uint16(0); i < p.SlotCount; i++ {
		slotStart := PageHeaderSize + i*SlotSize
		bytes := p.data[slotStart : slotStart+SlotSize]
		slot := NewSlot([4]byte(bytes))
		if slot.isDeleted() {
			copy(p.data[slotStart:], newSlot.toChunk())
			return i, nil
		}
	}
	copy(p.data[p.FreeStart:], newSlot.toChunk())
	p.SlotCount++
	return p.SlotCount, nil
}

// GetRecord 返回记录的副本（切片为拷贝，调用方随意持有）。
func (p *Page) GetRecord(slotNo uint16) ([]byte, error) {
	if slotNo > p.SlotCount {
		return nil, ErrSlotNotFound
	}

	slotStart := PageHeaderSize + slotNo*SlotSize
	slotBytes := p.data[slotStart : slotStart+SlotSize]
	slot := NewSlot([4]byte(slotBytes))

	b := make([]byte, slot.Length)
	copy(b, slot.sliceFrom(p.data[:]))
	return b, nil
}

// DeleteRecord 把 slot 标记为墓碑，不移动其他记录、不立即回收空间。
func (p *Page) DeleteRecord(slotNo uint16) error {
	if slotNo > p.SlotCount {
		return ErrSlotNotFound
	}

	newSlot := Slot{0, 0}
	slotStart := PageHeaderSize + slotNo*SlotSize
	copy(p.data[slotStart:], newSlot.toChunk())
	return nil
}

// UpdateRecord 更新记录：长度不变则原地覆盖，否则删除后重插（slot 号可能变化）。
func (p *Page) UpdateRecord(slotNo uint16, rec []byte) (newSlot uint16, err error)

// Compact 碎片整理：把所有存活记录压到页尾，重建 slot 目录。
// 整理后 slot 号保持不变（墓碑保留），因此 RID 依然有效。
func (p *Page) Compact()

// Iterate 顺序遍历所有存活记录；fn 返回 false 时提前结束。
func (p *Page) Iterate(fn func(slotNo uint16, rec []byte) bool)

// --- 序列化 ---
func (p *Page) Bytes() []byte // 返回内部数组的切片，只读使用

func (p *Page) getSlot(slotNo uint16) *Slot {
	slotStart := PageHeaderSize + slotNo*SlotSize
	slotBytes := p.data[slotStart : slotStart+SlotSize]
	return NewSlot([4]byte(slotBytes))
}

func DecodePage(id PageID, buf []byte) (*Page, error)

// 32 bytes
type PageHeader struct {
	PageType   PageType // 页类型
	Flags      uint8    // 位标志，bit0 = 页内存在已删除 slot（供整理判断）
	SlotCount  uint16   // slot 总数，含已删除的墓碑 slot
	FreeStart  uint16   // 空闲区起始 = 页头 + slot 数组末尾
	FreeEnd    uint16   // 空闲区结束 = 记录区第一个字节
	NextPageId PageID   // 同类型页链表指针；0xFFFFFFFF 表示「无」
	PageId     PageID   // 本页页号，用于自校验与调试
	LSN        uint64
	CheckSum   uint32
	Reserved   uint32 // 补齐到 32，保持 8 字节对齐，恒为 0
}

// 4 bytes
type Slot struct {
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

func (s *Slot) toChunk() []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint16(b[0:], s.Offset)
	binary.LittleEndian.PutUint16(b[2:], s.Length)
	return b
}

func (s *Slot) end() uint16 {
	return s.Offset + s.Length
}

func (s *Slot) sliceFrom(data []byte) []byte {
	return data[s.Offset : s.Offset+s.Length]
}

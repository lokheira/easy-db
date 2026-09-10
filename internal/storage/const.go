package storage

import "errors"

const (
	PageSize       = 4096
	PageHeaderSize = 32
	SlotSize       = 4                                    // 每个 slot 4 字节（offset uint16 + length uint16）
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

type PageType uint8

const (
	PageTypeData  PageType = 1 // 数据页：存放用户记录
	PageTypeFree  PageType = 2 // 空闲页：内容已废弃，可被重新分配
	PageTypeMeta  PageType = 3 // 元数据页：第 0 页，记录文件级信息
	PageTypeIndex PageType = 4 // 索引页：阶段 4 使用，本阶段只占位
)

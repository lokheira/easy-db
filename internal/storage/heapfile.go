package storage

import (
	"io"
	"os"
)

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
func OpenHeapFile(path string) (*HeapFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 4)
	len, err := io.ReadFull(f, buf)
	if err != nil {
		return nil, err
	}
	if len != 4 {
		return nil, ErrBadMagic
	}

	return &HeapFile{f, path, 0, []PageID{}}, err
}

func (h *HeapFile) Close() error // 刷盘 + 关闭；Close 之后任何操作都返回错误
func (h *HeapFile) Sync() error  // 显式 fsync，崩溃测试用

func (h *HeapFile) PageCount() uint32
func (h *HeapFile) ReadPage(id PageID) (*Page, error)
func (h *HeapFile) WritePage(p *Page) error

// AllocatePage 优先复用空闲页，否则在文件末尾追加一页。
func (h *HeapFile) AllocatePage(t PageType) (*Page, error)

// FreePage 把页标记为 PageTypeFree 并加入空闲列表。
func (h *HeapFile) FreePage(id PageID) error

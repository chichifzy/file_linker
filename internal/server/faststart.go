package server

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"sync"
	"time"
)

// 一半以上的下载版 MP4 把索引（moov）放在文件尾部，普通播放器还能靠 Range 请求跳到末尾读取，
// 但 iOS Safari 打开视频时经常直接发一个不带 Range 的 GET（整段顺序下载），
// 这种情况下必须先读完整个文件才能解析出索引——表现就是「文件越大等越久，1GB 基本打不开」。
//
// 这里不去复制文件，而是在内存里把 moov 挪到最前面，并修正它内部记录的 chunk 偏移
// （整段顺序移动，所有偏移的增量相同），再用一个虚拟的 ReadSeeker 交给 ServeContent。
// 客户端拿到的仍然是一个结构完全合法的 MP4，磁盘占用为零。

var errUnsupportedMP4 = errors.New("mp4 结构不支持")

type mp4Box struct {
	typ  string
	off  int64 // 盒子起点
	size int64 // 含头部的总长度
	hdr  int64 // 头部长度（8 或 16）
}

// readMP4Boxes 只解析同一层级的盒子头，不进入内容。
func readMP4Boxes(r io.ReaderAt, start, end int64) ([]mp4Box, error) {
	var out []mp4Box
	buf := make([]byte, 16)
	for off := start; off+8 <= end && len(out) < 256; {
		n, err := r.ReadAt(buf, off)
		if n < 8 {
			if err != nil && err != io.EOF {
				return nil, err
			}
			break
		}
		size := int64(binary.BigEndian.Uint32(buf[0:4]))
		typ := string(buf[4:8])
		hdr := int64(8)
		switch size {
		case 0: // 一直到文件末尾
			size = end - off
		case 1: // 64 位长度
			if n < 16 {
				return nil, errUnsupportedMP4
			}
			size = int64(binary.BigEndian.Uint64(buf[8:16]))
			hdr = 16
		}
		if size < hdr || off+size > end {
			return nil, errUnsupportedMP4
		}
		out = append(out, mp4Box{typ: typ, off: off, size: size, hdr: hdr})
		off += size
	}
	return out, nil
}

// mp4Plan 是「moov 前置」后的虚拟文件布局：[pre][mdat 数据]
// pre = 原文件开头到 mdat 之前（ftyp/free）+ 打过偏移补丁的 moov + mdat 自己的盒子头
type mp4Plan struct {
	pre     []byte
	mdatOff int64 // 原文件中 mdat 数据的起点
	mdatLen int64
}

func (p *mp4Plan) size() int64 { return int64(len(p.pre)) + p.mdatLen }

// 容器盒子的类型，需要递归进去找 stco / co64
var mp4Containers = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "edts": true, "udta": true, "dinf": true,
}

// patchChunkOffsets 把 buf[start:end] 里所有 stco/co64 记录的 chunk 偏移加上 delta。
func patchChunkOffsets(buf []byte, start, end, delta int64) error {
	for off := start; off+8 <= end; {
		size := int64(binary.BigEndian.Uint32(buf[off : off+4]))
		typ := string(buf[off+4 : off+8])
		hdr := int64(8)
		switch size {
		case 0:
			size = end - off
		case 1:
			if off+16 > end {
				return errUnsupportedMP4
			}
			size = int64(binary.BigEndian.Uint64(buf[off+8 : off+16]))
			hdr = 16
		}
		if size < hdr || off+size > end {
			return errUnsupportedMP4
		}
		switch typ {
		case "stco", "co64":
			if size < hdr+8 {
				return errUnsupportedMP4
			}
			count := int64(binary.BigEndian.Uint32(buf[off+hdr+4 : off+hdr+8]))
			entry := int64(4)
			if typ == "co64" {
				entry = 8
			}
			if hdr+8+count*entry > size {
				return errUnsupportedMP4
			}
			for i := int64(0); i < count; i++ {
				p := off + hdr + 8 + i*entry
				if typ == "stco" {
					v := int64(binary.BigEndian.Uint32(buf[p : p+4]))
					nv := v + delta
					if nv < 0 || nv > math.MaxUint32 {
						return errUnsupportedMP4
					}
					binary.BigEndian.PutUint32(buf[p:p+4], uint32(nv))
				} else {
					v := int64(binary.BigEndian.Uint64(buf[p : p+8]))
					nv := v + delta
					if nv < 0 {
						return errUnsupportedMP4
					}
					binary.BigEndian.PutUint64(buf[p:p+8], uint64(nv))
				}
			}
		case "mvex":
			// 分片 MP4 的偏移在 moof 里，这里不处理
			return errUnsupportedMP4
		default:
			if mp4Containers[typ] {
				if err := patchChunkOffsets(buf, off+hdr, off+size, delta); err != nil {
					return err
				}
			}
		}
		off += size
	}
	return nil
}

// planFaststart 只接受「单个 mdat 且位于 moov 之前、非分片」的普通 MP4，其余一律返回错误由调用方退回原文件。
func planFaststart(f *os.File, size int64) (*mp4Plan, error) {
	boxes, err := readMP4Boxes(f, 0, size)
	if err != nil {
		return nil, err
	}
	var ftyp, mdat, moov *mp4Box
	for i := range boxes {
		b := &boxes[i]
		switch b.typ {
		case "ftyp":
			if ftyp == nil {
				ftyp = b
			}
		case "mdat":
			if mdat != nil {
				return nil, errUnsupportedMP4 // 多个 mdat 时偏移增量不统一
			}
			mdat = b
		case "moov":
			if moov == nil {
				moov = b
			}
		}
	}
	if ftyp == nil || mdat == nil || moov == nil || moov.off < mdat.off {
		return nil, errUnsupportedMP4
	}

	head := make([]byte, mdat.off)
	if _, err := io.ReadFull(io.NewSectionReader(f, 0, mdat.off), head); err != nil {
		return nil, err
	}
	moovBuf := make([]byte, moov.size)
	if _, err := io.ReadFull(io.NewSectionReader(f, moov.off, moov.size), moovBuf); err != nil {
		return nil, err
	}

	// mdat 自己的盒子头也要带上，否则拼出来的不是合法 MP4
	mdatHdr := make([]byte, mdat.hdr)
	if _, err := io.ReadFull(io.NewSectionReader(f, mdat.off, mdat.hdr), mdatHdr); err != nil {
		return nil, err
	}
	plan := &mp4Plan{mdatOff: mdat.off + mdat.hdr, mdatLen: mdat.size - mdat.hdr}
	plan.pre = make([]byte, 0, len(head)+len(moovBuf)+len(mdatHdr))
	plan.pre = append(append(append(plan.pre, head...), moovBuf...), mdatHdr...)

	delta := int64(len(plan.pre)) - plan.mdatOff
	if delta != 0 {
		if err := patchChunkOffsets(plan.pre, int64(len(head))+moov.hdr, int64(len(plan.pre)-int(mdat.hdr)), delta); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// faststartCache 缓存解析结果（head + 打过补丁的 moov），避免每个 Range 请求都重新读一遍。
// moov 可能有几 MB，所以按条目数和总字节数双重限制。
const (
	faststartMaxEntries = 24
	faststartMaxBytes   = 64 << 20
)

type faststartEntry struct {
	key   string
	plan  *mp4Plan
	at    time.Time
	bytes int
}

var faststartMu sync.Mutex
var faststartItems []*faststartEntry // 按插入顺序，超出上限时丢最早的

func faststartKey(full string, size int64, mod time.Time) string {
	return full + "|" + time.Duration(size).String() + "|" + mod.UTC().Format(time.RFC3339Nano)
}

// faststartPlan 返回虚拟 faststart 方案；文件本来就在前面时返回 (nil, nil)，表示照原样输出。
func faststartPlan(full string, fi os.FileInfo, f *os.File) (*mp4Plan, error) {
	key := faststartKey(full, fi.Size(), fi.ModTime())
	faststartMu.Lock()
	for _, e := range faststartItems {
		if e.key == key {
			plan := e.plan
			faststartMu.Unlock()
			return plan, nil
		}
	}
	faststartMu.Unlock()

	plan, err := planFaststart(f, fi.Size())
	if err != nil {
		return nil, err
	}
	bytes := len(plan.pre)
	faststartMu.Lock()
	faststartItems = append(faststartItems, &faststartEntry{key: key, plan: plan, at: time.Now(), bytes: bytes})
	for len(faststartItems) > faststartMaxEntries {
		faststartItems = faststartItems[1:]
	}
	total := 0
	for _, e := range faststartItems {
		total += e.bytes
	}
	for total > faststartMaxBytes && len(faststartItems) > 1 {
		total -= faststartItems[0].bytes
		faststartItems = faststartItems[1:]
	}
	faststartMu.Unlock()
	return plan, nil
}

// faststartReader 让 ServeContent 看到一个「索引在前」的虚拟文件，数据仍从原文件读。
type faststartReader struct {
	f   *os.File
	p   *mp4Plan
	pos int64
}

func (r *faststartReader) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.pos + offset
	case io.SeekEnd:
		next = r.p.size() + offset
	}
	if next < 0 {
		return 0, errors.New("seek 越界")
	}
	r.pos = next
	return next, nil
}

func (r *faststartReader) Read(b []byte) (int, error) {
	n, err := r.ReadAt(b, r.pos)
	r.pos += int64(n)
	return n, err
}

func (r *faststartReader) ReadAt(b []byte, off int64) (int, error) {
	total := r.p.size()
	if off >= total {
		return 0, io.EOF
	}
	if max := total - off; int64(len(b)) > max {
		b = b[:max]
	}
	preLen := int64(len(r.p.pre))
	n := 0
	// 前半段（ftyp + moov + mdat 头）直接在内存里拷，剩下的从原文件 mdat 读
	if off < preLen {
		upTo := min64(preLen, off+int64(len(b)))
		n += copy(b, r.p.pre[off:upTo])
	}
	if n < len(b) {
		src := r.p.mdatOff + (off + int64(n) - preLen)
		m, err := r.f.ReadAt(b[n:], src)
		n += m
		if err != nil && n < len(b) {
			return n, err
		}
	}
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

package unpack

import (
	"archive/zip"
	"compress/bzip2"
	"compress/flate"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"io/fs"
)

// ZIP 里的压缩方法编号（标准库只导出了 Store 和 Deflate）
const methodBzip2 uint16 = 12

// zipArchive 用标准库读 ZIP。标准库不支持加密条目：加密数据用 OpenRaw
// 取出来自己解密（见 zipcrypto.go / zipaes.go），再按原来的压缩方法解压。
type zipArchive struct {
	rc      *zip.ReadCloser
	pw      string
	entries []Entry
}

func openZip(full, password string) (Archive, error) {
	rc, err := zip.OpenReader(full)
	if err != nil {
		if errors.Is(err, zip.ErrFormat) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrUnsupportedFormat
		}
		return nil, err
	}
	a := &zipArchive{rc: rc, pw: password}
	for _, f := range rc.File {
		a.entries = append(a.entries, Entry{
			Name:    f.Name,
			IsDir:   f.FileInfo().IsDir(),
			Size:    int64(f.UncompressedSize64),
			ModTime: f.Modified,
			Symlink: f.Mode()&fs.ModeSymlink != 0,
		})
	}
	// 有加密条目就先把密码验一次：只读一个字节，很快。
	// 这样「要不要密码」「密码对不对」在开始解压之前就确定了。
	if err := a.checkPassword(); err != nil {
		rc.Close()
		return nil, err
	}
	return a, nil
}

func (a *zipArchive) Entries() []Entry { return a.entries }
func (a *zipArchive) Close() error     { return a.rc.Close() }

func (a *zipArchive) checkPassword() error {
	for i, f := range a.rc.File {
		if f.Flags&0x1 == 0 {
			continue
		}
		if a.pw == "" {
			return ErrNeedPassword
		}
		rc, err := a.Open(i)
		if err != nil {
			return err
		}
		// 空文件读到 EOF 也算密码正确：校验值在开头的 12 字节里已经查过
		_, err = io.CopyN(io.Discard, rc, 1)
		rc.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	}
	return nil
}

func (a *zipArchive) Open(i int) (io.ReadCloser, error) {
	f := a.rc.File[i]
	if f.Flags&0x1 == 0 {
		return f.Open()
	}
	if a.pw == "" {
		return nil, ErrNeedPassword
	}
	return openEncrypted(f, a.pw)
}

// openEncrypted 解密一个加密条目：WinZip AES 和传统的 ZipCrypto 各走一条路。
func openEncrypted(f *zip.File, pw string) (io.ReadCloser, error) {
	raw, err := f.OpenRaw()
	if err != nil {
		return nil, err
	}
	var (
		dec     io.Reader
		method  = f.Method
		checkCR = true
	)
	if info, ok := parseAESExtra(f); ok {
		method, checkCR = info.method, info.version == 1 // AE-2 不写 CRC
		dec, err = newAESReader(raw, int64(f.CompressedSize64), info, pw)
	} else {
		dec, err = newZipCryptoReader(raw, f, pw)
	}
	if err != nil {
		return nil, err
	}
	r, err := decompress(dec, method)
	if err != nil {
		return nil, err
	}
	if checkCR {
		r = &crcReader{r: r, h: crc32.NewIEEE(), want: f.CRC32}
	}
	return io.NopCloser(r), nil
}

// decompress 按 ZIP 的压缩方法解压。0/8/12 覆盖了几乎所有压缩包。
func decompress(r io.Reader, method uint16) (io.Reader, error) {
	switch method {
	case zip.Store:
		return r, nil
	case zip.Deflate:
		return flate.NewReader(r), nil
	case methodBzip2:
		return bzip2.NewReader(r), nil
	}
	return nil, ErrUnsupportedFormat
}

// crcReader 读完核对 CRC32：密码不对或文件损坏时明确报错，
// 而不是把一堆乱码写进共享目录。
type crcReader struct {
	r    io.Reader
	h    hash.Hash32
	want uint32
	done bool
}

func (c *crcReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.h.Write(p[:n])
	}
	if errors.Is(err, io.EOF) && !c.done {
		c.done = true
		if c.h.Sum32() != c.want {
			return n, ErrBadData
		}
	}
	return n, err
}

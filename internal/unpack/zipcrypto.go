package unpack

import (
	"archive/zip"
	"hash/crc32"
	"io"
)

// 传统 ZIP 加密（PKWARE ZipCrypto）：3 个滚动密钥 + CRC32 查表。
// 密文开头 12 字节是加密头，最后一个字节是校验值（1/256 会误判，
// 剩下的交给 CRC32 兜底），其余部分按字节流解密。
type zipCrypto struct{ k0, k1, k2 uint32 }

func newZipCrypto(pw []byte) *zipCrypto {
	z := &zipCrypto{0x12345678, 0x23456789, 0x34567890}
	for _, b := range pw {
		z.update(b)
	}
	return z
}

func (z *zipCrypto) update(b byte) {
	z.k0 = crc32.IEEETable[byte(z.k0)^b] ^ (z.k0 >> 8)
	z.k1 = (z.k1+uint32(byte(z.k0)))*134775813 + 1
	z.k2 = crc32.IEEETable[byte(z.k2)^byte(z.k1>>24)] ^ (z.k2 >> 8)
}

func (z *zipCrypto) magic() byte {
	t := z.k2 | 2
	return byte((t * (t ^ 1)) >> 8)
}

// newZipCryptoReader 读掉 12 字节加密头并校验密码，返回解密剩余字节的流。
func newZipCryptoReader(raw io.Reader, f *zip.File, pw string) (io.Reader, error) {
	z := newZipCrypto([]byte(pw))
	hdr := make([]byte, 12)
	if _, err := io.ReadFull(raw, hdr); err != nil {
		return nil, ErrBadData
	}
	var last byte
	for _, c := range hdr {
		p := c ^ z.magic()
		z.update(p)
		last = p
	}
	// 写了数据描述符（流式写入）的包用修改时间的高字节做校验
	want := byte(f.CRC32 >> 24)
	if f.Flags&0x8 != 0 {
		want = byte(f.ModifiedTime >> 8)
	}
	if last != want {
		return nil, ErrWrongPassword
	}
	return &zipCryptoReader{r: raw, z: z}, nil
}

type zipCryptoReader struct {
	r io.Reader
	z *zipCrypto
}

func (c *zipCryptoReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	for i := 0; i < n; i++ {
		v := p[i] ^ c.z.magic()
		c.z.update(v)
		p[i] = v
	}
	return n, err
}

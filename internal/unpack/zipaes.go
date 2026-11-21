package unpack

import (
	"archive/zip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"hash"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

// WinZip AES（AE-1 / AE-2）：salt + 2 字节密码校验值 + AES-CTR 密文 + 10 字节 HMAC-SHA1。
// 计数器是小端，Go 标准库的 CTR 是大端，所以自己实现。
const aesExtraID = 0x9901

type aesExtra struct {
	version  uint16
	strength byte
	method   uint16
}

func parseAESExtra(f *zip.File) (aesExtra, bool) {
	for extra := f.Extra; len(extra) >= 4; {
		id := binary.LittleEndian.Uint16(extra)
		size := int(binary.LittleEndian.Uint16(extra[2:]))
		if len(extra) < 4+size {
			break
		}
		if id == aesExtraID && size >= 7 {
			d := extra[4 : 4+size]
			return aesExtra{
				version:  binary.LittleEndian.Uint16(d),
				strength: d[4],
				method:   binary.LittleEndian.Uint16(d[5:]),
			}, true
		}
		extra = extra[4+size:]
	}
	return aesExtra{}, false
}

func newAESReader(raw io.Reader, compSize int64, info aesExtra, pw string) (io.Reader, error) {
	var keyLen int
	switch info.strength {
	case 1:
		keyLen = 16
	case 2:
		keyLen = 24
	case 3:
		keyLen = 32
	default:
		return nil, ErrUnsupportedFormat
	}
	saltLen := keyLen / 2
	head := make([]byte, saltLen+2)
	if _, err := io.ReadFull(raw, head); err != nil {
		return nil, ErrBadData
	}
	// PBKDF2(HMAC-SHA1, 1000 轮)：加密密钥 + 校验密钥 + 2 字节校验值
	keys := pbkdf2.Key([]byte(pw), head[:saltLen], 1000, keyLen*2+2, sha1.New)
	if !hmac.Equal(keys[keyLen*2:keyLen*2+2], head[saltLen:]) {
		return nil, ErrWrongPassword
	}
	dataLen := compSize - int64(saltLen) - 2 - 10
	if dataLen < 0 {
		return nil, ErrBadData
	}
	block, err := aes.NewCipher(keys[:keyLen])
	if err != nil {
		return nil, ErrBadData
	}
	mac := hmac.New(sha1.New, keys[keyLen:keyLen*2])
	return &aesReader{
		raw:    raw,
		src:    io.TeeReader(io.LimitReader(raw, dataLen), mac),
		ctr:    newLECTR(block),
		mac:    mac,
		remain: dataLen,
	}, nil
}

type aesReader struct {
	raw      io.Reader // 密文之后还有 10 字节 HMAC
	src      io.Reader // 密文（读的同时算 HMAC）
	ctr      *leCTR
	mac      hash.Hash
	remain   int64
	verified bool
	err      error
}

func (a *aesReader) Read(p []byte) (int, error) {
	if a.err != nil {
		return 0, a.err
	}
	if a.remain == 0 {
		if err := a.verify(); err != nil {
			a.err = err
			return 0, err
		}
		a.err = io.EOF
		return 0, io.EOF
	}
	if int64(len(p)) > a.remain {
		p = p[:a.remain]
	}
	n, err := a.src.Read(p)
	if n > 0 {
		a.ctr.XORKeyStream(p[:n], p[:n])
		a.remain -= int64(n)
	}
	if err != nil && err != io.EOF {
		a.err = err
		return n, err
	}
	if a.remain > 0 && err == io.EOF {
		a.err = ErrBadData
		return n, a.err
	}
	if a.remain == 0 {
		if verr := a.verify(); verr != nil {
			a.err = verr
			return n, verr
		}
	}
	return n, err
}

// verify 核对 HMAC：数据被改过或密码不对时，解出来的内容是错的但不一定解不开。
func (a *aesReader) verify() error {
	if a.verified {
		return nil
	}
	a.verified = true
	tag := make([]byte, 10)
	if _, err := io.ReadFull(a.raw, tag); err != nil {
		return ErrBadData
	}
	if !hmac.Equal(tag, a.mac.Sum(nil)[:10]) {
		return ErrBadData
	}
	return nil
}

// leCTR 是 Windows 版 ZIP 用的 AES-CTR：16 字节小端计数器，从 1 开始。
type leCTR struct {
	block cipher.Block
	ctr   [16]byte
	buf   [16]byte
	used  int // buf 里已经用掉的字节数，16 表示需要重新生成
}

func newLECTR(b cipher.Block) *leCTR {
	c := &leCTR{block: b, used: 16}
	c.ctr[0] = 1
	return c
}

func (c *leCTR) XORKeyStream(dst, src []byte) {
	if len(dst) < len(src) {
		panic("unpack: output smaller than input")
	}
	for i := range src {
		if c.used >= 16 {
			c.block.Encrypt(c.buf[:], c.ctr[:])
			for j := 0; j < len(c.ctr); j++ {
				c.ctr[j]++
				if c.ctr[j] != 0 {
					break
				}
			}
			c.used = 0
		}
		dst[i] = src[i] ^ c.buf[c.used]
		c.used++
	}
}

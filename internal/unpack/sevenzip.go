package unpack

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/bodgit/sevenzip"
)

const sevenZipSignature = "7z\xBC\xAF\x27\x1C"

// 7z 没有写文件头，只能靠开头 6 个字节认出来
func has7zSignature(full string) bool {
	f, err := os.Open(full)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, len(sevenZipSignature))
	if _, err := io.ReadFull(f, buf); err != nil {
		return false
	}
	return string(buf) == sevenZipSignature
}

type sevenArchive struct {
	rc      *sevenzip.ReadCloser
	pw      string
	entries []Entry
}

func open7z(full, password string) (Archive, error) {
	if !has7zSignature(full) {
		return nil, ErrUnsupportedFormat
	}
	rc, err := sevenzip.OpenReaderWithPassword(full, password)
	if err != nil {
		return nil, sevenErr(err, password)
	}
	a := &sevenArchive{rc: rc, pw: password}
	for _, f := range rc.File {
		info := f.FileInfo()
		a.entries = append(a.entries, Entry{
			Name:    f.Name,
			IsDir:   info.IsDir(),
			Size:    int64(f.UncompressedSize),
			ModTime: f.Modified,
			Symlink: info.Mode()&fs.ModeSymlink != 0,
		})
	}
	// 不在打开时试读：文件头没加密的包，试读要把整个压缩块解开才知道密码对不对。
	// 密码不对会在真正解压时以 ErrNeedPassword / ErrWrongPassword 报出来。
	return a, nil
}

func (a *sevenArchive) Entries() []Entry { return a.entries }
func (a *sevenArchive) Close() error     { return a.rc.Close() }

func (a *sevenArchive) Open(i int) (io.ReadCloser, error) {
	rc, err := a.rc.File[i].Open()
	if err != nil {
		return nil, sevenErr(err, a.pw)
	}
	// 密码不对时错误是读数据时才出现的，包一层把错误翻译成人话
	return &sevenReader{rc: rc, pw: a.pw}, nil
}

type sevenReader struct {
	rc io.ReadCloser
	pw string
}

func (r *sevenReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if err != nil && err != io.EOF {
		return n, sevenErr(err, r.pw)
	}
	return n, err
}

func (r *sevenReader) Close() error { return r.rc.Close() }

// sevenErr 把库里的错误分成「要密码」「密码错」「包坏了」三种。
func sevenErr(err error, password string) error {
	var re *sevenzip.ReadError
	if errors.As(err, &re) && re.Encrypted {
		if password == "" {
			return ErrNeedPassword
		}
		return ErrWrongPassword
	}
	return fmt.Errorf("%w（%v）", ErrBadData, err)
}

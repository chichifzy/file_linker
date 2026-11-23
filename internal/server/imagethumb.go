package server

import (
	"bufio"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"

	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// 图片缩略图：列表里原来直接用原图当图标，几 MB 的照片只为显示 40px，
// 下载和解码都慢，还会占满浏览器连接。这里解码后缩到 thumbWidth 存成 jpg，
// 和视频缩略图共用缓存目录与 key 规则。

const (
	imageThumbMaxPixels = 100_000_000 // 超过一亿像素不解码，防止单张图吃掉几百 MB 内存
	imageThumbQuality   = 80
)

// 解码吃 CPU 和内存，限制同时解码的张数
var imageThumbSem = make(chan struct{}, max(2, runtime.NumCPU()/2))

func (s *Server) serveImageThumb(w http.ResponseWriter, r *http.Request, full, key, dst string) {
	if !fileExists(dst) {
		defer thumbLock(key)()
		if !fileExists(dst) {
			select {
			case imageThumbSem <- struct{}{}:
			case <-r.Context().Done():
				return
			}
			err := makeImageThumb(full, dst)
			<-imageThumbSem
			if err != nil {
				// svg、avif 等解不了的格式，前端收到错误后改用原图
				slog.Debug("生成图片缩略图失败", "file", full, "err", err)
				writeError(w, http.StatusUnprocessableEntity, "无法生成缩略图")
				return
			}
		}
	}
	serveThumbFile(w, r, dst)
}

func makeImageThumb(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(bufio.NewReader(f))
	if err != nil {
		return err
	}
	if int64(cfg.Width)*int64(cfg.Height) > imageThumbMaxPixels {
		return errors.New("图片尺寸过大")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	orient := jpegOrientation(f)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	img, _, err := image.Decode(bufio.NewReader(f))
	if err != nil {
		return err
	}

	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return errors.New("空图片")
	}
	// 方向 5-8 宽高互换：先按显示后的宽度算缩放比
	dispW, dispH := sw, sh
	if orient >= 5 {
		dispW, dispH = sh, sw
	}
	tw := min(thumbWidth, dispW)
	th := max(1, dispH*tw/dispW)
	if orient >= 5 {
		tw, th = th, tw
	}

	// 先铺白底：透明的 png/gif 编码成 jpg 后不会变成黑底
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(out, out.Bounds(), img, b, draw.Over, nil)
	final := applyOrientation(out, orient)

	tmp := dst + ".part"
	o, err := os.Create(tmp)
	if err != nil {
		return err
	}
	err = jpeg.Encode(o, final, &jpeg.Options{Quality: imageThumbQuality})
	if cerr := o.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// applyOrientation 按 EXIF 方向把图片转正。Go 的解码器不处理 EXIF，
// 不转的话手机竖拍的照片在缩略图里是横的，和点开后浏览器显示的不一致。
func applyOrientation(src *image.RGBA, o int) image.Image {
	if o < 2 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.SetRGBA(dx, dy, src.RGBAAt(x, y))
		}
	}
	return dst
}

// jpegOrientation 从 JPEG 的 APP1/Exif 段读出 Orientation（0x0112），读不到返回 1。
func jpegOrientation(r io.Reader) int {
	br := bufio.NewReader(r)
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xFF, 0xD8} {
		return 1
	}
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil || hdr[0] != 0xFF {
			return 1
		}
		marker := hdr[1]
		size := int(binary.BigEndian.Uint16(hdr[2:])) - 2
		if marker == 0xDA || marker == 0xD9 || size < 0 { // 到了图像数据，后面不会再有 Exif
			return 1
		}
		if marker != 0xE1 {
			if _, err := br.Discard(size); err != nil {
				return 1
			}
			continue
		}
		seg := make([]byte, size)
		if _, err := io.ReadFull(br, seg); err != nil {
			return 1
		}
		if len(seg) < 14 || string(seg[:6]) != "Exif\x00\x00" {
			continue
		}
		return exifOrientation(seg[6:])
	}
}

func exifOrientation(t []byte) int {
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for i := 0; i < n; i++ {
		p := off + 2 + i*12
		if p+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[p:]) == 0x0112 {
			if v := int(bo.Uint16(t[p+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

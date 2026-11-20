package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// 视频缩略图：用 ffmpeg 抽一帧存成 jpg，之后直接返回缓存文件。
//
// 之前的做法是在列表里放 <video preload="metadata">，浏览器会为每个视频各开一个
// 连接去读文件头。HTTP/1.1 下同一个来源最多 6 个连接，一个目录里几十个大视频
// 就能把连接占满，点开某个视频时的请求要排在它们后面；再加上几十个随机读把
// 磁盘打散，表现就是「目录里视频多，点开特别慢，缩略图也一直转」。
// 换成几十 KB 的 jpg 后，单个请求瞬间结束、不再占着连接，缩略图本身也从
// 「读视频文件头并解码一帧」变成「读一个小图」，第二次进入目录基本是瞬时的。

const (
	thumbWidth    = 320                // 缩略图宽度，高度按比例
	thumbQuality  = 4                  // mjpeg 质量，4 约等于 90%
	thumbMaxCount = 5000               // 缓存文件数上限，超出后按最旧删除
	thumbTimeout  = 20 * time.Second   // 单个视频抽帧的超时
	thumbWorkers  = 2                  // 抽帧要读磁盘，并发太高会让磁盘来回寻道
	thumbMaxAge   = 7 * 24 * time.Hour // 浏览器端缓存时长
)

var (
	thumbRoot string
	thumbSem  = make(chan struct{}, thumbWorkers)

	// 同一个文件被并发请求时只抽一次帧
	thumbMu     sync.Mutex
	thumbFlight = map[string]chan struct{}{}
)

// 抽取时间点：跳过片头黑屏/台标，太短的视频退回到第 0 秒。
var thumbSeekPoints = []string{"3", "0"}

func initThumbs() {
	root, err := os.UserCacheDir()
	if err != nil {
		slog.Warn("无法定位缓存目录，视频缩略图关闭", "err", err)
		return
	}
	dir := filepath.Join(root, "file_linker", "thumbnails")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("无法创建缩略图目录，视频缩略图关闭", "dir", dir, "err", err)
		return
	}
	thumbRoot = dir
	pruneThumbs()
}

// pruneThumbs 清理上次中断留下的半成品，并在数量过多时删掉最旧的。
func pruneThumbs() {
	ents, err := os.ReadDir(thumbRoot)
	if err != nil {
		return
	}
	type item struct {
		name string
		mod  time.Time
	}
	var files []item
	for _, e := range ents {
		p := filepath.Join(thumbRoot, e.Name())
		if e.IsDir() || filepath.Ext(e.Name()) != ".jpg" {
			os.RemoveAll(p)
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, item{e.Name(), fi.ModTime()})
	}
	if len(files) <= thumbMaxCount {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files[:len(files)-thumbMaxCount] {
		os.Remove(filepath.Join(thumbRoot, f.name))
	}
}

// serveThumb 返回视频的第一帧（jpg）。生成过就一直是缓存文件，源文件改动后 key 变化会重新生成。
func (s *Server) serveThumb(w http.ResponseWriter, r *http.Request) {
	if thumbRoot == "" {
		writeError(w, http.StatusServiceUnavailable, "缩略图功能不可用")
		return
	}
	_, full, _, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	if fileKind(fi.Name()) != "video" {
		writeError(w, http.StatusBadRequest, "只有视频有缩略图")
		return
	}
	key := mediaKey(full, fi.Size(), fi.ModTime().Unix())
	dst := filepath.Join(thumbRoot, key+".jpg")
	if !fileExists(dst) {
		if ffmpegPath() == "" {
			writeError(w, http.StatusServiceUnavailable, "没有安装 ffmpeg，无法生成缩略图")
			return
		}
		// 等待同一个文件的另一次生成，避免重复抽帧
		defer thumbLock(key)()
		if !fileExists(dst) {
			if err := makeThumb(r.Context(), ffmpegPath(), full, dst); err != nil {
				slog.Warn("生成缩略图失败", "file", full, "err", err)
				writeError(w, http.StatusUnprocessableEntity, "无法生成缩略图")
				return
			}
		}
	}
	serveThumbFile(w, r, dst)
}

func makeThumb(ctx context.Context, ff, src, dst string) error {
	var lastErr error
	for _, ss := range thumbSeekPoints {
		select {
		case thumbSem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		tmp := dst + ".part"
		cctx, cancel := context.WithTimeout(ctx, thumbTimeout)
		cmd := exec.CommandContext(cctx, ff, "-hide_banner", "-loglevel", "error",
			"-ss", ss, "-i", src,
			"-frames:v", "1",
			"-vf", fmt.Sprintf("scale=%d:-2", thumbWidth),
			"-q:v", strconv.Itoa(thumbQuality),
			"-f", "mjpeg", "-y", tmp,
		)
		err := cmd.Run()
		cancel()
		<-thumbSem

		if fi, statErr := os.Stat(tmp); statErr == nil && fi.Size() > 0 {
			if err := os.Rename(tmp, dst); err != nil {
				os.Remove(tmp)
				return err
			}
			return nil
		}
		os.Remove(tmp)
		if err != nil {
			lastErr = err
		} else {
			lastErr = errors.New("没有取到画面")
		}
	}
	return lastErr
}

// thumbLock 让同一个 key 的生成串行执行；后来的请求等前一个完成。
func thumbLock(key string) func() {
	for {
		thumbMu.Lock()
		if wait, ok := thumbFlight[key]; ok {
			thumbMu.Unlock()
			<-wait
			continue // 前一个已完成，重新判断也许已经有缓存了
		}
		done := make(chan struct{})
		thumbFlight[key] = done
		thumbMu.Unlock()
		return func() {
			thumbMu.Lock()
			delete(thumbFlight, key)
			thumbMu.Unlock()
			close(done)
		}
	}
}

func serveThumbFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "缩略图不存在")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusNotFound, "缩略图不存在")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("X-Content-Type-Options", "nosniff")
	// 文件名带内容哈希（路径+大小+修改时间），同一地址内容不会变
	h.Set("Cache-Control", fmt.Sprintf("private, max-age=%d", int(thumbMaxAge.Seconds())))
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

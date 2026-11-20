package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 视频转码缓存：浏览器吃不下 AV1/HEVC 时，用 ffmpeg 转成 H.264。
//
// 为了不让用户盯着进度条等几分钟，转码不产出一个大文件再播放，而是输出 HLS
// 分段（index.m3u8 + seg_00001.ts…）。ffmpeg 的转码速度是播放速度的几十倍，
// 首个分段几十毫秒就能生成，前端拿到播放列表就能开始播，转码在后面追着跑。
//
// 转完后再把分段流拷贝成一个 mp4（不变编码，几秒钟），这样下次打开可以直接
// 拖动进度条。缓存是持久的，服务重启后仍然有效；默认不限制容量，一直保留到
// 用户在缓存列表里手动删除，只有显式配置 -cache-gb 时才按最久未使用淘汰。

const (
	mediaMetaName = "meta.json"  // 每个条目的元数据
	mediaListName = "index.m3u8" // HLS 播放列表
	mediaMP4Name  = "out.mp4"    // 转完后合成的完整文件
	mediaSegSec   = 4            // 单个分段时长，越短首帧越快
	mediaIdleSec  = 60           // 转完后 HLS 空闲多久就清理（前端会在完成时切到 mp4）
	mediaDefLimit = 20 << 30     // 未配置上限时的兜底值（仅在 SetMediaCache 未被调用时生效）
)

var (
	mediaLimit atomic.Int64
	// mediaLimit 无法区分「没配置」和「显式配置为 0（不限制）」，单独记一个标记
	mediaLimitSet atomic.Bool

	mediaMu      sync.Mutex
	mediaEntries = map[string]*mediaEntry{}

	// 转码吃满 CPU，串行执行，避免把机器拖垮
	mediaSem = make(chan struct{}, 1)
)

var segNameRe = regexp.MustCompile(`^seg_\d{5}\.ts$`)

// mediaEntry 是一条转码缓存。带 json 标签的字段会写入 meta.json 持久保存。
type mediaEntry struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`      // 文件名
	ShareID   int64   `json:"shareId"`   // 来源分享，仅用于展示
	Rel       string  `json:"rel"`       // 相对共享目录的路径
	SrcSize   int64   `json:"srcSize"`   // 源文件大小
	SrcMod    int64   `json:"srcMod"`    // 源文件修改时间（UnixNano），变了就重新转
	Duration  float64 `json:"duration"`  // 源视频时长（秒）
	Status    string  `json:"status"`    // running / done / error
	Mode      string  `json:"mode"`      // copy（只换封装）/ encode（重新编码）
	Err       string  `json:"err,omitempty"`
	Bytes     int64   `json:"bytes"`     // 缓存占用字节数
	Created   int64   `json:"created"`   // Unix 秒
	Accessed  int64   `json:"accessed"`  // Unix 秒，LRU 依据

	percent   atomic.Int64 // 0-100
	readySec  atomic.Int64 // 已转码到的秒数
	readyAt   atomic.Int64 // 首次拿到有效播放列表的时间，0 表示还没准备好
	lastHLS   atomic.Int64 // 最近一次 HLS 请求（Unix 毫秒）
	lastGood  atomic.Value // 上次解析成功的播放列表内容
	cancel    context.CancelFunc
}

// SetMediaCache 配置缓存目录与容量上限。dir 为空时用系统缓存目录；
// limitBytes <= 0 表示不限制容量（缓存永久保留，只由用户手动删除）。
func SetMediaCache(dir string, limitBytes int64) {
	if dir != "" {
		// 命令行传相对路径时按当前目录补全，避免服务从别处启动后路径对不上
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	setMediaDirRoot(dir)
	mediaLimit.Store(limitBytes)
	mediaLimitSet.Store(true)
}

func mediaEntryDir(id string) string {
	return filepath.Join(mediaDir(), id)
}

func mediaEntryFile(id, name string) string {
	return filepath.Join(mediaDir(), id, name)
}

// mediaPlaySize 是当前实际播放的那个文件的大小，前端用「已缓冲比例 × 大小」估算下载速度。
// 转码完成时 e.Bytes 还包含尚未清理的 HLS 分段（接近成品两倍），所以这里单独取 mp4。
func mediaPlaySize(id string) int64 {
	if fi, err := os.Stat(mediaEntryFile(id, mediaMP4Name)); err == nil {
		return fi.Size()
	}
	return 0
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// dirSize 统计目录占用，失败时返回已统计到的部分。
func dirSize(dir string) int64 {
	var total int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		if fi, err := de.Info(); err == nil {
			total += fi.Size()
		}
	}
	return total
}

// initTranscode 恢复上次的缓存索引，丢弃没转完的残留（分段不完整，无法继续），
// 然后启动清理协程。
func (s *Server) initTranscode() {
	if mediaDir() == "" {
		// 网页上改过的目录记在 settings 里；命令行 -cache-dir 已经在
		// SetMediaCache 里设过，优先级更高，这里不会再覆盖
		dir := ""
		if s != nil && s.db != nil {
			d, err := s.savedMediaDir(context.Background())
			if err != nil {
				slog.Warn("读取缓存目录设置失败，用默认目录", "err", err)
			}
			dir = d
		}
		if dir == "" {
			dir = defaultMediaDir()
		}
		setMediaDirRoot(dir)
	}
	root := mediaDir()
	// 只有从未显式配置过上限时才用默认值。显式传 0 表示不限制，
	// 不能在这里被 20 GiB 覆盖掉，否则「永久保留」失效。
	if !mediaLimitSet.Load() {
		mediaLimit.Store(mediaDefLimit)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		slog.Warn("无法创建转码缓存目录", "dir", root, "err", err)
		return
	}

	dirents, err := os.ReadDir(root)
	if err != nil {
		slog.Warn("无法读取转码缓存目录", "dir", root, "err", err)
		return
	}
	restored, dropped := 0, 0
	for _, de := range dirents {
		id := de.Name()
		dir := filepath.Join(root, id)
		// 丢弃时记录原因，否则缓存「莫名消失」时无从排查
		drop := func(reason string, args ...any) {
			slog.Warn("丢弃转码缓存："+reason, args...)
			os.RemoveAll(dir)
			dropped++
		}
		if !de.IsDir() || !isHexID(id) {
			drop("目录名不是缓存 ID", "dir", de.Name())
			continue
		}
		e := &mediaEntry{}
		b, err := os.ReadFile(filepath.Join(dir, mediaMetaName))
		if err != nil {
			drop("缺少 meta.json", "id", id)
			continue
		}
		if err := json.Unmarshal(b, e); err != nil {
			drop("meta.json 损坏", "id", id, "err", err)
			continue
		}
		if e.Status != "done" {
			// 服务在转码中途退出：分段不完整，无法接着转，只能重新来过
			drop("上次没有转完", "id", id, "name", e.Name, "status", e.Status)
			continue
		}
		if !fileExists(filepath.Join(dir, mediaMP4Name)) {
			drop("成品 mp4 缺失", "id", id, "name", e.Name)
			continue
		}
		e.ID = id
		e.Bytes = dirSize(dir)
		mediaEntries[id] = e
		restored++
	}
	limitText := "不限制"
	if l := mediaLimit.Load(); l > 0 {
		limitText = fmtBytes(l)
	}
	slog.Info("转码缓存已载入", "dir", root,
		"restored", restored, "dropped", dropped,
		"used", fmtBytes(totalBytesLocked()), "limit", limitText)

	go mediaJanitor()
}

// ffmpegPath 优先用 PATH 里的 ffmpeg，找不到时尝试常见安装位置。
func ffmpegPath() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	for _, p := range []string{
		`C:\ffmpeg\bin\ffmpeg.exe`,
		`C:\Program Files\ffmpeg\bin\ffmpeg.exe`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\WinGet\Links\ffmpeg.exe`),
	} {
		if fileExists(p) {
			return p
		}
	}
	// winget 安装的包目录带版本号（ffmpeg-9.1-full_build），用通配符找
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		pattern := filepath.Join(local, "Microsoft", "WinGet", "Packages",
			"Gyan.FFmpeg_*", "ffmpeg-*", "bin", "ffmpeg.exe")
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

// mediaKey 由文件绝对路径、大小和修改时间生成，保证同一份内容只转一次；
// 源文件被替换后 key 会变，自然重新转码。
func mediaKey(full string, size int64, modUnix int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", full, size, modUnix)))
	return hex.EncodeToString(sum[:8])
}

func isHexID(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// totalBytesLocked 汇总缓存占用，调用前需持有 mediaMu。
func totalBytesLocked() int64 {
	var total int64
	for _, e := range mediaEntries {
		total += e.Bytes
	}
	return total
}

// evictLocked 在超出容量上限时按最久未使用淘汰已完成的条目。调用前需持有 mediaMu。
// 正在转码的条目不会被动，keep 指定的条目也会跳过。
func evictLocked(keep string) {
	limit := mediaLimit.Load()
	if limit <= 0 {
		return
	}
	var done []*mediaEntry
	for _, e := range mediaEntries {
		if e.Status == "done" && e.ID != keep {
			done = append(done, e)
		}
	}
	total := totalBytesLocked()
	if total <= limit {
		return
	}
	sort.Slice(done, func(i, j int) bool { return done[i].Accessed < done[j].Accessed })
	for _, e := range done {
		if total <= limit {
			break
		}
		slog.Info("淘汰转码缓存", "name", e.Name, "bytes", e.Bytes)
		delete(mediaEntries, e.ID)
		os.RemoveAll(mediaEntryDir(e.ID))
		total -= e.Bytes
	}
}

// removeHLS 删除转码中间产物（分段与播放列表），只留下合成好的 mp4。
// 返回释放的字节数。
func removeHLS(e *mediaEntry) int64 {
	var freed int64
	matches, _ := filepath.Glob(mediaEntryFile(e.ID, "seg_*.ts"))
	matches = append(matches, mediaEntryFile(e.ID, mediaListName))
	for _, p := range matches {
		if fi, err := os.Stat(p); err == nil {
			freed += fi.Size()
		}
		os.Remove(p)
	}
	// 播放列表已经删掉，缓存的旧内容不能再返回，否则播放器会去拉已删除的分段
	e.lastGood.Store("")
	return freed
}

// mediaJanitor 定期回收转码完成后的分段，并检查容量上限。
func mediaJanitor() {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for range tick.C {
		mediaMu.Lock()
		now := time.Now().UnixMilli()
		for _, e := range mediaEntries {
			if e.Status == "running" {
				// 转码中的条目也要反映真实占用，缓存列表里才看得见
				e.Bytes = dirSize(mediaEntryDir(e.ID))
				continue
			}
			if e.Status != "done" {
				continue
			}
			// 还有人在播分段时不能删
			if now-e.lastHLS.Load() < mediaIdleSec*1000 {
				continue
			}
			if freed := removeHLS(e); freed > 0 {
				e.Bytes = dirSize(mediaEntryDir(e.ID))
				e.saveMeta()
			}
		}
		evictLocked("")
		mediaMu.Unlock()
	}
}

// saveMeta 把条目写入 meta.json。调用前需持有 mediaMu。
func (e *mediaEntry) saveMeta() {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	if err := os.WriteFile(mediaEntryFile(e.ID, mediaMetaName), b, 0o644); err != nil {
		slog.Warn("写入转码元数据失败", "id", e.ID, "err", err)
	}
}

// touch 记录访问时间，供 LRU 使用。
func (e *mediaEntry) touch() {
	mediaMu.Lock()
	e.Accessed = time.Now().Unix()
	mediaMu.Unlock()
}

// readPlaylist 读取并校验播放列表。ffmpeg 是边写边更新的，可能读到半截内容，
// 这时退回上一次解析成功的版本，避免给浏览器一个残缺的列表。
func (e *mediaEntry) readPlaylist() (string, error) {
	b, err := os.ReadFile(mediaEntryFile(e.ID, mediaListName))
	if err == nil {
		if txt := string(b); validPlaylist(txt) {
			if e.readyAt.Load() == 0 {
				e.readyAt.Store(time.Now().UnixNano())
			}
			e.lastGood.Store(txt)
			return txt, nil
		}
	}
	if txt, _ := e.lastGood.Load().(string); txt != "" {
		return txt, nil
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("播放列表尚未生成")
}

// validPlaylist 用「以换行结束」判断内容是否写完整，再确认至少有一个分段。
func validPlaylist(s string) bool {
	return strings.HasPrefix(s, "#EXTM3U") &&
		strings.Contains(s, "#EXTINF") &&
		strings.HasSuffix(s, "\n")
}

// lookupTranscode 查询是否已有现成的转码缓存，并顺带给出容器与编码信息。
// 前端在开始播放之前先问一次：
//   - 命中缓存就直接播成品，不再「先播原文件、失败后才提示转码」；
//   - 没命中时看返回的 encoding，AV1/HEVC 这类浏览器解不了的可以立刻提示，
//     不用等它把数据拉下来、尝试解码几十秒后才报错。
//
// 改过缓存目录（缓存已迁到新目录）之后，这一步也照样能认出老缓存。
func (s *Server) lookupTranscode(w http.ResponseWriter, r *http.Request) {
	// 没有缓存时把探测结果一起带回去。探测有缓存且 mp4 只读文件头尾，开销很小
	miss := func(full string) {
		resp := map[string]any{"cached": false}
		if full != "" {
			if info := probeMedia(full); info != nil {
				resp["encoding"] = info
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
	_, full, _, err := s.resolve(r)
	if err != nil {
		miss("")
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		miss("")
		return
	}
	id := mediaKey(full, fi.Size(), fi.ModTime().UnixNano())
	mediaMu.Lock()
	e := mediaEntries[id]
	status, mode := "", ""
	if e != nil {
		status, mode = e.Status, e.Mode
	}
	mediaMu.Unlock()
	// 失败/不存在都当作没有缓存；正在转的交给正常流程（同一条目，可以边转边播）
	if status != "done" && status != "running" {
		miss(full)
		return
	}
	bytes := int64(0)
	if status == "done" {
		// 状态是 done 但成品被手动删掉时不算命中
		if bytes = mediaPlaySize(id); bytes <= 0 {
			miss(full)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cached": true, "id": id, "status": status, "mode": mode, "bytes": bytes,
	})
}

// createTranscode 为指定视频创建（或复用）转码任务。
func (s *Server) createTranscode(w http.ResponseWriter, r *http.Request) {
	if mediaMoving.Load() {
		writeError(w, http.StatusConflict, "缓存目录正在迁移，请几秒后再试")
		return
	}
	_, full, rel, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	if ffmpegPath() == "" {
		writeError(w, http.StatusServiceUnavailable,
			"运行服务的电脑上没有安装 ffmpeg，无法转换编码")
		return
	}
	shareID, _ := strconv.ParseInt(r.URL.Query().Get("share"), 10, 64)
	id := mediaKey(full, fi.Size(), fi.ModTime().UnixNano())

	mediaMu.Lock()
	if e := mediaEntries[id]; e != nil {
		// 失败过的任务允许重试
		if e.Status != "error" {
			status := e.Status
			bytes := e.Bytes
			stale := false
			if status == "done" {
				if sz := mediaPlaySize(id); sz > 0 {
					bytes = sz
				} else {
					// 记录说转完了，但成品文件不见了（目录被手动清过、磁盘被清理）：
					// 不能当命中，否则前端会去播一个 404 的地址，只能重新转一遍
					stale = true
				}
			}
			if !stale {
				mode := e.Mode
				name := e.Name
				mediaMu.Unlock()
				slog.Info("转码缓存命中", "file", name, "id", id, "status", status)
				writeJSON(w, http.StatusOK, map[string]any{
					"id": id, "status": status, "mode": mode, "ready": e.isReady(), "bytes": bytes,
				})
				return
			}
			slog.Warn("缓存成品缺失，重新转码", "id", id, "name", e.Name)
		}
		delete(mediaEntries, id)
	}
	mediaMu.Unlock()

	select {
	case mediaSem <- struct{}{}:
	default:
		writeError(w, http.StatusServiceUnavailable, "服务器正在转换其他视频，请稍后再试")
		return
	}

	dir := mediaEntryDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		<-mediaSem
		internalError(w, r, err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	// 编码本身浏览器就吃不下（AV1/HEVC）时必须重编码；只是容器不对（mkv/avi/flv）
	// 而编码是 H.264 时，换封装就够了：1.2GB 的片子几秒就能转完，且画质无损。
	mode := "encode"
	if info := probeMedia(full); !info.Playable && info.Remuxable {
		mode = "copy"
	}
	e := &mediaEntry{
		ID: id, Name: filepath.Base(full), ShareID: shareID, Rel: rel,
		SrcSize: fi.Size(), SrcMod: fi.ModTime().UnixNano(),
		Status: "running", Mode: mode, Created: time.Now().Unix(), Accessed: time.Now().Unix(),
		cancel: cancel,
	}
	if mode == "copy" {
		slog.Info("容器不可直接播放，改用无损封装", "file", fi.Name())
	} else {
		slog.Info("新建转码任务", "file", fi.Name(), "id", id)
	}
	mediaMu.Lock()
	mediaEntries[id] = e
	e.saveMeta()
	mediaMu.Unlock()

	go func() {
		defer func() { <-mediaSem }()
		runErr := runHLS(ctx, full, dir, e, mode)
		// 无损封装失败（比如音轨是 TS 装不下的编码）：清掉重来，退回重新编码
		if runErr != nil && mode == "copy" && ctx.Err() == nil {
			slog.Warn("无损封装失败，改为重新编码", "src", full, "err", runErr)
			mediaMu.Lock()
			e.Mode = "encode"
			e.percent.Store(0)
			e.readySec.Store(0)
			mediaMu.Unlock()
			os.RemoveAll(dir)
			if err := os.MkdirAll(dir, 0o755); err == nil {
				mode = "encode"
				runErr = runHLS(ctx, full, dir, e, mode)
			}
		}
		if runErr == nil {
			// 分段转完后合成 mp4：不变编码，只需几秒，下次打开可以直接拖动
			runErr = muxToMP4(ctx, dir)
		}

		mediaMu.Lock()
		defer mediaMu.Unlock()
		if runErr != nil {
			slog.Warn("转码失败", "src", full, "err", runErr)
			e.Status = "error"
			e.Err = "转码失败：" + runErr.Error()
			if ctx.Err() != nil {
				e.Err = "转码已取消"
			}
			e.cancel = nil
			// 半成品没有价值，直接释放空间；条目保留，前端才能看到失败原因
			os.RemoveAll(dir)
			e.Bytes = 0
			return
		}
		e.Status = "done"
		e.percent.Store(100)
		e.cancel = nil
		e.Bytes = dirSize(dir)
		e.saveMeta()
		evictLocked(id)
	}()

	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "status": "running", "mode": mode, "ready": false, "bytes": 0,
	})
}

// isReady 表示播放列表已经可以读取，前端可以开始播放了。
func (e *mediaEntry) isReady() bool {
	return e.readyAt.Load() != 0
}

// runHLS 调用 ffmpeg 生成 HLS 分段，同时用 -progress 解析进度。
// mode 为 "copy" 时只换封装（源文件编码已经可播，只是容器不对），不重编码。
func runHLS(ctx context.Context, src, dir string, e *mediaEntry, mode string) error {
	args := []string{"-hide_banner", "-y", "-i", src}
	if mode == "copy" {
		// MKV 里常带字幕/附件，mpegts 装不下，只取一路视频和一路音频
		args = append(args, "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy")
	} else {
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "128k")
	}
	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(mediaSegSec),
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_segment_filename", filepath.Join(dir, "seg_%05d.ts"),
		"-progress", "pipe:1", "-nostats",
		filepath.Join(dir, mediaListName))
	cmd := exec.CommandContext(ctx, ffmpegPath(), args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var (
		total    float64 // 源视频总时长（秒），从 stderr 的 Duration 行获取
		totalMu  sync.Mutex
		errLines []string
		wg       sync.WaitGroup
	)
	getTotal := func() float64 {
		totalMu.Lock()
		defer totalMu.Unlock()
		return total
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if d := parseFFDuration(line); d > 0 {
				totalMu.Lock()
				if total == 0 {
					total = d
					mediaMu.Lock()
					e.Duration = d
					mediaMu.Unlock()
				}
				totalMu.Unlock()
			}
			low := strings.ToLower(line)
			if strings.Contains(low, "error") || strings.Contains(low, "invalid") || strings.Contains(low, "failed") {
				errLines = append(errLines, strings.TrimSpace(line))
				if len(errLines) > 3 {
					errLines = errLines[1:]
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			v, ok := strings.CutPrefix(sc.Text(), "out_time_us=")
			if !ok {
				continue
			}
			us, err := strconv.ParseInt(v, 10, 64)
			if err != nil || us < 0 {
				continue
			}
			sec := us / 1e6
			e.readySec.Store(sec)
			if d := getTotal(); d > 0 {
				p := int64(float64(sec) / d * 100)
				if p > 99 {
					p = 99 // 100 留给真正结束
				}
				e.percent.Store(p)
			}
		}
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		if len(errLines) > 0 {
			return fmt.Errorf("%s", strings.Join(errLines, "；"))
		}
		return err
	}
	return nil
}

// muxToMP4 把 HLS 分段流拷贝成 mp4（不重新编码），并前置 moov 以便快速起播。
func muxToMP4(ctx context.Context, dir string) error {
	out := filepath.Join(dir, mediaMP4Name)
	args := []string{
		"-hide_banner", "-y",
		"-i", filepath.Join(dir, mediaListName),
		"-c", "copy",
		"-movflags", "+faststart",
		"-f", "mp4", out,
	}
	cmd := exec.CommandContext(ctx, ffmpegPath(), args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// 合成失败不影响播放，分段仍然可用
		slog.Warn("合成 mp4 失败", "dir", dir, "err", err)
		os.Remove(out)
		return nil
	}
	return nil
}

// parseFFDuration 从 ffmpeg 的 "Duration: 00:01:23.45, start: ..." 行解析总时长。
func parseFFDuration(line string) float64 {
	i := strings.Index(line, "Duration: ")
	if i < 0 {
		return 0
	}
	s := strings.TrimSpace(line[i+len("Duration: "):])
	if c := strings.IndexByte(s, ','); c >= 0 {
		s = s[:c]
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h, _ := strconv.ParseFloat(parts[0], 64)
	m, _ := strconv.ParseFloat(parts[1], 64)
	sec, _ := strconv.ParseFloat(parts[2], 64)
	return h*3600 + m*60 + sec
}

// transcodeStatus 查询转码进度。ready 为真时前端就可以开始播放了。
func (s *Server) transcodeStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mediaMu.Lock()
	e := mediaEntries[id]
	if e == nil {
		mediaMu.Unlock()
		writeError(w, http.StatusNotFound, "转码任务不存在或已被清除")
		return
	}
	status, msg, dur, bytes, mode := e.Status, e.Err, e.Duration, e.Bytes, e.Mode
	mediaMu.Unlock()
	if status == "done" {
		if sz := mediaPlaySize(id); sz > 0 {
			bytes = sz
		}
	}

	if status == "running" && !e.isReady() {
		// 顺手探测首个分段，省掉一个后台轮询协程
		e.readPlaylist()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       status,
		"error":        msg,
		"mode":         mode,
		"percent":      e.percent.Load(),
		"ready":        e.isReady(),
		"duration":     dur,
		"bytes":        bytes,
		"readySeconds": e.readySec.Load(),
	})
}

// serveHLSFile 输出播放列表或分段。分段支持 Range，浏览器会并发预取。
func (s *Server) serveHLSFile(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	if !isHexID(id) {
		writeError(w, http.StatusNotFound, "缓存不存在")
		return
	}
	mediaMu.Lock()
	e := mediaEntries[id]
	mediaMu.Unlock()
	if e == nil {
		writeError(w, http.StatusNotFound, "缓存不存在或已被清除")
		return
	}
	e.lastHLS.Store(time.Now().UnixMilli())

	if name == mediaListName {
		txt, err := e.readPlaylist()
		if err != nil {
			writeError(w, http.StatusConflict, "正在准备视频数据，请稍候")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache, no-store")
		io.WriteString(w, txt)
		return
	}
	if !segNameRe.MatchString(name) {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	f, err := os.Open(mediaEntryFile(id, name))
	if err != nil {
		writeError(w, http.StatusNotFound, "分段不存在")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取分段")
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// serveTranscode 输出转码完成后合成的完整 mp4，支持 Range 以便拖动进度条。
func (s *Server) serveTranscode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isHexID(id) {
		writeError(w, http.StatusNotFound, "缓存不存在")
		return
	}
	full := mediaEntryFile(id, mediaMP4Name)
	f, err := os.Open(full)
	if err != nil {
		mediaMu.Lock()
		e := mediaEntries[id]
		status := ""
		if e != nil {
			status = e.Status
		}
		mediaMu.Unlock()
		if status == "running" {
			writeError(w, http.StatusConflict, "视频还没有转换完成")
		} else {
			writeError(w, http.StatusNotFound, "转码文件不存在，请重新转换")
		}
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取转码文件")
		return
	}
	if e := lookupMedia(id); e != nil {
		e.touch()
		e.lastHLS.Store(time.Now().UnixMilli())
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, id+".mp4", fi.ModTime(), f)
}

func lookupMedia(id string) *mediaEntry {
	mediaMu.Lock()
	defer mediaMu.Unlock()
	return mediaEntries[id]
}

// mediaItem 是返回给前端的缓存条目。只给出分享内的相对路径，
// 不暴露运行服务的电脑上的绝对路径。
type mediaItem struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	ShareID    int64   `json:"shareId"`
	ShareName  string  `json:"shareName"`
	Rel        string  `json:"rel"`
	SrcSize    int64   `json:"srcSize"`
	Duration   float64 `json:"duration"`
	Status     string  `json:"status"`
	Mode       string  `json:"mode"`
	Error      string  `json:"error,omitempty"`
	Bytes      int64   `json:"bytes"`
	Percent    int64   `json:"percent"`
	Created    int64   `json:"created"`
	Accessed   int64   `json:"accessed"`
}

// listMedia 列出转码缓存。
func (s *Server) listMedia(w http.ResponseWriter, r *http.Request) {
	names := map[int64]string{}
	if list, err := s.shares(r); err == nil {
		for _, sh := range list {
			names[sh.ID] = sh.Name
		}
	}
	mediaMu.Lock()
	items := make([]mediaItem, 0, len(mediaEntries))
	var used int64
	for _, e := range mediaEntries {
		used += e.Bytes
		percent := e.percent.Load()
		if e.Status == "done" {
			percent = 100
		}
		items = append(items, mediaItem{
			ID: e.ID, Name: e.Name, ShareID: e.ShareID, ShareName: names[e.ShareID],
			Rel: e.Rel, SrcSize: e.SrcSize, Duration: e.Duration, Status: e.Status, Mode: e.Mode,
			Error: e.Err, Bytes: e.Bytes, Percent: percent,
			Created: e.Created, Accessed: e.Accessed,
		})
	}
	mediaMu.Unlock()
	sort.Slice(items, func(i, j int) bool { return items[i].Created > items[j].Created })
	// 缓存目录是运行服务的电脑上的绝对路径，和共享目录一样只给能改它的管理员
	editable := s.mediaDirEditable(r)
	resp := map[string]any{
		"entries": items, "used": used, "limit": mediaLimit.Load(),
		"editable": editable,
	}
	if editable {
		resp["dir"] = mediaDir()
		resp["defaultDir"] = defaultMediaDir()
	}
	writeJSON(w, http.StatusOK, resp)
}

// deleteMedia 删除一条缓存；正在转码的会先取消任务。
func (s *Server) deleteMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isHexID(id) {
		writeError(w, http.StatusNotFound, "缓存不存在")
		return
	}
	mediaMu.Lock()
	e := mediaEntries[id]
	if e == nil {
		mediaMu.Unlock()
		writeError(w, http.StatusNotFound, "缓存不存在")
		return
	}
	cancel := e.cancel
	delete(mediaEntries, id)
	mediaMu.Unlock()

	if cancel != nil {
		cancel()
	}
	os.RemoveAll(mediaEntryDir(id))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// clearMedia 清空全部缓存（正在转码的任务会被取消）。
func (s *Server) clearMedia(w http.ResponseWriter, r *http.Request) {
	mediaMu.Lock()
	var dirs []string
	for id, e := range mediaEntries {
		if e.cancel != nil {
			e.cancel()
		}
		dirs = append(dirs, mediaEntryDir(id))
		delete(mediaEntries, id)
	}
	mediaMu.Unlock()

	for _, d := range dirs {
		os.RemoveAll(d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(dirs)})
}

// StopTranscodes 取消所有正在进行的转码，供服务退出时调用。
// 否则 ffmpeg 子进程会在父进程结束后变成孤儿，继续吃 CPU 往已被清理的目录里写文件。
func StopTranscodes() {
	mediaMu.Lock()
	defer mediaMu.Unlock()
	for _, e := range mediaEntries {
		if e.cancel != nil {
			e.cancel()
			e.cancel = nil
		}
	}
}

// fmtBytes 人类可读的字节数，只用于日志。
func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

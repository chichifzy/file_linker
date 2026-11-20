package server

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 容器嗅探与媒体信息探测。
//
// 起因：有些文件的扩展名是 .mp4，实际是 Matroska(MKV) 封装。服务端原来只按扩展名
// 给 Content-Type: video/mp4，iOS Safari 按 MP4 解析这个文件却永远解析不出索引，
// 于是退化成不带 Range 的整段下载——1.2GB 的片子下了三十多秒才报错。
//
// 所以在输出前先按文件头判断真实容器：不可播放的容器直接回 415，前端拿到
// 容器与编码信息后决定走「无损封装（-c copy，几秒）」还是「重新编码」。

// mediaInfo 是一个视频文件的容器与编码信息，字段直接给前端用。
type mediaInfo struct {
	Container string  `json:"container"` // mp4 / mov / webm / matroska / avi / flv / mpegts / ogg / unknown
	Codec     string  `json:"codec"`     // H.264 / HEVC / AV1 / VP9 / MPEG-4 / …
	Audio     string  `json:"audio"`     // AAC / MP3 / Opus / FLAC / …
	Duration  float64 `json:"duration"`  // 秒，只有跑过 ffmpeg 探测才有
	Playable  bool    `json:"playable"`  // 浏览器能否直接播放这个容器
	Remuxable bool    `json:"remuxable"` // 只换封装（-c copy）就能变成可播的 mp4
}

const probeCacheLimit = 256

var (
	probeMu     sync.Mutex
	probeCache  = map[string]*mediaInfo{} // key: 绝对路径|大小|修改时间
	probeFlight = map[string]chan struct{}{}
)

func probeKey(full string, fi os.FileInfo) string {
	return full + "|" + strconv.FormatInt(fi.Size(), 10) + "|" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
}

// sniffContainer 只读文件头判断容器。读不到或认不出时返回 "unknown"。
func sniffContainer(full string) string {
	f, err := os.Open(full)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	return sniffOpened(f)
}

// sniffOpened 用于已经打开了文件的场景（文件服务里每个 Range 请求都会走一次），
// 用 ReadAt 读 64 字节，不影响文件当前的读取位置。
func sniffOpened(f *os.File) string {
	head := make([]byte, 64)
	n, _ := f.ReadAt(head, 0)
	return sniffHead(head[:n])
}

// sniffHead 是纯函数版本，便于在没有文件句柄时复用。
func sniffHead(head []byte) string {
	switch {
	case len(head) >= 8 && string(head[4:8]) == "ftyp":
		// 第 8~12 字节是 major brand
		if len(head) >= 12 && bytes.HasPrefix(head[8:12], []byte("qt")) {
			return "mov"
		}
		return "mp4"
	case len(head) >= 4 && bytes.Equal(head[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
		// EBML：webm 与 mkv 共用文件头，靠 DocType 区分
		if bytes.Contains(head, []byte("webm")) {
			return "webm"
		}
		return "matroska"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "AVI ":
		return "avi"
	case len(head) >= 4 && bytes.HasPrefix(head, []byte("FLV\x01")):
		return "flv"
	case len(head) >= 4 && bytes.HasPrefix(head, []byte("OggS")):
		return "ogg"
	case len(head) >= 1 && head[0] == 0x47 && (len(head) < 189 || head[188] == 0x47):
		return "mpegts"
	case len(head) >= 4 && bytes.Equal(head[:4], []byte{0x00, 0x00, 0x01, 0xBA}):
		return "mpegps"
	}
	return "unknown"
}

// containerPlayable 判断浏览器能否直接播放该容器。
// mkv/avi/flv/ts 在 iOS Safari 上一律不支持；认不出的容器不做拦截，避免误伤。
func containerPlayable(container string) bool {
	switch container {
	case "matroska", "avi", "flv", "mpegts", "mpegps":
		return false
	}
	return true
}

// probeMedia 返回文件的容器与编码信息，结果按「路径+大小+修改时间」缓存。
// 容器不是 mp4 时用 ffmpeg 读一次文件头；ffmpeg 不在就只给出容器。
func probeMedia(full string) *mediaInfo {
	fi, err := os.Stat(full)
	if err != nil {
		return &mediaInfo{Container: "unknown", Playable: true}
	}
	key := probeKey(full, fi)

	probeMu.Lock()
	if c := probeCache[key]; c != nil {
		probeMu.Unlock()
		return c
	}
	// 同一个文件的并发探测合并成一次，避免重复起 ffmpeg
	if wait, ok := probeFlight[key]; ok {
		probeMu.Unlock()
		<-wait
		probeMu.Lock()
		c := probeCache[key]
		probeMu.Unlock()
		if c != nil {
			return c
		}
		return &mediaInfo{Container: sniffContainer(full), Playable: true}
	}
	done := make(chan struct{})
	probeFlight[key] = done
	probeMu.Unlock()

	info := buildMediaInfo(full)

	probeMu.Lock()
	if len(probeCache) >= probeCacheLimit {
		// 缓存只用于展示与决策，清空比淘汰更简单
		probeCache = map[string]*mediaInfo{}
	}
	probeCache[key] = info
	delete(probeFlight, key)
	close(done)
	probeMu.Unlock()
	return info
}

func buildMediaInfo(full string) *mediaInfo {
	info := &mediaInfo{Container: sniffContainer(full)}
	info.Playable = containerPlayable(info.Container)
	if info.Container == "mp4" || info.Container == "mov" {
		info.Codec = videoCodec(full) // MP4 用 sample entry 标识，够了
	} else if p := ffmpegPath(); p != "" {
		probeWithFFmpeg(p, full, info)
	}
	info.Remuxable = !info.Playable && info.Codec == "H.264" &&
		(info.Audio == "" || info.Audio == "AAC" || info.Audio == "MP3")
	return info
}

// probeWithFFmpeg 用 `ffmpeg -i` 的输出补全编码、音频与时长。
// ffmpeg 只给输入不给输出时必定以「At least one output file must be specified」退出，
// 所以这里不把退出码当失败，只看 stderr 里解析到了什么。
func probeWithFFmpeg(bin, full string, info *mediaInfo) {
	cmd := exec.Command(bin, "-hide_banner", "-i", full)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 10 * time.Second
	_ = cmd.Run()

	for _, line := range strings.Split(stderr.String(), "\n") {
		line = strings.TrimSpace(line)
		if d := parseFFDuration(line); d > 0 && info.Duration == 0 {
			info.Duration = d
		}
		i := strings.Index(line, "Stream #")
		if i < 0 {
			continue
		}
		// 形如 "Stream #0:0: Video: h264 (High), yuv420p, 1920x1080, 30 fps"
		if k := strings.Index(line[i:], "Video: "); k >= 0 {
			rest := line[i+k+len("Video: "):]
			name := strings.TrimSpace(strings.SplitN(rest, ",", 2)[0])
			name = strings.SplitN(name, " ", 2)[0] // 去掉 "(High)" 之类的 profile
			if info.Codec == "" {
				info.Codec = codecDisplayName(name)
			}
		} else if k := strings.Index(line[i:], "Audio: "); k >= 0 {
			rest := line[i+k+len("Audio: "):]
			name := strings.TrimSpace(strings.SplitN(rest, ",", 2)[0])
			name = strings.SplitN(name, " ", 2)[0]
			if info.Audio == "" {
				info.Audio = codecDisplayName(name)
			}
		}
	}
	if info.Codec != "" || info.Audio != "" {
		slog.Debug("媒体探测", "file", filepath.Base(full),
			"container", info.Container, "video", info.Codec, "audio", info.Audio)
	}
}

// codecDisplayName 把 ffmpeg 的编码名换成前端展示用的名字。
func codecDisplayName(name string) string {
	switch strings.ToLower(name) {
	case "h264":
		return "H.264"
	case "hevc", "h265":
		return "HEVC"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	case "vp8":
		return "VP8"
	case "mpeg4":
		return "MPEG-4"
	case "theora":
		return "Theora"
	case "aac":
		return "AAC"
	case "mp3":
		return "MP3"
	case "opus":
		return "Opus"
	case "flac":
		return "FLAC"
	case "vorbis":
		return "Vorbis"
	case "ac3":
		return "AC-3"
	case "eac3":
		return "E-AC-3"
	case "pcm_s16le", "pcm_s24le":
		return "PCM"
	}
	return name
}

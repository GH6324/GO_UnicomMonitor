package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
)

//go:embed static/*
var staticFS embed.FS // 静态文件

// ==================== HTTP 网站服务 ====================

// StartHttp 启动网站服务
func StartHttp(config *Config) {
	// 解析用户名和密码
	username := strings.Split(config.User, ":")[0]
	password := strings.Split(config.User, ":")[1]
	// 静态目录需要认证
	http.HandleFunc("/", basicAuth(func(w http.ResponseWriter, r *http.Request) {
		subFS, _ := fs.Sub(staticFS, "static")
		http.FileServer(http.FS(subFS)).ServeHTTP(w, r)
	}, username, password))
	// 文件列表需要认证
	http.HandleFunc("/files", basicAuth(func(w http.ResponseWriter, r *http.Request) {
		handleFileList(w, r, config.Path)
	}, username, password))
	// 文件内容需要认证
	http.HandleFunc("/get", basicAuth(func(w http.ResponseWriter, r *http.Request) {
		handleFileContent(w, r, config.Path)
	}, username, password))
	// 启动服务器
	http.ListenAndServe(config.Host, nil)
}

// 身份验证中间件
func basicAuth(next http.HandlerFunc, username, password string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	}
}

// 处理文件列表请求
func handleFileList(w http.ResponseWriter, r *http.Request, dirPath string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 获取文件列表
	files, err := listFiles(dirPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 返回文件列表
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

// 处理文件内容请求
func handleFileContent(w http.ResponseWriter, r *http.Request, dirPath string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 获取文件名
	filename := r.URL.Query().Get("file")
	if filename == "" {
		http.Error(w, "File parameter is required", http.StatusBadRequest)
		return
	}
	// 打开文件
	fullPath := filepath.Join(dirPath, filename)
	file, err := os.Open(fullPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()
	// 获取文件信息以设置Content-Length
	fileInfo, err := file.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 设置响应头
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))
	// 直接将文件流复制到响应流中
	http.ServeContent(w, r, filename, fileInfo.ModTime(), file)
}

// 获取文件列表
func listFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			// 将路径转换为相对路径
			relPath, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			files = append(files, relPath)
		}
		return nil
	})
	return files, err
}

// ==================== RTSP 转发服务 ====================

// 每个设备的转发数据
type forwardDevice struct {
	video          Video
	stream         *gortsplib.ServerStream
	media          *description.Media
	mu             sync.Mutex
	ready          bool
	vps, sps, pps  []byte
	encoder        *rtph265.Encoder
	rtspAddr       string
}

var forwardDevices = map[string]*forwardDevice{}
var forwardMu sync.RWMutex

// rtspHandler 实现 gortsplib v5 handler
type rtspHandler struct{}

// ForwardMode 启动 RTSP 转发模式
func StartRtsp(config *Config, videos []Video) {
	FmtPrint("启动转发模式")

	// 从配置提取 RTSP 地址
	addr := config.Host
	if !strings.HasPrefix(addr, ":") {
		addr = ":" + addr
	}

	// 初始化设备
	for i := range videos {
		fd := &forwardDevice{video: videos[i], rtspAddr: addr}
		forwardMu.Lock()
		forwardDevices[videos[i].Name] = fd
		forwardMu.Unlock()
	}

	// 创建 RTSP 服务
	server := &gortsplib.Server{
		Handler:      &rtspHandler{},
		RTSPAddress:  addr,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	if err := server.Start(); err != nil {
		FmtPrint("RTSP 服务启动失败: %v", err)
		return
	}
	defer server.Close()

	// FmtPrint(fmt.Sprintf("RTSP 服务: rtsp://%s:%s@%s/{设备名}", user, pass, addr))

	// 启动每个设备的 WebSocket 连接
	for i := range videos {
		go func(v *Video) {
			forwardMu.RLock()
			fd := forwardDevices[v.Name]
			forwardMu.RUnlock()
			runForwardStream(server, v, fd)
		}(&videos[i])
	}

	// 等待
	panic(server.Wait())
}

// OnDescribe 处理 RTSP DESCRIBE 请求
func (h *rtspHandler) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	path, _ := url.QueryUnescape(strings.TrimPrefix(ctx.Path, "/"))

	forwardMu.RLock()
	fd, ok := forwardDevices[path]
	forwardMu.RUnlock()

	if !ok || fd.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	// FmtPrint("RTSP DESCRIBE: %s", path)
	return &base.Response{StatusCode: base.StatusOK}, fd.stream, nil
}

// OnSetup 处理 RTSP SETUP 请求
func (h *rtspHandler) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	path, _ := url.QueryUnescape(strings.TrimPrefix(ctx.Path, "/"))

	forwardMu.RLock()
	fd, ok := forwardDevices[path]
	forwardMu.RUnlock()

	if !ok || fd.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	return &base.Response{StatusCode: base.StatusOK}, fd.stream, nil
}

// OnPlay 处理 RTSP PLAY 请求
func (h *rtspHandler) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	// FmtPrint("RTSP PLAY: %s", path)
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// createStream 创建 gortsplib ServerStream
func createStream(server *gortsplib.Server, fd *forwardDevice, video *Video, rtspAddr string) {
	desc := &description.Session{
		Medias: []*description.Media{{
			Type: description.MediaTypeVideo,
			Formats: []format.Format{&format.H265{
				PayloadTyp: 96,
				VPS:        fd.vps,
				SPS:        fd.sps,
				PPS:        fd.pps,
			}},
		}},
	}

	stream := &gortsplib.ServerStream{
		Server: server,
		Desc:   desc,
	}
	if err := stream.Initialize(); err != nil {
		FmtPrint(video.Name+" 创建流失败: %v", err)
		return
	}
	fd.stream = stream
	fd.media = desc.Medias[0]
	FmtPrint(video.Name+" 转发地址：rtsp://localhost%s/%s", rtspAddr, video.Name)
}

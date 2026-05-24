package main

import (
	"crypto/tls"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

// 开始录制
func GoRecording(config *Config, video *Video) {
	// 临时变量
	tempPath := config.Path + "/" + video.Name
	// 断开后重连
	for {
		// 连接服务器传输数据
		bytes := linkServer(video)
		// 检查数据
		if len(bytes) == 0 {
			FmtPrint(video.Name + " 连接失败，稍后重连(" + strconv.Itoa(config.Sleep) + "秒)")
			timeout := time.Duration(config.Sleep)
			time.Sleep(timeout * time.Second)
			continue
		}
		// 文件名称
		fileName := getFileName(tempPath) + ".hevc"
		// 保存文件
		saveFile(fileName, &bytes)
		// 录制完成
		FmtPrint(video.Name + " 录制完成：" + fileName)
	}
}

// 连接服务器
func linkServer(video *Video) []byte {
	bytes := []byte{}
	uri := url.URL{
		Scheme: "wss",
		Host:   video.WsHost,
		Path:   "/h5player/live",
	}
	// 跳过证书验证
	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	// 发起连接
	conn, _, err := dialer.Dial(uri.String(), nil)
	if err != nil {
		FmtPrint(video.Name+" 无法连接: %v", err)
		return bytes
	}
	defer conn.Close()

	// 发送消息
	paramMsg := BuildParamMsg(video.Token, video.DeviceId, video.ChannelNo, video.RelayServer, video.Name)
	message := "_paramStr_=" + paramMsg
	err = conn.WriteMessage(websocket.TextMessage, []byte(message))
	if err != nil {
		FmtPrint(video.Name+" 发送消息失败: %v", err)
		return bytes
	}
	FmtPrint(video.Name + " 已连接，开始录制")

	// 接收消息
	for {
		_, response, err := conn.ReadMessage()
		if err != nil {
			FmtPrint(video.Name+" 连接断开: %v", err)
			return bytes
		}
		// 检查特定条件
		if len(response) > 1 {
			// 打印数据的长度
			// FmtPrint("数据长度：", len(bytes))
			// 拼接数据
			bytes = append(bytes, response[:]...)
			// 结束条件
			if len(bytes) > 1024*1024*video.Size {
				// 结束
				return bytes
			}
		}
	}
}

// 获取文件名称
func getFileName(dirPath string) string {
	// 添加日期文件夹
	dateFolder := time.Now().Format("20060102")
	fullPath := dirPath + "/" + dateFolder
	// 检查文件夹是否存在
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		// 文件夹不存在，创建它
		err := os.MkdirAll(fullPath, 0755)
		if err != nil {
			FmtPrint("创建文件夹失败：", err)
			os.Exit(0)
		}
	}
	//文件名称
	fileName := time.Now().Format("150405")
	tempPathh := fullPath + "/" + fileName
	return tempPathh
}

// 保存文件
func saveFile(fileName string, bytes *[]byte) {
	file, err := os.OpenFile(fileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		FmtPrint("保存文件失败: ", err)
		os.Exit(0)
	}
	defer file.Close()
	file.Write(*bytes)
}

// 删除文件夹下的旧文件夹
func DeleteOldFiles(config *Config, video *Video) {
	// 临时变量
	dirPath := config.Path + "/" + video.Name
	foldersToKeep := video.Count
	// 读取文件夹
	var folders []fs.FileInfo
	entries, _ := os.ReadDir(dirPath)
	for _, entry := range entries {
		if entry.IsDir() {
			info, _ := os.Stat(filepath.Join(dirPath, entry.Name()))
			folders = append(folders, info)
		}
	}
	// 检查文件夹数量
	if len(folders) <= foldersToKeep {
		return
	}
	// 按时间排序
	sort.Slice(folders, func(i, j int) bool {
		return folders[i].ModTime().After(folders[j].ModTime())
	})
	// 删除最旧的文件夹
	for i := foldersToKeep; i < len(folders); i++ {
		oldFolder := filepath.Join(dirPath, folders[i].Name())
		_ = os.RemoveAll(oldFolder)
	}
}

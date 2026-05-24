package main

import "time"

// 主函数
func main() {
	FmtPrint("开源：https://github.com/zgcwkjOpenProject/GO_UnicomMonitor")
	FmtPrint("作者：zgcwkj")
	FmtPrint("版本：20260525_001")
	FmtPrint("请尊重开源协议，保留作者信息！")
	FmtPrint("")

	// 读取配置文件
	config, videos := GetConfig()
	if config.Path == "" {
		config.Path = "./"
	}

	// 自动发现设备: 有 token 但没配置 video 时自动获取
	if config.Token != "" && len(videos) == 0 {
		videos = AutoConfig(config.Token, config.Mobile)
		SaveVideoConfig(videos)
	}

	// 根据模式运行
	switch config.Mode {
	case "forward":
		// 转发模式
		RunForwardMode(&config, videos)

	default:
		// 录制模式 (默认)
		RunRecordMode(&config, videos)
	}
}

// RunRecordMode 录制模式
func RunRecordMode(config *Config, videos []Video) {
	// 启动录制协程
	FmtPrint("启动录制服务，存储路径：" + config.Path)
	for i := range videos {
		go GoRecording(config, &videos[i])
	}

	// 删除旧文件协程
	go func() {
		for {
			timeout := time.Duration(config.Sleep)
			time.Sleep(timeout * time.Second)
			for i := range videos {
				DeleteOldFiles(config, &videos[i])
			}
		}
	}()

	// 运行类型
	if config.Host == "" {
		// 后台运行
		for {
			FmtPrint("程序运行正常")
			timeout := time.Duration(config.Sleep)
			time.Sleep(timeout * time.Second)
		}
	} else {
		// 网站服务
		FmtPrint("启动网站服务：" + config.Host)
		StartHttp(config)
	}
}

// RunForwardMode 转发模式
func RunForwardMode(config *Config, videos []Video) {
	// 启动 RTSP 服务
	FmtPrint("启动 RTSP 服务：" + config.Host)
	StartRtsp(config, videos)
}

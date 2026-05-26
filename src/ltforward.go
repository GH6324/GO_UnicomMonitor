package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/gorilla/websocket"
)

func runForwardStream(server *gortsplib.Server, video *Video, fd *forwardDevice) {
	fd.encoder = &rtph265.Encoder{PayloadType: 96}
	fd.encoder.Init()

	for {
		forwardLoopWithStream(server, video, fd)
		FmtPrint(video.Name + " 连接断开，稍后重连")
		time.Sleep(3 * time.Second)
	}
}

func forwardLoopWithStream(server *gortsplib.Server, video *Video, fd *forwardDevice) {
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
	// 请求头
	headers := http.Header{}
	headers.Set("User-Agent", "ChinaUnicom/12.1200 (Android 16)")
	// 发起连接
	conn, _, err := dialer.Dial(uri.String(), headers)
	if err != nil {
		FmtPrint(video.Name+" 无法连接: %v", err)
		return
	}
	defer conn.Close()

	paramMsg := BuildParamMsg(video.Token, video.DeviceId, video.ChannelNo, video.RelayServer, video.Name)
	message := "_paramStr_=" + paramMsg
	err = conn.WriteMessage(websocket.TextMessage, []byte(message))
	if err != nil {
		FmtPrint(video.Name+" 发送消息失败: %v", err)
		return
	}
	FmtPrint(video.Name + " 已连接，开始转发")

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if len(data) <= 1 {
			continue
		}

		// 跳过 FLV 头消息
		if len(data) > 4 && data[1] == 'F' && data[2] == 'L' && data[3] == 'V' {
			continue
		}

		// 在前 300 字节内查找 URL 编码的 JSON
		limit := len(data)
		if limit > 300 {
			limit = 300
		}
		jsonStart := bytes.Index(data[:limit], []byte("%7B"))
		if jsonStart < 0 {
			continue
		}

		jsonEnd := bytes.Index(data[jsonStart:], []byte("%7D"))
		if jsonEnd < 0 {
			continue
		}
		jsonEndAbs := jsonStart + jsonEnd + 3

		// URL 解码 JSON
		jsonStr := jsUnescape(string(data[jsonStart:jsonEndAbs]))
		if len(jsonStr) == 0 || jsonStr[0] != '{' {
			continue
		}

		// 解析 JSON
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
			continue
		}

		// 处理 videoSPS 消息 (type 字段或 cmd 字段)
		if msg["type"] == "videoSPS" {
			handleVideoSPS(msg, fd, server, video)
			continue
		}

		// 处理 sync 或 cmd 消息 - JSON 后面跟视频数据
		if _, hasSync := msg["sync"]; hasSync {
			// sync=1 表示视频帧
			videoData := data[jsonEndAbs:]
			if len(videoData) > 10 {
				forwardHEVCData(videoData, fd, server, video)
			}
			continue
		}

		// 处理 cmd=1 (配置消息，带 videoEncodeType)
		if cmd, ok := msg["cmd"].(float64); ok && cmd == 1 {
			dataObj, _ := msg["data"].(map[string]interface{})
			if dataObj != nil {
				if videoType, ok := dataObj["videoEncodeType"].(float64); ok {
					if videoType == 1 {
						// FmtPrint("[%s]检测到 HEVC (H.265) 编码", video.Name)
					}
				}
			}
		}
	}
}

// handleVideoSPS 处理 videoSPS 消息，提取 SPS 数据
func handleVideoSPS(msg map[string]interface{}, fd *forwardDevice, server *gortsplib.Server, video *Video) {
	dataObj, _ := msg["data"].(map[string]interface{})
	if dataObj == nil {
		return
	}

	dataArr, _ := dataObj["data"].([]interface{})
	if len(dataArr) == 0 {
		return
	}

	// 将数组转换为字节 (SPS NALU)
	spsData := make([]byte, len(dataArr))
	for i, v := range dataArr {
		if n, ok := v.(float64); ok {
			spsData[i] = byte(n)
		}
	}

	fd.mu.Lock()
	if len(spsData) > 2 && len(fd.vps) > 0 && len(fd.pps) > 0 {
		fd.sps = spsData
		// 如果已经有 VPS 和 PPS，创建流
		if !fd.ready {
			createStream(server, fd, video, fd.rtspAddr)
			fd.ready = true
		}
	}
	fd.mu.Unlock()
}

// forwardHEVCData 从 Annex B 格式数据中提取 NAL 单元并转发
func forwardHEVCData(data []byte, fd *forwardDevice, server *gortsplib.Server, video *Video) {
	// 提取 Annex B NAL 单元
	nalus := extractAnnexBNalus(data)
	if len(nalus) == 0 {
		return
	}

	var accessUnit [][]byte
	fd.mu.Lock()

	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}

		// HEVC NALU 类型: (byte0 >> 1) & 0x3F
		// 32=VPS, 33=SPS, 34=PPS, 39/40=SEI, 1=IDR_W_RADL, 19=IDR_W_RPSL, others=slice
		nalType := (nalu[0] >> 1) & 0x3F

		switch nalType {
		case 32: // VPS
			fd.vps = nalu
		case 33: // SPS
			fd.sps = nalu
		case 34: // PPS
			fd.pps = nalu
			accessUnit = append(accessUnit, nalu) // 发送 PPS
		case 39, 40: // SEI
			accessUnit = append(accessUnit, nalu)
		case 1, 19, 20, 21: // VCL NAL 单元 (slice, IDR)
			accessUnit = append(accessUnit, nalu)
		}
	}

	// 检查是否需要创建流
	if !fd.ready && len(fd.vps) > 0 && len(fd.sps) > 0 && len(fd.pps) > 0 {
		FmtPrint(video.Name + " 已获取 VPS/SPS/PPS，创建 RTSP 流")
		createStream(server, fd, video, fd.rtspAddr)
		fd.ready = true
	}

	// 如果流已创建，先发送 VPS/SPS/PPS，再发送帧数据
	if fd.ready {
		// 发送参数集
		if len(fd.vps) > 0 {
			pkts, _ := fd.encoder.Encode([][]byte{fd.vps})
			for _, pkt := range pkts {
				fd.stream.WritePacketRTP(fd.media, pkt)
			}
		}
		if len(fd.sps) > 0 {
			pkts, _ := fd.encoder.Encode([][]byte{fd.sps})
			for _, pkt := range pkts {
				fd.stream.WritePacketRTP(fd.media, pkt)
			}
		}
		if len(fd.pps) > 0 {
			pkts, _ := fd.encoder.Encode([][]byte{fd.pps})
			for _, pkt := range pkts {
				fd.stream.WritePacketRTP(fd.media, pkt)
			}
		}
	}

	fd.mu.Unlock()

	// 发送访问单元 (VCL NAL 单元)
	if fd.ready && len(accessUnit) > 0 {
		pkts, _ := fd.encoder.Encode(accessUnit)
		for _, pkt := range pkts {
			fd.stream.WritePacketRTP(fd.media, pkt)
		}
	}
}

// extractAnnexBNalus 从 Annex B 格式数据中提取 NAL 单元列表
func extractAnnexBNalus(data []byte) [][]byte {
	var nalus [][]byte
	startCode := []byte{0, 0, 0, 1}

	i := 0
	for i < len(data)-4 {
		if bytes.HasPrefix(data[i:], startCode) {
			naluStart := i + 4

			// 查找下一个 start code
			nextStart := bytes.Index(data[naluStart:], startCode)
			var naluEnd int
			if nextStart < 0 {
				naluEnd = len(data)
			} else {
				naluEnd = naluStart + nextStart
			}

			if naluEnd > naluStart && naluEnd-naluStart >= 2 {
				nalus = append(nalus, data[naluStart:naluEnd])
			}

			i = naluEnd
		} else {
			i++
		}
	}

	return nalus
}

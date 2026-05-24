package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/url"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/gorilla/websocket"
)

// encoder 从 rtspHandler 的 OnPlay 回调中创建

// runForwardStream WebSocket 连接循环，创建 stream 后开始转发
func runForwardStream(server *gortsplib.Server, video *Video, fd *forwardDevice) {
	fd.encoder = &rtph264.Encoder{PayloadType: 96}
	fd.encoder.Init()

	for {
		forwardLoopWithStream(server, video, fd)
		FmtPrint(video.Name + " 转发断开，3秒后重连")
		time.Sleep(3 * time.Second)
	}
}

// forwardLoopWithStream 单次 WebSocket 连接，接收并处理数据
func forwardLoopWithStream(server *gortsplib.Server, video *Video, fd *forwardDevice) {
	uri := url.URL{Scheme: "wss", Host: video.WsHost, Path: "/h5player/live"}
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	conn, _, err := dialer.Dial(uri.String(), nil)
	if err != nil {
		FmtPrint(video.Name+" 转发连接失败: %v", err)
		return
	}
	defer conn.Close()

	paramMsg := BuildParamMsg(video.Token, video.DeviceId, video.ChannelNo, video.RelayServer, video.Name)
	if err := conn.WriteMessage(websocket.TextMessage, []byte("_paramStr_="+paramMsg)); err != nil {
		return
	}
	FmtPrint(video.Name + " 转发已连接")

	started := false
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if len(data) <= 10 {
			continue
		}

		// 尝试解密为 _paramStr_ 控制消息
		plain := DecryptParam(string(data))
		if len(plain) > 0 && plain[0] == '{' {
			handleControlMsg(plain, fd, server, video)
			continue
		}

		// FLV 二进制数据
		if !started {
			idx := bytes.Index(data, []byte("FLV"))
			if idx < 0 {
				continue
			}
			data = data[idx+13:] // 跳过 FLV 头(9) + PreviousTagSize(4)
			started = true
			FmtPrint(video.Name + " 收到FLV头")
		}

		parseAndForward(data, fd, server, video)
	}
}

// handleControlMsg 处理加密的控制消息 (videoSPS 等)
func handleControlMsg(plain string, fd *forwardDevice, server *gortsplib.Server, video *Video) {
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(plain), &msg); err != nil {
		return
	}

	msgType, _ := msg["type"].(string)
	switch msgType {
	case "videoSPS":
		// 提取 SPS/PPS 创建流
		if dataObj, ok := msg["data"].(map[string]interface{}); ok {
			if dataArr, ok := dataObj["data"].([]interface{}); ok {
				bytes := make([]byte, len(dataArr))
				for i, v := range dataArr {
					if n, ok := v.(float64); ok {
						bytes[i] = byte(n)
					}
				}
				sps, pps := extractSPSPPS(bytes)
				if len(sps) > 0 && len(pps) > 0 {
					fd.mu.Lock()
					if !fd.ready {
						fd.sps = sps
						fd.pps = pps
						createStream(server, fd, video)
						fd.ready = true
					}
					fd.mu.Unlock()
				}
			}
		}
	}
}

// parseAndForward 解析 FLV 并转发到 RTSP
func parseAndForward(data []byte, fd *forwardDevice, server *gortsplib.Server, video *Video) {
	offset := 0
	for offset+11 <= len(data) {
		tagType := data[offset]
		dataSize := int(data[offset+1])<<16 | int(data[offset+2])<<8 | int(data[offset+3])
		tagEnd := offset + 11 + dataSize
		if tagEnd > len(data) {
			break
		}

		if tagType == 9 && dataSize > 5 {
			videoData := data[offset+11 : tagEnd]
			codecID := videoData[0] & 0x0F
			avcPacketType := videoData[1]

			if codecID == 7 && avcPacketType == 0 {
				// AVC sequence header → 获取 SPS/PPS
				sps, pps := extractSPSPPS(videoData[5:])
				if len(sps) > 0 && len(pps) > 0 {
					fd.mu.Lock()
					if !fd.ready {
						fd.sps = sps
						fd.pps = pps
						createStream(server, fd, video)
						fd.ready = true
					}
					fd.mu.Unlock()

					// 发送 SPS/PPS RTP 包
					pkts, _ := fd.encoder.Encode([][]byte{sps, pps})
					for _, pkt := range pkts {
						fd.stream.WritePacketRTP(fd.media, pkt)
					}
				}
			} else if codecID == 7 && avcPacketType == 1 && fd.ready {
				// H.264 NAL 数据
				nalData := videoData[5:]
				nalOffset := 0
				for nalOffset+4 <= len(nalData) {
					nalLen := int(nalData[nalOffset])<<24 | int(nalData[nalOffset+1])<<16 |
						int(nalData[nalOffset+2])<<8 | int(nalData[nalOffset+3])
					nalOffset += 4
					if nalOffset+nalLen <= len(nalData) {
						pkts, _ := fd.encoder.Encode([][]byte{nalData[nalOffset : nalOffset+nalLen]})
						for _, pkt := range pkts {
							fd.stream.WritePacketRTP(fd.media, pkt)
						}
						nalOffset += nalLen
					} else {
						break
					}
				}
			}
		}
		offset = tagEnd + 4
	}
}

// extractSPSPPS 从 AVCDecoderConfigurationRecord 提取 SPS/PPS
func extractSPSPPS(data []byte) (sps, pps []byte) {
	if len(data) < 7 {
		return
	}
	numSPS := int(data[5] & 0x1F)
	pos := 6
	for j := 0; j < numSPS && pos+2 <= len(data); j++ {
		spsLen := int(data[pos])<<8 | int(data[pos+1])
		pos += 2
		if pos+spsLen <= len(data) {
			sps = data[pos : pos+spsLen]
			pos += spsLen
		}
	}
	if pos+1 <= len(data) {
		numPPS := int(data[pos])
		pos++
		for j := 0; j < numPPS && pos+2 <= len(data); j++ {
			ppsLen := int(data[pos])<<8 | int(data[pos+1])
			pos += 2
			if pos+ppsLen <= len(data) {
				pps = data[pos : pos+ppsLen]
				pos += ppsLen
			}
		}
	}
	return
}

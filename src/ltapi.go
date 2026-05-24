package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ==================== API 常量 & 类型 ====================

const (
	vdFileHost  = "https://vd-file.wojiazongguan.cn" // 联通视频云地址
	productKey  = "3bd0c1bc-f50"                     // 产品标识
	signSecret  = "html5_open_api_check_secret"      // API 签名密钥
	channelName = "720p"                             // 默认清晰度
)

// 设备信息 (从 deviceList 接口获取)
type deviceInfo struct {
	DeviceId   string // 设备ID
	DeviceName string // 设备名称
	ChannelNo  string // 通道号
	Status     string // 在线状态
	Region     string // 服务器区域 (如 vd-file-sh1-qcloud.wojiazongguan.cn:50443/cds)
	RelayHost  string // 中继主机 (从 iplist[0] 获取)
	RelayPort  string // 中继端口
}

// 中继服务器信息 (从 getRelayIp 接口获取)
type relayInfo struct {
	PrivateIp string // 内网IP
	RelayPort string // 端口
}

// AutoConfig 完整流程: 刷新登录 → 获取设备列表 → 生成视频配置
func AutoConfig(tokenOnline, mobile string) []Video {
	FmtPrint("======== 自动发现设备 ========")

	// 刷新 token_online 登录
	privateToken, _, err := refreshToken(tokenOnline)
	if err != nil {
		FmtPrint("刷新登录失败: %v", err)
		return nil
	}

	// 取联通票据
	ticket, err := getTicketNative(privateToken)
	if err != nil {
		FmtPrint("获取票据失败: %v", err)
		return nil
	}

	// 获取 accessToken
	accessToken, err := getAutoLoginToken(ticket)
	if err != nil {
		FmtPrint("获取 accessToken 失败: %v", err)
		return nil
	}

	// 登录视频云平台
	cloudToken, err := cloudLogin(mobile, accessToken)
	if err != nil {
		FmtPrint("视频云登录失败: %v", err)
		return nil
	}

	// 获取设备列表并生成配置
	FmtPrint("[5/5] 获取设备列表...")
	devices := getDeviceList(cloudToken)
	if len(devices) == 0 {
		FmtPrint("未发现任何设备")
		return nil
	}
	FmtPrint("发现 %d 个设备", len(devices))

	var wsHost string
	for _, dev := range devices {
		if dev.Status == "available" && dev.Region != "" {
			wsHost = dev.Region
			FmtPrint("WebSocket 地址: %s", wsHost)
			break
		}
	}
	if wsHost == "" {
		FmtPrint("无法获取 WebSocket 地址")
		return nil
	}

	var videos []Video
	for _, dev := range devices {
		if dev.Status != "available" {
			FmtPrint("跳过离线设备: %s", dev.DeviceName)
			continue
		}

		if dev.Region == "" {
			dev.Region = wsHost
		}
		if dev.RelayHost == "" || dev.RelayPort == "" {
			FmtPrint("跳过无中继设备: %s", dev.DeviceName)
			continue
		}
		relayServer := fmt.Sprintf("%s:%s", dev.RelayHost, dev.RelayPort)
		FmtPrint("设备 [%s] 中继: %s", dev.DeviceName, relayServer)

		videos = append(videos, Video{
			Name:        dev.DeviceName,
			Size:        10,
			Count:       10,
			WsHost:      dev.Region,
			DeviceId:    dev.DeviceId,
			ChannelNo:   dev.ChannelNo,
			Token:       cloudToken,
			RelayServer: relayServer,
		})
		FmtPrint("已配置: %s (id=%s)", dev.DeviceName, dev.DeviceId)
	}

	FmtPrint("======== 自动发现完成 (共%d个) ========", len(videos))
	return videos
}

// ==================== 登录链路 ====================

// httpPost 通用 POST 请求 (application/x-www-form-urlencoded)
func httpPost(urlStr string, body map[string]string) (map[string]interface{}, error) {
	form := url.Values{}
	for k, v := range body {
		form.Set(k, v)
	}

	req, _ := http.NewRequest("POST", urlStr, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Dalvik/2.1.0 (Linux; U; Android 16; 2211133C Build/BP2A.250605.031.A3);unicom{version:android@12.0900};ltst;")

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			ForceAttemptHTTP2:     false,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}

	// 重试3次
	var resp *http.Response
	var lastErr error
	for i := 0; i < 3; i++ {
		resp, lastErr = client.Do(req)
		if lastErr == nil {
			break
		}
		if i < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(respBytes, &result)
	return result, nil
}

// httpGet 通用 GET 请求
func httpGet(urlStr string) (map[string]interface{}, error) {
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("User-Agent", "ChinaUnicom/12.1200 (Android 16)")

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			ForceAttemptHTTP2:     false,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}

	// 重试3次
	var resp *http.Response
	var lastErr error
	for i := 0; i < 3; i++ {
		resp, lastErr = client.Do(req)
		if lastErr == nil {
			break
		}
		if i < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(respBytes, &result)
	return result, nil
}

// refreshToken 用 token_online 刷新登录，获取 private_token (JWT)
func refreshToken(tokenOnline string) (privateToken, mobile string, err error) {
	body := map[string]string{
		"version":      "android@12.0900",
		"token_online": tokenOnline,
	}

	resp, err := httpPost("https://loginxhm.10010.com/mobileService/onLine.htm", body)
	if err != nil {
		return "", "", fmt.Errorf("onLine.htm 请求失败: %w", err)
	}

	if vdStr(resp, "code") != "0" {
		return "", "", fmt.Errorf("onLine.htm 返回错误: %v", resp)
	}

	privateToken = vdStr(resp, "private_token")
	mobile = vdStr(resp, "desmobile")
	FmtPrint("登录成功: %s", mobile)
	return privateToken, mobile, nil
}

// getTicketNative 用 JWT 获取联通票据
func getTicketNative(privateToken string) (string, error) {
	appId := "edop_unicom_7da41905"
	apiUrl := fmt.Sprintf("https://m.client.10010.com/edop_ng/getTicketByNative?appId=%s&token=%s", appId, url.QueryEscape(privateToken))

	resp, err := httpGet(apiUrl)
	if err != nil {
		return "", fmt.Errorf("getTicketByNative 请求失败: %w", err)
	}

	ticket := vdStr(resp, "ticket")
	if ticket == "" {
		return "", fmt.Errorf("getTicketByNative 返回异常: %v", resp)
	}
	FmtPrint("获取 Ticket: %s", ticket)
	return ticket, nil
}

// getAutoLoginToken 通过 wohome/dispatcher 获取 accessToken
func getAutoLoginToken(ticket string) (string, error) {
	reqSeq := RandomDigits(5)
	resTime := fmt.Sprintf("%d", time.Now().UnixMilli())

	// 签名: md5(key + resTime + reqSeq + "wohome")
	sign := Md5Sum("UnicomAppMiniProgramAutoLogin" + resTime + reqSeq + "wohome")

	reqBody, _ := json.Marshal(map[string]interface{}{
		"header": map[string]string{
			"key":     "UnicomAppMiniProgramAutoLogin",
			"resTime": resTime,
			"reqSeq":  reqSeq,
			"channel": "wohome",
			"version": "",
			"sign":    sign,
		},
		"body": map[string]string{
			"ticket":   ticket,
			"appId":    "edop_unicom_7da41905",
			"clientId": "1001000122",
		},
	})

	req, _ := http.NewRequest("POST", "https://iotpservice.smartont.net/wohome/dispatcher", strings.NewReader(string(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 16; 2211133C Build/BP2A.250605.031.A3; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/137.0.7151.115 Mobile Safari/537.36; unicom{version:android@12.1200,desmobile:0}")

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("dispatcher 请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(respBytes, &result)

	rsp, _ := result["RSP"].(map[string]interface{})
	data, _ := rsp["DATA"].(map[string]interface{})
	accessToken := vdStr(data, "accessToken")
	if accessToken == "" {
		return "", fmt.Errorf("dispatcher 返回异常: %v", result)
	}
	FmtPrint("获取 accessToken: %s", accessToken)
	return accessToken, nil
}

// cloudLogin 第三方登录获取视频云 token
func cloudLogin(mobile, accessToken string) (string, error) {
	deviceId := RandomDigits(16)

	// 必须保持 key 顺序与 JS JSON.stringify 一致 (否则签名不对)
	extra := fmt.Sprintf(`{"accessToken":"%s","phone":"%s","deviceType":"WEB","deviceId":"%s","appName":"smartHome","version":"0.0.1"}`,
		accessToken, mobile, deviceId)

	payload := map[string]interface{}{
		"productKey": productKey,
		"account":    mobile,
		"loginType":  "WJJK",
		"extra":      extra,
	}
	resp, err := vdPost("/h5player/api/open/cloud/thirdLogin", payload)
	if err != nil {
		return "", fmt.Errorf("thirdLogin 请求失败: %w", err)
	}

	data, _ := resp["data"].(map[string]interface{})
	cloudToken := vdStr(data, "token")
	if cloudToken == "" {
		return "", fmt.Errorf("thirdLogin 返回异常: %v", resp)
	}
	FmtPrint("获取视频云 Token: %s", cloudToken)
	return cloudToken, nil
}

// ==================== 业务 API ====================

// getDeviceList 获取账号下的摄像头设备列表
func getDeviceList(token string) []deviceInfo {
	payload := map[string]interface{}{
		"token":        token,
		"productKey":   productKey,
		"settingCodes": "[501,500,2067,1086,2045]",
	}

	resp, err := vdPost("/h5player/api/open/esd/deviceList", payload)
	if err != nil {
		FmtPrint("获取设备列表失败: %v", err)
		return nil
	}

	data, _ := resp["data"].(map[string]interface{})
	devicesRaw, _ := data["devicelist"].([]interface{})

	var devices []deviceInfo
	for _, d := range devicesRaw {
		dev, _ := d.(map[string]interface{})
		// 从 iplist[0] 获取中继信息
		var relayHost, relayPort string
		if iplist, ok := dev["iplist"].([]interface{}); ok && len(iplist) > 0 {
			if ip, ok := iplist[0].(map[string]interface{}); ok {
				relayHost = vdStr(ip, "relayhost")
				relayPort = vdStr(ip, "relayport")
			}
		}
		// region 去掉 /cds 后缀得到 wsHost
		region := strings.TrimSuffix(vdStr(dev, "region"), "/cds")

		devices = append(devices, deviceInfo{
			DeviceId:   vdStr(dev, "deviceid"),
			DeviceName: vdStr(dev, "devicename"),
			ChannelNo:  vdStr(dev, "channelNo"),
			Status:     vdStr(dev, "onlineStatus"),
			Region:     region,
			RelayHost:  relayHost,
			RelayPort:  relayPort,
		})
	}
	return devices
}

// getRelayIp 获取摄像头的中继服务器地址
func getRelayIp(token, deviceId, channelNo string) *relayInfo {
	payload := map[string]interface{}{
		"token":      token,
		"productKey": productKey,
		"channelNo":  channelNo,
		"deviceId":   deviceId,
		"channel":    channelName,
	}

	resp, err := vdPost("/h5player/api/open/lookup/getRelayIp", payload)
	if err != nil {
		FmtPrint("获取中继服务器失败: %v", err)
		return nil
	}

	data, _ := resp["data"].(map[string]interface{})
	return &relayInfo{
		PrivateIp: vdStr(data, "privateip"),
		RelayPort: vdStr(data, "relayport"),
	}
}

// getWsHost 获取 WebSocket 视频流服务器地址
func getWsHost(token, deviceId string) string {
	payload := map[string]interface{}{
		"productKey": productKey,
		"token":      token,
		"deviceId":   deviceId,
		"channelNo":  "",
	}

	resp, err := vdPost("/h5player/api/open/config", payload)
	if err != nil {
		FmtPrint("获取 WebSocket 配置失败: %v", err)
		return ""
	}

	data, _ := resp["data"].(map[string]interface{})
	wsServers, _ := data["html5PlayerWebSocketServer"].(map[string]interface{})

	for _, key := range []string{"bluramsWo", "bluramsCN", "bluramsOS"} {
		if region, ok := wsServers[key].(map[string]interface{}); ok {
			if pro := vdStr(region, "pro"); pro != "" {
				return strings.TrimPrefix(pro, "wss://")
			}
		}
	}
	return ""
}

// BuildParamMsg 构建 WebSocket 连接时发送的 _paramStr_ 参数
func BuildParamMsg(token, deviceId, channelNo, relayServer, deviceName string) string {
	payload := map[string]interface{}{
		"requestTime":        fmt.Sprintf("%d", time.Now().UnixMilli()),
		"productKey":         productKey,
		"deviceId":           deviceId,
		"channelNo":          channelNo,
		"token":              token,
		"hasAudio":           "true",
		"region":             "",
		"isPermanentStorage": "false",
		"channel":            channelName,
		"deviceName":         deviceName,
		"clientId":           "WEBCLIENT_H5_" + RandomDigits(22) + fmt.Sprintf("%d", time.Now().UnixMilli()),
		"shareId":            "",
		"relayServer":        relayServer,
		"isSDCardPlayback":   "false",
		"preConnect":         "false",
		"releaseVersion":     "H5PlayerServer_220719_B1072_4a25458_xml2json",
		"isSupportWASM":      "1",
	}

	jsonBytes, _ := json.Marshal(payload)
	return EncryptParam(string(jsonBytes))
}

// ==================== vd-file API 请求 ====================

// vdPost 向联通视频云发送 POST 请求，自动加签加密
func vdPost(apiPath string, payload map[string]interface{}) (map[string]interface{}, error) {
	payload["_timestamp"] = time.Now().UnixMilli()
	payload["signature"] = vdSign(payload)

	jsonBytes, _ := json.Marshal(payload)
	paramStr := EncryptParam(string(jsonBytes))

	body := "_paramStr_=" + paramStr
	req, _ := http.NewRequest("POST", vdFileHost+apiPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "ChinaUnicom/12.1200 (Android 16)")

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	plain := DecryptParam(string(respBytes))

	var result map[string]interface{}
	json.Unmarshal([]byte(plain), &result)
	return result, nil
}

// vdSign 生成 API 签名 (MD5(signSecret + key1=val1 + key2=val2 + ...))
func vdSign(payload map[string]interface{}) string {
	keys := make([]string, 0, len(payload))
	for k := range payload {
		if k != "signature" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString(signSecret)
	for _, k := range keys {
		if v := payload[k]; v != nil {
			sb.WriteString(fmt.Sprintf("%s=%v", k, v))
		}
	}
	return Md5Sum(sb.String())
}

// vdStr 安全获取 map 中的字符串值
func vdStr(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}

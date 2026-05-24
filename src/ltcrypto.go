package main

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
)

// ==================== _paramStr_ 加解密 ====================
// 算法来源: webPlayer.min.js (vd-file.wojiazongguan.cn)
//
// 加密: JSON → 前后半交换(按字符数) → URL编码(JS escape) → Base64 → 前缀10位随机字符
// 解密: 去掉前10位 → Base64解码 → URL解码(JS unescape) → 前后半交换还原(按字符数)

// EncryptParam 加密 JSON 字符串为 _paramStr_ 值
func EncryptParam(plaintext string) string {
	runes := []rune(plaintext)
	half := len(runes) / 2
	swapped := string(runes[half:]) + string(runes[:half])
	escaped := jsEscape(swapped)
	b64 := base64.StdEncoding.EncodeToString([]byte(escaped))
	n, _ := rand.Int(rand.Reader, big.NewInt(9000000000))
	randNum := fmt.Sprintf("%010d", n.Int64()+10000000000)
	prefix := base64.StdEncoding.EncodeToString([]byte(randNum))[:10]
	return prefix + b64
}

// DecryptParam 解密 _paramStr_ 值为 JSON 字符串
func DecryptParam(encrypted string) string {
	if encrypted == "" {
		return encrypted
	}
	withoutPrefix := encrypted[10:]
	decoded, err := base64.StdEncoding.DecodeString(withoutPrefix)
	if err != nil {
		return ""
	}
	urlDecoded := jsUnescape(string(decoded))
	runes := []rune(urlDecoded)
	l := len(runes)
	half := l / 2
	return string(runes[l-half:]) + string(runes[:l-half])
}

// jsEscape 模拟 JavaScript 的 escape() 函数
func jsEscape(s string) string {
	var builder strings.Builder
	for _, r := range s {
		switch {
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '*' || r == '@' ||
			r == '-' || r == '_' || r == '+' || r == '.' || r == '/':
			builder.WriteRune(r)
		case r <= 0xFF:
			fmt.Fprintf(&builder, "%%%02X", r)
		default:
			fmt.Fprintf(&builder, "%%u%04X", r)
		}
	}
	return builder.String()
}

// jsUnescape 模拟 JavaScript 的 unescape() 函数
func jsUnescape(s string) string {
	result := s
	for {
		idx := strings.Index(result, "%u")
		if idx == -1 {
			break
		}
		if idx+6 <= len(result) {
			var cp int
			fmt.Sscanf(result[idx+2:idx+6], "%04X", &cp)
			result = result[:idx] + string(rune(cp)) + result[idx+6:]
		} else {
			break
		}
	}
	var builder strings.Builder
	i := 0
	for i < len(result) {
		if result[i] == '%' && i+2 < len(result) {
			var hv int
			if n, _ := fmt.Sscanf(result[i+1:i+3], "%02X", &hv); n == 1 {
				builder.WriteByte(byte(hv))
				i += 3
				continue
			}
		}
		builder.WriteByte(result[i])
		i++
	}
	return builder.String()
}

// ==================== 工具函数 ====================

// Md5Sum 计算字符串的 MD5 哈希值
func Md5Sum(text string) string {
	h := md5.Sum([]byte(text))
	return fmt.Sprintf("%x", h)
}

// RandomDigits 生成指定长度的随机数字字符串
func RandomDigits(length int) string {
	s := ""
	for i := 0; i < length; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(10))
		s += fmt.Sprintf("%d", n.Int64())
	}
	return s
}

// RandomHex 生成指定长度的随机十六进制字符串
func RandomHex(length int) string {
	b := make([]byte, (length+1)/2)
	rand.Read(b)
	return fmt.Sprintf("%x", b)[:length]
}

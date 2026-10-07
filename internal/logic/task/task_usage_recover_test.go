package task

import (
	"encoding/json"
	"testing"

	"github.com/qianfree/team-api/relay/common"
	"github.com/qianfree/team-api/relay/constant"
	"github.com/qianfree/team-api/relay/dto"

	// 注册 ali / volcengine 任务适配器，供 recoverTerminalFacts 经注册表重解析
	_ "github.com/qianfree/team-api/relay/taskchannel/ali"
	_ "github.com/qianfree/team-api/relay/taskchannel/volcengine"
)

// TestRecoverTerminalFacts 重试结算从已持久化的终态上游响应体重解析 token 用量与素材计量：
// 与首次轮询解析同一份 body，回收结果一致；渠道缺失/空响应体/未注册渠道回退零值
// （仅损失展示保真度，结算金额由已持久化的 ActualCost 保证，不随回退漂移）。
func TestRecoverTerminalFacts(t *testing.T) {
	// 1. volcengine 终态响应体：回收 token 用量（prompt = total - completion）
	body := []byte(`{"status":"succeeded","content":{"video_url":"https://cdn.example.com/v.mp4"},"usage":{"total_tokens":112500,"completion_tokens":50000}}`)
	ch := &common.ChannelBasicInfo{Type: int(constant.ProviderVolcengine)}
	prompt, completion, total, usage := recoverTerminalFacts(ch, body)
	if prompt != 62500 || completion != 50000 || total != 112500 {
		t.Errorf("volcengine facts = (%d, %d, %d), want (62500, 50000, 112500)", prompt, completion, total)
	}
	if usage != nil {
		t.Errorf("volcengine has no material usage, got %+v", usage)
	}

	// 2. ali 终态响应体：回收素材计量（wan3.0 按秒计费的官方输出秒数，驱动快照按秒命中明细）
	aliBody := []byte(`{"output":{"task_status":"SUCCEEDED","video_url":"https://cdn.example.com/a.mp4"},"usage":{"output_video_duration":8}}`)
	_, _, _, mUsage := recoverTerminalFacts(&common.ChannelBasicInfo{Type: int(constant.ProviderAli)}, aliBody)
	if mUsage == nil || mUsage.OutputSeconds != 8 {
		t.Errorf("ali material usage = %+v, want OutputSeconds=8", mUsage)
	}

	// 3. 回退：渠道缺失 / 空 body / 未注册渠道类型 → 全零值
	if p, c, tt, u := recoverTerminalFacts(nil, body); p != 0 || c != 0 || tt != 0 || u != nil {
		t.Errorf("nil channel should fall back to zeros, got (%d,%d,%d,%v)", p, c, tt, u)
	}
	if p, c, tt, u := recoverTerminalFacts(ch, nil); p != 0 || c != 0 || tt != 0 || u != nil {
		t.Errorf("empty data should fall back to zeros, got (%d,%d,%d,%v)", p, c, tt, u)
	}
	if p, c, tt, u := recoverTerminalFacts(&common.ChannelBasicInfo{Type: -1}, body); p != 0 || c != 0 || tt != 0 || u != nil {
		t.Errorf("unregistered channel type should fall back to zeros, got (%d,%d,%d,%v)", p, c, tt, u)
	}
}

// TestExtractImageUsage_FromNormalizedBlob 归一化响应体保留 usage（buildImageResult）后，
// sync_image 重试结算可从已落库的 t.Data 重解析回收 token 用量，与首次结算同保真度。
func TestExtractImageUsage_FromNormalizedBlob(t *testing.T) {
	normalized, err := json.Marshal(dto.ImageResponse{
		Created: 1700000000,
		Data:    []dto.ImageData{{URL: "https://cdn.example.com/a.png"}},
		Usage:   &dto.ImageUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	})
	if err != nil {
		t.Fatalf("marshal normalized blob: %v", err)
	}
	prompt, completion, total := extractImageUsage(normalized)
	if prompt != 10 || completion != 20 || total != 30 {
		t.Errorf("usage from normalized = (%d, %d, %d), want (10, 20, 30)", prompt, completion, total)
	}

	// 无 usage 的归一化体（旧存量任务行）：回退零值，不报错
	legacy, _ := json.Marshal(dto.ImageResponse{
		Created: 1700000000,
		Data:    []dto.ImageData{{URL: "https://cdn.example.com/a.png"}},
	})
	if p, c, tt := extractImageUsage(legacy); p != 0 || c != 0 || tt != 0 {
		t.Errorf("legacy blob without usage = (%d, %d, %d), want zeros", p, c, tt)
	}
}

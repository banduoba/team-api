package task

import (
	"strings"
	"testing"
	"time"

	"github.com/qianfree/team-api/internal/logic/billing"
	"github.com/qianfree/team-api/relay/common"
)

func testUsageTask() *common.AsyncTask {
	submit := time.Now().Add(-time.Minute)
	finish := time.Now()
	return &common.AsyncTask{
		PublicTaskID:     "task_abc123",
		RequestID:        "req-usage-1",
		TenantID:         1,
		UserID:           2,
		ApiKeyID:         3,
		ChannelID:        4,
		ModelName:        "wan2.5-t2v",
		PreDeductAmount:  billing.NewFromFloat(2.5),
		SubmitTime:       &submit,
		FinishTime:       &finish,
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
	}
}

// TestBuildTaskUsageRecord_CostColumnSemantics 费用列口径与同步链路（relay_handler）严格一致：
//   - total_cost  = settleResult.BaseCost（折扣前基础费用）；
//   - actual_cost = 折扣后实际扣款。
//
// 此前任务路径把 actual 同时写进两列，折扣租户下 SUM(total_cost) 类趋势统计
// （同步行折前、任务行折后）口径混杂。
func TestBuildTaskUsageRecord_CostColumnSemantics(t *testing.T) {
	task := testUsageTask()
	task.ActualCost = billing.NewFromFloat(1.7) // 折前 2.0 × 租户 0.85 = 1.7

	settle := &common.SettlementResult{
		PreDeductAmount: 2.5,
		BaseCost:        2.0, // 折扣前
		ActualCost:      1.7, // 折后实扣
		RefundAmount:    0.8,
		BillingMode:     "per_second",
		BillingSource:   "base",
		RateMultiplier:  0.85,
		BillingSnapshot: `{"pricing":{"billing_mode":"per_second"}}`,
		BillingSummary:  "按秒计费",
	}
	rec := buildTaskUsageRecord(task, nil, true, "", settle)

	if rec.TotalCost != 2.0 {
		t.Errorf("TotalCost = %v, want 2.0 (BaseCost 折扣前，与同步链路口径一致)", rec.TotalCost)
	}
	if rec.ActualCost != 1.7 {
		t.Errorf("ActualCost = %v, want 1.7 (折扣后实扣)", rec.ActualCost)
	}
	if rec.PreDeductAmount != 2.5 {
		t.Errorf("PreDeductAmount = %v, want 2.5", rec.PreDeductAmount)
	}
	if rec.RefundAmount != 0.8 {
		t.Errorf("RefundAmount = %v, want 0.8", rec.RefundAmount)
	}
	// 快照/摘要/计费元数据透传
	if rec.BillingSnapshot != settle.BillingSnapshot || rec.BillingSummary != settle.BillingSummary {
		t.Errorf("snapshot/summary not carried: %q / %q", rec.BillingSnapshot, rec.BillingSummary)
	}
	if rec.BillingMode != "per_second" || rec.BillingSource != "base" {
		t.Errorf("billing meta = %q/%q, want per_second/base", rec.BillingMode, rec.BillingSource)
	}
	if rec.Currency == "" {
		t.Error("Currency should be set (本位币)")
	}
	if !rec.Success || rec.Status != "success" {
		t.Errorf("success/status = %v/%q, want true/success", rec.Success, rec.Status)
	}
}

// TestBuildTaskUsageRecord_FailureZeroCost 失败/超时任务（settleResult=nil）：
// 无费用（total_cost / actual_cost 均为 0）、status=error、错误信息脱敏、
// 计费模式取价回退不 panic（无 DB 单测环境）。
func TestBuildTaskUsageRecord_FailureZeroCost(t *testing.T) {
	task := testUsageTask()
	task.ActualCost = billing.Zero

	rec := buildTaskUsageRecord(task, nil, false, "upstream failed, see http://upstream.invalid/x from 10.0.0.8", nil)

	if rec.TotalCost != 0 || rec.ActualCost != 0 {
		t.Errorf("failed task costs = %v/%v, want 0/0", rec.TotalCost, rec.ActualCost)
	}
	if rec.Success || rec.Status != "error" {
		t.Errorf("success/status = %v/%q, want false/error", rec.Success, rec.Status)
	}
	if strings.Contains(rec.ErrorMessage, "http://") || strings.Contains(rec.ErrorMessage, "10.0.0.8") {
		t.Errorf("error message not redacted: %q", rec.ErrorMessage)
	}
	if rec.BillingMode == "" {
		t.Error("BillingMode should fall back to per_request, got empty")
	}
	if rec.PreDeductAmount != 2.5 {
		t.Errorf("PreDeductAmount = %v, want 2.5 (退款痕迹)", rec.PreDeductAmount)
	}
}

// TestBuildTaskUsageRecord_DurationAndUpstreamID 按秒任务时长取 spec.duration 事实键
// （只认 spec.*，乘数语义的旧 duration 键不可作时长）；上游请求 ID 优先追踪 ID 回退任务句柄。
func TestBuildTaskUsageRecord_DurationAndUpstreamID(t *testing.T) {
	task := testUsageTask()
	task.ActualCost = billing.NewFromFloat(1.0)
	task.PrivateData = []byte(`{
		"upstream_task_id": "up-task-9",
		"billing_context": {"ratios": {"spec.duration": 4.5, "duration": 99, "spec.resolution": "720p"}}
	}`)

	rec := buildTaskUsageRecord(task, nil, true, "", &common.SettlementResult{BaseCost: 1.0, ActualCost: 1.0})

	if rec.DurationSeconds != 4 {
		t.Errorf("DurationSeconds = %d, want 4 (spec.duration 4.5 截断，忽略乘数语义的 duration=99)", rec.DurationSeconds)
	}
	if rec.UpstreamRequestID != "up-task-9" {
		t.Errorf("UpstreamRequestID = %q, want up-task-9 (无追踪 ID 回退任务句柄)", rec.UpstreamRequestID)
	}

	// 有上游追踪 ID 时优先
	task.PrivateData = []byte(`{
		"upstream_task_id": "up-task-9",
		"upstream_request_id": "req-up-777",
		"billing_context": {"ratios": {}}
	}`)
	rec = buildTaskUsageRecord(task, nil, true, "", &common.SettlementResult{BaseCost: 1.0, ActualCost: 1.0})
	if rec.UpstreamRequestID != "req-up-777" {
		t.Errorf("UpstreamRequestID = %q, want req-up-777 (追踪 ID 优先)", rec.UpstreamRequestID)
	}
	if rec.DurationSeconds != 0 {
		t.Errorf("DurationSeconds = %d, want 0 (无 spec.duration)", rec.DurationSeconds)
	}
}

// TestBuildTaskUsageRecord_ChannelAndLatency 渠道信息透传与端到端延迟计算。
func TestBuildTaskUsageRecord_ChannelAndLatency(t *testing.T) {
	task := testUsageTask()
	task.ActualCost = billing.NewFromFloat(0.5)

	ch := &common.ChannelBasicInfo{ID: 4, Name: "ali-wan", Type: 42}
	rec := buildTaskUsageRecord(task, ch, true, "", &common.SettlementResult{BaseCost: 0.5, ActualCost: 0.5})

	if rec.ChannelName != "ali-wan" || rec.ChannelType != 42 || rec.ChannelID != 4 {
		t.Errorf("channel fields = %q/%d/%d", rec.ChannelName, rec.ChannelType, rec.ChannelID)
	}
	if rec.LatencyMs < 59000 || rec.LatencyMs > 61000 {
		t.Errorf("LatencyMs = %v, want ≈60000 (submit→finish)", rec.LatencyMs)
	}
	if rec.RequestType != 3 {
		t.Errorf("RequestType = %d, want 3 (async)", rec.RequestType)
	}
	if rec.TaskID != "task_abc123" || rec.RequestID != "req-usage-1" {
		t.Errorf("task/request id = %q/%q", rec.TaskID, rec.RequestID)
	}
}

package billing

import (
	"context"
	"encoding/json"
	"fmt"

	rcommon "github.com/qianfree/team-api/relay/common"
)

// BillingSnapshot 计费快照（写入 JSONB）
type BillingSnapshot struct {
	Pricing     BillingSnapshotPricing      `json:"pricing"`
	Multipliers BillingSnapshotMultipliers  `json:"multipliers"`
	CachePrices *BillingSnapshotCachePrices `json:"cache_prices,omitempty"`
	TokenCosts  map[string]TokenCostDetail  `json:"token_costs"`
	Settlement  BillingSnapshotSettlement   `json:"settlement"`
	RequestMeta BillingSnapshotRequestMeta  `json:"request_meta"`
}

// BillingSnapshotPricing 价格来源信息
type BillingSnapshotPricing struct {
	BaseInputPrice       float64 `json:"base_input_price"`
	BaseOutputPrice      float64 `json:"base_output_price"`
	EffectiveInputPrice  float64 `json:"effective_input_price"`
	EffectiveOutputPrice float64 `json:"effective_output_price"`
	BillingMode          string  `json:"billing_mode"`
	BillingSource        string  `json:"billing_source"`
	// Scheme 命中的特殊计费方案名（pricing JSONB 顶层 scheme 键）；空 = 通用引擎。
	// 供账单事后追溯该笔走的计费方案，摘要据此追加「计费方案」行
	Scheme string `json:"scheme,omitempty"`
	// PerSecondPrices 按秒单价矩阵（仅 per_second / special 模式填充，其余模式 omitted）：
	// special 的矩阵是输出生成组件的定价依据，快照携带供账单解释
	PerSecondPrices map[string]float64 `json:"per_second_prices,omitempty"`
	// PerRequestPrice 按次单价（仅 per_request 模式填充）：摘要的「按次单价」行取此值。
	// 此前误用 EffectiveInputPrice（token 输入价，纯按次模型恒为 0，展示「按次单价: $0」）
	PerRequestPrice float64 `json:"per_request_price,omitempty"`
}

// BillingSnapshotMultipliers 倍率信息
type BillingSnapshotMultipliers struct {
	ModelMultiplier  float64 `json:"model_multiplier"`
	TenantMultiplier float64 `json:"tenant_multiplier"`
	DiscountRatio    float64 `json:"discount_ratio"`
	RateMultiplier   float64 `json:"rate_multiplier"`
	TimeMultiplier   float64 `json:"time_multiplier"` // 时段乘数（未启用时段定价时为 1）
	TimeRule         string  `json:"time_rule"`       // 命中的时段名（供账单解释；未命中为空）
}

// BillingSnapshotCachePrices 缓存价格信息
type BillingSnapshotCachePrices struct {
	CacheReadPrice     float64 `json:"cache_read_price"`
	CacheCreationPrice float64 `json:"cache_creation_price"`
}

// TokenCostDetail 单类 token 的费用明细
type TokenCostDetail struct {
	Tokens    int     `json:"tokens"`
	UnitPrice float64 `json:"unit_price"`
	// Multiplier 该分项已乘的综合倍率（租户倍率 × 时段乘数）。
	// Cost 是已乘倍率的实际费用而 UnitPrice 是未乘倍率的原价，二者口径不同：
	// 缺少该字段时「tokens/1M × unit_price = cost」表面不成立（折上折观感），
	// 写入后完整算式 tokens/1M × unit_price × multiplier = cost 可自洽复算。
	// 恰为 1（无折扣）时 omitempty 省略；旧快照无此字段，读取方按 1 处理。
	Multiplier float64 `json:"multiplier,omitempty"`
	Cost       float64 `json:"cost"`
}

// BillingSnapshotSettlement 结算信息
type BillingSnapshotSettlement struct {
	PreDeductAmount  float64 `json:"pre_deduct_amount"`
	ActualCost       float64 `json:"actual_cost"`
	RefundAmount     float64 `json:"refund_amount"`
	SupplementAmount float64 `json:"supplement_amount"`
}

// BillingSnapshotRequestMeta 请求元信息
type BillingSnapshotRequestMeta struct {
	RequestedModel string `json:"requested_model,omitempty"`
	UpstreamModel  string `json:"upstream_model,omitempty"`
	IsModelMapped  bool   `json:"is_model_mapped"`
	IsStream       bool   `json:"is_stream"`
	FirstTokenMs   int    `json:"first_token_ms,omitempty"`
}

// GenerateBillingSnapshot 生成完整计费快照
func GenerateBillingSnapshot(
	pricing *PricingResult,
	breakdown *CostBreakdown,
	usage *rcommon.Usage,
	settlement *SettlementResult,
	info *rcommon.RelayInfo,
) *BillingSnapshot {
	if pricing == nil || breakdown == nil {
		return nil
	}

	snapshot := &BillingSnapshot{
		Pricing: BillingSnapshotPricing{
			BaseInputPrice:       pricing.BaseInputPrice,
			BaseOutputPrice:      pricing.BaseOutputPrice,
			EffectiveInputPrice:  pricing.InputPrice,
			EffectiveOutputPrice: pricing.OutputPrice,
			BillingMode:          pricing.BillingMode,
			BillingSource:        pricing.BillingSource,
			Scheme:               pricing.Scheme,
			PerSecondPrices:      pricing.PerSecondPrices,
			PerRequestPrice:      pricing.PerRequestPrice,
		},
		Multipliers: BillingSnapshotMultipliers{
			ModelMultiplier:  pricing.ModelMultiplier,
			TenantMultiplier: pricing.TenantMultiplier,
			DiscountRatio:    pricing.DiscountRatio,
			RateMultiplier:   pricing.TenantMultiplier,
			TimeMultiplier:   effectiveTimeMultiplier(pricing),
			TimeRule:         pricing.TimeRuleName,
		},
		TokenCosts: buildTokenCosts(pricing, breakdown),
	}

	// Cache 比率（仅当有 cache token 时填充）
	if breakdown.CacheReadTokens > 0 || breakdown.CacheCreationTokens > 0 {
		snapshot.CachePrices = &BillingSnapshotCachePrices{
			CacheReadPrice:     pricing.CacheReadPrice,
			CacheCreationPrice: pricing.CacheCreationPrice,
		}
	}

	// 结算信息
	if settlement != nil {
		snapshot.Settlement = BillingSnapshotSettlement{
			PreDeductAmount:  settlement.PreDeductAmount,
			ActualCost:       settlement.ActualCost,
			RefundAmount:     settlement.RefundAmount,
			SupplementAmount: settlement.SupplementAmount,
		}
	}

	// 请求元信息
	if info != nil {
		requestMeta := BillingSnapshotRequestMeta{
			RequestedModel: info.OriginModelName,
			IsStream:       info.IsStream,
		}
		if info.ChannelMeta != nil {
			requestMeta.UpstreamModel = info.ChannelMeta.UpstreamModelName
			requestMeta.IsModelMapped = info.ChannelMeta.IsModelMapped
		}
		if !info.FirstResponseTime.IsZero() {
			requestMeta.FirstTokenMs = int(info.FirstResponseTime.Sub(info.StartTime).Milliseconds())
		}
		snapshot.RequestMeta = requestMeta
	}

	return snapshot
}

// buildTokenCosts 构建各类 token 的费用明细。
// Cost 分项为已乘综合倍率的实际费用（与 computeCost 的 InputCost/OutputCost 口径一致），
// Multiplier 随行写入使「tokens/1M × unit_price × multiplier = cost」可自洽复算。
func buildTokenCosts(pricing *PricingResult, breakdown *CostBreakdown) map[string]TokenCostDetail {
	// decimal 相乘后回转 float64：避免 0.85×1.2 在 IEEE double 下产生
	// 1.0199999999999998 类尾差进入快照与展示算式
	mul := InexactFloat64(NewFromFloat(pricing.TenantMultiplier).Mul(NewFromFloat(effectiveTimeMultiplier(pricing))))

	detail := func(tokens int, unitPrice, cost float64) TokenCostDetail {
		return TokenCostDetail{Tokens: tokens, UnitPrice: unitPrice, Multiplier: mul, Cost: cost}
	}

	costs := map[string]TokenCostDetail{
		"input":  detail(breakdown.InputTokens, pricing.InputPrice, breakdown.InputCost),
		"output": detail(breakdown.OutputTokens, pricing.OutputPrice, breakdown.OutputCost),
	}

	if breakdown.CacheReadTokens > 0 {
		// direct cache price
		costs["cache_read"] = detail(breakdown.CacheReadTokens, pricing.CacheReadPrice, breakdown.CacheReadCost)
	}

	if breakdown.CacheCreationTokens > 0 {
		// direct cache creation price
		costs["cache_creation"] = detail(breakdown.CacheCreationTokens, pricing.CacheCreationPrice, breakdown.CacheCreationCost)
	}

	return costs
}

// GenerateBillingSummary 生成人类可读的计费摘要文本（中文）。
// 金额符号取系统本位币（billing_currency），摘要为写入时快照，货币在初始化后不可更改。
func GenerateBillingSummary(ctx context.Context, snapshot *BillingSnapshot) string {
	if snapshot == nil {
		return ""
	}

	// 货币符号跟随本位币；money 统一拼接符号 + 价格
	sym := CurrencySymbol(ctx)
	money := func(v float64) string { return sym + formatPrice(v) }

	// 价格来源中文映射
	sourceMap := map[string]string{
		"base":          "基础定价",
		"tenant_custom": "租户独立价",
		"plan":          "套餐价",
	}
	source := sourceMap[snapshot.Pricing.BillingSource]
	if source == "" {
		source = snapshot.Pricing.BillingSource
	}

	// 计费模式中文映射
	modeMap := map[string]string{
		"token":            "按量计费",
		"per_request":      "按次计费",
		"tiered":           "阶梯计费",
		"per_second":       "按秒计费",
		BillingModeSpecial: "特殊计费",
	}
	mode := modeMap[snapshot.Pricing.BillingMode]
	if mode == "" {
		mode = snapshot.Pricing.BillingMode
	}

	// 模型名
	modelName := snapshot.RequestMeta.RequestedModel
	if modelName == "" {
		modelName = "-"
	}

	lines := make([]string, 0, 15)
	lines = append(lines, fmt.Sprintf("模型: %s | 计费模式: %s | 价格来源: %s", modelName, mode, source))
	// 特殊计费方案行：方案名直出（custom:minimax-material 等），便于账单事后追溯；
	// 不做后端中文映射表——新增方案不应要求改动本文件（与 scheme 注册表设计一致）
	if snapshot.Pricing.Scheme != "" {
		lines = append(lines, fmt.Sprintf("计费方案: %s", snapshot.Pricing.Scheme))
	}
	lines = append(lines, "---")

	// 按次计费特殊处理
	if snapshot.Pricing.BillingMode == "per_request" {
		// 按次单价取 PerRequestPrice（此前误用 EffectiveInputPrice = token 输入价，
		// 纯按次模型恒为 0，摘要展示「按次单价: $0.000000」）
		lines = append(lines, fmt.Sprintf("按次单价: %s", money(snapshot.Pricing.PerRequestPrice)))
	} else {
		// 各类 token 费用明细：tokens × 原价/1M × 倍率 = 实际费用。
		// Cost 分项已含折扣（computeCost 分项 × mul），倍率必须显式进入算式，
		// 否则「原价单价 = 折后费用」表面不成立；无折扣时省略倍率段
		tokenRows := []struct {
			label string
			key   string
		}{
			{"输入", "input"},
			{"输出", "output"},
			{"缓存读取", "cache_read"},
			{"缓存创建", "cache_creation"},
		}
		var costParts []string
		for _, row := range tokenRows {
			tc, ok := snapshot.TokenCosts[row.key]
			if !ok || tc.Tokens <= 0 {
				continue
			}
			multPart := ""
			if tc.Multiplier > 0 && tc.Multiplier != 1.0 {
				multPart = fmt.Sprintf(" × %s", formatMultiplier(tc.Multiplier))
			}
			lines = append(lines, fmt.Sprintf("%s: %s tokens × %s/1M%s = %s",
				row.label, formatInt(tc.Tokens), money(tc.UnitPrice), multPart, money(tc.Cost)))
			if tc.Cost > 0 {
				costParts = append(costParts, money(tc.Cost))
			}
		}

		// 倍率说明行 + 合计行。分项费用已含折扣，合计 = 各分项之和 = 实际费用（等式成立）。
		// 不再展示「(分项之和) × 倍率 = 实际」——旧格式的分项是折后值，再乘倍率
		// 呈现折上折观感，算式两边对不上（分项之和本就等于实际费用）
		effTenant := snapshot.Multipliers.TenantMultiplier
		effTime := snapshot.Multipliers.TimeMultiplier
		if effTime <= 0 {
			effTime = 1.0
		}
		if effTenant > 0 && (effTenant != 1.0 || effTime != 1.0) {
			multDesc := fmt.Sprintf("已应用倍率: 租户倍率(%s)", formatMultiplier(effTenant))
			if effTime != 1.0 {
				multDesc += fmt.Sprintf(" × 时段乘数(%s)", formatMultiplier(effTime))
			}
			lines = append(lines, multDesc)
		}
		if len(costParts) > 1 {
			lines = append(lines, fmt.Sprintf("合计: (%s) = %s",
				joinWithPlus(costParts), money(snapshot.Settlement.ActualCost)))
		}
	}

	// 时段定价行：放在 token/per_request 分支之后统一展示，保证账单可解释（按次计费同样适用时段乘数）
	if snapshot.Multipliers.TimeRule != "" && snapshot.Multipliers.TimeMultiplier > 0 && snapshot.Multipliers.TimeMultiplier != 1.0 {
		lines = append(lines, fmt.Sprintf("时段: %s ×%.2f", snapshot.Multipliers.TimeRule, snapshot.Multipliers.TimeMultiplier))
	}

	// 结算信息
	lines = append(lines, "---")
	s := snapshot.Settlement
	if s.PreDeductAmount > 0 {
		if s.RefundAmount > 0 {
			lines = append(lines, fmt.Sprintf("预扣: %s → 实际: %s → 退还: %s",
				money(s.PreDeductAmount), money(s.ActualCost), money(s.RefundAmount)))
		} else if s.SupplementAmount > 0 {
			lines = append(lines, fmt.Sprintf("预扣: %s → 实际: %s → 补扣: %s",
				money(s.PreDeductAmount), money(s.ActualCost), money(s.SupplementAmount)))
		} else {
			lines = append(lines, fmt.Sprintf("预扣: %s → 实际: %s（无差额）",
				money(s.PreDeductAmount), money(s.ActualCost)))
		}
	} else if s.ActualCost > 0 {
		lines = append(lines, fmt.Sprintf("实际费用: %s", money(s.ActualCost)))
	}

	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}
	return result
}

// SnapshotToJSON 将快照序列化为 JSON 字符串。
// nil 时返回 "null"（合法 JSON），确保 JSONB 列不会收到空字符串。
func SnapshotToJSON(snapshot *BillingSnapshot) string {
	if snapshot == nil {
		return "null"
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "null"
	}
	return string(data)
}

// formatPrice 格式化价格（去除尾部多余零）
func formatPrice(v float64) string {
	return formatCost(v)
}

// joinWithPlus 用 " + " 连接字符串数组
func joinWithPlus(parts []string) string {
	result := ""
	for i, part := range parts {
		if i > 0 {
			result += " + "
		}
		result += part
	}
	return result
}

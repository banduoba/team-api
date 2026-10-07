package billing

import (
	"encoding/json"
	"testing"
)

// TestParseTenantPricingPatch 补丁解析：三态保真（缺键=继承 / 空数组=显式关闭 / 非空=覆盖）
// 与容错（空串/null/坏 JSON 返回 nil，不阻断计费）
func TestParseTenantPricingPatch(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantNil bool
		check   func(*testing.T, *TenantPricingPatch)
	}{
		{"空串", "", true, nil},
		{"null", "null", true, nil},
		{"空对象", "{}", true, nil},
		{"坏 JSON", "{bad", true, nil},
		{"空 map prices 归一为 nil", `{"prices":{}}`, true, nil},
		{"仅矩阵", `{"prices":{"720p":0.1}}`, false, func(t *testing.T, p *TenantPricingPatch) {
			if len(p.Prices) != 1 || p.Prices["720p"] != 0.1 {
				t.Errorf("prices = %v", p.Prices)
			}
			if p.TimeSegments != nil || p.ParamMultipliers != nil {
				t.Errorf("time_segments/param_multipliers 应为 nil（继承）")
			}
		}},
		{"时段显式关闭", `{"time_segments":[]}`, false, func(t *testing.T, p *TenantPricingPatch) {
			if p.TimeSegments == nil {
				t.Fatalf("time_segments 应为非 nil 指针指向空切片（显式关闭），得到 nil")
			}
			if len(*p.TimeSegments) != 0 {
				t.Errorf("*TimeSegments 应为空切片")
			}
		}},
		{"时段覆盖", `{"time_segments":[{"name":"夜间","multiplier":0.8}]}`, false, func(t *testing.T, p *TenantPricingPatch) {
			if p.TimeSegments == nil || len(*p.TimeSegments) != 1 || (*p.TimeSegments)[0].Name != "夜间" {
				t.Errorf("time_segments 解析错误: %+v", p.TimeSegments)
			}
		}},
		{"倍率显式关闭", `{"param_multipliers":[]}`, false, func(t *testing.T, p *TenantPricingPatch) {
			if p.ParamMultipliers == nil || len(*p.ParamMultipliers) != 0 {
				t.Errorf("param_multipliers 应为非 nil 空切片（显式关闭）")
			}
		}},
		{"三键并存", `{"prices":{"*":1},"time_segments":[],"param_multipliers":[{"conditions":[{"path":"a","match":"has_value"}],"multiplier":1.5}]}`, false, func(t *testing.T, p *TenantPricingPatch) {
			if len(p.Prices) != 1 || p.TimeSegments == nil || p.ParamMultipliers == nil || len(*p.ParamMultipliers) != 1 {
				t.Errorf("三键解析错误: %+v", p)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseTenantPricingPatch(tt.raw)
			if tt.wantNil {
				if got != nil {
					t.Errorf("ParseTenantPricingPatch(%q) = %+v, want nil", tt.raw, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("ParseTenantPricingPatch(%q) = nil, want patch", tt.raw)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

// TestTenantPricingPatchRoundtrip 序列化 roundtrip：显式关闭（空切片）必须保真为 []，
// 不得被序列化层吞成缺键——这是「关闭」与「继承」语义的分界
func TestTenantPricingPatchRoundtrip(t *testing.T) {
	empty := []TimeSegment{}
	patch := &TenantPricingPatch{TimeSegments: &empty}
	b, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	// 显式关闭必须产出 "time_segments":[] 键
	if string(b) != `{"time_segments":[]}` {
		t.Fatalf("空切片序列化 = %s, want {\"time_segments\":[]}", b)
	}
	back := ParseTenantPricingPatch(string(b))
	if back == nil || back.TimeSegments == nil || len(*back.TimeSegments) != 0 {
		t.Fatalf("roundtrip 丢失显式关闭语义: %+v", back)
	}

	// 继承（nil 指针）序列化必须省键
	inherit := &TenantPricingPatch{Prices: map[string]float64{"*": 1}}
	b2, _ := json.Marshal(inherit)
	if string(b2) != `{"prices":{"*":1}}` {
		t.Fatalf("nil 指针序列化 = %s, 应省略 time_segments/param_multipliers 键", b2)
	}
}

// TestApplyTenantPricingPatch 覆盖应用：键存在的维度替换、缺失的维度平台值直传
func TestApplyTenantPricingPatch(t *testing.T) {
	basePrices := map[string]float64{"720p": 0.1, "*": 0.2}
	baseSegs := []TimeSegment{{Name: "平台夜间", Multiplier: 0.8}}
	baseRules := []ParamRule{{Conditions: []ParamCondition{{Path: "a", Match: "has_value"}}, Multiplier: 1.2}}

	// nil 补丁全透传（存量行为不变）
	p, s, r := ApplyTenantPricingPatch(nil, basePrices, baseSegs, baseRules)
	if len(p) != 2 || len(s) != 1 || len(r) != 1 {
		t.Errorf("nil 补丁应全透传: p=%v s=%v r=%v", p, s, r)
	}

	// 矩阵替换、其余继承
	p, s, r = ApplyTenantPricingPatch(&TenantPricingPatch{Prices: map[string]float64{"1080p": 0.3}}, basePrices, baseSegs, baseRules)
	if len(p) != 1 || p["1080p"] != 0.3 {
		t.Errorf("矩阵应整体替换: %v", p)
	}
	if len(s) != 1 || s[0].Name != "平台夜间" || len(r) != 1 {
		t.Errorf("时段/倍率应继承平台")
	}

	// 时段显式关闭（空切片替换 → len=0 → TimeMultiplier 兜底 1.0）
	emptySegs := []TimeSegment{}
	p, s, r = ApplyTenantPricingPatch(&TenantPricingPatch{TimeSegments: &emptySegs}, basePrices, baseSegs, baseRules)
	if len(s) != 0 {
		t.Errorf("时段应被关闭（空）, got %v", s)
	}
	if len(p) != 2 || len(r) != 1 {
		t.Errorf("矩阵/倍率应继承平台")
	}

	// 倍率覆盖
	newRules := []ParamRule{{Conditions: []ParamCondition{{Path: "b", Match: "equals", Value: "hd"}}, Multiplier: 2}}
	p, s, r = ApplyTenantPricingPatch(&TenantPricingPatch{ParamMultipliers: &newRules}, basePrices, baseSegs, baseRules)
	if len(r) != 1 || r[0].Conditions[0].Path != "b" {
		t.Errorf("倍率应整体替换: %v", r)
	}
	if len(s) != 1 || len(p) != 2 {
		t.Errorf("矩阵/时段应继承平台")
	}
}

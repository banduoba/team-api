package admin

import (
	"testing"

	v1 "github.com/qianfree/team-api/api/admin/v1"
)

// pptrOf 双层指针快捷构造（模拟 API 请求里「键存在」的传参形态）
func pptrOf[T any](v *T) **T { return &v }

// TestBuildTenantPricingPatchUpdate 补丁读改写合并：四态语义（省略=不动 / null=清除恢复继承 /
// 空数组=显式关闭 / 非空=整体替换）与旧补丁部分更新
func TestBuildTenantPricingPatchUpdate(t *testing.T) {
	validSegs := []v1.TimeSegmentItem{{Name: "夜间", StartTime: "22:00", EndTime: "06:00", Multiplier: 0.8}}
	validRules := []v1.ParamMultiplierItem{{
		Conditions: []v1.ParamConditionItem{{Path: "quality", Match: "equals", Value: "hd"}},
		Multiplier: 1.5,
	}}

	t.Run("全部省略=不动", func(t *testing.T) {
		jsonStr, patch, err := buildTenantPricingPatchUpdate("", nil, nil, nil)
		if err != nil || jsonStr != "" || patch != nil {
			t.Errorf("空旧补丁应返回空，got (%q, %+v, %v)", jsonStr, patch, err)
		}
		// 旧补丁存在但请求全省略 → 原样返回旧值（不动）
		jsonStr, patch, err = buildTenantPricingPatchUpdate(`{"prices":{"*":1}}`, nil, nil, nil)
		if err != nil || jsonStr != `{"prices":{"*":1}}` || patch == nil || len(patch.Prices) != 1 {
			t.Errorf("全省略应原样返回旧补丁，got (%q, %+v, %v)", jsonStr, patch, err)
		}
	})

	t.Run("内层 null=清除恢复继承", func(t *testing.T) {
		oldRaw := `{"prices":{"*":1},"time_segments":[{"name":"x","multiplier":0.5}]}`
		var nilPrices *map[string]float64
		var nilSegs *[]v1.TimeSegmentItem
		jsonStr, patch, err := buildTenantPricingPatchUpdate(oldRaw, &nilPrices, &nilSegs, nil)
		if err != nil {
			t.Fatal(err)
		}
		// 两键同时清除 → 补丁全空，应写 NULL（返回 patch 为 nil）
		if patch != nil {
			t.Fatalf("清除后应全空（写 NULL），got patch=%+v json=%s", patch, jsonStr)
		}
		if jsonStr != "" {
			t.Errorf("全空补丁应写 NULL, got %s", jsonStr)
		}
	})

	t.Run("空数组=显式关闭", func(t *testing.T) {
		emptySegs := []v1.TimeSegmentItem{}
		jsonStr, patch, err := buildTenantPricingPatchUpdate("", nil, pptrOf(&emptySegs), nil)
		if err != nil {
			t.Fatal(err)
		}
		if patch.TimeSegments == nil || len(*patch.TimeSegments) != 0 {
			t.Fatalf("空数组应保留显式关闭语义（非 nil 空切片）")
		}
		if jsonStr != `{"time_segments":[]}` {
			t.Errorf("序列化 = %s, want {\"time_segments\":[]}", jsonStr)
		}
	})

	t.Run("部分更新：旧补丁矩阵 + 新时段，矩阵保留", func(t *testing.T) {
		oldRaw := `{"prices":{"720p":0.1}}`
		jsonStr, patch, err := buildTenantPricingPatchUpdate(oldRaw, nil, pptrOf(&validSegs), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(patch.Prices) != 1 || patch.Prices["720p"] != 0.1 {
			t.Errorf("未提交的矩阵键应保留旧值: %+v", patch.Prices)
		}
		if patch.TimeSegments == nil || len(*patch.TimeSegments) != 1 {
			t.Errorf("时段应写入: %s", jsonStr)
		}
	})

	t.Run("矩阵非法输入拒绝（全零价）", func(t *testing.T) {
		badPrices := map[string]float64{"720p": 0}
		if _, _, err := buildTenantPricingPatchUpdate("", pptrOf(&badPrices), nil, nil); err == nil {
			t.Errorf("全零矩阵应被拒绝（至少一档正价）")
		}
	})

	t.Run("矩阵非法输入拒绝（空规格键）", func(t *testing.T) {
		badPrices := map[string]float64{"": 1}
		if _, _, err := buildTenantPricingPatchUpdate("", pptrOf(&badPrices), nil, nil); err == nil {
			t.Errorf("空规格键应被拒绝")
		}
	})

	t.Run("时段校验失败拒绝（乘数超界）", func(t *testing.T) {
		badSegs := []v1.TimeSegmentItem{{Name: "坏", Multiplier: 20}}
		if _, _, err := buildTenantPricingPatchUpdate("", nil, pptrOf(&badSegs), nil); err == nil {
			t.Errorf("乘数超界应被拒绝")
		}
	})

	t.Run("倍率校验失败拒绝", func(t *testing.T) {
		badRules := []v1.ParamMultiplierItem{{Conditions: []v1.ParamConditionItem{{Path: "a", Match: "has_value"}}, Multiplier: 0}}
		if _, _, err := buildTenantPricingPatchUpdate("", nil, nil, pptrOf(&badRules)); err == nil {
			t.Errorf("零乘数应被拒绝")
		}
	})

	t.Run("矩阵空 map=清除恢复继承", func(t *testing.T) {
		emptyPrices := map[string]float64{}
		oldRaw := `{"prices":{"720p":0.1},"time_segments":[]}`
		jsonStr, patch, err := buildTenantPricingPatchUpdate(oldRaw, pptrOf(&emptyPrices), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if patch.Prices != nil {
			t.Errorf("空 map 矩阵应清除")
		}
		if patch.TimeSegments == nil {
			t.Errorf("未提交的时段键（显式关闭）应保留")
		}
		if jsonStr != `{"time_segments":[]}` {
			t.Errorf("序列化 = %s, want {\"time_segments\":[]}", jsonStr)
		}
	})

	t.Run("三键并存完整覆盖", func(t *testing.T) {
		prices := map[string]float64{"1080p": 0.3, "*": 0.2}
		jsonStr, patch, err := buildTenantPricingPatchUpdate("", pptrOf(&prices), pptrOf(&validSegs), pptrOf(&validRules))
		if err != nil {
			t.Fatal(err)
		}
		if len(patch.Prices) != 2 || patch.TimeSegments == nil || patch.ParamMultipliers == nil {
			t.Errorf("三键应并存: %s", jsonStr)
		}
	})
}

// TestDisplayColValue 展示字段列值口径：trim 后空串=清除（NULL Raw），非空=覆盖原值
func TestDisplayColValue(t *testing.T) {
	if v, ok := displayColValue("  夜间特惠  ").(string); !ok || v != "夜间特惠" {
		t.Errorf("应 trim 后返回字符串, got %v", displayColValue("  夜间特惠  "))
	}
	// 空串/纯空白返回 gdb.Raw（NULL），类型不同于 string——两种输入产生不同类型即可验证口径分叉
	_, emptyIsStr := displayColValue("   ").(string)
	_, nonEmptyIsStr := displayColValue("x").(string)
	if emptyIsStr || !nonEmptyIsStr {
		t.Errorf("空串应返回非 string 的 NULL 哨兵，非空应返回 string")
	}
}

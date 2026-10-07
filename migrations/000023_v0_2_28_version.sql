-- +goose Up
-- ============================================================
-- 明确 bil_usage_logs 费用列口径注释
--
-- total_cost 与 actual_cost 的语义契约（同步对话与异步任务两条写入链路一致）：
--   total_cost  = 基础费用（不含租户/时段折扣；异步任务行仍含请求级附加乘数
--                 ——video_input 折扣、quality 倍率等属请求定价结构，随实际费用
--                 产生、按两乘数还原，见 billing.preMultiplierCost）；
--   actual_cost = 折后实际扣款金额（钱包真实扣减）。
-- 此前任务链路曾把 actual 同时写入两列，与同步链路分叉，导致折扣租户下
-- SUM(total_cost) 类趋势统计口径混杂；本迁移仅澄清列注释固化口径，不改数据。
-- ============================================================

COMMENT ON COLUMN bil_usage_logs.total_cost IS '本次调用基础费用（不含租户/时段折扣；异步任务行含请求级附加乘数，与 bil_records 快照 BaseCost 同口径）';
COMMENT ON COLUMN bil_usage_logs.actual_cost IS '实际扣除费用（含折扣后，钱包真实扣减金额；报表聚合优先取本列，0/NULL 回退 total_cost）';

-- ============================================================
-- 租户模型定价能力与平台全量对齐：custom_pricing 覆盖补丁 + 展示字段覆盖
-- ============================================================

-- 覆盖补丁（只承载三类扩展计费配置；价格类覆盖仍走既有扁平列，避免两套真相）。
-- 键三态语义：缺键=继承平台；time_segments/param_multipliers 为空数组=显式关闭平台配置；非空=整体替换。
-- prices 为按秒矩阵（非空=整体替换，仅平台计费模式 per_second/special 时被消费）
ALTER TABLE mdl_tenant_models
    ADD COLUMN IF NOT EXISTS custom_pricing JSONB;

COMMENT ON COLUMN mdl_tenant_models.custom_pricing IS '定价覆盖补丁（JSONB，三键）：prices 按秒矩阵 {规格:每秒单价}；time_segments 时段定价数组（[] = 显式关闭平台时段）；param_multipliers 参数倍率规则（[] = 显式关闭）。键缺失 = 继承平台定价';

-- 展示字段覆盖（NULL = 继承平台 mdl_pricing 对应列）
ALTER TABLE mdl_tenant_models
    ADD COLUMN IF NOT EXISTS price_note        VARCHAR(500),
    ADD COLUMN IF NOT EXISTS discount_label    VARCHAR(50),
    ADD COLUMN IF NOT EXISTS price_change_note VARCHAR(200);

COMMENT ON COLUMN mdl_tenant_models.price_note IS '价格说明覆盖（仅管理后台可见），NULL 表示继承平台';
COMMENT ON COLUMN mdl_tenant_models.discount_label IS '折扣标签覆盖（对外展示），NULL 表示继承平台';
COMMENT ON COLUMN mdl_tenant_models.price_change_note IS '价格调整说明覆盖（对外展示），NULL 表示继承平台';

-- +goose Down
ALTER TABLE mdl_tenant_models
    DROP COLUMN IF EXISTS custom_pricing,
    DROP COLUMN IF EXISTS price_note,
    DROP COLUMN IF EXISTS discount_label,
    DROP COLUMN IF EXISTS price_change_note;

COMMENT ON COLUMN bil_usage_logs.total_cost IS '本次调用费用';
COMMENT ON COLUMN bil_usage_logs.actual_cost IS '实际扣除费用（含折扣后）';

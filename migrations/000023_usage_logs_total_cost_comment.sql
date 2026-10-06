-- +goose Up
-- ============================================================
-- 明确 bil_usage_logs 费用列口径注释
--
-- total_cost 与 actual_cost 的语义契约（同步对话与异步任务两条写入链路一致）：
--   total_cost  = 折扣前基础费用（租户/时段/附加乘数应用前，BaseCost）；
--   actual_cost = 折扣后实际扣款金额（钱包真实扣减）。
-- 此前任务链路曾把 actual 同时写入两列，与同步链路分叉，导致折扣租户下
-- SUM(total_cost) 类趋势统计口径混杂；本迁移仅澄清列注释固化口径，不改数据。
-- ============================================================

COMMENT ON COLUMN bil_usage_logs.total_cost IS '本次调用基础费用（折扣前：租户/时段/附加乘数应用前，与 bil_records 快照 BaseCost 同口径）';
COMMENT ON COLUMN bil_usage_logs.actual_cost IS '实际扣除费用（含折扣后，钱包真实扣减金额；报表聚合优先取本列，0/NULL 回退 total_cost）';

-- +goose Down

COMMENT ON COLUMN bil_usage_logs.total_cost IS '本次调用费用';
COMMENT ON COLUMN bil_usage_logs.actual_cost IS '实际扣除费用（含折扣后）';

package agents

// 全局常量 - 统一所有Agent使用的阈值与风控参数
const (
	// === 市场体制阈值 ===
	ATRPctNarrowC = 1.0  // <1.0% = 窄幅盘整(C)
	ATRPctLow     = 2.0  // <2.0% = 低波动
	ATRPctMid     = 4.0  // <4.0% = 中波动, >=4.0% = 高波动

	// === 止损止盈倍数范围 ===
	MinStopMultiple = 2.0  // 最小止损倍数（xATR）
	MaxStopMultiple = 8.0  // 最大止损倍数（xATR）
	MinTPMultiple   = 6.0  // 最小止盈倍数（xATR）
	MaxTPMultiple   = 20.0 // 最大止盈倍数（xATR）

	// === 风险回报比要求 ===
	MinRiskReward        = 2.0  // 最低R/R比要求
	RRFloatTolerance     = 0.05 // R/R比允许的浮点误差（5%）
	RRStrictTolerance    = 0.01 // 强平调整前的严格误差（1%）

	// === 成交量信号阈值 ===
	VolumeExpandThreshold = 20.0  // 成交量放大阈值（%）
	VolumeShrinkThreshold = -50.0 // 成交量萎缩阈值（%）

	// === 资金费率阈值 ===
	FundingRateShortThreshold = 0.01 // 做空资金费率阈值（%）

	// === 强平价安全边距 ===
	LiquidationSafetyRatio = 0.3  // 止损价距强平价的安全缓冲（30%）
	LiquidationMarginRate  = 0.95 // 强平保证金率（近似）

	// === 仓位sizing风险预算 ===
	RiskBudgetPerTrade = 0.01 // 每笔交易的风险预算（1%净值）
	MarginUsageLimit   = 0.95 // 可用保证金使用上限（95%）

	// === 信号评分体系 ===
	SignalBaseScore       = 50 // 基础分
	SignalPerDimensScore  = 12 // 每个信号维度加分
	SignalPerfectBonus    = 10 // 完美体制匹配加分
	SignalMinForValid     = 3  // 有效信号的最低维度数

	// === 绩效记忆门槛 ===
	SharpeCircuitBreaker  = -0.5 // 夏普比率熔断阈值
	SharpeStrictThreshold = 0.0  // 严格控制阈值
	SharpeNormalThreshold = 0.7  // 正常/优异分界线

	ConfidenceDefault    = 80  // 默认信心度门槛
	ConfidenceStrict     = 85  // 严格控制门槛
	ConfidenceExcellent  = 75  // 优异表现门槛
	ConfidenceCircuitBrk = 101 // 熔断门槛（>100=禁止开仓）

	// === 信心等级调整系数 ===
	ConfidenceHighMultiplier   = 1.2 // 高信心仓位倍数
	ConfidenceMediumMultiplier = 1.0 // 中等信心仓位倍数
	ConfidenceLowMultiplier    = 0.8 // 低信心仓位倍数

	// === EMA位置判断阈值 ===
	EMA20TolerancePct = 0.02 // EMA20附近的宽容度（±2%）
)

package memory

import "time"

// SimpleMemory Sprint 1版本：工作记忆 + 基础记录
type SimpleMemory struct {
	Version      string       `json:"version"`
	TraderID     string       `json:"trader_id"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	TotalTrades  int          `json:"total_trades"`
	Status       string       `json:"status"` // learning/mature

	// Working Memory: 最近20笔交易
	RecentTrades []TradeEntry `json:"recent_trades"`

	// Seed Knowledge: 只保留硬约束（基础风控）
	HardConstraints []string `json:"hard_constraints"`

	// 🆕 自适应学习模块
	LearningSummary *LearningSummary `json:"learning_summary,omitempty"`
}

// 🆕 LearningSummary 学习总结（自动生成）
type LearningSummary struct {
	UpdatedAt time.Time `json:"updated_at"`

	// 信号成功率统计
	SignalStats map[string]*SignalStat `json:"signal_stats"`

	// 失败模式识别
	FailurePatterns []string `json:"failure_patterns"`

	// 成功经验总结
	SuccessPatterns []string `json:"success_patterns"`

	// 市场环境偏好
	MarketPreferences map[string]float64 `json:"market_preferences"` // regime -> win_rate
}

// 🆕 SignalStat 信号统计
type SignalStat struct {
	SignalName  string  `json:"signal_name"`
	TotalCount  int     `json:"total_count"`
	WinCount    int     `json:"win_count"`
	LossCount   int     `json:"loss_count"`
	WinRate     float64 `json:"win_rate"`
	AvgReturn   float64 `json:"avg_return"`
	LastUsed    time.Time `json:"last_used"`
}

// TradeEntry 单笔交易记录
type TradeEntry struct {
	TradeID   int       `json:"trade_id"`
	Cycle     int       `json:"cycle"`
	Timestamp time.Time `json:"timestamp"`

	// 市场环境
	MarketRegime string `json:"market_regime"` // accumulation/markup/distribution/markdown
	RegimeStage  string `json:"regime_stage"`  // early/mid/late

	// 决策信息
	Action    string   `json:"action"`    // open/close/hold
	Symbol    string   `json:"symbol"`    // BTCUSDT/ETHUSDT/...
	Side      string   `json:"side"`      // long/short
	Signals   []string `json:"signals"`   // ["MACD金叉", "RSI超卖"]
	Reasoning string   `json:"reasoning"` // AI的推理过程

	// AI预测信息（用于验证）
	PredictedDirection string  `json:"predicted_direction,omitempty"` // up/down
	PredictedProb      float64 `json:"predicted_prob,omitempty"`      // 0.0-1.0
	PredictedMove      float64 `json:"predicted_move,omitempty"`      // 预期涨跌幅%

	// 持仓信息
	EntryPrice  float64 `json:"entry_price,omitempty"`
	ExitPrice   float64 `json:"exit_price,omitempty"`
	PositionPct float64 `json:"position_pct"` // 仓位占比%
	Leverage    int     `json:"leverage,omitempty"`

	// 🆕 限价单信息
	IsLimitOrder bool    `json:"is_limit_order,omitempty"` // 是否是限价单
	LimitPrice   float64 `json:"limit_price,omitempty"`    // 限价单目标价格
	CurrentPrice float64 `json:"current_price,omitempty"`  // 提交时的市价

	// 🆕 市场数值快照（关键技术指标）
	MarketSnapshot *MarketSnapshot `json:"market_snapshot,omitempty"`

	// 结果
	HoldMinutes int     `json:"hold_minutes,omitempty"` // 持仓时长
	ReturnPct   float64 `json:"return_pct"`             // 收益率%
	Result      string  `json:"result"`                 // win/loss/break_even
}

// 🆕 MarketSnapshot 市场数值快照（用于精准复盘）
// 记录开仓/平仓时的关键市场指标，帮助AI识别失败模式
type MarketSnapshot struct {
	// RSI指标（识别超买超卖）
	RSI7  float64 `json:"rsi7"`  // 7周期RSI（更敏感）
	RSI14 float64 `json:"rsi14"` // 14周期RSI（标准）

	// MACD指标（识别趋势反转）
	MACD       float64 `json:"macd"`        // MACD线
	MACDSignal float64 `json:"macd_signal"` // 信号线
	MACDHist   float64 `json:"macd_hist"`   // 柱状图（快速判断金叉/死叉）

	// ADX & DI（识别趋势强度和方向）
	ADX     float64 `json:"adx"`      // 趋势强度（0-100）
	PlusDI  float64 `json:"plus_di"`  // 多头力量
	MinusDI float64 `json:"minus_di"` // 空头力量

	// 价格变化（识别追涨杀跌）
	PriceChange1h  float64 `json:"price_change_1h"`  // 1小时涨跌幅%
	PriceChange4h  float64 `json:"price_change_4h"`  // 4小时涨跌幅%
	PriceChange24h float64 `json:"price_change_24h"` // 24小时涨跌幅%

	// EMA位置（识别趋势）
	PriceVsEMA20Pct float64 `json:"price_vs_ema20_pct"` // 价格相对EMA20偏离度%
	PriceVsEMA50Pct float64 `json:"price_vs_ema50_pct"` // 价格相对EMA50偏离度%

	// 当前价格（用于计算）
	CurrentPrice float64 `json:"current_price"`
}

// OverallStats 整体统计（用于可视化）
type OverallStats struct {
	TotalTrades   int     `json:"total_trades"`
	WinCount      int     `json:"win_count"`
	LossCount     int     `json:"loss_count"`
	WinRate       float64 `json:"win_rate"`
	AvgReturn     float64 `json:"avg_return"`
	TotalReturn   float64 `json:"total_return"`
	MaxDrawdown   float64 `json:"max_drawdown"`
	RecentWinRate float64 `json:"recent_win_rate"` // 最近10笔
}

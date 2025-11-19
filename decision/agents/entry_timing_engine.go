package agents

import (
	"fmt"
	"nofx/decision/types"
	"nofx/market"
)

// EntryTimingEngine 入场时机规则引擎（无需AI调用）
type EntryTimingEngine struct {
	// 规则引擎配置
	ADXMinimum        float64 // ADX最低要求（强趋势过滤）
	FundingRateLimit  float64 // 资金费率上限（永续合约风控）
	RSIOverBought     float64 // RSI超买阈值
	RSIOverSold       float64 // RSI超卖阈值
	PriceEMA20MaxDist float64 // 价格距离EMA20最大偏离%
}

// NewEntryTimingEngine 创建入场时机引擎
func NewEntryTimingEngine() *EntryTimingEngine {
	return &EntryTimingEngine{
		ADXMinimum:        25.0,  // ADX>25强趋势
		FundingRateLimit:  0.0001, // 0.01%资金费率上限
		RSIOverBought:     70.0,   // RSI>70超买
		RSIOverSold:       30.0,   // RSI<30超卖
		PriceEMA20MaxDist: 3.0,    // 价格距EMA20最大3%
	}
}

// EntryDecision 入场决策
type EntryDecision struct {
	Strategy      string  // "immediate" 或 "wait_pullback" 或 "reject"
	LimitPrice    float64 // 限价单价格（wait_pullback时）
	CurrentPrice  float64 // 当前价格
	PullbackPct   float64 // 期望回调百分比
	ExpiryHours   int     // 有效期（小时）
	Reasoning     string  // 决策推理
	KeyLevels     []float64 // 关键价位（EMA20, EMA50等）
}

// Decide 决策入场时机
func (e *EntryTimingEngine) Decide(
	prediction *types.Prediction,
	marketData *market.Data,
) (*EntryDecision, error) {

	// 🚫 第1步：趋势过滤（硬性拒绝）
	if err := e.validateTrend(prediction.Direction, marketData); err != nil {
		return nil, fmt.Errorf("趋势验证失败: %w", err)
	}

	// 🚫 第2步：ADX强度过滤
	if marketData.CurrentADX < e.ADXMinimum {
		return nil, fmt.Errorf("拒绝入场：ADX=%.1f < %.1f，趋势不够强（震荡市）",
			marketData.CurrentADX, e.ADXMinimum)
	}

	// 🚫 第3步：资金费率监控（永续合约关键）
	if err := e.validateFundingRate(prediction.Direction, marketData); err != nil {
		return nil, fmt.Errorf("资金费率风控: %w", err)
	}

	// ✅ 第4步：判断入场时机（immediate / wait / reject）
	timing := e.classifyEntryTiming(prediction.Direction, marketData)

	switch timing {
	case "immediate":
		return &EntryDecision{
			Strategy:     "immediate",
			CurrentPrice: marketData.CurrentPrice,
			Reasoning: fmt.Sprintf("健康入场：RSI=%.1f, ADX=%.1f, +DI/−DI=%.1f/%.1f",
				marketData.CurrentRSI14, marketData.CurrentADX,
				marketData.CurrentPlusDI, marketData.CurrentMinusDI),
		}, nil

	case "wait":
		// 计算回调目标价
		targetPrice := e.calculateTargetPrice(prediction.Direction, marketData)
		pullbackPct := (targetPrice - marketData.CurrentPrice) / marketData.CurrentPrice * 100
		expiry := e.calculateExpiry(prediction, marketData)

		return &EntryDecision{
			Strategy:     "wait_pullback",
			LimitPrice:   targetPrice,
			CurrentPrice: marketData.CurrentPrice,
			PullbackPct:  pullbackPct,
			ExpiryHours:  expiry,
			Reasoning: e.buildWaitReasoning(prediction.Direction, marketData, targetPrice),
			KeyLevels: []float64{
				marketData.LongerTermContext.EMA20,
				marketData.LongerTermContext.EMA50,
			},
		}, nil

	case "reject":
		return nil, fmt.Errorf("入场条件不佳: %s", e.buildRejectReason(prediction.Direction, marketData))

	default:
		return nil, fmt.Errorf("未知入场时机类型: %s", timing)
	}
}

// validateTrend 趋势验证（核心改进：基于EMA50和+DI/-DI）
func (e *EntryTimingEngine) validateTrend(direction string, md *market.Data) error {
	if md.LongerTermContext == nil {
		return fmt.Errorf("缺少长期数据")
	}

	currentPrice := md.CurrentPrice
	ema50 := md.LongerTermContext.EMA50
	plusDI := md.CurrentPlusDI
	minusDI := md.CurrentMinusDI

	// 计算价格相对EMA50的偏离度
	distPct := (currentPrice - ema50) / ema50 * 100

	// 🔧 容差范围：价格在EMA50的±1%内视为盘整区间
	// 盘整区间内主要依靠+DI/-DI判断，不强制要求价格位置
	const tolerancePct = 1.0

	if direction == "up" {
		// ✅ 做多：只有空头力量明显占优（≥1.5倍）时才拒绝
		// 允许多空胶着时综合其他指标判断
		if minusDI > plusDI*1.5 {
			return fmt.Errorf("-DI(%.1f) > +DI(%.1f)*1.5，空头力量明显占优",
				minusDI, plusDI)
		}

		// 🔧 价格检查：只有在明显低于EMA50时才拒绝（偏离>1%）
		// 允许在EMA50附近盘整时开多（只要空头不是明显占优）
		if distPct < -tolerancePct {
			return fmt.Errorf("价格%.2f < EMA50 %.2f (%.2f%%)，长期趋势向下（偏离超过%.1f%%容差）",
				currentPrice, ema50, distPct, tolerancePct)
		}

	} else if direction == "down" {
		// ✅ 做空：只有多头力量明显占优（≥1.5倍）时才拒绝
		// 允许多空胶着时综合其他指标判断
		if plusDI > minusDI*1.5 {
			return fmt.Errorf("+DI(%.1f) > -DI(%.1f)*1.5，多头力量明显占优",
				plusDI, minusDI)
		}

		// 🔧 价格检查：只有在明显高于EMA50时才拒绝（偏离>1%）
		// 允许在EMA50附近盘整时开空（只要多头不是明显占优）
		if distPct > tolerancePct {
			return fmt.Errorf("价格%.2f > EMA50 %.2f (%.2f%%)，长期趋势向上（偏离超过%.1f%%容差）",
				currentPrice, ema50, distPct, tolerancePct)
		}
	}

	return nil
}

// validateFundingRate 资金费率验证（永续合约风控）
func (e *EntryTimingEngine) validateFundingRate(direction string, md *market.Data) error {
	fundingRate := md.FundingRate

	if direction == "up" {
		// 做多：资金费率过高 → 多头拥挤
		if fundingRate > e.FundingRateLimit {
			return fmt.Errorf("资金费率%.4f%% > %.4f%%，多头过度拥挤",
				fundingRate*100, e.FundingRateLimit*100)
		}
	} else if direction == "down" {
		// 做空：资金费率过低（负值） → 空头拥挤
		if fundingRate < -e.FundingRateLimit {
			return fmt.Errorf("资金费率%.4f%% < -%.4f%%，空头过度拥挤",
				fundingRate*100, e.FundingRateLimit*100)
		}
	}

	return nil
}

// classifyEntryTiming 分类入场时机（简化版 - 防止过拟合）
// 核心原则：只拒绝明显不合理的入场，避免过多条件导致过拟合
func (e *EntryTimingEngine) classifyEntryTiming(direction string, md *market.Data) string {
	rsi14 := md.CurrentRSI14
	priceChange1h := md.PriceChange1h
	ema20 := md.LongerTermContext.EMA20
	currentPrice := md.CurrentPrice

	// 计算价格相对EMA20的偏离度
	priceToEMA := ((currentPrice - ema20) / ema20) * 100

	if direction == "up" {
		// ============ 做多入场时机（简化版）============

		// 🚫 硬性拒绝：极端超买
		if rsi14 > 80 {
			return "reject"
		}

		// 🚫 硬性拒绝：1h涨幅过大（追高风险）
		if priceChange1h > 5.0 {
			return "reject"
		}

		// 🚫 硬性拒绝：价格远高于EMA20（过度偏离）
		if priceToEMA > 4.0 {
			return "reject"
		}

		// ⏰ 等待回调：中度超买或中度涨幅
		if rsi14 > 70 || priceChange1h > 3.0 || priceToEMA > 2.5 {
			return "wait"
		}

		// ✅ 其他情况：立即入场
		return "immediate"

	} else if direction == "down" {
		// ============ 做空入场时机（简化版）============

		// 🚫 硬性拒绝：极端超卖
		if rsi14 < 20 {
			return "reject"
		}

		// 🚫 硬性拒绝：1h跌幅过大（杀跌风险）
		if priceChange1h < -5.0 {
			return "reject"
		}

		// 🚫 硬性拒绝：价格远低于EMA20（过度偏离）
		if priceToEMA < -4.0 {
			return "reject"
		}

		// ⏰ 等待反弹：中度超卖或中度跌幅
		if rsi14 < 30 || priceChange1h < -3.0 || priceToEMA < -2.5 {
			return "wait"
		}

		// ✅ 其他情况：立即入场
		return "immediate"
	}

	return "reject"
}

// calculateTargetPrice 计算回调目标价
func (e *EntryTimingEngine) calculateTargetPrice(direction string, md *market.Data) float64 {
	currentPrice := md.CurrentPrice
	ema20 := md.LongerTermContext.EMA20
	rsi14 := md.CurrentRSI14
	priceChange1h := md.PriceChange1h

	var candidates []float64

	if direction == "up" {
		// 档位1：EMA20支撑（优先）
		ema20Dist := (currentPrice - ema20) / currentPrice * 100
		if ema20Dist > 0.3 && ema20Dist < 2.5 {
			candidates = append(candidates, ema20)
		}

		// 档位2：1h涨幅回吐50%
		if priceChange1h > 2.0 {
			priceAgo := currentPrice / (1 + priceChange1h/100)
			retracement := currentPrice - (currentPrice-priceAgo)*0.5
			candidates = append(candidates, retracement)
		}

		// 档位3：固定百分比回调（保底）
		pullbackPct := 0.5
		if rsi14 > 70 {
			pullbackPct = 1.5
		} else if rsi14 > 65 {
			pullbackPct = 1.0
		}
		candidates = append(candidates, currentPrice*(1-pullbackPct/100))

		// 选择最接近当前价的（更容易成交）
		return e.selectClosestPrice(candidates, currentPrice)

	} else {
		// 做空：等反弹到更高价格
		ema20Dist := (ema20 - currentPrice) / currentPrice * 100
		if ema20Dist > 0.3 && ema20Dist < 2.5 {
			candidates = append(candidates, ema20)
		}

		// 跌幅反弹50%
		if priceChange1h < -2.0 {
			priceAgo := currentPrice / (1 + priceChange1h/100)
			retracement := currentPrice + (priceAgo-currentPrice)*0.5
			candidates = append(candidates, retracement)
		}

		// 固定反弹
		bouncePct := 0.5
		if rsi14 < 30 {
			bouncePct = 1.5
		} else if rsi14 < 35 {
			bouncePct = 1.0
		}
		candidates = append(candidates, currentPrice*(1+bouncePct/100))

		return e.selectClosestPrice(candidates, currentPrice)
	}
}

// selectClosestPrice 选择最接近当前价的候选价格
func (e *EntryTimingEngine) selectClosestPrice(candidates []float64, currentPrice float64) float64 {
	if len(candidates) == 0 {
		return currentPrice
	}

	closest := candidates[0]
	minDist := abs(candidates[0] - currentPrice)

	for _, price := range candidates[1:] {
		dist := abs(price - currentPrice)
		if dist < minDist {
			minDist = dist
			closest = price
		}
	}

	return closest
}

// calculateExpiry 计算限价单有效期
func (e *EntryTimingEngine) calculateExpiry(prediction *types.Prediction, md *market.Data) int {
	baseExpiry := 2 // 默认2小时

	// 根据预测时间框架调整
	switch prediction.Timeframe {
	case "1h":
		baseExpiry = 1
	case "4h":
		baseExpiry = 3
	case "24h":
		baseExpiry = 6
	}

	// 根据波动率调整
	atrPct := (md.LongerTermContext.ATR14 / md.CurrentPrice) * 100
	if atrPct > 2.0 {
		baseExpiry = int(float64(baseExpiry) * 0.7) // 高波动-30%
	} else if atrPct < 0.5 {
		baseExpiry = int(float64(baseExpiry) * 1.3) // 低波动+30%
	}

	// 限制范围
	if baseExpiry < 1 {
		baseExpiry = 1
	}
	if baseExpiry > 8 {
		baseExpiry = 8
	}

	return baseExpiry
}

// buildWaitReasoning 构建等待回调的推理
func (e *EntryTimingEngine) buildWaitReasoning(direction string, md *market.Data, targetPrice float64) string {
	rsi14 := md.CurrentRSI14
	priceChange1h := md.PriceChange1h
	pullbackPct := (targetPrice - md.CurrentPrice) / md.CurrentPrice * 100

	if direction == "up" {
		if rsi14 > 65 {
			return fmt.Sprintf("RSI=%.1f超买，等回调%.2f%%到%.2f（EMA20附近）",
				rsi14, pullbackPct, targetPrice)
		}
		if priceChange1h > 3.0 {
			return fmt.Sprintf("1h涨幅%.2f%%过快，等回调%.2f%%到%.2f",
				priceChange1h, pullbackPct, targetPrice)
		}
		return fmt.Sprintf("等待回调%.2f%%到%.2f入场", pullbackPct, targetPrice)
	} else {
		if rsi14 < 35 {
			return fmt.Sprintf("RSI=%.1f超卖，等反弹%.2f%%到%.2f（EMA20阻力）",
				rsi14, pullbackPct, targetPrice)
		}
		if priceChange1h < -3.0 {
			return fmt.Sprintf("1h跌幅%.2f%%过快，等反弹%.2f%%到%.2f",
				priceChange1h, pullbackPct, targetPrice)
		}
		return fmt.Sprintf("等待反弹%.2f%%到%.2f入场", pullbackPct, targetPrice)
	}
}

// buildRejectReason 构建拒绝理由（包含具体市场数据）
func (e *EntryTimingEngine) buildRejectReason(direction string, md *market.Data) string {
	rsi14 := md.CurrentRSI14
	rsi7 := md.CurrentRSI7
	priceChange1h := md.PriceChange1h
	macd := md.CurrentMACD
	macdSignal := md.MACDSignal
	ema20 := md.LongerTermContext.EMA20
	priceToEMA := ((md.CurrentPrice - ema20) / ema20) * 100

	// 收集所有不合格的原因
	reasons := []string{}

	if direction == "up" {
		// 做多拒绝原因（统一阈值75）
		if rsi14 > 75 {
			reasons = append(reasons, fmt.Sprintf("RSI14=%.1f严重超买(>75)", rsi14))
		}
		if rsi7 > 75 {
			reasons = append(reasons, fmt.Sprintf("RSI7=%.1f严重超买(>75)", rsi7))
		}
		if priceChange1h > 4.0 {
			reasons = append(reasons, fmt.Sprintf("1h涨幅%.2f%%极端追高(>4%%)", priceChange1h))
		}
		if priceToEMA > 3.0 {
			reasons = append(reasons, fmt.Sprintf("价格高于EMA20达%.1f%%(>3%%)", priceToEMA))
		}
	} else if direction == "down" {
		// 做空拒绝原因（统一阈值35）
		if rsi14 < 35 {
			reasons = append(reasons, fmt.Sprintf("RSI14=%.1f超卖(<35)", rsi14))
		}
		if rsi7 < 35 {
			reasons = append(reasons, fmt.Sprintf("RSI7=%.1f超卖(<35)", rsi7))
		}
		if macd > macdSignal && rsi14 < 55 {
			reasons = append(reasons, fmt.Sprintf("MACD金叉(%.2f>%.2f)且RSI14=%.1f", macd, macdSignal, rsi14))
		}
		if priceChange1h < -3.0 {
			reasons = append(reasons, fmt.Sprintf("1h跌幅%.2f%%急跌(<-3%%)", priceChange1h))
		}
		if priceToEMA < -2.0 {
			reasons = append(reasons, fmt.Sprintf("价格低于EMA20达%.1f%%(<-2%%)", priceToEMA))
		}
	}

	// 如果没有具体原因，返回当前市场数据摘要
	if len(reasons) == 0 {
		return fmt.Sprintf("市场数据: RSI7=%.1f, RSI14=%.1f, MACD=%.2f/信号线=%.2f, 1h变化=%.2f%%, EMA偏离=%.1f%%",
			rsi7, rsi14, macd, macdSignal, priceChange1h, priceToEMA)
	}

	// 返回所有原因
	if len(reasons) == 1 {
		return reasons[0]
	}
	return fmt.Sprintf("%s", reasons)
}

// abs 绝对值
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

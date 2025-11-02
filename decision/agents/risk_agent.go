package agents

import (
	"encoding/json"
	"fmt"
	"nofx/market"
	"nofx/mcp"
	"strings"
)

// RiskParameters 风险计算参数
type RiskParameters struct {
	Leverage       int     `json:"leverage"`         // 杠杆倍数
	PositionSize   float64 `json:"position_size"`    // 仓位大小（USDT）
	StopLoss       float64 `json:"stop_loss"`        // 止损价
	TakeProfit     float64 `json:"take_profit"`      // 止盈价
	RiskReward     float64 `json:"risk_reward"`      // 风险回报比
	Valid          bool    `json:"valid"`            // 是否通过验证
	Reasoning      string  `json:"reasoning"`        // 计算过程

	// 验证细节
	LiquidationPrice float64 `json:"liquidation_price"` // 强平价
	RiskPercent      float64 `json:"risk_percent"`      // 风险百分比
	RewardPercent    float64 `json:"reward_percent"`    // 收益百分比
}

// AIRiskChoice AI的风险参数选择（仅选择倍数，不做计算）
type AIRiskChoice struct {
	StopMultiple       float64 `json:"stop_multiple"`        // 止损倍数
	TakeProfitMultiple float64 `json:"take_profit_multiple"` // 止盈倍数
	Reasoning          string  `json:"reasoning"`            // 选择理由
}

// RiskAgent 风险计算专家
type RiskAgent struct {
	mcpClient       *mcp.Client
	btcEthLeverage  int
	altcoinLeverage int
}

// NewRiskAgent 创建风险计算专家
func NewRiskAgent(mcpClient *mcp.Client, btcEthLeverage, altcoinLeverage int) *RiskAgent {
	return &RiskAgent{
		mcpClient:       mcpClient,
		btcEthLeverage:  btcEthLeverage,
		altcoinLeverage: altcoinLeverage,
	}
}

// Calculate 计算风险参数（Zero-Trust：Go代码做所有数学计算）
func (a *RiskAgent) Calculate(symbol string, direction string, confidenceLevel string, marketData *market.Data, regime *RegimeResult, accountEquity, availableBalance float64) (*RiskParameters, error) {
	if marketData == nil || marketData.LongerTermContext == nil {
		return nil, fmt.Errorf("市场数据不完整")
	}

	currentPrice := marketData.CurrentPrice
	atr := marketData.LongerTermContext.ATR14

	// Go代码计算ATR%（零信任：不让AI算）
	atrPct := (atr / currentPrice) * 100

	// 调用AI获取倍数选择
	aiChoice, err := a.getAIChoice(symbol, direction, currentPrice, atr, atrPct, regime)
	if err != nil {
		return nil, fmt.Errorf("AI选择失败: %w", err)
	}

	// Go代码验证倍数范围（防止AI作弊）
	if aiChoice.StopMultiple < 2.0 || aiChoice.StopMultiple > 8.0 {
		return nil, fmt.Errorf("AI选择的止损倍数%.1f超出合理范围[2.0-8.0]", aiChoice.StopMultiple)
	}
	if aiChoice.TakeProfitMultiple < 6.0 || aiChoice.TakeProfitMultiple > 20.0 {
		return nil, fmt.Errorf("AI选择的止盈倍数%.1f超出合理范围[6.0-20.0]", aiChoice.TakeProfitMultiple)
	}

	// 🚨 验证AI选择的倍数是否符合规则
	// 先验证基本范围
	if aiChoice.StopMultiple < MinStopMultiple || aiChoice.StopMultiple > MaxStopMultiple {
		return nil, fmt.Errorf("AI选择的止损倍数%.1f超出合理范围[%.1f-%.1f]", aiChoice.StopMultiple, MinStopMultiple, MaxStopMultiple)
	}
	if aiChoice.TakeProfitMultiple < MinTPMultiple || aiChoice.TakeProfitMultiple > MaxTPMultiple {
		return nil, fmt.Errorf("AI选择的止盈倍数%.1f超出合理范围[%.1f-%.1f]", aiChoice.TakeProfitMultiple, MinTPMultiple, MaxTPMultiple)
	}

	// 再验证是否符合ATR%期望
	expectedStopMultiple, expectedMinTPMultiple, expectedMaxTPMultiple := a.getExpectedMultiples(atrPct, regime)
	if aiChoice.StopMultiple < expectedStopMultiple-0.5 || aiChoice.StopMultiple > expectedStopMultiple+0.5 {
		return nil, fmt.Errorf("🚨 AI作弊：ATR%%=%.2f%%时期望止损%.1fx（±0.5），但AI选择了%.1fx",
			atrPct, expectedStopMultiple, aiChoice.StopMultiple)
	}
	if aiChoice.TakeProfitMultiple < expectedMinTPMultiple || aiChoice.TakeProfitMultiple > expectedMaxTPMultiple {
		return nil, fmt.Errorf("🚨 AI作弊：ATR%%=%.2f%%+体制%s时期望止盈%.1f-%.1fx，但AI选择了%.1fx",
			atrPct, regime.Regime, expectedMinTPMultiple, expectedMaxTPMultiple, aiChoice.TakeProfitMultiple)
	}

	// Go代码计算杠杆（零信任：不让AI算）
	leverage := a.calculateLeverage(symbol, atrPct)

	// Go代码计算强平价（零信任：不让AI算）
	// 必须先计算强平价，然后才能验证止损是否合理
	marginRate := LiquidationMarginRate / float64(leverage)
	var liquidationPrice float64
	if direction == "long" {
		liquidationPrice = currentPrice * (1.0 - marginRate)
	} else {
		liquidationPrice = currentPrice * (1.0 + marginRate)
	}

	// Go代码计算止损止盈价格（零信任：不让AI算）
	var stopLoss, takeProfit float64
	stopMultiple := aiChoice.StopMultiple
	takeProfitMultiple := aiChoice.TakeProfitMultiple
	needsAdjustment := false

	if direction == "long" {
		stopLoss = currentPrice - (atr * stopMultiple)
		// 🔧 关键修复：确保止损不超出强平价（做多止损必须高于强平价）
		if stopLoss <= liquidationPrice {
			needsAdjustment = true
			// 调整止损到强平价上方的安全位置（使用常量安全边距）
			safeStopLoss := liquidationPrice + (currentPrice-liquidationPrice)*LiquidationSafetyRatio
			actualStopMultiple := (currentPrice - safeStopLoss) / atr

			// 🚨 验证调整后的倍数是否仍在合理范围
			if actualStopMultiple < MinStopMultiple || actualStopMultiple > MaxStopMultiple {
				return nil, fmt.Errorf("强平调整后止损倍数%.2fx超出[%.1f-%.1f]范围，该交易风险过高，放弃",
					actualStopMultiple, MinStopMultiple, MaxStopMultiple)
			}

			stopLoss = safeStopLoss
			stopMultiple = actualStopMultiple
			// 同步调整止盈以维持R/R比
			takeProfitMultiple = actualStopMultiple * (aiChoice.TakeProfitMultiple / aiChoice.StopMultiple)

			// 🚨 验证调整后的止盈倍数是否仍在合理范围
			if takeProfitMultiple < MinTPMultiple || takeProfitMultiple > MaxTPMultiple {
				// 尝试使用最小止盈倍数
				takeProfitMultiple = MinTPMultiple
				// 重新计算R/R比
				newRR := takeProfitMultiple / actualStopMultiple
				if newRR < MinRiskReward*(1.0-RRFloatTolerance) {
					return nil, fmt.Errorf("强平调整后无法维持R/R≥%.1f:1，该交易风险回报比过低，放弃", MinRiskReward)
				}
			}
		}
		takeProfit = currentPrice + (atr * takeProfitMultiple)
	} else {
		stopLoss = currentPrice + (atr * stopMultiple)
		// 🔧 关键修复：确保止损不超出强平价（做空止损必须低于强平价）
		if stopLoss >= liquidationPrice {
			needsAdjustment = true
			// 调整止损到强平价下方的安全位置
			safeStopLoss := liquidationPrice - (liquidationPrice-currentPrice)*LiquidationSafetyRatio
			actualStopMultiple := (safeStopLoss - currentPrice) / atr

			// 🚨 验证调整后的倍数是否仍在合理范围
			if actualStopMultiple < MinStopMultiple || actualStopMultiple > MaxStopMultiple {
				return nil, fmt.Errorf("强平调整后止损倍数%.2fx超出[%.1f-%.1f]范围，该交易风险过高，放弃",
					actualStopMultiple, MinStopMultiple, MaxStopMultiple)
			}

			stopLoss = safeStopLoss
			stopMultiple = actualStopMultiple
			// 同步调整止盈以维持R/R比
			takeProfitMultiple = actualStopMultiple * (aiChoice.TakeProfitMultiple / aiChoice.StopMultiple)

			// 🚨 验证调整后的止盈倍数是否仍在合理范围
			if takeProfitMultiple < MinTPMultiple || takeProfitMultiple > MaxTPMultiple {
				// 尝试使用最小止盈倍数
				takeProfitMultiple = MinTPMultiple
				// 重新计算R/R比
				newRR := takeProfitMultiple / actualStopMultiple
				if newRR < MinRiskReward*(1.0-RRFloatTolerance) {
					return nil, fmt.Errorf("强平调整后无法维持R/R≥%.1f:1，该交易风险回报比过低，放弃", MinRiskReward)
				}
			}
		}
		takeProfit = currentPrice - (atr * takeProfitMultiple)
	}

	// Go代码计算R/R比（零信任：不让AI算）
	var riskPercent, rewardPercent float64
	if direction == "long" {
		riskPercent = (currentPrice - stopLoss) / currentPrice * 100
		rewardPercent = (takeProfit - currentPrice) / currentPrice * 100
	} else {
		riskPercent = (stopLoss - currentPrice) / currentPrice * 100
		rewardPercent = (currentPrice - takeProfit) / currentPrice * 100
	}
	riskReward := rewardPercent / riskPercent

	// 🚨 验证R/R比的合理性
	// 理论R/R比 = 实际止盈倍数 / 实际止损倍数（可能已被强平价调整）
	theoreticalRR := takeProfitMultiple / stopMultiple
	// 实际R/R比应该与理论R/R比接近
	// 使用不同的容差：强平调整前用严格容差，调整后用宽松容差
	tolerance := RRStrictTolerance
	if needsAdjustment {
		tolerance = RRFloatTolerance
	}
	rrDifference := riskReward - theoreticalRR
	if rrDifference < -tolerance*theoreticalRR || rrDifference > tolerance*theoreticalRR {
		return nil, fmt.Errorf("🚨 R/R计算异常：理论R/R=%.2f:1(%.1fx/%.1fx)，但实际计算=%.2f:1，差异=%.3f",
			theoreticalRR, takeProfitMultiple, stopMultiple, riskReward, rrDifference)
	}

	// 🚨 硬约束：R/R比必须≥MinRiskReward（使用统一常量）
	if riskReward < MinRiskReward*(1.0-RRFloatTolerance) {
		return nil, fmt.Errorf("🚨 风险回报比过低：R/R=%.2f:1 < %.1f:1要求（止损%.1fx, 止盈%.1fx）",
			riskReward, MinRiskReward, stopMultiple, takeProfitMultiple)
	}

	// Go代码计算仓位大小（零信任：基于风险预算反推）
	positionSize := a.calculatePositionSize(symbol, confidenceLevel, currentPrice, stopLoss, leverage, accountEquity, availableBalance)

	// 构建reasoning（包含Go代码计算的所有数值，以及是否进行了强平价调整）
	reasoningPrefix := "Go计算"
	if stopMultiple != aiChoice.StopMultiple || takeProfitMultiple != aiChoice.TakeProfitMultiple {
		reasoningPrefix = fmt.Sprintf("Go计算(⚠️ 已调整：AI建议%.1fx/%.1fx → 实际%.1fx/%.1fx，避免超出强平价)",
			aiChoice.StopMultiple, aiChoice.TakeProfitMultiple, stopMultiple, takeProfitMultiple)
	}
	reasoning := fmt.Sprintf("%s: ATR%%=%.2f%% | 止损%.1fx→%.4f | 止盈%.1fx→%.4f | R/R=%.2f:1 | 强平价%.4f | 杠杆%dx | AI理由:%s",
		reasoningPrefix, atrPct, stopMultiple, stopLoss, takeProfitMultiple, takeProfit,
		riskReward, liquidationPrice, leverage, aiChoice.Reasoning)

	result := &RiskParameters{
		Leverage:         leverage,
		PositionSize:     positionSize,
		StopLoss:         stopLoss,
		TakeProfit:       takeProfit,
		RiskReward:       riskReward,
		Valid:            true,
		Reasoning:        reasoning,
		LiquidationPrice: liquidationPrice,
		RiskPercent:      riskPercent,
		RewardPercent:    rewardPercent,
	}

	// Go代码验证（双重保险）
	if err := a.validateResult(result, symbol, direction, currentPrice); err != nil {
		result.Valid = false
		result.Reasoning += fmt.Sprintf(" [验证失败: %v]", err)
	}

	return result, nil
}

// getAIChoice 调用AI获取止损止盈倍数选择（AI只做选择，不做计算）
func (a *RiskAgent) getAIChoice(symbol string, direction string, currentPrice, atr, atrPct float64, regime *RegimeResult) (*AIRiskChoice, error) {
	var sb strings.Builder

	sb.WriteString("你是风险管理专家。根据市场体制和波动率，**选择**止损和止盈倍数。\n\n")
	sb.WriteString("⚠️ **重要**: 你只需要选择倍数，不需要做任何数学计算！\n\n")

	sb.WriteString("# 输入数据\n\n")
	sb.WriteString(fmt.Sprintf("**币种**: %s %s\n", symbol, direction))
	sb.WriteString(fmt.Sprintf("**当前价格**: %.4f\n", currentPrice))
	sb.WriteString(fmt.Sprintf("**4h ATR14**: %.4f\n", atr))
	sb.WriteString(fmt.Sprintf("**ATR%%**: %.2f%% (已由Go代码计算)\n", atrPct))
	sb.WriteString(fmt.Sprintf("**市场体制**: %s (%s)\n\n", regime.Regime, regime.Strategy))

	sb.WriteString("# 任务：选择止损止盈倍数\n\n")

	sb.WriteString("**规则：根据ATR%确定基础倍数**\n")
	sb.WriteString("```\n")
	sb.WriteString("低波动 (ATR% < 2%):       止损4.0×ATR | 止盈基础8.0×ATR\n")
	sb.WriteString("中波动 (2% ≤ ATR% < 4%): 止损5.0×ATR | 止盈基础10.0×ATR\n")
	sb.WriteString("高波动 (ATR% ≥ 4%):      止损6.0×ATR | 止盈基础12.0×ATR\n")
	sb.WriteString("```\n\n")

	sb.WriteString("**规则：根据体制调整止盈倍数**\n")
	sb.WriteString("```\n")
	sb.WriteString("体制(A1/A2)趋势: 提高止盈 → 低波动12-15x, 中波动12-16x, 高波动14-18x\n")
	sb.WriteString("体制(B)震荡:     基础止盈 → 低波动8x, 中波动10x, 高波动12x\n")
	sb.WriteString("```\n\n")

	sb.WriteString("# 输出要求\n\n")
	sb.WriteString("必须输出纯JSON（不要markdown代码块），格式：\n")
	sb.WriteString("```\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"stop_multiple\": 4.0,\n")
	sb.WriteString("  \"take_profit_multiple\": 12.0,\n")
	sb.WriteString("  \"reasoning\": \"ATR%=1.8%(低波动) + 体制A2(趋势) → 止损4x, 止盈12x\"\n")
	sb.WriteString("}\n")
	sb.WriteString("```\n\n")
	sb.WriteString("**注意**: 你只需要输出倍数，Go代码会自动计算所有价格、R/R比和强平价！\n")

	prompt := sb.String()

	// 调用AI
	response, err := a.mcpClient.CallWithMessages("", prompt)
	if err != nil {
		return nil, fmt.Errorf("调用AI失败: %w", err)
	}

	// 解析结果
	jsonStr := extractJSON(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("响应中没有找到JSON")
	}

	var choice AIRiskChoice
	if err := json.Unmarshal([]byte(jsonStr), &choice); err != nil {
		return nil, fmt.Errorf("JSON解析失败: %w", err)
	}

	return &choice, nil
}

// calculateLeverage Go代码计算杠杆（零信任）
func (a *RiskAgent) calculateLeverage(symbol string, atrPct float64) int {
	// 判断是BTC/ETH还是山寨币
	var baseLeverage int
	if symbol == "BTCUSDT" || symbol == "ETHUSDT" {
		baseLeverage = a.btcEthLeverage
	} else {
		baseLeverage = a.altcoinLeverage
	}

	// 根据波动率调整杠杆系数
	var coefficient float64
	if atrPct < 2.0 {
		coefficient = 1.0 // 低波动
	} else if atrPct < 4.0 {
		coefficient = 0.8 // 中波动
	} else {
		coefficient = 0.6 // 高波动
	}

	// 实际杠杆 = 基础杠杆 × 系数（向下取整）
	leverage := int(float64(baseLeverage) * coefficient)
	if leverage < 1 {
		leverage = 1
	}

	return leverage
}

// calculatePositionSize Go代码计算仓位大小（零信任 + 基于风险预算）
func (a *RiskAgent) calculatePositionSize(symbol string, confidenceLevel string, currentPrice, stopLoss float64, leverage int, accountEquity, availableBalance float64) float64 {
	// 🎯 核心改进：基于风险预算计算仓位，而非简单倍数
	// 每笔交易风险预算 = 账户净值 × RiskBudgetPerTrade（通常1%）
	riskBudget := accountEquity * RiskBudgetPerTrade

	// 计算价格变动百分比（入场价到止损价）
	var priceMovePct float64
	if currentPrice > stopLoss {
		priceMovePct = (currentPrice - stopLoss) / currentPrice
	} else {
		priceMovePct = (stopLoss - currentPrice) / currentPrice
	}

	if priceMovePct <= 0 || priceMovePct > 0.5 { // 防御性：止损距离不能超过50%
		return 0
	}

	// 理想名义规模 = 风险预算 / 价格变动百分比
	// 逻辑：如果止损触发，损失 = 名义规模 × 价格变动% = 风险预算
	idealNotional := riskBudget / priceMovePct

	// 根据信心等级调整仓位倍数
	var confidenceMultiplier float64
	switch confidenceLevel {
	case "high":
		confidenceMultiplier = ConfidenceHighMultiplier // 1.2x
	case "medium":
		confidenceMultiplier = ConfidenceMediumMultiplier // 1.0x
	case "low":
		confidenceMultiplier = ConfidenceLowMultiplier // 0.8x
	default:
		confidenceMultiplier = ConfidenceMediumMultiplier
	}
	idealNotional *= confidenceMultiplier

	// 保证金约束：名义规模/杠杆 = 所需保证金，不能超过可用保证金的95%
	neededMargin := idealNotional / float64(leverage)
	maxNotionalByMargin := (availableBalance * MarginUsageLimit) * float64(leverage)

	// 🔒 硬约束：取理想规模与保证金约束的最小值
	finalNotional := idealNotional
	if neededMargin > availableBalance*MarginUsageLimit {
		// 保证金不足，反推最大可行名义规模
		finalNotional = (availableBalance * MarginUsageLimit) * float64(leverage)
	}
	if maxNotionalByMargin < finalNotional {
		finalNotional = maxNotionalByMargin
	}

	// 防御性：确保不为负
	if finalNotional < 0 {
		finalNotional = 0
	}

	return finalNotional
}

// validateResult Go代码验证（双重保险）
func (a *RiskAgent) validateResult(result *RiskParameters, symbol string, direction string, currentPrice float64) error {
	// 验证杠杆
	maxLeverage := a.altcoinLeverage
	if symbol == "BTCUSDT" || symbol == "ETHUSDT" {
		maxLeverage = a.btcEthLeverage
	}
	if result.Leverage <= 0 || result.Leverage > maxLeverage {
		return fmt.Errorf("杠杆%d超出配置上限%d", result.Leverage, maxLeverage)
	}

	// 验证止损止盈的合理性
	if direction == "long" {
		if result.StopLoss >= currentPrice {
			return fmt.Errorf("做多止损价%.2f必须小于当前价%.2f", result.StopLoss, currentPrice)
		}
		if result.TakeProfit <= currentPrice {
			return fmt.Errorf("做多止盈价%.2f必须大于当前价%.2f", result.TakeProfit, currentPrice)
		}
	} else {
		if result.StopLoss <= currentPrice {
			return fmt.Errorf("做空止损价%.2f必须大于当前价%.2f", result.StopLoss, currentPrice)
		}
		if result.TakeProfit >= currentPrice {
			return fmt.Errorf("做空止盈价%.2f必须小于当前价%.2f", result.TakeProfit, currentPrice)
		}
	}

	// 验证R/R比（使用统一常量）
	if result.RiskPercent <= 0 {
		return fmt.Errorf("风险百分比异常: %.2f%%", result.RiskPercent)
	}
	actualRR := result.RewardPercent / result.RiskPercent
	if actualRR < MinRiskReward*(1.0-RRFloatTolerance) {
		return fmt.Errorf("风险回报比%.2f:1低于%.1f:1要求", actualRR, MinRiskReward)
	}

	// 验证强平价
	if direction == "long" {
		if result.StopLoss <= result.LiquidationPrice {
			return fmt.Errorf("做多止损价%.2f低于强平价%.2f，止损将失效", result.StopLoss, result.LiquidationPrice)
		}
	} else {
		if result.StopLoss >= result.LiquidationPrice {
			return fmt.Errorf("做空止损价%.2f高于强平价%.2f，止损将失效", result.StopLoss, result.LiquidationPrice)
		}
	}

	return nil
}

// getExpectedMultiples 根据ATR%和体制计算期望的止损止盈倍数
// 返回：(止损倍数, 最小止盈倍数, 最大止盈倍数)
// 使用统一的ATR阈值常量
func (a *RiskAgent) getExpectedMultiples(atrPct float64, regime *RegimeResult) (float64, float64, float64) {
	var stopMultiple, minTPMultiple, maxTPMultiple float64

	// 根据ATR%确定基础倍数（使用统一常量）
	if atrPct < ATRPctLow {
		// 低波动 (<2%)
		stopMultiple = 4.0
		minTPMultiple = 8.0
		maxTPMultiple = 8.0
	} else if atrPct < ATRPctMid {
		// 中波动 (2-4%)
		stopMultiple = 5.0
		minTPMultiple = 10.0
		maxTPMultiple = 10.0
	} else {
		// 高波动 (>=4%)
		stopMultiple = 6.0
		minTPMultiple = 12.0
		maxTPMultiple = 12.0
	}

	// 根据体制调整止盈倍数
	if regime.Regime == "A1" || regime.Regime == "A2" {
		// 趋势行情：提高止盈倍数
		if atrPct < ATRPctLow {
			minTPMultiple = 12.0
			maxTPMultiple = 15.0
		} else if atrPct < ATRPctMid {
			minTPMultiple = 12.0
			maxTPMultiple = 16.0
		} else {
			minTPMultiple = 14.0
			maxTPMultiple = 18.0
		}
	}
	// 体制B震荡使用基础倍数，已在上面设置

	return stopMultiple, minTPMultiple, maxTPMultiple
}

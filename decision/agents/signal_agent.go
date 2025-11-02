package agents

import (
	"encoding/json"
	"fmt"
	"math"
	"nofx/market"
	"nofx/mcp"
	"strings"
)

// SignalResult 信号检测结果
type SignalResult struct {
	Symbol          string   `json:"symbol"`
	Direction       string   `json:"direction"`        // "long", "short", "none"
	SignalList      []string `json:"signal_list"`      // 匹配的信号维度列表
	Score           int      `json:"score"`            // 信号强度分数 (0-100)
	ConfidenceLevel string   `json:"confidence_level"` // 信心等级: "high", "medium", "low"
	Valid           bool     `json:"valid"`            // 是否满足≥3个信号共振
	Reasoning       string   `json:"reasoning"`        // 分析过程
	Scenario        string   `json:"scenario,omitempty"`
}

type signalAudit struct {
	count             int
	scenario          string
	pullbackConfirmed bool
}

// SignalAgent 信号检测专家
type SignalAgent struct {
	mcpClient *mcp.Client
}

// NewSignalAgent 创建信号检测专家
func NewSignalAgent(mcpClient *mcp.Client) *SignalAgent {
	return &SignalAgent{
		mcpClient: mcpClient,
	}
}

// Detect 检测交易信号（单一币种）
func (a *SignalAgent) Detect(symbol string, marketData *market.Data, regime *RegimeResult) (*SignalResult, error) {
	if marketData == nil {
		return nil, fmt.Errorf("市场数据不完整")
	}

	prompt := a.buildPrompt(symbol, marketData, regime)

	// 调用AI
	response, err := a.mcpClient.CallWithMessages("", prompt)
	if err != nil {
		return nil, fmt.Errorf("调用AI失败: %w", err)
	}

	// 解析结果
	result, err := a.parseResult(response)
	if err != nil {
		return nil, fmt.Errorf("解析结果失败: %w\n响应: %s", err, response)
	}

	audit := a.auditSignals(marketData, regime, result.Direction)
	result.Scenario = audit.scenario

	// 🚨 零信任原则：Go代码计算信号强度分数，覆盖AI的score
	result.Score = a.calculateScore(audit.count, result.Direction, regime)

	// 🚨 Go代码计算信心等级（用于动态仓位大小）
	result.ConfidenceLevel = a.calculateConfidenceLevel(result.Score)

	// 以Go端重新计算的维度数为准，强制覆盖AI的valid字段
	result.Valid = audit.count >= SignalMinForValid && result.Direction != "none"

	// 如果是A2趋势下的反弹做空，但尚未完成确认，则直接标记为无效
	if audit.scenario == ScenarioPullback && !audit.pullbackConfirmed {
		result.Valid = false
		if !strings.Contains(result.Reasoning, "回落确认不足") {
			if strings.TrimSpace(result.Reasoning) != "" {
				result.Reasoning += " | "
			}
			result.Reasoning += "Go校验: 回落确认不足，等待收盘确认"
		}
	}

	// Go代码验证（双重保险）
	if err := a.validateResult(result, regime, audit); err != nil {
		result.Valid = false
		result.Reasoning += fmt.Sprintf(" [验证失败: %v]", err)
	}

	return result, nil
}

// buildPrompt 构建信号检测prompt
func (a *SignalAgent) buildPrompt(symbol string, marketData *market.Data, regime *RegimeResult) string {
	var sb strings.Builder

	sb.WriteString("你是交易信号检测专家。分析币种的多维度信号共振。\n\n")

	sb.WriteString("# 输入数据\n\n")
	sb.WriteString(fmt.Sprintf("**币种**: %s\n", symbol))
	sb.WriteString(fmt.Sprintf("**当前价格**: %.4f\n", marketData.CurrentPrice))
	sb.WriteString(fmt.Sprintf("**市场体制**: %s (%s)\n", regime.Regime, regime.Strategy))
	sb.WriteString("\n")

	// 输出完整市场数据
	sb.WriteString("**技术指标**:\n")
	sb.WriteString(fmt.Sprintf("- 当前RSI(7): %.2f\n", marketData.CurrentRSI7))
	sb.WriteString(fmt.Sprintf("- 当前MACD: %.4f\n", marketData.CurrentMACD))
	sb.WriteString(fmt.Sprintf("- 当前EMA20: %.4f\n", marketData.CurrentEMA20))
	sb.WriteString("\n")

	if marketData.LongerTermContext != nil {
		sb.WriteString("**4h数据**:\n")
		sb.WriteString(fmt.Sprintf("- 4h EMA20: %.4f\n", marketData.LongerTermContext.EMA20))
		sb.WriteString(fmt.Sprintf("- 4h EMA50: %.4f\n", marketData.LongerTermContext.EMA50))
		sb.WriteString(fmt.Sprintf("- 4h EMA200: %.4f\n", marketData.LongerTermContext.EMA200))
		sb.WriteString(fmt.Sprintf("- 4h ATR14: %.4f\n", marketData.LongerTermContext.ATR14))
		sb.WriteString(fmt.Sprintf("- 4h ATR3: %.4f\n", marketData.LongerTermContext.ATR3))
		sb.WriteString(fmt.Sprintf("- 价格变化1h: %+.2f%%\n", marketData.PriceChange1h))
		sb.WriteString(fmt.Sprintf("- 价格变化4h: %+.2f%%\n", marketData.PriceChange4h))

		// Volume comparison
		volumeChangeText := ""
		if marketData.LongerTermContext.AverageVolume > 0 {
			volumeChange := ((marketData.LongerTermContext.CurrentVolume - marketData.LongerTermContext.AverageVolume) / marketData.LongerTermContext.AverageVolume) * 100
			volumeChangeText = fmt.Sprintf("- 成交量变化: %+.2f%%\n", volumeChange)
		}
		sb.WriteString(volumeChangeText)
		sb.WriteString("\n")
	}

	if marketData.OpenInterest != nil {
		sb.WriteString("**持仓量 & 资金费率**:\n")
		sb.WriteString(fmt.Sprintf("- 当前OI: %.0f\n", marketData.OpenInterest.Latest))
		sb.WriteString(fmt.Sprintf("- 资金费率: %.4f%%\n", marketData.FundingRate*100))
		sb.WriteString("\n")
	}

	sb.WriteString("# 任务：5维度信号检测\n\n")

	sb.WriteString("⚠️ **强制要求**：对于每个维度，你必须在reasoning中写明**具体数值**和**判断逻辑**！\n")
	sb.WriteString("**禁止作弊**：不要在信号列表中包含未满足的维度！Go代码会验证你的逻辑！\n\n")

	sb.WriteString("**检测以下5个独立维度的信号**：\n\n")

	sb.WriteString("**维度1: 体制/趋势匹配**\n")
	sb.WriteString("```\n")
	sb.WriteString("做多: 体制=(A1)上升趋势 OR 体制=(B)震荡下轨\n")
	sb.WriteString("做空: 体制=(A2)下降趋势 OR 体制=(B)震荡上轨\n")
	sb.WriteString("```\n")
	sb.WriteString("**要求**: reasoning中必须写 `维度1(体制): %s → 满足/不满足`\n\n")

	sb.WriteString("**维度2: 动量指标**\n")
	sb.WriteString("```\n")
	sb.WriteString("做多: (4h MACD > 0 且上升) OR (1h RSI曾跌破30并回升至>35)\n")
	sb.WriteString("做空: (4h MACD < 0) 且 (1h RSI曾超买>70，并已回落到<65)\n")
	sb.WriteString("```\n")
	sb.WriteString("**要求**: reasoning中必须写 `维度2(动量): MACD=X.XX 或 RSI=X.XX → 满足/不满足`\n\n")

	sb.WriteString("**维度3: 位置/技术形态**\n")
	sb.WriteString("```\n")
	sb.WriteString("做多(A1/B): 价格回踩 1h EMA20 支撑企稳，或突破关键阻力并站稳\n")
	sb.WriteString("做空(A2趋势): 必须满足两个条件：\n")
	sb.WriteString("  条件1: 价格曾反弹至 [4h EMA20 ~ 4h EMA50] 阻力区（至少触及4h EMA20附近）\n")
	sb.WriteString("  条件2: 价格已重新跌回 1h EMA20 下方（收盘价确认，至少2根1h K线）\n")
	sb.WriteString("  ⚠️ 缺一不可！仅价格低于1h EMA20但未触及4h阻力区 → 不满足（抢跑）\n")
	sb.WriteString("做空(B震荡): 价格触及震荡上轨并出现反转信号\n")
	sb.WriteString("```\n")
	sb.WriteString("**要求**:\n")
	sb.WriteString("- (A1/B做多): reasoning中必须写 `维度3(位置): 价格[X.XX] vs 1h_EMA20=[X.XX] → 满足/不满足`\n")
	sb.WriteString("- (A2做空): reasoning中必须写 `维度3(位置): 条件1: 价格[最高触及Y.YY] vs [4h_EMA20=X.XX ~ 4h_EMA50=Z.ZZ] → [满足/不满足]; 条件2: 当前价格[W.WW] vs 1h_EMA20=[V.VV] → [满足/不满足]; 综合 → [满足/不满足]`\n\n")

	sb.WriteString("**维度4: 资金/成交量（最容易作弊的维度！）**\n")
	sb.WriteString("```\n")
	sb.WriteString("A2趋势做空: 只有在“反弹确认结束”后，缩量反弹(<-50%) 或 成交量放大(>+20%) 才算有效\n")
	sb.WriteString("A1趋势做多: 成交量放大(>+20%) 或 缩量回调(<-50%)\n")
	sb.WriteString("震荡市(B): 仅接受成交量放大(>+20%)\n")
	sb.WriteString("```\n")
	sb.WriteString("⚠️ **严格要求**：\n")
	sb.WriteString("- 缩量反弹只有在“收盘价确认跌回EMA20下方”之后才可计入维度4\n")
	sb.WriteString("- 仅出现缩量但价格仍在EMA20上方 → **不满足**\n")
	sb.WriteString("- 成交量变化+25% → 满足放大条件；-30% → 不满足任何条件\n")
	sb.WriteString("- reasoning中必须写：\n")
	sb.WriteString("  - `维度4(成交量): 成交量变化[+X.XX%] > +20% → 满足` 或\n")
	sb.WriteString("  - `维度4(成交量): 成交量变化[-X.XX%] < -50%，且价格已确认跌回EMA20下方 → 满足` 或\n")
	sb.WriteString("  - `维度4(成交量): 成交量变化[-30%] 不满足任何条件 → 不满足`\n")
	sb.WriteString("- **禁止**：价格仍在EMA20上方却声称维度4满足缩量条件！\n\n")

	sb.WriteString("🚨 **A2反弹做空特别提醒**：\n")
	sb.WriteString("- RSI(1h) 必须先超买>70再回落到<65\n")
	sb.WriteString("- 收盘价连续2根1h确认跌回1h EMA20下方\n")
	sb.WriteString("- 缩量反弹只有在上述确认完成后才有效\n")
	sb.WriteString("- 禁止在价格仍高于EMA20时提前开空\n\n")

	sb.WriteString("**维度5: 情绪/持仓**\n")
	sb.WriteString("```\n")
	sb.WriteString("做多: 资金费率<0 (空头主导，做多逆向机会)\n")
	sb.WriteString("做空: 资金费率>0.01% (多头主导，做空逆向机会)\n")
	sb.WriteString("```\n")
	sb.WriteString("**要求**: reasoning中必须写 `维度5(资金费率): 费率=X.XX%% → 满足/不满足`\n\n")

	sb.WriteString("**禁止开仓情况**（必须检查）：\n")
	sb.WriteString("```\n")
	sb.WriteString("1. 体制=(C)窄幅盘整 → 禁止开仓\n")
	sb.WriteString("2. 体制与信号冲突（例如：(A1)上升趋势中使用(B)逆转信号做空）\n")
	sb.WriteString("3. 指标矛盾（如MACD多头但价格已跌破EMA50）\n")
	sb.WriteString("```\n\n")

	sb.WriteString("# 判断规则\n\n")
	sb.WriteString("1. 逐个检查5个维度，在reasoning中写明每个维度的数值和判断\n")
	sb.WriteString("2. **只有真正满足的维度**才能加入signal_list\n")
	sb.WriteString("3. **如果≥3个维度同时成立** → valid=true, 输出方向和信号列表\n")
	sb.WriteString("4. **如果<3个维度** → valid=false, direction=\"none\"\n\n")
	sb.WriteString("⚠️ 注意：score字段将由Go代码计算，你不需要计算分数\n\n")

	sb.WriteString("# 输出要求\n\n")
	sb.WriteString("必须输出纯JSON（不要markdown代码块），格式：\n")
	sb.WriteString("```\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"symbol\": \"BNBUSDT\",\n")
	sb.WriteString("  \"direction\": \"short\",\n")
	sb.WriteString("  \"signal_list\": [\"体制=(A2)下降趋势\", \"MACD<0且下降\", \"价格反弹EMA20受阻\"],\n")
	sb.WriteString("  \"score\": 0,\n")
	sb.WriteString("  \"valid\": true,\n")
	sb.WriteString("  \"reasoning\": \"维度1(体制): A2下降→满足 | 维度2(动量): MACD=-0.52<0→满足 | 维度3(位置): 价格1093.53 vs EMA20=1095→满足 | 维度4(成交量): 变化[-89.84%]<+20%→不满足 | 维度5(费率): 0.02%>0.01%→满足 | 共4个维度满足\"\n")
	sb.WriteString("}\n")
	sb.WriteString("```\n")
	sb.WriteString("\n⚠️ 重要：score字段填0即可，Go代码会根据信号数量自动计算！\n")

	return sb.String()
}

// parseResult 解析AI响应
func (a *SignalAgent) parseResult(response string) (*SignalResult, error) {
	jsonStr := extractJSON(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("响应中没有找到JSON")
	}

	var result SignalResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("JSON解析失败: %w", err)
	}

	return &result, nil
}

// validateResult Go代码验证（双重保险 + 硬验证市场数据）
func (a *SignalAgent) validateResult(result *SignalResult, regime *RegimeResult, audit signalAudit) error {
	// 验证direction
	validDirections := map[string]bool{"long": true, "short": true, "none": true}
	if !validDirections[result.Direction] {
		return fmt.Errorf("无效的方向: %s", result.Direction)
	}

	// 验证体制禁止开仓
	if regime.Regime == "C" && result.Direction != "none" {
		return fmt.Errorf("体制(C)窄幅盘整时禁止开仓")
	}

	// 验证体制与方向匹配
	if result.Direction == "long" {
		// 做多只能在(A1)上升趋势或(B)震荡时
		if regime.Regime != "A1" && regime.Regime != "B" {
			return fmt.Errorf("体制%s时不应做多（只能在A1或B时做多）", regime.Regime)
		}
	} else if result.Direction == "short" {
		// 做空只能在(A2)下降趋势或(B)震荡时
		if regime.Regime != "A2" && regime.Regime != "B" {
			return fmt.Errorf("体制%s时不应做空（只能在A2或B时做空）", regime.Regime)
		}
	}

	// 验证信号数量
	if result.Valid && audit.count < SignalMinForValid {
		return fmt.Errorf("valid=true但Go重新计算只有%d个信号（需≥%d个）", audit.count, SignalMinForValid)
	}

	if audit.scenario == ScenarioPullback && !audit.pullbackConfirmed {
		return fmt.Errorf("反弹确认尚未完成，信号无效")
	}

	return nil
}

// auditSignals Go代码重新计算所有信号维度（Zero-Trust验证）
func (a *SignalAgent) auditSignals(marketData *market.Data, regime *RegimeResult, direction string) signalAudit {
	audit := signalAudit{
		count:             0,
		scenario:          ScenarioTrend,
		pullbackConfirmed: true,
	}

	if marketData == nil || direction == "" || direction == "none" {
		return audit
	}

	switch regime.Regime {
	case "A1":
		if direction == "long" {
			audit.scenario = ScenarioBreakout
		} else {
			audit.scenario = ScenarioCountertrend
		}
	case "A2":
		if direction == "short" {
			audit.scenario = ScenarioPullback
		} else {
			audit.scenario = ScenarioCountertrend
		}
	case "B":
		audit.scenario = ScenarioRange
	default:
		audit.scenario = ScenarioTrend
	}

	if (direction == "long" && (regime.Regime == "A1" || regime.Regime == "B")) ||
		(direction == "short" && (regime.Regime == "A2" || regime.Regime == "B")) {
		audit.count++
	}

	if audit.scenario == ScenarioPullback {
		rsiConfirmed := checkRSIOverboughtReturn(marketData)
		positionConfirmed := checkPullbackPosition(marketData)
		audit.pullbackConfirmed = rsiConfirmed && positionConfirmed

		if audit.pullbackConfirmed {
			// 动量与位置两项同时满足才计入 (视为维度2+维度3)
			audit.count += 2

			if checkPullbackVolume(marketData) {
				audit.count++
			}
			if checkFunding(direction, marketData) {
				audit.count++
			}
		}
	} else {
		if checkMomentum(direction, marketData) {
			audit.count++
		}
		if checkPosition(direction, marketData) {
			audit.count++
		}
		if checkVolumeExpansion(marketData) {
			audit.count++
		}
		if checkFunding(direction, marketData) {
			audit.count++
		}
	}

	return audit
}

// calculateScore Go代码计算信号强度分数（零信任原则）
func (a *SignalAgent) calculateScore(signalCount int, direction string, regime *RegimeResult) int {
	if signalCount < 0 {
		signalCount = 0
	}

	score := SignalBaseScore + signalCount*SignalPerDimensScore

	if (direction == "long" && regime.Regime == "A1") || (direction == "short" && regime.Regime == "A2") {
		score += SignalPerfectBonus
	}

	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}

	return score
}

func checkMomentum(direction string, data *market.Data) bool {
	if data == nil {
		return false
	}

	switch direction {
	case "long":
		if data.CurrentMACD > 0 {
			return true
		}
		return recoveredFromOversold(data)
	case "short":
		if data.CurrentMACD < 0 {
			return true
		}
		return cooledFromOverbought(data)
	default:
		return false
	}
}

func checkPosition(direction string, data *market.Data) bool {
	if data == nil {
		return false
	}

	price := data.CurrentPrice
	ema20 := data.CurrentEMA20
	if ema20 <= 0 {
		return false
	}

	tolerance := EMA20TolerancePct

	switch direction {
	case "long":
		return price >= ema20*(1.0-tolerance)
	case "short":
		return price <= ema20*(1.0+tolerance)
	default:
		return false
	}
}

func checkRSIOverboughtReturn(data *market.Data) bool {
	if data == nil {
		return false
	}

	current := data.CurrentRSI7
	if current >= 65 {
		return false
	}

	if data.IntradaySeries == nil {
		return false
	}

	series := data.IntradaySeries.RSI7Values
	if len(series) == 0 {
		return false
	}

	lookback := minInt(len(series), 40)
	maxRSI := -1.0
	maxIdx := -1
	for i := len(series) - lookback; i < len(series); i++ {
		if i < 0 {
			continue
		}
		if series[i] > maxRSI {
			maxRSI = series[i]
			maxIdx = i
		}
	}

	// 必须在近 40 根（≈2 小时）内曾经显著超买
	if maxRSI < 72 {
		return false
	}

	// 超买点必须距离当前不超过约 60 分钟
	if len(series)-1-maxIdx > 20 {
		return false
	}

	return true
}

func checkPullbackPosition(data *market.Data) bool {
	if data == nil || data.LongerTermContext == nil {
		return false
	}

	currentEMA20 := data.CurrentEMA20
	if currentEMA20 <= 0 {
		return false
	}

	price := data.CurrentPrice

	// ✅ 条件1: 价格必须已经重新跌回 1h EMA20 下方（V4.0）
	if price > currentEMA20*(1.0-EMA20TolerancePct) {
		return false // 还在反弹中，尚未确认
	}

	// ✅ 条件2: 需要至少两根 1h 确认K（≈ 60 分钟）的收盘价低于 1h EMA20
	// 并确认先前曾站上 EMA20（确认这是"反弹失败"而非"一路下跌"）
	if !confirmedBelowOneHourEMA(data, currentEMA20) {
		return false // 可能是假跌破
	}

	// ✅ 条件3: 必须曾经触及 4h EMA20 ~ EMA50 阻力带（V4.0耐心逻辑）
	if !touchedFourHourBand(data) {
		return false // 价格还在半路上，抢跑了
	}

	// 🎯 同时满足三个条件：反弹到位 + 确认跌回 + 持续在下方
	return true
}

func checkPullbackVolume(data *market.Data) bool {
	change, ok := computeVolumeChange(data)
	if !ok {
		return false
	}
	return change <= VolumeShrinkThreshold
}

func confirmedBelowOneHourEMA(data *market.Data, ema20 float64) bool {
	if data == nil || data.IntradaySeries == nil {
		return false
	}

	prices := data.IntradaySeries.MidPrices
	if len(prices) == 0 {
		return false
	}

	required := minInt(len(prices), 20) // 约 60 分钟
	lowerThreshold := ema20 * (1.0 - EMA20TolerancePct)
	upperThreshold := ema20 * (1.0 + EMA20TolerancePct/2)
	aboveSeen := false
	for i := len(prices) - required; i < len(prices); i++ {
		if i < 0 {
			continue
		}
		if prices[i] >= upperThreshold {
			aboveSeen = true
		}
		if prices[i] > lowerThreshold {
			return false
		}
	}

	if !aboveSeen {
		lookback := minInt(len(prices), 60)
		for i := len(prices) - required - lookback; i < len(prices)-required; i++ {
			if i < 0 {
				continue
			}
			if prices[i] >= upperThreshold {
				aboveSeen = true
				break
			}
		}
	}

	return aboveSeen
}

func touchedFourHourBand(data *market.Data) bool {
	if data == nil || data.IntradaySeries == nil || data.LongerTermContext == nil {
		return false
	}

	ema4h20 := data.LongerTermContext.EMA20
	ema4h50 := data.LongerTermContext.EMA50
	atr := data.LongerTermContext.ATR14

	if ema4h20 <= 0 || ema4h50 <= 0 || atr <= 0 {
		return false
	}

	// 定义阻力区：取4h EMA20和EMA50中较小的作为下限
	bandLow := math.Min(ema4h20, ema4h50)

	// V4.0: 价格必须至少触及阻力区下限（4h EMA20附近）
	// 使用0.5*ATR作为缓冲区（比之前的2%更合理）
	resistanceFloor := bandLow - (0.5 * atr)

	prices := data.IntradaySeries.MidPrices
	if len(prices) == 0 {
		return false
	}

	// 查看最近80根3分钟K线（约4小时）
	lookback := minInt(len(prices), 80)
	maxPrice := -math.MaxFloat64

	for i := len(prices) - lookback; i < len(prices); i++ {
		if i < 0 {
			continue
		}
		p := prices[i]
		if p > maxPrice {
			maxPrice = p
		}
	}

	// V4.0核心逻辑：最高价必须至少触及阻力区下限（耐心等待）
	if maxPrice < resistanceFloor {
		return false // 价格还在半路上，太早了
	}

	// 如果价格进入阻力区内部或突破上限，都算触及
	return true
}

func checkVolumeExpansion(data *market.Data) bool {
	change, ok := computeVolumeChange(data)
	return ok && change >= VolumeExpandThreshold
}

func computeVolumeChange(data *market.Data) (float64, bool) {
	if data == nil || data.LongerTermContext == nil {
		return 0, false
	}
	avg := data.LongerTermContext.AverageVolume
	if avg <= 0 {
		return 0, false
	}
	change := ((data.LongerTermContext.CurrentVolume - avg) / avg) * 100
	return change, true
}

func checkFunding(direction string, data *market.Data) bool {
	if data == nil {
		return false
	}
	funding := data.FundingRate * 100
	if direction == "long" {
		return funding < 0
	}
	if direction == "short" {
		return funding > FundingRateShortThreshold
	}
	return false
}

func recoveredFromOversold(data *market.Data) bool {
	if data == nil {
		return false
	}
	current := data.CurrentRSI7
	if current <= 35 {
		return false
	}
	if data.IntradaySeries == nil {
		return current > 35
	}
	series := data.IntradaySeries.RSI7Values
	lookback := minInt(len(series), 40)
	foundOversold := false
	for i := len(series) - lookback; i < len(series); i++ {
		if i >= 0 && series[i] < 30 {
			foundOversold = true
			break
		}
	}
	return foundOversold && current > 35
}

func cooledFromOverbought(data *market.Data) bool {
	if data == nil {
		return false
	}
	current := data.CurrentRSI7
	if current >= 65 {
		return false
	}
	if data.IntradaySeries == nil {
		return false
	}
	series := data.IntradaySeries.RSI7Values
	lookback := minInt(len(series), 40)
	for i := len(series) - lookback; i < len(series); i++ {
		if i >= 0 && series[i] > 70 {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// calculateConfidenceLevel Go代码计算信心等级（零信任原则）
// 用于动态调整仓位大小
func (a *SignalAgent) calculateConfidenceLevel(score int) string {
	if score >= 90 {
		return "high" // 高信心：完美体制匹配 + ≥4个信号
	} else if score >= 80 {
		return "medium" // 中等信心：正常信号
	} else {
		return "low" // 低信心：信号较弱
	}
}

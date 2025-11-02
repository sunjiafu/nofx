package agents

import (
	"encoding/json"
	"fmt"
	"log"
	"nofx/market"
	"nofx/mcp"
	"strings"
	"sync"
	"time"
)

// Context 交易上下文（从decision包传入）
type Context struct {
	CurrentTime     string
	RuntimeMinutes  int
	CallCount       int
	Account         AccountInfo
	Positions       []PositionInfoInput
	CandidateCoins  []CandidateCoin
	MarketDataMap   map[string]*market.Data
	Performance     interface{}
	BTCETHLeverage  int
	AltcoinLeverage int
}

// AccountInfo 账户信息
type AccountInfo struct {
	TotalEquity      float64
	AvailableBalance float64
	TotalPnL         float64
	TotalPnLPct      float64
	MarginUsed       float64
	MarginUsedPct    float64
	PositionCount    int
}

// PositionInfoInput 持仓信息输入
type PositionInfoInput struct {
	Symbol           string
	Side             string
	EntryPrice       float64
	MarkPrice        float64
	Quantity         float64
	Leverage         int
	UnrealizedPnL    float64
	UnrealizedPnLPct float64
	LiquidationPrice float64
	MarginUsed       float64
	UpdateTime       int64
}

// CandidateCoin 候选币种
type CandidateCoin struct {
	Symbol  string
	Sources []string
}

// Decision AI的交易决策
type Decision struct {
	Symbol          string  `json:"symbol"`
	Action          string  `json:"action"` // "open_long", "open_short", "close_long", "close_short", "hold", "wait"
	Leverage        int     `json:"leverage,omitempty"`
	PositionSizeUSD float64 `json:"position_size_usd,omitempty"`
	StopLoss        float64 `json:"stop_loss,omitempty"`
	TakeProfit      float64 `json:"take_profit,omitempty"`
	Confidence      int     `json:"confidence,omitempty"`
	RiskUSD         float64 `json:"risk_usd,omitempty"`
	Reasoning       string  `json:"reasoning"`
}

// FullDecision AI的完整决策（包含思维链）
type FullDecision struct {
	UserPrompt string
	CoTTrace   string
	Decisions  []Decision
	Timestamp  time.Time
}

// DecisionOrchestrator 决策协调器
type DecisionOrchestrator struct {
	mcpClient       *mcp.Client
	regimeAgent     *RegimeAgent
	signalAgent     *SignalAgent
	positionAgent   *PositionAgent
	riskAgent       *RiskAgent
	btcEthLeverage  int
	altcoinLeverage int
}

// NewDecisionOrchestrator 创建决策协调器
func NewDecisionOrchestrator(mcpClient *mcp.Client, btcEthLeverage, altcoinLeverage int) *DecisionOrchestrator {
	return &DecisionOrchestrator{
		mcpClient:       mcpClient,
		regimeAgent:     NewRegimeAgent(mcpClient),
		signalAgent:     NewSignalAgent(mcpClient),
		positionAgent:   NewPositionAgent(mcpClient),
		riskAgent:       NewRiskAgent(mcpClient, btcEthLeverage, altcoinLeverage),
		btcEthLeverage:  btcEthLeverage,
		altcoinLeverage: altcoinLeverage,
	}
}

// getSharpeFromPerformance 从Performance接口中提取夏普比率
func getSharpeFromPerformance(perf interface{}) (float64, bool) {
	if perf == nil {
		return 0, false
	}

	// 尝试直接类型断言为map
	if perfMap, ok := perf.(map[string]interface{}); ok {
		if sharpe, exists := perfMap["sharpe_ratio"]; exists {
			if sharpeFloat, ok := sharpe.(float64); ok {
				return sharpeFloat, true
			}
		}
	}

	// 如果不是map，尝试通过JSON序列化/反序列化
	type PerformanceData struct {
		SharpeRatio float64 `json:"sharpe_ratio"`
	}
	var perfData PerformanceData
	if jsonData, err := json.Marshal(perf); err == nil {
		if err := json.Unmarshal(jsonData, &perfData); err == nil {
			return perfData.SharpeRatio, true
		}
	}

	return 0, false
}

// GetFullDecision 获取AI的完整交易决策（多agent协作）
func (o *DecisionOrchestrator) GetFullDecision(ctx *Context) (*FullDecision, error) {
	var cotBuilder strings.Builder
	decisions := []Decision{}

	cotBuilder.WriteString("=== Multi-Agent Decision System ===\n\n")

	// 🚨 关键修复：提取绩效记忆（夏普比率）
	sharpeRatio, hasSharpe := getSharpeFromPerformance(ctx.Performance)
	minConfidence := 80 // 默认信心度门槛

	if !hasSharpe {
		cotBuilder.WriteString("## 📊 绩效记忆: 无法获取夏普比率，使用默认门槛(80)\n\n")
	} else if sharpeRatio < -0.5 {
		minConfidence = 101 // 101 = 事实上的"禁止开仓"
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆: 夏普=%.2f (<-0.5) → 🛑 停止开仓 (门槛>100，熔断)\n\n", sharpeRatio))
	} else if sharpeRatio < 0 {
		minConfidence = 85 // 轻微亏损，提高门槛
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆: 夏普=%.2f (<0) → ⚠️ 严格控制 (门槛%d)\n\n", sharpeRatio, minConfidence))
	} else if sharpeRatio < 0.7 {
		minConfidence = 80 // 正收益，正常门槛
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆: 夏普=%.2f (0-0.7) → ✅ 正常 (门槛%d)\n\n", sharpeRatio, minConfidence))
	} else {
		minConfidence = 75 // 优异表现，可适度降低门槛
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆: 夏普=%.2f (>0.7) → 🚀 优异 (门槛%d)\n\n", sharpeRatio, minConfidence))
	}

	// STEP 1: 市场体制分析（使用BTC数据）
	cotBuilder.WriteString("## STEP 1: 市场体制分析\n\n")
	btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]
	if !hasBTC || btcData == nil {
		return nil, fmt.Errorf("缺少BTC市场数据")
	}

	regime, err := o.regimeAgent.Analyze(btcData)
	if err != nil {
		return nil, fmt.Errorf("RegimeAgent失败: %w", err)
	}

	cotBuilder.WriteString(fmt.Sprintf("**体制判断**: %s\n", regime.Regime))
	cotBuilder.WriteString(fmt.Sprintf("**策略**: %s\n", regime.Strategy))
	cotBuilder.WriteString(fmt.Sprintf("**ATR%%**: %.2f%%\n", regime.ATRPct))
	cotBuilder.WriteString(fmt.Sprintf("**分析**: %s\n\n", regime.Reasoning))

	// STEP 2 & 3: 持仓管理和信号检测（并发执行）
	var wg sync.WaitGroup
	var posResult positionManagementResult
	var sigResult signalDetectionResult

	// 启动STEP 2: 持仓管理（goroutine 1）
	wg.Add(1)
	go func() {
		defer wg.Done()
		posResult = o.evaluatePositions(ctx, regime)
	}()

	// 启动STEP 3: 信号检测（goroutine 2）
	// 🚨 关键修复：传递minConfidence到信号检测
	wg.Add(1)
	go func() {
		defer wg.Done()
		sigResult = o.detectSignals(ctx, regime, minConfidence, sharpeRatio)
	}()

	// 等待两个goroutine完成
	wg.Wait()

	// 合并STEP 2的结果
	decisions = append(decisions, posResult.decisions...)
	cotBuilder.WriteString(posResult.cotText)

	// 合并STEP 3的结果
	cotBuilder.WriteString(sigResult.cotText)

	// STEP 4: 风险计算（为有效信号计算风险参数）
	if len(sigResult.signalResults) > 0 {
		cotBuilder.WriteString("## STEP 4: 风险计算\n\n")

		// 计算可用开仓名额
		maxPositions := 3
		currentPositions := len(ctx.Positions)
		availableSlots := maxPositions - currentPositions

		for i, sr := range sigResult.signalResults {
			if i >= availableSlots {
				cotBuilder.WriteString("⚠️  达到可开仓上限，剩余信号暂不处理\n")
				break
			}

			marketData := ctx.MarketDataMap[sr.symbol]
			riskParams, err := o.riskAgent.Calculate(sr.symbol, sr.signal.Direction, sr.signal.ConfidenceLevel, marketData, regime, ctx.Account.TotalEquity, ctx.Account.AvailableBalance)
			if err != nil {
				log.Printf("⚠️  RiskAgent计算%s风险失败: %v", sr.symbol, err)
				cotBuilder.WriteString(fmt.Sprintf("**%s**: 风险计算失败 - %v\n\n", sr.symbol, err))
				continue
			}

			cotBuilder.WriteString(fmt.Sprintf("**%s %s**:\n", sr.symbol, sr.signal.Direction))
			cotBuilder.WriteString(fmt.Sprintf("  - 杠杆: %dx\n", riskParams.Leverage))
			cotBuilder.WriteString(fmt.Sprintf("  - 仓位: %.0f USDT\n", riskParams.PositionSize))
			cotBuilder.WriteString(fmt.Sprintf("  - 止损: %.4f\n", riskParams.StopLoss))
			cotBuilder.WriteString(fmt.Sprintf("  - 止盈: %.4f\n", riskParams.TakeProfit))
			cotBuilder.WriteString(fmt.Sprintf("  - R/R: %.2f:1\n", riskParams.RiskReward))
			cotBuilder.WriteString(fmt.Sprintf("  - 分析: %s\n\n", riskParams.Reasoning))

			if !riskParams.Valid {
				cotBuilder.WriteString("  ⚠️  风险验证未通过，放弃该交易\n\n")
				continue
			}

			// 确定开仓方向
			action := "open_long"
			if sr.signal.Direction == "short" {
				action = "open_short"
			}

			// 计算风险金额
			riskUSD := riskParams.PositionSize * (riskParams.RiskPercent / 100.0)

			// 添加到决策列表
			decisions = append(decisions, Decision{
				Symbol:          sr.symbol,
				Action:          action,
				Leverage:        riskParams.Leverage,
				PositionSizeUSD: riskParams.PositionSize,
				StopLoss:        riskParams.StopLoss,
				TakeProfit:      riskParams.TakeProfit,
				Confidence:      sr.signal.Score,
				RiskUSD:         riskUSD,
				Reasoning: fmt.Sprintf("体制:%s | 信号:%v | 杠杆:%dx | 止损:%.4f | 止盈:%.4f | R/R:%.2f:1 | %s",
					regime.Regime, sr.signal.SignalList, riskParams.Leverage,
					riskParams.StopLoss, riskParams.TakeProfit, riskParams.RiskReward, riskParams.Reasoning),
			})
		}
	}

	// 如果没有任何决策，添加一个wait
	if len(decisions) == 0 {
		decisions = append(decisions, Decision{
			Symbol:    "BTCUSDT",
			Action:    "wait",
			Reasoning: fmt.Sprintf("市场体制:%s，当前无持仓，无有效信号，继续等待", regime.Regime),
		})
	}

	return &FullDecision{
		CoTTrace:  cotBuilder.String(),
		Decisions: decisions,
		Timestamp: time.Now(),
	}, nil
}

// ConvertToJSON 将决策转换为JSON字符串（用于日志）
func (fd *FullDecision) ConvertToJSON() (string, error) {
	data, err := json.MarshalIndent(fd.Decisions, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// positionManagementResult STEP 2持仓管理结果
type positionManagementResult struct {
	decisions []Decision
	cotText   string
	err       error
}

// signalDetectionResult STEP 3信号检测结果
type signalDetectionResult struct {
	signalResults []struct {
		signal *SignalResult
		symbol string
	}
	cotText string
	err     error
}

// evaluatePositions STEP 2: 持仓管理（并发执行）
func (o *DecisionOrchestrator) evaluatePositions(ctx *Context, regime *RegimeResult) positionManagementResult {
	var cotBuilder strings.Builder
	decisions := []Decision{}

	cotBuilder.WriteString("## STEP 2: 持仓管理\n\n")

	if len(ctx.Positions) > 0 {
		for _, pos := range ctx.Positions {
			// 转换为PositionInfo格式
			posInfo := &PositionInfo{
				Symbol:           pos.Symbol,
				Side:             pos.Side,
				EntryPrice:       pos.EntryPrice,
				MarkPrice:        pos.MarkPrice,
				Quantity:         pos.Quantity,
				Leverage:         pos.Leverage,
				UnrealizedPnL:    pos.UnrealizedPnL,
				UnrealizedPnLPct: pos.UnrealizedPnLPct,
				UpdateTime:       pos.UpdateTime,
			}

			marketData, hasData := ctx.MarketDataMap[pos.Symbol]
			if !hasData {
				log.Printf("⚠️  持仓%s缺少市场数据，跳过", pos.Symbol)
				continue
			}

			posDecision, err := o.positionAgent.Evaluate(posInfo, marketData, regime)
			if err != nil {
				log.Printf("⚠️  PositionAgent评估%s失败: %v", pos.Symbol, err)
				continue
			}

			cotBuilder.WriteString(fmt.Sprintf("**%s %s**: %s\n", pos.Symbol, strings.ToUpper(pos.Side), posDecision.Action))
			cotBuilder.WriteString(fmt.Sprintf("  - 理由: %s\n", posDecision.Reason))
			cotBuilder.WriteString(fmt.Sprintf("  - 分析: %s\n\n", posDecision.Reasoning))

			// 如果需要平仓，添加到决策列表
			if posDecision.Action == "close_long" || posDecision.Action == "close_short" {
				decisions = append(decisions, Decision{
					Symbol:     pos.Symbol,
					Action:     posDecision.Action,
					Confidence: posDecision.Confidence,
					Reasoning:  posDecision.Reasoning,
				})
			} else if posDecision.Action == "hold" {
				decisions = append(decisions, Decision{
					Symbol:     pos.Symbol,
					Action:     "hold",
					Reasoning:  posDecision.Reasoning,
				})
			}
		}
	} else {
		cotBuilder.WriteString("当前无持仓\n\n")
	}

	return positionManagementResult{
		decisions: decisions,
		cotText:   cotBuilder.String(),
		err:       nil,
	}
}

// detectSignals STEP 3: 信号检测（并发执行）
func (o *DecisionOrchestrator) detectSignals(ctx *Context, regime *RegimeResult, minConfidence int, sharpeRatio float64) signalDetectionResult {
	var cotBuilder strings.Builder
	signalResults := []struct {
		signal *SignalResult
		symbol string
	}{}

	cotBuilder.WriteString("## STEP 3: 信号检测\n\n")

	// 🟢 关键修复：创建持仓币种的Set，防止重复开仓
	positionSymbols := make(map[string]bool)
	for _, pos := range ctx.Positions {
		positionSymbols[pos.Symbol] = true
	}

	// 计算当前可用开仓名额
	maxPositions := 3
	currentPositions := len(ctx.Positions)
	availableSlots := maxPositions - currentPositions

	if availableSlots <= 0 {
		cotBuilder.WriteString(fmt.Sprintf("持仓已满（%d/%d），暂不寻找新机会\n\n", currentPositions, maxPositions))
		return signalDetectionResult{
			signalResults: signalResults,
			cotText:       cotBuilder.String(),
			err:           nil,
		}
	}

	if regime.Regime == "C" {
		cotBuilder.WriteString("体制(C)窄幅盘整，禁止开仓\n\n")
		return signalDetectionResult{
			signalResults: signalResults,
			cotText:       cotBuilder.String(),
			err:           nil,
		}
	}

	// 🚨 关键修复：如果夏普比率过低触发熔断，跳过信号检测
	if minConfidence > 100 {
		cotBuilder.WriteString(fmt.Sprintf("⚠️ 夏普比率过低(%.2f)，已触发熔断，跳过新机会扫描\n\n", sharpeRatio))
		return signalDetectionResult{
			signalResults: signalResults,
			cotText:       cotBuilder.String(),
			err:           nil,
		}
	}

	cotBuilder.WriteString(fmt.Sprintf("可开仓数量: %d | 绩效门槛: %d\n\n", availableSlots, minConfidence))

	// 检测候选币种信号
	for _, coin := range ctx.CandidateCoins {
		// 🟢 关键修复：跳过已持仓的币种，防止重复开仓
		if _, isHolding := positionSymbols[coin.Symbol]; isHolding {
			cotBuilder.WriteString(fmt.Sprintf("**%s**: 已持仓，跳过新信号检测\n\n", coin.Symbol))
			continue
		}

		marketData, hasData := ctx.MarketDataMap[coin.Symbol]
		if !hasData {
			continue
		}

		signal, err := o.signalAgent.Detect(coin.Symbol, marketData, regime)
		if err != nil {
			log.Printf("⚠️  SignalAgent检测%s失败: %v", coin.Symbol, err)
			continue
		}

		cotBuilder.WriteString(fmt.Sprintf("**%s**: %s (分数:%d)\n", coin.Symbol, signal.Direction, signal.Score))

		// 🚨 关键修复：Go代码强制执行信心度门槛（绩效记忆）
		if signal.Valid && signal.Direction != "none" {
			if signal.Score >= minConfidence {
				cotBuilder.WriteString(fmt.Sprintf("  ✓ 信号有效 且 信心度(%d) >= 门槛(%d)\n", signal.Score, minConfidence))
				cotBuilder.WriteString(fmt.Sprintf("  - 信号: %v\n", signal.SignalList))
				cotBuilder.WriteString(fmt.Sprintf("  - 分析: %s\n\n", signal.Reasoning))
				signalResults = append(signalResults, struct {
					signal *SignalResult
					symbol string
				}{signal, coin.Symbol})
			} else {
				cotBuilder.WriteString(fmt.Sprintf("  × 信号有效 但 信心度(%d) < 绩效门槛(%d)，已过滤 [绩效记忆拦截]\n\n", signal.Score, minConfidence))
			}
		} else {
			cotBuilder.WriteString(fmt.Sprintf("  × 信号不足 (%d个维度)\n\n", len(signal.SignalList)))
		}
	}

	return signalDetectionResult{
		signalResults: signalResults,
		cotText:       cotBuilder.String(),
		err:           nil,
	}
}

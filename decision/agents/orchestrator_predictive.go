package agents

import (
	"fmt"
	"log"
	"math"
	"nofx/decision/tracker"
	"nofx/decision/types"
	"nofx/market"
	"strings"
	"time"
)

// GetFullDecisionPredictive 预测驱动的决策方法（新架构）
func (o *DecisionOrchestrator) GetFullDecisionPredictive(ctx *Context) (*FullDecision, error) {
	var cotBuilder strings.Builder
	decisions := []Decision{}

	cotBuilder.WriteString("=== AI Prediction-Driven Decision System ===\n\n")

	// 🧠 注入AI记忆（Sprint 1）
	if ctx.MemoryPrompt != "" {
		cotBuilder.WriteString(ctx.MemoryPrompt)
		cotBuilder.WriteString("\n")
	}

	// 🚨 新增：提取夏普比率进行自适应风控
	sharpeRatio, hasSharpe := getSharpeFromPerformance(ctx.Performance)
	minProbability := 0.65   // 默认概率阈值65%（修正：AI在有冲突时最高给0.65）
	allowMediumConf := true  // 默认允许medium置信度（修正：AI在有冲突时给medium是合理的）

	// ⚠️  临时禁用夏普限制（用户要求）- 让系统可以正常开仓测试
	if !hasSharpe {
		cotBuilder.WriteString("## 📊 绩效记忆\n\n无历史绩效，使用默认阈值 (概率≥65%, 允许medium置信度)\n\n")
	} else {
		// 显示夏普但不限制
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f → ✅ **测试模式** (暂不限制，概率≥65%%, 允许medium)\n\n", sharpeRatio))
	}

	/* 🔒 原夏普限制（已临时禁用）
	if !hasSharpe {
		cotBuilder.WriteString("## 📊 绩效记忆\n\n无历史绩效，使用默认阈值 (概率≥65%, 允许medium置信度)\n\n")
	} else if sharpeRatio < -0.5 {
		// 🛑 熔断：夏普比率严重为负，停止开仓
		minProbability = 1.01 // 不可能达到的阈值
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (<-0.5) → 🛑 **熔断保护** (停止开仓)\n\n", sharpeRatio))
	} else if sharpeRatio < 0 {
		// ⚠️ 严格：夏普为负，提高阈值并禁用medium
		minProbability = 0.80 // 提高到80%
		allowMediumConf = false // 禁用medium
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (<0) → ⚠️  **严格控制** (概率≥80%%, 仅high置信度)\n\n", sharpeRatio))
	} else if sharpeRatio < 0.7 {
		// ✅ 正常：夏普轻微为正，正常阈值
		minProbability = 0.65
		allowMediumConf = true
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (0-0.7) → ✅ **正常运行** (概率≥65%%, 允许medium)\n\n", sharpeRatio))
	} else {
		// 🚀 宽松：夏普优异，降低阈值
		minProbability = 0.60  // 进一步降低到60%
		allowMediumConf = true // 允许medium置信度
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (>0.7) → 🚀 **优异表现** (概率≥60%%, 允许medium)\n\n", sharpeRatio))
	}
	*/

	// STEP 1: 收集市场情报
	cotBuilder.WriteString("## STEP 1: 市场情报收集\n\n")

	btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]
	if !hasBTC || btcData == nil {
		return nil, fmt.Errorf("缺少BTC市场数据")
	}

	// 收集所有候选币种符号
	symbols := []string{"BTCUSDT"}
	for _, coin := range ctx.CandidateCoins {
		if coin.Symbol != "BTCUSDT" {
			symbols = append(symbols, coin.Symbol)
		}
	}

	intelligence, err := o.intelligenceAgent.Collect(btcData, symbols, ctx.MarketDataMap)
	if err != nil {
		log.Printf("⚠️  市场情报收集失败: %v", err)
		intelligence = &MarketIntelligence{
			MarketPhase:      "unknown",
			KeyRisks:         []string{"数据获取失败"},
			KeyOpportunities: []string{},
			Summary:          "无法获取市场情报",
		}
	}

	cotBuilder.WriteString(fmt.Sprintf("**市场阶段**: %s\n", intelligence.MarketPhase))
	cotBuilder.WriteString(fmt.Sprintf("**市场综述**: %s\n\n", intelligence.Summary))

	if len(intelligence.KeyRisks) > 0 {
		cotBuilder.WriteString("**关键风险**:\n")
		for _, risk := range intelligence.KeyRisks {
			cotBuilder.WriteString(fmt.Sprintf("  - %s\n", risk))
		}
		cotBuilder.WriteString("\n")
	}

	if len(intelligence.KeyOpportunities) > 0 {
		cotBuilder.WriteString("**关键机会**:\n")
		for _, opp := range intelligence.KeyOpportunities {
			cotBuilder.WriteString(fmt.Sprintf("  - %s\n", opp))
		}
		cotBuilder.WriteString("\n")
	}

	// 统一的预测跟踪器与扩展数据缓存（避免重复I/O）
	predTracker := tracker.NewPredictionTracker("./prediction_logs")
	extendedDataCache := make(map[string]*market.ExtendedData)

	// STEP 2: 持仓管理（基于预测）
	cotBuilder.WriteString("## STEP 2: 持仓管理（基于AI预测）\n\n")

	if len(ctx.Positions) > 0 {
		for _, pos := range ctx.Positions {
			marketData, hasData := ctx.MarketDataMap[pos.Symbol]
			if !hasData {
				log.Printf("⚠️  持仓%s缺少市场数据，跳过", pos.Symbol)
				continue
			}

			extendedData, ok := extendedDataCache[pos.Symbol]
			if !ok {
				extendedData, _ = market.GetExtendedData(pos.Symbol)
				extendedDataCache[pos.Symbol] = extendedData
			}

			historicalPerf := predTracker.GetPerformance(pos.Symbol)
			recentFeedback := predTracker.GetRecentFeedback(pos.Symbol, 8)

			predCtx := &PredictionContext{
				Intelligence:   intelligence,
				MarketData:     marketData,
				ExtendedData:   extendedData,
				HistoricalPerf: historicalPerf,
				SharpeRatio:    sharpeRatio,
				Account:        &ctx.Account,
				Positions:      ctx.Positions,
				RecentFeedback: recentFeedback,
				TraderMemory:   ctx.MemoryPrompt, // 🧠 注入实际交易记忆
			}

			prediction, err := o.predictionAgent.PredictWithRetry(predCtx, 3)
			if err != nil {
				log.Printf("⚠️  预测%s失败: %v", pos.Symbol, err)
				continue
			}

			// 确保预测的symbol与当前持仓一致（防止AI默认返回BTC）
			prediction.Symbol = pos.Symbol

			cotBuilder.WriteString(fmt.Sprintf("**%s %s持仓预测**:\n", pos.Symbol, strings.ToUpper(pos.Side)))
			cotBuilder.WriteString(fmt.Sprintf("  预测方向: %s | 概率: %.0f%% | 预期幅度: %+.2f%%\n",
				prediction.Direction, prediction.Probability*100, prediction.ExpectedMove))
			cotBuilder.WriteString(fmt.Sprintf("  时间框架: %s | 置信度: %s | 风险级别: %s\n",
				prediction.Timeframe, prediction.Confidence, prediction.RiskLevel))
			cotBuilder.WriteString(fmt.Sprintf("  推理: %s\n\n", prediction.Reasoning))

			// 基于预测决定是否平仓
			shouldClose := o.shouldClosePosition(pos, prediction)

			if shouldClose {
				action := "close_long"
				if pos.Side == "short" {
					action = "close_short"
				}

				decisions = append(decisions, Decision{
					Symbol: pos.Symbol,
					Action: action,
					Reasoning: fmt.Sprintf("AI预测: %s (概率%.0f%%) | %s",
						prediction.Direction, prediction.Probability*100, prediction.Reasoning),
				})

				cotBuilder.WriteString(fmt.Sprintf("  ⚠️  决策: 平仓 (预测与持仓方向冲突)\n\n"))
			} else {
				decisions = append(decisions, Decision{
					Symbol:    pos.Symbol,
					Action:    "hold",
					Reasoning: fmt.Sprintf("AI预测支持持有 | %s", prediction.Reasoning),
				})

				cotBuilder.WriteString(fmt.Sprintf("  ✓ 决策: 持有 (预测支持当前方向)\n\n"))
			}
		}
	} else {
		cotBuilder.WriteString("当前无持仓\n\n")
	}

	// STEP 3: 寻找新机会（基于AI预测）
	cotBuilder.WriteString("## STEP 3: AI预测分析（寻找新机会）\n\n")

	// 计算可用开仓名额
	maxPositions := 3
	currentPositions := len(ctx.Positions)
	availableSlots := maxPositions - currentPositions

	if availableSlots <= 0 {
		cotBuilder.WriteString(fmt.Sprintf("持仓已满（%d/%d），暂不寻找新机会\n\n", currentPositions, maxPositions))
	} else {
		cotBuilder.WriteString(fmt.Sprintf("可开仓数量: %d\n\n", availableSlots))

		// 创建预测跟踪器
		predTracker := tracker.NewPredictionTracker("./prediction_logs")

		// 已持仓币种集合
		positionSymbols := make(map[string]bool)
		for _, pos := range ctx.Positions {
			positionSymbols[pos.Symbol] = true
		}

		// 收集所有有效预测
		validPredictions := []struct {
			symbol     string
			prediction *types.Prediction
		}{}

		for _, coin := range ctx.CandidateCoins {
			// 跳过已持仓的币种
			if positionSymbols[coin.Symbol] {
				continue
			}

			marketData, hasData := ctx.MarketDataMap[coin.Symbol]
			if !hasData {
				continue
			}

			extendedData, ok := extendedDataCache[coin.Symbol]
			if !ok {
				extendedData, _ = market.GetExtendedData(coin.Symbol)
				extendedDataCache[coin.Symbol] = extendedData
			}

			historicalPerf := predTracker.GetPerformance(coin.Symbol)
			recentFeedback := predTracker.GetRecentFeedback(coin.Symbol, 8)

			predCtx := &PredictionContext{
				Intelligence:   intelligence,
				MarketData:     marketData,
				ExtendedData:   extendedData,
				HistoricalPerf: historicalPerf,
				SharpeRatio:    sharpeRatio,
				Account:        &ctx.Account,
				Positions:      ctx.Positions,
				RecentFeedback: recentFeedback,
				TraderMemory:   ctx.MemoryPrompt, // 🧠 注入实际交易记忆
			}

			prediction, err := o.predictionAgent.PredictWithRetry(predCtx, 3)
			if err != nil {
				log.Printf("⚠️  预测%s失败: %v", coin.Symbol, err)
				continue
			}

			// 确保预测使用当前币种，避免AI返回默认BTC
			prediction.Symbol = coin.Symbol

			cotBuilder.WriteString(fmt.Sprintf("**%s预测**:\n", coin.Symbol))
			cotBuilder.WriteString(fmt.Sprintf("  方向: %s | 概率: %.0f%% | 预期幅度: %+.2f%% | 时间: %s\n",
				prediction.Direction, prediction.Probability*100, prediction.ExpectedMove, prediction.Timeframe))
			cotBuilder.WriteString(fmt.Sprintf("  置信度: %s | 风险: %s | 最好: %+.2f%% | 最坏: %+.2f%%\n",
				prediction.Confidence, prediction.RiskLevel, prediction.BestCase, prediction.WorstCase))
			cotBuilder.WriteString(fmt.Sprintf("  推理: %s\n", prediction.Reasoning))

			// 判断是否值得开仓
			// 条件：1) 概率满足动态阈值 2) 置信度满足要求 3) 方向明确
			meetsConfidence := prediction.Confidence == "high" ||
				prediction.Confidence == "very_high" ||
				(allowMediumConf && prediction.Confidence == "medium")

			if prediction.Probability >= minProbability && meetsConfidence && prediction.Direction != "neutral" {
				cotBuilder.WriteString(fmt.Sprintf("  ✓ 满足开仓条件（概率%.0f%% >= %.0f%% 且 置信度%s）\n\n",
					prediction.Probability*100, minProbability*100, prediction.Confidence))

				validPredictions = append(validPredictions, struct {
					symbol     string
					prediction *types.Prediction
				}{coin.Symbol, prediction})
			} else {
				// 详细说明不满足的原因
				if prediction.Direction == "neutral" {
					cotBuilder.WriteString(fmt.Sprintf("  × 方向neutral，不开仓\n\n"))
				} else if prediction.Probability < minProbability {
					cotBuilder.WriteString(fmt.Sprintf("  × 概率%.0f%% < 阈值%.0f%% (夏普调整)\n\n",
						prediction.Probability*100, minProbability*100))
				} else if !meetsConfidence {
					cotBuilder.WriteString(fmt.Sprintf("  × 置信度%s不满足要求 (需要high", prediction.Confidence))
					if allowMediumConf {
						cotBuilder.WriteString(" 或 medium)\n\n")
					} else {
						cotBuilder.WriteString(")\n\n")
					}
				}
			}
		}

		// STEP 4: 风险计算（基于AI预测的期望值）
		if len(validPredictions) > 0 {
			cotBuilder.WriteString("## STEP 4: 风险计算与仓位分配\n\n")

			opened := 0
			remainingBalance := ctx.Account.AvailableBalance

			for _, vp := range validPredictions {
				if opened >= availableSlots {
					cotBuilder.WriteString("⚠️  可开仓数量已耗尽\n")
					break
				}

				marketData := ctx.MarketDataMap[vp.symbol]

				positionSize, leverage, stopLoss, takeProfit, err := o.calculatePositionFromPrediction(
					vp.prediction, marketData, ctx.Account.TotalEquity, remainingBalance)

				if err != nil {
					cotBuilder.WriteString(fmt.Sprintf("**%s**: 风险计算失败 - %v\n\n", vp.symbol, err))
					continue
				}

				validationErr := o.validateRiskParameters(
					vp.symbol, vp.prediction.Direction, marketData,
					stopLoss, takeProfit, leverage)
				if validationErr != nil {
					cotBuilder.WriteString(fmt.Sprintf("**%s**: 风控验证失败 - %v\n\n", vp.symbol, validationErr))
					continue
				}

				// 🆕 入场时机验证（防止追涨杀跌）
				timingErr := validateEntryTiming(vp.prediction.Direction, marketData)
				if timingErr != nil {
					cotBuilder.WriteString(fmt.Sprintf("**%s**: %v\n\n", vp.symbol, timingErr))
					log.Printf("⏸️  [%s] 入场时机不佳，跳过开仓: %v", vp.symbol, timingErr)
					continue
				}

				requiredMargin := positionSize / float64(leverage)
				if requiredMargin > remainingBalance {
					cotBuilder.WriteString(fmt.Sprintf("**%s**: 剩余资金不足（需要%.2f, 剩余%.2f）\n\n",
						vp.symbol, requiredMargin, remainingBalance))
					continue
				}

				cotBuilder.WriteString(fmt.Sprintf("**%s**:\n", vp.symbol))
				cotBuilder.WriteString(fmt.Sprintf("  仓位: %.0f USDT | 杠杆: %dx | 保证金: %.2f\n",
					positionSize, leverage, requiredMargin))
				cotBuilder.WriteString(fmt.Sprintf("  止损: %.4f | 止盈: %.4f\n", stopLoss, takeProfit))
				cotBuilder.WriteString(fmt.Sprintf("  期望收益: %+.2f%% | 最大风险: %+.2f%%\n",
					vp.prediction.BestCase, vp.prediction.WorstCase))
				cotBuilder.WriteString(fmt.Sprintf("  可用资金: %.2f → %.2f\n\n",
					remainingBalance, remainingBalance-requiredMargin))

				action := "open_long"
				if vp.prediction.Direction == "down" {
					action = "open_short"
				}

				confidence := int(math.Round(vp.prediction.Probability * 100))
				if confidence > 100 {
					confidence = 100
				}
				if confidence < 0 {
					confidence = 0
				}

				riskPercent := math.Abs(vp.prediction.WorstCase)

				decisions = append(decisions, Decision{
					Symbol:          vp.symbol,
					Action:          action,
					Leverage:        leverage,
					PositionSizeUSD: positionSize,
					StopLoss:        stopLoss,
					TakeProfit:      takeProfit,
					Confidence:      confidence,
					RiskUSD:         positionSize * (riskPercent / 100.0),
					Reasoning: fmt.Sprintf("AI预测: %s (概率%.0f%%, 期望%+.2f%%) | %s",
						vp.prediction.Direction, vp.prediction.Probability*100,
						vp.prediction.ExpectedMove, vp.prediction.Reasoning),
				})

				if err := predTracker.Record(vp.prediction, marketData.CurrentPrice); err != nil {
					log.Printf("⚠️  记录预测失败: %v", err)
				}

				remainingBalance -= requiredMargin
				opened++
			}
		}
	}

	// 如果没有任何决策，添加一个wait
	if len(decisions) == 0 {
		decisions = append(decisions, Decision{
			Symbol:    "BTCUSDT",
			Action:    "wait",
			Reasoning: fmt.Sprintf("市场阶段:%s | 当前无持仓 | 无高概率预测机会", intelligence.MarketPhase),
		})
	}

	return &FullDecision{
		CoTTrace:  cotBuilder.String(),
		Decisions: decisions,
	}, nil
}

// shouldClosePosition 基于AI预测判断是否应该平仓
func (o *DecisionOrchestrator) shouldClosePosition(pos PositionInfoInput, prediction *types.Prediction) bool {
	// 1. 如果预测方向与持仓方向完全相反，且概率>65% 且 持仓>30分钟 → 平仓
	holdDuration := time.Since(pos.OpenTime)

	if pos.Side == "long" && prediction.Direction == "down" && prediction.Probability > 0.65 {
		if holdDuration > 30*time.Minute {
			return true
		}
	}
	if pos.Side == "short" && prediction.Direction == "up" && prediction.Probability > 0.65 {
		if holdDuration > 30*time.Minute {
			return true
		}
	}

	// 2. 如果已经亏损>10% → 止损
	if pos.UnrealizedPnLPct < -10.0 {
		return true
	}

	// 3. 如果已经盈利>20% 且预测变为中性 → 获利了结
	if pos.UnrealizedPnLPct > 20.0 && prediction.Direction == "neutral" {
		return true
	}

	return false
}

// calculatePositionFromPrediction 基于AI预测计算仓位参数
func (o *DecisionOrchestrator) calculatePositionFromPrediction(
	prediction *types.Prediction,
	marketData *market.Data,
	totalEquity float64,
	availableBalance float64,
) (positionSize float64, leverage int, stopLoss float64, takeProfit float64, err error) {

	// 🔧 修复AI预测值的符号错误和逻辑错误
	// 做空时：best_case应该<0且绝对值大（价格跌得多=盈利多），worst_case应该>0（价格涨=亏损）
	// 做多时：best_case应该>0（价格涨=盈利），worst_case应该<0（价格跌=亏损）
	if prediction.Direction == "down" {
		// 做空：三种错误情况
		if prediction.BestCase > 0 {
			// 情况1：best_case是正数，说明AI认为价格上涨是"最好情况" → 完全搞反
			log.Printf("🔧 %s 做空预测修正（类型1）：best_case %.2f%% → %.2f%%, worst_case %.2f%% → %.2f%%",
				prediction.Symbol, prediction.BestCase, -math.Abs(prediction.WorstCase),
				prediction.WorstCase, math.Abs(prediction.BestCase))
			prediction.BestCase, prediction.WorstCase = -math.Abs(prediction.WorstCase), math.Abs(prediction.BestCase)
		} else if prediction.BestCase < 0 && prediction.WorstCase < 0 {
			// 情况2：两个都是负数，AI理解为"价格跌幅"，但把小跌幅当成最好 → 逻辑反了
			// 对做空：跌得多才是最好的，所以应该交换
			if math.Abs(prediction.BestCase) < math.Abs(prediction.WorstCase) {
				// best_case的绝对值小于worst_case，说明AI认为"跌得少=好"，这是错的
				log.Printf("🔧 %s 做空预测修正（类型2）：交换best/worst并调整符号",
					prediction.Symbol)
				log.Printf("   修正前: best=%.2f%%, worst=%.2f%%", prediction.BestCase, prediction.WorstCase)
				// 交换并修正：跌得多的变成best_case（保持负号），跌得少的变成worst_case（改正号表示止损）
				prediction.BestCase, prediction.WorstCase = prediction.WorstCase, -prediction.BestCase
				log.Printf("   修正后: best=%.2f%%, worst=%.2f%%", prediction.BestCase, prediction.WorstCase)
			} else {
				// best_case绝对值已经大于worst_case，只需要修正worst_case的符号
				log.Printf("🔧 %s 做空worst_case符号修正：%.2f%% → %.2f%%",
					prediction.Symbol, prediction.WorstCase, -prediction.WorstCase)
				prediction.WorstCase = -prediction.WorstCase
			}
		} else if prediction.WorstCase < 0 {
			// 情况3：best_case正确（负数），worst_case错误（也是负数）
			log.Printf("🔧 %s 做空worst_case符号修正：%.2f%% → %.2f%%",
				prediction.Symbol, prediction.WorstCase, -prediction.WorstCase)
			prediction.WorstCase = -prediction.WorstCase
		}
	} else if prediction.Direction == "up" {
		// 做多：检查AI是否理解错误
		if prediction.BestCase < 0 {
			// best_case是负数，说明AI认为价格下跌是"最好情况"，这对做多是错的
			log.Printf("🔧 %s 做多预测修正：best_case %.2f%% → %.2f%%, worst_case %.2f%% → %.2f%%",
				prediction.Symbol, prediction.BestCase, math.Abs(prediction.WorstCase),
				prediction.WorstCase, -math.Abs(prediction.BestCase))
			prediction.BestCase, prediction.WorstCase = math.Abs(prediction.WorstCase), -math.Abs(prediction.BestCase)
		} else if prediction.WorstCase > 0 {
			// best_case已经是正数（正确），但worst_case也是正数（错误）
			// worst_case应该是负数（价格下跌=止损）
			log.Printf("🔧 %s 做多worst_case修正：%.2f%% → %.2f%%",
				prediction.Symbol, prediction.WorstCase, -prediction.WorstCase)
			prediction.WorstCase = -prediction.WorstCase
		}
	}

	// 基于概率和风险计算仓位（简化的凯利公式）
	// f* = (p*b - q) / b
	// p = 胜率, q = 败率, b = 盈亏比

	winRate := prediction.Probability
	loseRate := 1 - prediction.Probability

	// 🔧 修复：根据ATR%动态确保best_case和worst_case有合理值
	// 在低波动市场中，AI可能给出极小的值，需要根据ATR调整
	atrPct := (marketData.LongerTermContext.ATR14 / marketData.CurrentPrice) * 100

	// 动态计算最小case值：至少为3倍ATR（确保止盈/止损倍数在合理范围）
	minCaseValue := math.Max(0.5, atrPct*3.0)

	if math.Abs(prediction.BestCase) < minCaseValue {
		log.Printf("⚠️  %s best_case=%.2f%%过小（ATR%%=%.2f%%），调整为%.2f%%",
			prediction.Symbol, prediction.BestCase, atrPct, minCaseValue)
		if prediction.BestCase >= 0 {
			prediction.BestCase = minCaseValue
		} else {
			prediction.BestCase = -minCaseValue
		}
	}

	if math.Abs(prediction.WorstCase) < minCaseValue {
		log.Printf("⚠️  %s worst_case=%.2f%%过小（ATR%%=%.2f%%），调整为%.2f%%",
			prediction.Symbol, prediction.WorstCase, atrPct, minCaseValue)
		if prediction.WorstCase >= 0 {
			prediction.WorstCase = minCaseValue
		} else {
			prediction.WorstCase = -minCaseValue
		}
	}

	// 🔧 新增：确保调整后的R/R比 ≥ 2.0
	// 这是风控的硬性要求，即使在低波动市场也必须满足
	minRR := 2.0

	// 🔧 修复：做空和做多的R/R计算都应该是 盈利/亏损
	if prediction.Direction == "down" {
		// 做空：|best_case| / |worst_case| ≥ 2.0（盈利/亏损）
		currentRR := math.Abs(prediction.BestCase) / math.Abs(prediction.WorstCase)
		if currentRR < minRR {
			// R/R不足，调整best_case（增大止盈目标）以满足要求
			requiredBestCase := math.Abs(prediction.WorstCase) * minRR
			// best_case应该是负数（价格下跌）
			prediction.BestCase = -requiredBestCase
			log.Printf("🔧 %s 做空R/R调整: best_case %.2f%% → %.2f%% (确保R/R≥%.1f)",
				prediction.Symbol, math.Abs(prediction.BestCase), requiredBestCase, minRR)
		}
	} else {
		// 做多：best_case / |worst_case| ≥ 2.0（盈利/亏损）
		currentRR := math.Abs(prediction.BestCase) / math.Abs(prediction.WorstCase)
		if currentRR < minRR {
			// R/R不足，调整best_case（增大止盈目标）以满足要求
			requiredBestCase := math.Abs(prediction.WorstCase) * minRR
			// best_case应该是正数（价格上涨）
			prediction.BestCase = requiredBestCase
			log.Printf("🔧 %s 做多R/R调整: best_case %.2f%% → %.2f%% (确保R/R≥%.1f)",
				prediction.Symbol, math.Abs(prediction.BestCase), requiredBestCase, minRR)
		}
	}

	// 🔧 关键修复：根据方向正确计算盈亏比
	// AI预测的 best_case/worst_case 是价格变化百分比
	// 需要转换为持仓盈亏比
	var payoffRatio float64

	if prediction.Direction == "down" {
		// 做空时：
		// - best_case应该是负数（价格下跌，盈利）
		// - worst_case应该是正数或小负数（价格上涨或小跌，亏损）
		// 但AI可能返回都是负数的情况，需要兼容处理

		// 取绝对值确保计算正确
		absBest := math.Abs(prediction.BestCase)
		absWorst := math.Abs(prediction.WorstCase)

		if absBest < 1e-6 {
			return 0, 0, 0, 0, fmt.Errorf("做空时best_case(%.2f)过小，无法计算盈亏比", prediction.BestCase)
		}

		// 做空的盈亏比 = 盈利幅度 / 亏损幅度
		// 如果都是负数，说明AI理解为都是跌幅，取较大的跌幅作为盈利
		if prediction.BestCase < 0 && prediction.WorstCase < 0 {
			// 都是负数：取绝对值较大的作为盈利（价格跌得更多）
			if absWorst > absBest {
				payoffRatio = absWorst / absBest // worst_case跌得更多，是盈利
			} else {
				payoffRatio = absBest / absWorst // best_case跌得更多，是盈利
			}
		} else {
			// 正常情况：best_case负（盈利），worst_case正（亏损）
			payoffRatio = absBest / absWorst
		}

	} else {
		// 做多时：价格上涨是盈利（best_case），价格下跌是亏损（worst_case）
		// 盈亏比 = best_case / |worst_case|
		absWorst := math.Abs(prediction.WorstCase)
		if absWorst < 1e-6 {
			return 0, 0, 0, 0, fmt.Errorf("做多时worst_case(%.2f)过小，无法计算盈亏比", prediction.WorstCase)
		}
		payoffRatio = prediction.BestCase / absWorst
	}

	if payoffRatio <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("无效的盈亏比: %.2f", payoffRatio)
	}

	// 凯利比例
	kellyFraction := (winRate*payoffRatio - loseRate) / payoffRatio

	if kellyFraction <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("凯利比例为负，不应开仓")
	}

	// 使用全凯利 - 数学最优解，最大化长期增长率
	conservativeKelly := kellyFraction * 1.0

	// 计算仓位大小
	positionSize = totalEquity * conservativeKelly

	// 硬约束：单币最多60%总资金
	maxPositionSize := totalEquity * 0.6
	if positionSize > maxPositionSize {
		positionSize = maxPositionSize
	}

	// 硬约束：不超过可用余额
	if positionSize > availableBalance*0.9 {
		positionSize = availableBalance * 0.9
	}

	// 最小仓位检查
	if positionSize < 10 {
		return 0, 0, 0, 0, fmt.Errorf("计算的仓位太小: %.2f USDT", positionSize)
	}

	// 计算杠杆（基于波动率）
	isBTCETH := (prediction.Symbol == "BTCUSDT" || prediction.Symbol == "ETHUSDT")
	baseLeverage := o.altcoinLeverage
	if isBTCETH {
		baseLeverage = o.btcEthLeverage
	}

	// 根据风险级别调整杠杆
	switch prediction.RiskLevel {
	case "low":
		leverage = baseLeverage // 使用基础杠杆
	case "medium":
		leverage = int(float64(baseLeverage) * 0.8) // 降低20%
	case "high":
		leverage = int(float64(baseLeverage) * 0.6) // 降低40%
	default:
		leverage = int(float64(baseLeverage) * 0.8)
	}

	if leverage < 1 {
		leverage = 1
	}

	// 计算止损止盈（基于AI预测的最好/最坏情况）
	currentPrice := marketData.CurrentPrice

	if prediction.Direction == "up" {
		// 做多
		stopLoss = currentPrice * (1 + prediction.WorstCase/100.0)  // 最坏情况（价格下跌=止损）
		takeProfit = currentPrice * (1 + prediction.BestCase/100.0) // 最好情况（价格上涨=止盈）
	} else {
		// 做空
		// 🔧 修复后的值：best_case是负数（价格下跌=盈利=止盈），worst_case是正数（价格上涨=亏损=止损）
		stopLoss = currentPrice * (1 + prediction.WorstCase/100.0)   // worst_case正数=价格上涨=止损
		takeProfit = currentPrice * (1 + prediction.BestCase/100.0)  // best_case负数=价格下跌=止盈
	}

	// 验证止损在强平价范围内
	marginRate := LiquidationMarginRate / float64(leverage)
	var liquidationPrice float64

	if prediction.Direction == "up" {
		liquidationPrice = currentPrice * (1 - marginRate)
		if stopLoss <= liquidationPrice {
			// 止损价太低，调整杠杆
			leverage = int(float64(leverage) * 0.7)
			if leverage < 1 {
				leverage = 1
			}
			// 重新计算强平价
			marginRate = LiquidationMarginRate / float64(leverage)
			liquidationPrice = currentPrice * (1 - marginRate)
		}
	} else {
		liquidationPrice = currentPrice * (1 + marginRate)
		if stopLoss >= liquidationPrice {
			// 止损价太高，调整杠杆
			leverage = int(float64(leverage) * 0.7)
			if leverage < 1 {
				leverage = 1
			}
			// 重新计算强平价
			marginRate = LiquidationMarginRate / float64(leverage)
			liquidationPrice = currentPrice * (1 + marginRate)
		}
	}

	return positionSize, leverage, stopLoss, takeProfit, nil
}

// validateRiskParameters 验证风控参数（预测模式的风控防线）
// 检查：1) ATR合理性  2) R/R≥2.0  3) 强平价安全距离
func (o *DecisionOrchestrator) validateRiskParameters(
	symbol string,
	direction string,
	marketData *market.Data,
	stopLoss float64,
	takeProfit float64,
	leverage int,
) error {
	if marketData == nil || marketData.LongerTermContext == nil {
		return fmt.Errorf("市场数据不完整")
	}

	currentPrice := marketData.CurrentPrice
	atr := marketData.LongerTermContext.ATR14
	atrPct := (atr / currentPrice) * 100

	// 1️⃣ 计算止损止盈的ATR倍数
	var stopDistancePct, tpDistancePct float64
	var stopMultiple, tpMultiple float64

	// 🔧 修复：direction参数是"up"/"down"，而不是"long"/"short"
	if direction == "up" || direction == "long" {
		stopDistancePct = (currentPrice - stopLoss) / currentPrice * 100
		tpDistancePct = (takeProfit - currentPrice) / currentPrice * 100
		stopMultiple = (currentPrice - stopLoss) / atr
		tpMultiple = (takeProfit - currentPrice) / atr
	} else {
		stopDistancePct = (stopLoss - currentPrice) / currentPrice * 100
		tpDistancePct = (currentPrice - takeProfit) / currentPrice * 100
		stopMultiple = (stopLoss - currentPrice) / atr
		tpMultiple = (currentPrice - takeProfit) / atr
	}

	// 🚨 检查止损是否在ATR合理范围内 [2.0-8.0倍]
	if stopMultiple < MinStopMultiple || stopMultiple > MaxStopMultiple {
		return fmt.Errorf("止损倍数%.2fx超出合理范围[%.1f-%.1f]ATR（止损%.2f%%, ATR%%=%.2f%%）",
			stopMultiple, MinStopMultiple, MaxStopMultiple, stopDistancePct, atrPct)
	}

	// 🚨 检查止盈是否在ATR合理范围内 [6.0-20.0倍]
	if tpMultiple < MinTPMultiple || tpMultiple > MaxTPMultiple {
		return fmt.Errorf("止盈倍数%.2fx超出合理范围[%.1f-%.1f]ATR（止盈%.2f%%, ATR%%=%.2f%%）",
			tpMultiple, MinTPMultiple, MaxTPMultiple, tpDistancePct, atrPct)
	}

	// 2️⃣ 计算R/R比（使用与riskAgent相同的逻辑）
	riskReward := tpDistancePct / stopDistancePct

	// 🚨 硬约束：R/R必须≥2.0（与传统模式一致）
	minRR := MinRiskReward * (1.0 - RRFloatTolerance) // 2.0 * 0.95 = 1.90
	if riskReward < minRR {
		return fmt.Errorf("风险回报比%.2f:1 < %.1f:1要求（止损%.1fx, 止盈%.1fx, 差值%.2f）",
			riskReward, MinRiskReward, stopMultiple, tpMultiple, MinRiskReward-riskReward)
	}

	// 3️⃣ 检查强平价安全距离（使用与riskAgent相同的标准）
	marginRate := LiquidationMarginRate / float64(leverage)
	var liquidationPrice float64
	var safeStopLoss float64

	if direction == "long" {
		liquidationPrice = currentPrice * (1.0 - marginRate)
		// 止损必须高于强平价 + 安全缓冲
		safeStopLoss = liquidationPrice + (currentPrice-liquidationPrice)*LiquidationSafetyRatio

		if stopLoss < safeStopLoss {
			distanceToLiq := (stopLoss - liquidationPrice) / currentPrice * 100
			safeDistance := (safeStopLoss - liquidationPrice) / currentPrice * 100
			return fmt.Errorf("止损%.4f离强平价%.4f过近（实际%.2f%% < 安全要求%.2f%%）",
				stopLoss, liquidationPrice, distanceToLiq, safeDistance)
		}
	} else { // short
		liquidationPrice = currentPrice * (1.0 + marginRate)
		// 止损必须低于强平价 - 安全缓冲
		safeStopLoss = liquidationPrice - (liquidationPrice-currentPrice)*LiquidationSafetyRatio

		if stopLoss > safeStopLoss {
			distanceToLiq := (liquidationPrice - stopLoss) / currentPrice * 100
			safeDistance := (liquidationPrice - safeStopLoss) / currentPrice * 100
			return fmt.Errorf("止损%.4f离强平价%.4f过近（实际%.2f%% < 安全要求%.2f%%）",
				stopLoss, liquidationPrice, distanceToLiq, safeDistance)
		}
	}

	// ✅ 所有检查通过
	log.Printf("✅ [%s] 风控验证通过: 止损%.1fx ATR | 止盈%.1fx ATR | R/R=%.2f:1 | 强平价安全距离OK",
		symbol, stopMultiple, tpMultiple, riskReward)

	return nil
}

// ==================== 入场时机验证 ====================

// validateEntryTiming 验证入场时机，防止追涨杀跌
// 这是硬约束层，不依赖AI prompt，在决策执行前强制检查
func validateEntryTiming(direction string, md *market.Data) error {
	if md == nil {
		return fmt.Errorf("市场数据为空")
	}

	symbol := md.Symbol
	price := md.CurrentPrice
	rsi7 := md.CurrentRSI7
	rsi14 := md.CurrentRSI14
	change15m := md.PriceChange15m
	change1h := md.PriceChange1h
	ema20 := md.CurrentEMA20

	// 计算价格偏离EMA20的幅度
	var deviationFromEMA float64
	if ema20 > 0 {
		deviationFromEMA = (price - ema20) / ema20 * 100
	}

	// ============ 做多入场时机检查 ============
	if direction == "long" {
		// 1. 禁止在严重超买区做多（追高风险极大）
		if rsi7 > 75 {
			return fmt.Errorf("[%s] 🚫 禁止追高：RSI7=%.1f >75（严重超买，等待回调）", symbol, rsi7)
		}
		if rsi14 > 70 {
			return fmt.Errorf("[%s] 🚫 禁止追高：RSI14=%.1f >70（超买区域，等待回调）", symbol, rsi14)
		}

		// 2. 禁止在短期暴涨后做多
		if change15m > 3.0 {
			return fmt.Errorf("[%s] 🚫 禁止追高：15分钟涨幅%.2f%% >3%%（短期暴涨，等待回调）", symbol, change15m)
		}
		if change1h > 5.0 {
			return fmt.Errorf("[%s] 🚫 禁止追高：1小时涨幅%.2f%% >5%%（涨幅过大，等待回调）", symbol, change1h)
		}

		// 3. 禁止在价格远高于EMA20时做多（偏离均线过远）
		if deviationFromEMA > 4.0 {
			return fmt.Errorf("[%s] 🚫 禁止追高：价格偏离EMA20 +%.2f%% >4%%（偏离过远，等待回踩）", symbol, deviationFromEMA)
		}

		// ✅ 理想做多区域：RSI 30-60，小幅回调或横盘后启动
		if rsi7 >= 30 && rsi7 <= 60 && change15m < 2.0 && deviationFromEMA < 3.0 {
			log.Printf("✅ [%s] 入场时机良好：RSI7=%.1f, 15m涨幅%.2f%%, 偏离EMA20=%.2f%%",
				symbol, rsi7, change15m, deviationFromEMA)
		}
	}

	// ============ 做空入场时机检查 ============
	if direction == "short" {
		// 1. 禁止在严重超卖区做空（杀跌风险极大）
		if rsi7 < 25 {
			return fmt.Errorf("[%s] 🚫 禁止杀跌：RSI7=%.1f <25（严重超卖，可能反弹）", symbol, rsi7)
		}
		if rsi14 < 30 {
			return fmt.Errorf("[%s] 🚫 禁止杀跌：RSI14=%.1f <30（超卖区域，可能反弹）", symbol, rsi14)
		}

		// 2. 禁止在短期暴跌后做空
		if change15m < -3.0 {
			return fmt.Errorf("[%s] 🚫 禁止杀跌：15分钟跌幅%.2f%% <-3%%（短期暴跌，可能反弹）", symbol, change15m)
		}
		if change1h < -5.0 {
			return fmt.Errorf("[%s] 🚫 禁止杀跌：1小时跌幅%.2f%% <-5%%（跌幅过大，可能反弹）", symbol, change1h)
		}

		// 3. 禁止在价格远低于EMA20时做空（偏离均线过远）
		if deviationFromEMA < -4.0 {
			return fmt.Errorf("[%s] 🚫 禁止杀跌：价格偏离EMA20 %.2f%% <-4%%（偏离过远，可能反抽）", symbol, deviationFromEMA)
		}

		// ✅ 理想做空区域：RSI 40-70，小幅反弹或横盘后下跌
		if rsi7 >= 40 && rsi7 <= 70 && change15m > -2.0 && deviationFromEMA > -3.0 {
			log.Printf("✅ [%s] 入场时机良好：RSI7=%.1f, 15m跌幅%.2f%%, 偏离EMA20=%.2f%%",
				symbol, rsi7, change15m, deviationFromEMA)
		}
	}

	return nil
}

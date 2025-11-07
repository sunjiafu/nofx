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

	// 🚨 新增：提取夏普比率进行自适应风控
	sharpeRatio, hasSharpe := getSharpeFromPerformance(ctx.Performance)
	minProbability := 0.70   // 默认概率阈值70%
	allowMediumConf := false // 默认不允许medium置信度

	if !hasSharpe {
		cotBuilder.WriteString("## 📊 绩效记忆\n\n无历史绩效，使用默认阈值 (概率≥70%, 置信度high)\n\n")
	} else if sharpeRatio < -0.5 {
		// 🛑 熔断：夏普比率严重为负，停止开仓
		minProbability = 1.01 // 不可能达到的阈值
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (<-0.5) → 🛑 **熔断保护** (停止开仓)\n\n", sharpeRatio))
	} else if sharpeRatio < -0.3 {
		// 🚨 极度严格：夏普严重为负，大幅提高阈值
		minProbability = 0.80 // 提高到80%
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (<-0.3) → 🚨 **极度严格** (概率≥80%%)\n\n", sharpeRatio))
	} else if sharpeRatio < -0.1 {
		// ⚠️ 较严格：夏普轻微为负，适度提高阈值
		minProbability = 0.75 // 提高到75%
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (-0.3~-0.1) → ⚠️  **较严格** (概率≥75%%)\n\n", sharpeRatio))
	} else if sharpeRatio < 0 {
		// ✅ 正常：夏普接近零，保持正常阈值
		minProbability = 0.70
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (-0.1~0) → ✅ **正常运行** (概率≥70%%)\n\n", sharpeRatio))
	} else if sharpeRatio < 0.5 {
		// ✅ 正常：夏普轻微为正，正常阈值
		minProbability = 0.70
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (0-0.5) → ✅ **正常运行** (概率≥70%%)\n\n", sharpeRatio))
	} else if sharpeRatio < 0.7 {
		// ✅ 正常：夏普轻微为正，正常阈值
		minProbability = 0.70
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (0.5-0.7) → ✅ **正常运行** (概率≥70%%)\n\n", sharpeRatio))
	} else {
		// 🚀 宽松：夏普优异，降低阈值
		minProbability = 0.65  // 降低到65%
		allowMediumConf = true // 允许medium置信度
		cotBuilder.WriteString(fmt.Sprintf("## 📊 绩效记忆\n\n夏普=%.2f (>0.7) → 🚀 **优异表现** (概率≥65%%, 允许medium)\n\n", sharpeRatio))
	}

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

	// STEP 2: 持仓管理（基于预测）
	cotBuilder.WriteString("## STEP 2: 持仓管理（基于AI预测）\n\n")

	if len(ctx.Positions) > 0 {
		for _, pos := range ctx.Positions {
			marketData, hasData := ctx.MarketDataMap[pos.Symbol]
			if !hasData {
				log.Printf("⚠️  持仓%s缺少市场数据，跳过", pos.Symbol)
				continue
			}

			// 获取扩展数据
			extendedData, _ := market.GetExtendedData(pos.Symbol)

			// 获取历史预测表现
			predTracker := tracker.NewPredictionTracker("./prediction_logs")
			historicalPerf := predTracker.GetPerformance(pos.Symbol)

			// AI预测该持仓币种的未来走势（包含账户上下文）
			predCtx := &PredictionContext{
				Intelligence:   intelligence,
				MarketData:     marketData,
				ExtendedData:   extendedData,
				HistoricalPerf: historicalPerf,
				SharpeRatio:    sharpeRatio,
				Account:        &ctx.Account,  // 传入账户信息（用于整体风险评估）
				Positions:      ctx.Positions, // 传入当前所有持仓（用于避免冲突）
			}

			prediction, err := o.predictionAgent.Predict(predCtx)
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

			// 获取扩展数据
			extendedData, _ := market.GetExtendedData(coin.Symbol)

			// 获取历史表现
			historicalPerf := predTracker.GetPerformance(coin.Symbol)

			// 构建预测上下文（包含账户和持仓信息）
			predCtx := &PredictionContext{
				Intelligence:   intelligence,
				MarketData:     marketData,
				ExtendedData:   extendedData,
				HistoricalPerf: historicalPerf,
				SharpeRatio:    sharpeRatio,
				Account:        &ctx.Account,  // 传入账户信息
				Positions:      ctx.Positions, // 传入当前所有持仓
			}

			prediction, err := o.predictionAgent.Predict(predCtx)
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

			// 【新增】质量评估：过滤低质量预测
			isValidQuality, qualityScore, qualityReason := evaluatePredictionQuality(prediction)
			if !isValidQuality {
				cotBuilder.WriteString(fmt.Sprintf("  × 质量不合格: %s (评分: %d/100)\n\n", qualityReason, qualityScore))
				continue
			}
			cotBuilder.WriteString(fmt.Sprintf("  ✓ 质量检查通过 (评分: %d/100)\n", qualityScore))

			// 判断是否值得开仓
			// 条件：1) 概率满足动态阈值 2) 置信度满足要求 3) 方向明确
			meetsConfidence := prediction.Confidence == "very_high" || prediction.Confidence == "high"
			if prediction.Confidence == "medium" && (allowMediumConf || prediction.Probability >= minProbability+0.03) {
				meetsConfidence = true
			}
			if prediction.Confidence == "low" && prediction.Probability >= minProbability+0.07 {
				meetsConfidence = true
			}

			if prediction.Probability >= minProbability && meetsConfidence && prediction.Direction != "neutral" {
				cotBuilder.WriteString(fmt.Sprintf("  ✓ 满足开仓条件（概率%.0f%% >= %.0f%% 且 置信度%s）\n\n",
					prediction.Probability*100, minProbability*100, prediction.Confidence))

				validPredictions = append(validPredictions, struct {
					symbol     string
					prediction *types.Prediction
				}{coin.Symbol, prediction})

				// 记录预测
				if err := predTracker.Record(prediction, marketData.CurrentPrice); err != nil {
					log.Printf("⚠️  记录预测失败: %v", err)
				}
			} else {
				// 详细说明不满足的原因
				if prediction.Direction == "neutral" {
					cotBuilder.WriteString(fmt.Sprintf("  × 方向neutral，不开仓\n\n"))
				} else if prediction.Probability < minProbability {
					cotBuilder.WriteString(fmt.Sprintf("  × 概率%.0f%% < 阈值%.0f%% (夏普调整)\n\n",
						prediction.Probability*100, minProbability*100))
				} else if !meetsConfidence {
					mediumNeed := (minProbability + 0.03) * 100
					lowNeed := (minProbability + 0.07) * 100
					if allowMediumConf {
						cotBuilder.WriteString(fmt.Sprintf("  × 置信度%s不满足要求 (需要high/very_high或medium)\n\n", prediction.Confidence))
					} else {
						cotBuilder.WriteString(fmt.Sprintf("  × 置信度%s不满足要求 (high/very_high；medium≥%.0f%%；low≥%.0f%%)\n\n",
							prediction.Confidence, mediumNeed, lowNeed))
					}
				}
			}
		}

		// STEP 4: 风险计算（基于AI预测的期望值）
		if len(validPredictions) > 0 {
			cotBuilder.WriteString("## STEP 4: 风险计算与仓位分配\n\n")

			opened := 0
			for _, vp := range validPredictions {
				if opened >= availableSlots {
					cotBuilder.WriteString("⚠️  可开仓数量已耗尽\n")
					break
				}

				marketData := ctx.MarketDataMap[vp.symbol]

				// 使用预测计算仓位（基于凯利公式的简化版本）
				positionSize, leverage, stopLoss, takeProfit, err := o.calculatePositionFromPrediction(
					vp.prediction, marketData, ctx.Account.TotalEquity, ctx.Account.AvailableBalance)

				if err != nil {
					cotBuilder.WriteString(fmt.Sprintf("**%s**: 风险计算失败 - %v\n\n", vp.symbol, err))
					continue
				}

				cotBuilder.WriteString(fmt.Sprintf("**%s**:\n", vp.symbol))
				cotBuilder.WriteString(fmt.Sprintf("  仓位: %.0f USDT | 杠杆: %dx\n", positionSize, leverage))
				cotBuilder.WriteString(fmt.Sprintf("  止损: %.4f | 止盈: %.4f\n", stopLoss, takeProfit))
				cotBuilder.WriteString(fmt.Sprintf("  期望收益: %+.2f%% | 最大风险: %+.2f%%\n\n",
					vp.prediction.BestCase, vp.prediction.WorstCase))

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
	// shouldClosePosition 判断是否应该平仓（基于AI预测 + 增强止盈策略）

	// 🔧 最小持仓时间保护：防止频繁开平仓
	if pos.UpdateTime > 0 {
		holdingMinutes := float64(time.Now().UnixMilli()-pos.UpdateTime) / 60000.0
		if holdingMinutes < 15 {
			// 持仓时间<15分钟，给予"呼吸空间"，不因方向变化平仓
			log.Printf("🛡️  [持仓保护] %s %s 持仓仅%.1f分钟，暂不因预测变化平仓",
				pos.Symbol, pos.Side, holdingMinutes)
			// 但仍然检查止损等其他条件
		} else {
			// 1. 如果预测方向与持仓方向完全相反，且概率≥80% → 平仓（提高到80%，防止噪音）
			if pos.Side == "long" && prediction.Direction == "down" && prediction.Probability >= 0.80 {
				log.Printf("⚠️  [方向逆转平仓] %s LONG | AI预测DOWN 概率%.0f%% ≥ 80%%",
					pos.Symbol, prediction.Probability*100)
				return true
			}
			if pos.Side == "short" && prediction.Direction == "up" && prediction.Probability >= 0.80 {
				log.Printf("⚠️  [方向逆转平仓] %s SHORT | AI预测UP 概率%.0f%% ≥ 80%%",
					pos.Symbol, prediction.Probability*100)
				return true
			}
		}
	}

	// 2. 如果已经亏损>10% → 止损
	if pos.UnrealizedPnLPct < -10.0 {
		return true
	}

	// 【新增止盈策略】根据盈利百分比主动止盈
	profitPct := pos.UnrealizedPnLPct

	// 3. 大盈利直接止盈（盈利≥8%）
	if profitPct >= 8.0 {
		log.Printf("🎯 [触发大盈利止盈] %s %s | 盈利%.2f%% ≥ 8%%", pos.Symbol, pos.Side, profitPct)
		return true
	}

	// 4. 中等盈利 + AI预测转中性（盈利≥3% 且 方向neutral）
	if profitPct >= 3.0 && prediction.Direction == "neutral" {
		log.Printf("🎯 [触发预测转中性止盈] %s %s | 盈利%.2f%%, AI转neutral", pos.Symbol, pos.Side, profitPct)
		return true
	}

	// 5. 小盈利 + 高风险预测（盈利≥2% 且 风险very_high）
	if profitPct >= 2.0 && prediction.RiskLevel == "very_high" {
		log.Printf("🎯 [触发风险升高止盈] %s %s | 盈利%.2f%%, 风险变为very_high", pos.Symbol, pos.Side, profitPct)
		return true
	}

	// 6. 持仓时间过长止盈（盈利≥2% 且 持仓>4小时）
	if profitPct >= 2.0 && pos.UpdateTime > 0 {
		holdingMinutes := float64(time.Now().UnixMilli()-pos.UpdateTime) / 60000.0
		if holdingMinutes > 240 { // 4小时 = 240分钟
			log.Printf("🎯 [触发长期持仓止盈] %s %s | 盈利%.2f%%, 持仓%.0f分钟", pos.Symbol, pos.Side, profitPct, holdingMinutes)
			return true
		}
	}

	// 7. 原有的大盈利+预测中性止盈（保留，作为兜底）
	if profitPct > 20.0 && prediction.Direction == "neutral" {
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

	// 基于概率和风险计算仓位（简化的凯利公式）
	// f* = (p*b - q) / b
	// p = 胜率, q = 败率, b = 盈亏比

	winRate := prediction.Probability
	loseRate := 1 - prediction.Probability
	confidenceMultiplier := confidencePositionMultiplier(prediction.Confidence)

	// 🔧 关键修复：根据方向正确计算盈亏比
	// AI预测的 best_case/worst_case 是价格变化百分比
	// 需要转换为持仓盈亏比
	var payoffRatio float64

	if prediction.Direction == "down" {
		// 做空时：价格下跌是盈利（worst_case），价格上涨是亏损（best_case）
		// 盈亏比 = |worst_case| / best_case
		if prediction.BestCase < 1e-6 {
			return 0, 0, 0, 0, fmt.Errorf("做空时best_case(%.2f)过小，无法计算盈亏比", prediction.BestCase)
		}
		payoffRatio = math.Abs(prediction.WorstCase) / prediction.BestCase

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

	// 使用3/4凯利 - 较激进但可控，结合AI置信度
	conservativeKelly := kellyFraction * 0.75 * confidenceMultiplier

	// 【优化】动态上限：小资金更激进
	var maxKellyFraction float64
	if totalEquity < 500 {
		maxKellyFraction = 0.60  // 小资金（<500 USDT）：最多60%
		log.Printf("🔹 小资金模式：上限60%")
	} else if totalEquity < 2000 {
		maxKellyFraction = 0.50  // 中资金（500-2000 USDT）：最多50%
	} else {
		maxKellyFraction = 0.40  // 大资金（>2000 USDT）：最多40%
	}

	if conservativeKelly > maxKellyFraction {
		conservativeKelly = maxKellyFraction
	}

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

	log.Printf("🧮 仓位评估 %s: prob=%.2f conf=%s multiplier=%.2f kelly=%.3f size=%.2f",
		prediction.Symbol, prediction.Probability, prediction.Confidence, confidenceMultiplier, conservativeKelly, positionSize)

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
		stopLoss = currentPrice * (1 + prediction.WorstCase/100.0)  // 最坏情况
		takeProfit = currentPrice * (1 + prediction.BestCase/100.0) // 最好情况
	} else {
		// 做空
		stopLoss = currentPrice * (1 - prediction.WorstCase/100.0)  // 最坏情况
		takeProfit = currentPrice * (1 - prediction.BestCase/100.0) // 最好情况
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

func confidencePositionMultiplier(confidence string) float64 {
	switch confidence {
	case "very_high":
		return 1.3
	case "high":
		return 1.1
	case "medium":
		return 1.0
	case "low":
		return 0.75
	default:
		return 0.5
	}
}

// evaluatePredictionQuality 评估预测质量（过滤低质量预测）
func evaluatePredictionQuality(prediction *types.Prediction) (isValid bool, score int, reason string) {
	score = 0

	// 1. 预期收益检查（40分）- 至少要值得交易
	absExpectedMove := prediction.ExpectedMove
	if absExpectedMove < 0 {
		absExpectedMove = -absExpectedMove
	}

	if absExpectedMove >= 3.0 {
		score += 40
	} else if absExpectedMove >= 2.0 {
		score += 30
	} else if absExpectedMove >= 1.0 {
		score += 20
	} else if absExpectedMove >= 0.5 {
		score += 10
	} else {
		// 预期收益太小，直接拒绝
		return false, score, fmt.Sprintf("预期收益太小(%.2f%%), 不值得交易（至少需要0.5%%）", absExpectedMove)
	}

	// 2. 风险回报比检查（30分）- 🔧 修复：根据方向正确计算
	var potentialProfit, potentialLoss, rr float64

	if prediction.Direction == "down" {
		// 做空：价格下跌盈利，价格上涨亏损
		potentialProfit = math.Abs(prediction.WorstCase)  // 价格最大跌幅 = 最大盈利
		potentialLoss = math.Abs(prediction.BestCase)     // 价格最大涨幅 = 最大亏损

		if potentialLoss < 0.01 {
			return false, score, "做空时best_case接近0，无法计算风险回报比"
		}
		rr = potentialProfit / potentialLoss

	} else if prediction.Direction == "up" {
		// 做多：价格上涨盈利，价格下跌亏损
		potentialProfit = math.Abs(prediction.BestCase)   // 价格最大涨幅 = 最大盈利
		potentialLoss = math.Abs(prediction.WorstCase)    // 价格最大跌幅 = 最大亏损

		if potentialLoss < 0.01 {
			return false, score, "做多时worst_case接近0，无法计算风险回报比"
		}
		rr = potentialProfit / potentialLoss

	} else {
		// neutral方向不评估风险回报比
		rr = 0
	}

	if rr >= 2.0 {
		score += 30
	} else if rr >= 1.5 {
		score += 20
	} else if rr >= 1.0 {
		score += 10
	}
	// rr < 1.0 不加分，但不直接拒绝

	// 3. 置信度检查（30分）
	switch prediction.Confidence {
	case "very_high":
		score += 30
	case "high":
		score += 25
	case "medium":
		score += 15
	case "low":
		score += 5
	default:
		score += 0
	}

	// 4. 判断是否合格（60分及格）
	if score >= 60 {
		return true, score, fmt.Sprintf("质量评分: %d/100 (及格)", score)
	} else {
		return false, score, fmt.Sprintf("质量评分: %d/100 (不及格，需要≥60分)", score)
	}
}

package agents

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"nofx/decision/types"
	"nofx/market"
	"nofx/mcp"
	"strings"
	"time"
)

// PredictionAgent AI预测引擎（核心）
// 负责基于市场情报预测未来价格走势
type PredictionAgent struct {
	mcpClient *mcp.Client
}

// NewPredictionAgent 创建预测Agent
func NewPredictionAgent(mcpClient *mcp.Client) *PredictionAgent {
	return &PredictionAgent{
		mcpClient: mcpClient,
	}
}

// PredictionContext 预测上下文（包含历史表现）
type PredictionContext struct {
	Intelligence   *MarketIntelligence
	MarketData     *market.Data
	ExtendedData   *market.ExtendedData         // 🆕 扩展市场数据（情绪/清算/OI变化）
	HistoricalPerf *types.HistoricalPerformance // 历史预测表现
	SharpeRatio    float64                      // 系统近期夏普（用于概率校准）
	Account        *AccountInfo                 // 账户上下文
	Positions      []PositionInfoInput          // 当前持仓列表
	RecentFeedback string                       // tracker生成的近期反馈
	TraderMemory   string                       // 🧠 交易员记忆（实际交易经验）
}

// Predict 预测币种未来走势
func (agent *PredictionAgent) Predict(ctx *PredictionContext) (*types.Prediction, error) {
	if err := agent.validateMarketData(ctx); err != nil {
		return nil, fmt.Errorf("数据验证失败: %w", err)
	}

	systemPrompt, userPrompt := agent.buildPredictionPrompt(ctx)

	response, err := agent.mcpClient.CallWithMessages(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("AI调用失败: %w", err)
	}

	// 解析AI响应
	prediction := &types.Prediction{}
	jsonData := extractJSON(response)
	if jsonData == "" {
		// 打印原始响应以调试DeepSeek R1
		log.Printf("⚠️  无法提取JSON，原始响应前800字符:\n%s", truncateString(response, 800))
		log.Printf("⚠️  原始响应长度: %d字符", len(response))
		return nil, fmt.Errorf("无法从响应中提取JSON")
	}

	log.Printf("🔍 AI原始预测JSON: %s", jsonData)

	if err := json.Unmarshal([]byte(jsonData), prediction); err != nil {
		return nil, fmt.Errorf("JSON解析失败: %w\nJSON: %s", err, jsonData)
	}

	normalizePrediction(prediction)
	agent.calibrateProbability(prediction, ctx)
	if prediction.Timeframe == "" {
		prediction.Timeframe = agent.selectTimeframe(ctx.MarketData)
	}

	// 验证预测结果
	if err := agent.validatePrediction(prediction); err != nil {
		return nil, fmt.Errorf("预测验证失败: %w", err)
	}
	if err := agent.validatePredictionEnhanced(prediction, ctx.MarketData); err != nil {
		return nil, fmt.Errorf("预测验证失败: %w", err)
	}

	return prediction, nil
}

// PredictWithRetry 对AI预测增加重试机制，提高稳定性
func (agent *PredictionAgent) PredictWithRetry(ctx *PredictionContext, maxRetries int) (*types.Prediction, error) {
	if maxRetries <= 0 {
		maxRetries = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		prediction, err := agent.Predict(ctx)
		if err == nil {
			return prediction, nil
		}
		lastErr = err
		log.Printf("⚠️  AI预测失败(第%d次尝试/%d): %v", attempt, maxRetries, err)
		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	return nil, fmt.Errorf("AI预测多次失败: %w", lastErr)
}

func normalizePrediction(pred *types.Prediction) {
	pred.Direction = normalizeEnum(pred.Direction, map[string]string{
		"up":      "up",
		"long":    "up",
		"bull":    "up",
		"down":    "down",
		"short":   "down",
		"bear":    "down",
		"neutral": "neutral",
	})

	pred.Timeframe = normalizeEnum(pred.Timeframe, map[string]string{
		"1h":  "1h",
		"1hr": "1h",
		"4h":  "4h",
		"4hr": "4h",
		"24h": "24h",
		"1d":  "24h",
	})

	pred.Confidence = normalizeEnum(pred.Confidence, map[string]string{
		"very_high": "very_high",
		"very high": "very_high",
		"very-high": "very_high",
		"high":      "high",
		"medium":    "medium",
		"moderate":  "medium",
		"mid":       "medium",
		"low":       "low",
		"very_low":  "very_low",
		"very low":  "very_low",
		"very-low":  "very_low",
	})

	pred.RiskLevel = normalizeEnum(pred.RiskLevel, map[string]string{
		"very_high": "very_high",
		"high":      "high",
		"medium":    "medium",
		"moderate":  "medium",
		"low":       "low",
		"very_low":  "very_low",
	})

	pred.Symbol = strings.ToUpper(pred.Symbol)
}

func normalizeEnum(value string, mapping map[string]string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if mapped, ok := mapping[value]; ok {
		return mapped
	}
	return value
}

// buildPredictionPrompt 构建预测Prompt（中文版 + 动态教训）
func (agent *PredictionAgent) buildPredictionPrompt(ctx *PredictionContext) (systemPrompt string, userPrompt string) {
	// 🆕 动态生成"最近错误教训"（基于实际表现）
	mistakesSection := agent.buildMistakesSection(ctx)

	systemPrompt = `你是一名专业的加密货币量化预测员，专为 BTC/ETH 预测短期走势（1h/4h/24h）。必须综合考虑【账户风险+持仓情况+技术指标】做出决策，并严格输出 JSON。

🌟 **心态指引**：
- 这是小资金测试账户，用于优化策略和积累经验
- 不要因历史亏损而过度悲观或恐惧，每次决策都是独立的
- 专注当前市场信号和机会，而非过度纠结过往失误
- 满足风控阈值且信号明确时，应果断行动而非观望

=====================
【0. 🎯 综合决策框架（核心优先级）】

⚠️ **决策优先级**（从高到低）：
1. 账户风险控制（累计盈亏、保证金占用）
2. 持仓状态分析（盈亏、持仓时长、方向）
3. 技术指标验证（趋势、动量、超买超卖）
4. 市场情绪参考（资金费率、OI变化、情绪指数）

✅ **必须遵守的决策逻辑**：
- 🎯 **风控阈值**：系统会在输入数据中明确告诉你"当前风控阈值"，你**必须严格遵守**，不得擅自修改或使用其他数值
- 🛑 账户风险红线：当系统告知禁止开仓时，必须输出neutral（prob=0.50-0.55）
- 🔒 持仓已满(3/3) → 新机会概率必须 > 0.80 才考虑替换
- 🛑 保证金占用 > 60% → 严禁新开仓，倾向neutral
- ⚠️ 保证金占用 > 40% → 降低预期收益(expected_move ≤ 2%)
- ✅ 持仓有大幅盈利(>5%) → 考虑建议部分止盈（在reasoning中提示）
- ⚠️ 单个持仓亏损 > 5% → 考虑止损（在reasoning中提示）

📊 **持仓方向冲突处理**：
- 已有多单且预测down → 如盈利>3%建议平仓，否则neutral观望
- 已有空单且预测up → 如盈利>3%建议平仓，否则neutral观望
- 持仓时长<4小时且盈亏不极端 → 倾向neutral继续持有

=====================
【1. 最近错误教训（自动注入）】
` + mistakesSection + `

=====================
【2. 技术分析原则（次要逻辑）】
- 技术指标权重：EMA/MACD/RSI/ADX = 50%（降低权重）
- 账户风险权重：持仓盈亏/保证金/风险等级 = 30%（新增）
- 情绪/资金费率/社交等占 20%
- 2~3 个关键指标一致 + 账户风险可控 → 输出 up/down（0.65–0.75）
- 信号轻微冲突或账户有风险 → 选neutral或降低概率到0.50-0.60
- 严格避免追涨/杀跌（BTC/ETH 专用规则见下方）

=====================
【3. 硬禁止规则（BTC/ETH 专用，触发即 neutral & prob=0.50）】

【做多禁止】
- RSI7 > 75 或 RSI14 > 75              # 过度超买 → 禁止追涨（与Entry Engine统一）
- 1h涨幅 > 4% 或 价格 > EMA20 + 3%     # 大阳线 + 偏离均线（BTC/ETH实际波动调整）
- atr% > 3.5 且 1h涨幅 > 3%             # 高波动+大单边拉升（降低阈值）
- -DI > +DI * 1.5                        # 空头力量明显占优（≥50%）
- ADX>25 且 p<EMA50 且 -DI>+DI           # 强下跌趋势中禁止抄底

【做空禁止】
- RSI7 < 35 或 RSI14 < 35              # 接近超卖 → 禁止杀跌（与Entry Engine统一）
- 1h跌幅 < -3% 且 价格 < EMA20 - 2%    # 大阴线 + 跌破均线（BTC/ETH实际波动调整）
- atr% > 3.5 且 1h跌幅 < -3%            # 高波动+大单边下跌（降低阈值）
- +DI > -DI * 1.5                        # 多头力量明显占优（≥50%）
- ADX>25 且 p>EMA50 且 +DI>-DI           # 强上涨趋势中禁止抄底做空

=====================
【4. 警告信号（限幅处理，适配 BTC/ETH）】
触发任意一条 → probability ≤ 0.65，expected_move ≤ ±2%：
【做多警告】
- RSI7 > 70 或 RSI14 > 68
- 1h涨幅 > 2%                            # 降低阈值以匹配实际波动
- p > EMA20 + 1.5%                       # 降低阈值以匹配实际波动

【做空警告】
- RSI7 < 35 或 RSI14 < 35
- 1h跌幅 < -2%                           # 降低阈值以匹配实际波动
- p < EMA20 - 1.5%                       # 降低阈值以匹配实际波动

同时触发 ≥2 条 → 倾向 neutral 或 probability=0.58~0.62

=====================
【5. 趋势结构（核心趋势判断）】
- 上升趋势：p>EMA20>EMA50 且 MACD>0 → UP（0.65~0.75）
- 下跌趋势：p<EMA20<EMA50 且 MACD<0 → DOWN（0.65~0.75）
- 横盘：ADX<20 → neutral 或偏向最强方（prob<0.62）

MACD：
- m>ms 且上升 → 金叉 → 看涨信号
- m<ms 且下降 → 死叉 → 看跌信号

ADX：
- ADX<20 → 震荡（不可信趋势）
- ADX>25 + 金叉 → 高质量趋势信号
- ADX下降 → 趋势疲软 → expected_move 应缩小

=====================
【6. 历史经验（交易记忆必须使用）】
推理必须包含：
- 当前账户风险状态（盈亏、保证金、持仓数量）
- 持仓情况对新决策的影响（方向冲突、盈亏状态）
- 当前市场是否类似过去盈利模式（提高概率）
- 是否接近过往亏损模式（降低概率）
- 如出现强烈相似 → 调整 probability ±0.03

⚠️ **推理格式要求**：
第1句：说明账户风险状态（如：账户浮亏-3.2%，风险偏高）
第2-3句：技术分析（趋势、指标、信号）
第4句：综合账户+技术的最终判断

=====================
【7. 概率 / 置信度规则】
- probability 范围：0.50–1.00
- neutral: 0.50–0.58
- up/down ≥ 0.58
- expected_move：±10% 以内
- confidence：high / medium / low
- timeframe：1h / 4h / 24h

若模型逻辑冲突 → 以"硬禁止"优先级最高，其次"趋势结构"，再次"警告信号"。

=====================
【8. 严格 JSON 输出（必须符合结构）】
仅输出以下 JSON，不要解释，不要多余文本：
{"symbol":"SYMBOL","direction":"up|down|neutral","probability":0.65,"expected_move":2.5,"timeframe":"1h|4h|24h","confidence":"high|medium|low","reasoning":"中文推理<150字","key_factors":["因素1","因素2","因素3"],"risk_level":"high|medium|low","worst_case":-1.5,"best_case":3.5}

数据字段说明:
- p:价格 | 1h/4h/24h:涨跌幅% | r7/r14:RSI指标
- m:MACD值 | ms:MACD信号线 | e20/e50:EMA均线 | atr%:波动率百分比
- adx:趋势强度 | +di/-di:多空力量 | vol24h:24h成交额(百万USDT)
- f:资金费率 | oiΔ4h/24h:持仓量变化% | fgi:恐慌贪婪指数 | social:社交情绪`

	return systemPrompt, agent.buildUserPrompt(ctx)
}

func (agent *PredictionAgent) buildUserPrompt(ctx *PredictionContext) string {
	var sb strings.Builder

	sb.WriteString("# 市场背景\n")
	if ctx != nil && ctx.Intelligence != nil {
		sb.WriteString(fmt.Sprintf("阶段: %s\n", ctx.Intelligence.MarketPhase))
		if ctx.Intelligence.Summary != "" {
			sb.WriteString(fmt.Sprintf("综述: %s\n", ctx.Intelligence.Summary))
		}
		if len(ctx.Intelligence.KeyRisks) > 0 {
			sb.WriteString(fmt.Sprintf("风险: %s\n", strings.Join(ctx.Intelligence.KeyRisks, " | ")))
		}
		if len(ctx.Intelligence.KeyOpportunities) > 0 {
			sb.WriteString(fmt.Sprintf("机会: %s\n", strings.Join(ctx.Intelligence.KeyOpportunities, " | ")))
		}
	}

	recommendedTF := agent.selectTimeframe(ctx.MarketData)
	sb.WriteString(fmt.Sprintf("推荐时间框架: %s\n", recommendedTF))

	if ctx != nil && ctx.MarketData != nil {
		md := ctx.MarketData
		sb.WriteString(fmt.Sprintf("\n# %s\n", md.Symbol))
		// 🆕 方案C：全面增强数据维度（+120 tokens）
		compactData := make(map[string]interface{})

		// === 基础数据（原有11个维度）===
		compactData["p"] = md.CurrentPrice
		compactData["1h"] = md.PriceChange1h
		compactData["4h"] = md.PriceChange4h
		compactData["r7"] = md.CurrentRSI7   // 改名区分
		compactData["m"] = md.CurrentMACD
		compactData["f"] = md.FundingRate

		if md.LongerTermContext != nil {
			ltc := md.LongerTermContext
			compactData["e20"] = ltc.EMA20
			compactData["e50"] = ltc.EMA50
			if md.CurrentPrice > 0 && ltc.ATR14 > 0 {
				compactData["atr%"] = (ltc.ATR14 / md.CurrentPrice) * 100
			}
			if ltc.AverageVolume > 0 && ltc.CurrentVolume > 0 {
				compactData["vol%"] = (ltc.CurrentVolume/ltc.AverageVolume - 1) * 100
			}
		}

		// === 方案A维度（+40 tokens）===
		compactData["24h"] = md.PriceChange24h  // 🆕 24h涨跌幅
		compactData["r14"] = md.CurrentRSI14    // 🆕 RSI14
		compactData["ms"] = md.MACDSignal       // 🆕 MACD Signal线
		if md.Volume24h > 0 {
			compactData["vol24h"] = md.Volume24h / 1e6 // 🆕 24h成交额(M USDT)
		}
		// 🆕 ADX趋势强度指标
		if md.CurrentADX > 0 {
			compactData["adx"] = md.CurrentADX // 🆕 趋势强度(0-100)
			if md.CurrentPlusDI > 0 || md.CurrentMinusDI > 0 {
				compactData["+di"] = md.CurrentPlusDI  // 🆕 多头力量
				compactData["-di"] = md.CurrentMinusDI // 🆕 空头力量
			}
		}

		// === 方案B维度（+30 tokens）===
		if md.LongerTermContext != nil {
			ltc := md.LongerTermContext
			compactData["atr14"] = ltc.ATR14 // 🆕 ATR14绝对值（止损距离参考）

			// 🆕 OI变化率（从ExtendedData获取）
			if ctx.ExtendedData != nil && ctx.ExtendedData.Derivatives != nil {
				d := ctx.ExtendedData.Derivatives
				if d.OIChange4h != 0 {
					compactData["oiΔ4h"] = d.OIChange4h
				}
				if d.OIChange24h != 0 {
					compactData["oiΔ24h"] = d.OIChange24h
				}
			}
		}

		// === 方案C维度（+50 tokens）===
		if ctx.ExtendedData != nil {
			// 🆕 恐慌贪婪指数
			if ctx.ExtendedData.Sentiment != nil {
				s := ctx.ExtendedData.Sentiment
				compactData["fgi"] = s.FearGreedIndex // Fear & Greed Index (0-100)
				if s.SocialSentiment != "neutral" {
					compactData["social"] = s.SocialSentiment // bullish/bearish
				}
			}

			// 🆕 清算密集区（如果可用）
			if ctx.ExtendedData.Liquidation != nil {
				liq := ctx.ExtendedData.Liquidation
				if len(liq.LongLiqZones) > 0 {
					// 只显示最近的清算区（避免token浪费）
					topZone := liq.LongLiqZones[0]
					compactData["liqL"] = fmt.Sprintf("%.0f@%.1fM", topZone.Price, topZone.Volume/1e6)
				}
				if len(liq.ShortLiqZones) > 0 {
					topZone := liq.ShortLiqZones[0]
					compactData["liqS"] = fmt.Sprintf("%.0f@%.1fM", topZone.Price, topZone.Volume/1e6)
				}
			}

			// 🆕 资金费率趋势
			if ctx.ExtendedData.Derivatives != nil {
				d := ctx.ExtendedData.Derivatives
				if d.FundingRateTrend != "stable" {
					compactData["fTrend"] = d.FundingRateTrend // increasing/decreasing
				}
			}
		}

		if jsonBytes, err := json.Marshal(compactData); err == nil {
			sb.WriteString(string(jsonBytes))
			sb.WriteString("\n")
			// 🔍 临时调试：打印完整数据（验证Plan C）
			log.Printf("🔍 [Plan C] %s: %s", md.Symbol, string(jsonBytes))
		}
	}

	// 🆕 账户-持仓-风险综合分析（核心优化）
	if ctx != nil && ctx.Account != nil {
		sb.WriteString("\n# 💰 账户风险全景\n")

		// 1️⃣ 账户基本信息
		sb.WriteString(fmt.Sprintf("账户净值: %.2f USDT | 可用余额: %.2f USDT (%.1f%%)\n",
			ctx.Account.TotalEquity,
			ctx.Account.AvailableBalance,
			(ctx.Account.AvailableBalance/ctx.Account.TotalEquity)*100))

		// 2️⃣ 风险指标
		sb.WriteString(fmt.Sprintf("保证金占用: %.1f%% | ", ctx.Account.MarginUsedPct))

		// 🔧 使用账户总体盈亏（已实现+未实现）
		accountTotalPnL := ctx.Account.TotalPnL
		accountTotalPnLPct := ctx.Account.TotalPnLPct

		// 计算当前持仓浮动盈亏（仅用于显示）
		totalUnrealizedPnL := 0.0
		totalUnrealizedPnLPct := 0.0
		if len(ctx.Positions) > 0 {
			for _, pos := range ctx.Positions {
				totalUnrealizedPnL += pos.UnrealizedPnL
			}
			totalUnrealizedPnLPct = (totalUnrealizedPnL / ctx.Account.TotalEquity) * 100
		}

		sb.WriteString(fmt.Sprintf("账户总盈亏: %+.2f USDT (%+.2f%%) | 持仓浮动: %+.2f USDT (%+.2f%%)\n",
			accountTotalPnL, accountTotalPnLPct, totalUnrealizedPnL, totalUnrealizedPnLPct))

		// 3️⃣ 风险等级评估
		riskLevel := "低"
		if ctx.Account.MarginUsedPct > 60 {
			riskLevel = "高"
		} else if ctx.Account.MarginUsedPct > 40 {
			riskLevel = "中"
		}
		sb.WriteString(fmt.Sprintf("风险等级: %s | ", riskLevel))

		if ctx.SharpeRatio != 0 {
			sb.WriteString(fmt.Sprintf("夏普比率: %.2f", ctx.SharpeRatio))
		}
		sb.WriteString("\n")

		// 4️⃣ 持仓详情（如果有）
		if len(ctx.Positions) > 0 {
			sb.WriteString(fmt.Sprintf("\n## 📊 当前持仓 (%d/3)\n", len(ctx.Positions)))
			for i, pos := range ctx.Positions {
				// 计算持仓时长
				holdingTime := ""
				if !pos.OpenTime.IsZero() {
					duration := time.Since(pos.OpenTime)
					hours := duration.Hours()
					if hours < 1 {
						holdingTime = fmt.Sprintf("%.0f分钟", duration.Minutes())
					} else if hours < 24 {
						holdingTime = fmt.Sprintf("%.1f小时", hours)
					} else {
						holdingTime = fmt.Sprintf("%.1f天", hours/24)
					}
				}

				// 计算盈亏状态标识
				pnlEmoji := "📈"
				if pos.UnrealizedPnLPct < -3 {
					pnlEmoji = "🔴" // 严重亏损
				} else if pos.UnrealizedPnLPct < 0 {
					pnlEmoji = "📉" // 轻微亏损
				} else if pos.UnrealizedPnLPct > 5 {
					pnlEmoji = "🟢" // 大幅盈利
				}

				sb.WriteString(fmt.Sprintf("[%d] %s %s %s | ",
					i+1, pos.Symbol, strings.ToUpper(pos.Side), pnlEmoji))
				sb.WriteString(fmt.Sprintf("入场:%.2f → 当前:%.2f | ",
					pos.EntryPrice, pos.MarkPrice))
				sb.WriteString(fmt.Sprintf("盈亏:%+.2f%% (%+.2f USDT) | ",
					pos.UnrealizedPnLPct, pos.UnrealizedPnL))
				sb.WriteString(fmt.Sprintf("杠杆:%dx | 持仓:%s\n",
					pos.Leverage, holdingTime))
			}

			// 根据持仓数量给出建议（保留在if块内）
			if len(ctx.Positions) >= 3 {
				sb.WriteString("\n- 🔒 持仓已满(3/3)，新机会必须 > 80% 概率才考虑替换最弱持仓\n")
			} else if len(ctx.Positions) >= 2 {
				sb.WriteString(fmt.Sprintf("\n- 📌 已有%d个持仓，剩余1个槽位，新机会需谨慎评估\n", len(ctx.Positions)))
			}
		} else {
			sb.WriteString("\n## 📊 当前持仓: 无\n")
			sb.WriteString("✅ 可自由开仓，建议首仓使用较低杠杆测试市场\n")
		}

		// 5️⃣ 账户风控提示（基于账户总体盈亏）- 🔧 修复：移到if-else外部，确保无论是否有持仓都显示
		// 🎯 首先，明确显示当前所需的最低概率阈值
		var requiredMinProb float64
		var riskStatus string
		if accountTotalPnLPct < -20 {
			requiredMinProb = 1.01 // 禁止开仓
			riskStatus = "🛑 严格禁止"
		} else if accountTotalPnLPct < -15 {
			requiredMinProb = 0.75 // 降低阈值，给AI更多机会
			riskStatus = "⚠️ 谨慎交易"
		} else if accountTotalPnLPct < -10 {
			requiredMinProb = 0.70
			riskStatus = "💡 适度谨慎"
		} else if accountTotalPnLPct < -5 {
			requiredMinProb = 0.68
			riskStatus = "✅ 正常偏谨慎"
		} else {
			requiredMinProb = 0.65
			riskStatus = "✅ 正常"
		}

		// 🐛 调试日志：输出实际的亏损百分比和计算出的阈值
		log.Printf("🔍 [风控阈值调试] 币种:%s 账户累计亏损:%.2f%% 计算阈值:%.0f%% 状态:%s",
			ctx.MarketData.Symbol, accountTotalPnLPct, requiredMinProb*100, riskStatus)

		// 🎯 最重要：在最显眼的位置告诉AI当前阈值
		sb.WriteString("\n## 🎯 当前风控阈值（必须满足）\n")
		if requiredMinProb > 1.0 {
			sb.WriteString(fmt.Sprintf("状态: %s | 账户累计亏损: %.2f%%\n", riskStatus, accountTotalPnLPct))
			sb.WriteString("**⛔ 严格禁止新开仓，必须输出 neutral（概率 0.50-0.55）**\n")
		} else {
			sb.WriteString(fmt.Sprintf("**📢 当前风控状态：%s | 账户亏损 %.2f%% | 最低概率阈值：%.0f%%**\n",
				riskStatus, accountTotalPnLPct, requiredMinProb*100))
			sb.WriteString(fmt.Sprintf("**⚠️ 你不得擅自修改此阈值！概率 < %.0f%% 的预测将被系统强制拒绝！**\n\n", requiredMinProb*100))

			// 🌟 添加积极提示
			sb.WriteString("💡 **重要提醒**：\n")
			sb.WriteString("- 这是**小资金测试账户**，目的是优化策略和积累经验\n")
			sb.WriteString("- 不要因历史亏损而过度悲观，每次决策都是独立的新机会\n")
			sb.WriteString("- 关注**当前技术信号**和市场机会，而非过度纠结历史表现\n")
			sb.WriteString("- 符合概率阈值且技术信号明确时，应该**果断行动**而非观望\n")
		}

		sb.WriteString("\n⚠️ 决策要求:\n")

		// 🔧 根据账户总体盈亏给出强制约束（不是持仓浮动盈亏）
		// 💡 使用前面计算的动态阈值，避免与实际风控不一致
		if accountTotalPnLPct < -20 {
			sb.WriteString("- 🛑 账户累计亏损 > 20%，**严格禁止**新开仓，必须输出neutral（概率0.50-0.55）\n")
			sb.WriteString("- 立即减仓或止损，保护剩余资金\n")
		} else if accountTotalPnLPct < -15 {
			sb.WriteString(fmt.Sprintf("- ⚠️ 账户累计亏损15-20%%，新开仓概率必须 ≥ %.0f%%\n", requiredMinProb*100))
			sb.WriteString("- 优先考虑与现有持仓风险对冲的方向\n")
			sb.WriteString("- 检查亏损持仓是否需要止损\n")
		} else if accountTotalPnLPct < -10 {
			sb.WriteString(fmt.Sprintf("- 💡 账户累计亏损10-15%%，新开仓概率必须 ≥ %.0f%%\n", requiredMinProb*100))
			sb.WriteString("- 检查亏损持仓是否需要调整或止损\n")
		} else if accountTotalPnLPct < -5 {
			sb.WriteString(fmt.Sprintf("- ✅ 账户累计亏损5-10%%，新开仓概率建议 ≥ %.0f%%\n", requiredMinProb*100))
		} else if accountTotalPnLPct > 10 {
			sb.WriteString("- ✅ 账户盈利 > 10%，可考虑部分止盈锁定利润\n")
			sb.WriteString("- 检查盈利持仓是否达到移动止损条件\n")
		}

		// 根据保证金使用率给出建议
		if ctx.Account.MarginUsedPct > 60 {
			sb.WriteString("- 🛑 保证金占用 > 60%，严禁新开仓，优先降低风险敞口\n")
		} else if ctx.Account.MarginUsedPct > 40 {
			sb.WriteString("- ⚠️ 保证金占用 > 40%，新开仓需降低杠杆或仓位\n")
		}

		sb.WriteString("\n")
	}


	if ctx != nil && ctx.HistoricalPerf != nil && ctx.HistoricalPerf.OverallWinRate > 0 {
		perf := ctx.HistoricalPerf
		sb.WriteString(fmt.Sprintf("\n# 历史表现\n胜率:%.0f%% 准确率:%.0f%%",
			perf.OverallWinRate*100, perf.AvgAccuracy*100))
		if perf.CommonMistakes != "" {
			sb.WriteString(fmt.Sprintf(" ⚠️ 避免: %s", perf.CommonMistakes))
		}
		sb.WriteString("\n")
	}

	if ctx != nil && ctx.RecentFeedback != "" {
		sb.WriteString("\n# 近期预测案例\n")
		sb.WriteString(ctx.RecentFeedback)
		sb.WriteString("\n检查: 是否与过去的失败相似？是否重复成功模式？\n")
	}

	// 🧠 新增：注入实际交易记忆（优先级高于prediction tracker）
	if ctx != nil && ctx.TraderMemory != "" {
		log.Printf("🔍 [DEBUG] TraderMemory长度: %d字符", len(ctx.TraderMemory))
		sb.WriteString("\n# 📚 你的交易历史\n")
		sb.WriteString(ctx.TraderMemory)
		sb.WriteString("\n✓ 从胜利中学习: 哪些信号有效？\n")
		sb.WriteString("✓ 避免亏损: 需要避免什么错误？\n")
		sb.WriteString("✓ 应用模式: 当前市场是否类似？\n")
	} else {
		log.Printf("⚠️  [DEBUG] TraderMemory为空！ctx=%v, TraderMemory长度=%d", ctx != nil, len(ctx.TraderMemory))
	}

	sb.WriteString("\n# 开始预测\n")
	return sb.String()
}

// buildMistakesSection 动态生成"最近错误教训"（基于实际表现）
func (agent *PredictionAgent) buildMistakesSection(ctx *PredictionContext) string {
	if ctx == nil {
		// 没有上下文，使用默认教训
		return `最近错误教训（默认）:
- 输出中性导致错过机会
- 概率过低接近随机猜测
- 过度依赖市场情绪而忽视技术指标`
	}

	// 🆕 从历史表现和交易记忆中提取实际错误
	var mistakes []string

	// 1. 检查预测准确率
	if ctx.HistoricalPerf != nil && ctx.HistoricalPerf.AvgAccuracy > 0 {
		avgProb := ctx.HistoricalPerf.OverallWinRate
		accuracy := ctx.HistoricalPerf.AvgAccuracy

		// 概率校准问题
		if accuracy < 0.55 {
			mistakes = append(mistakes, fmt.Sprintf("预测准确率%.0f%%偏低（接近随机）→ 需提高分析质量", accuracy*100))
		}

		// 中性过多
		if ctx.HistoricalPerf.CommonMistakes != "" {
			mistakes = append(mistakes, ctx.HistoricalPerf.CommonMistakes)
		}

		// 概率不够果断
		if avgProb > 0 && avgProb < 0.60 {
			mistakes = append(mistakes, fmt.Sprintf("平均概率仅%.0f%%（不够果断）→ 有信号时提高至65-75%%", avgProb*100))
		}
	}

	// 2. 从交易记忆中提取失败模式（解析TraderMemory字符串）
	if ctx.TraderMemory != "" {
		// 简单检查是否提到了失败案例
		if strings.Contains(ctx.TraderMemory, "loss") || strings.Contains(ctx.TraderMemory, "❌") {
			// 可以从memory中提取具体的失败案例，但为了简洁，这里只给通用提示
			mistakes = append(mistakes, "检查交易历史中的失败案例 → 避免重复相同错误")
		}
	}

	// 3. 如果没有提取到任何错误，使用默认教训
	if len(mistakes) == 0 {
		return `最近错误教训（系统初始化）:
- 避免过度输出中性 → 有2个以上指标对齐时果断给出方向
- 提高预测概率 → 明确信号时应给65-75%概率
- 技术指标优先 → MACD/RSI/EMA权重70%，情绪权重30%`
	}

	// 4. 格式化错误教训
	var sb strings.Builder
	sb.WriteString("最近错误教训（基于实际表现）:\n")
	for _, mistake := range mistakes {
		sb.WriteString(fmt.Sprintf("- %s\n", mistake))
	}

	return sb.String()
}

// validatePrediction 验证预测结果（增强版 - 完整性约束）
func (agent *PredictionAgent) validatePrediction(pred *types.Prediction) error {
	// 验证必填字段
	if pred.Symbol == "" {
		return fmt.Errorf("symbol不能为空")
	}

	// 验证direction
	validDirections := map[string]bool{"up": true, "down": true, "neutral": true}
	if !validDirections[pred.Direction] {
		return fmt.Errorf("无效的direction: %s", pred.Direction)
	}

	// 验证probability范围
	if pred.Probability < 0.5 || pred.Probability > 1 {
		return fmt.Errorf("probability必须在0.5-1之间: %.2f", pred.Probability)
	}

	// 🆕 验证expected_move合理性
	if math.Abs(pred.ExpectedMove) > 10.0 {
		return fmt.Errorf("expected_move=%.2f%%超出合理范围(应在±10%%内)", pred.ExpectedMove)
	}

	// 🆕 验证best_case/worst_case合理性
	if math.Abs(pred.BestCase) > 15.0 {
		return fmt.Errorf("best_case=%.2f%%超出合理范围(应在±15%%内)", pred.BestCase)
	}
	if math.Abs(pred.WorstCase) > 15.0 {
		return fmt.Errorf("worst_case=%.2f%%超出合理范围(应在±15%%内)", pred.WorstCase)
	}

	// 验证confidence（统一为3级）
	validConfidence := map[string]bool{
		"high": true, "medium": true, "low": true,
		// 兼容旧数据
		"very_high": true, "very_low": true,
	}
	if !validConfidence[pred.Confidence] {
		return fmt.Errorf("无效的confidence: %s (应为high/medium/low)", pred.Confidence)
	}

	// 🆕 自动转换旧的very_high/very_low
	if pred.Confidence == "very_high" {
		pred.Confidence = "high"
	} else if pred.Confidence == "very_low" {
		pred.Confidence = "low"
	}

	// 验证timeframe
	validTimeframes := map[string]bool{"1h": true, "4h": true, "24h": true}
	if !validTimeframes[pred.Timeframe] {
		return fmt.Errorf("无效的timeframe: %s", pred.Timeframe)
	}

	// 验证risk_level（统一为3级）
	validRiskLevels := map[string]bool{
		"low": true, "medium": true, "high": true,
		// 兼容旧数据
		"very_low": true, "very_high": true,
	}
	if !validRiskLevels[pred.RiskLevel] {
		return fmt.Errorf("无效的risk_level: %s (应为low/medium/high)", pred.RiskLevel)
	}

	// 🆕 自动转换旧的very_high/very_low
	if pred.RiskLevel == "very_high" {
		pred.RiskLevel = "high"
	} else if pred.RiskLevel == "very_low" {
		pred.RiskLevel = "low"
	}

	// ✅ 完整性验证 - worst_case < best_case
	if pred.BestCase <= pred.WorstCase {
		return fmt.Errorf("best_case (%.2f) 必须 > worst_case (%.2f)",
			pred.BestCase, pred.WorstCase)
	}

	// ✅ 方向一致性验证
	switch pred.Direction {
	case "up":
		if pred.BestCase <= 0 {
			return fmt.Errorf("direction=up 但 best_case=%.2f ≤ 0", pred.BestCase)
		}
		if pred.WorstCase > 0 {
			return fmt.Errorf("direction=up 但 worst_case=%.2f > 0 (应该允许回撤)", pred.WorstCase)
		}
		if pred.ExpectedMove <= 0 {
			return fmt.Errorf("direction=up 但 expected_move=%.2f ≤ 0", pred.ExpectedMove)
		}

	case "down":
		if pred.WorstCase >= 0 {
			return fmt.Errorf("direction=down 但 worst_case=%.2f ≥ 0", pred.WorstCase)
		}
		// 🔧 放宽best_case限制：允许best_case为负数（强烈下跌时，最好的情况也可能是"少跌点"）
		// 只要保证 best_case > worst_case 即可（已在前面验证）
		if pred.ExpectedMove >= 0 {
			return fmt.Errorf("direction=down 但 expected_move=%.2f ≥ 0", pred.ExpectedMove)
		}

	case "neutral":
		// 🔧 neutral的概率范围放宽到 [0.50, 0.60]
		if pred.Probability > 0.60 {
			return fmt.Errorf("direction=neutral 但 probability=%.2f > 0.60", pred.Probability)
		}
	}

	// ✅ 概率-置信度一致性（放宽检查）
	if pred.Probability >= 0.80 && pred.Confidence == "low" {
		return fmt.Errorf("probability %.2f 但 confidence=%s (不一致)",
			pred.Probability, pred.Confidence)
	}

	if pred.Probability < 0.55 && pred.Confidence == "high" {
		return fmt.Errorf("probability %.2f 但 confidence=%s (不一致)",
			pred.Probability, pred.Confidence)
	}

	return nil
}

func (agent *PredictionAgent) validateMarketData(ctx *PredictionContext) error {
	if ctx == nil || ctx.MarketData == nil {
		return fmt.Errorf("市场数据为空")
	}
	md := ctx.MarketData
	if md.CurrentPrice <= 0 {
		return fmt.Errorf("价格数据无效")
	}
	if md.CurrentRSI7 < 0 || md.CurrentRSI7 > 100 {
		return fmt.Errorf("RSI数据异常: %.2f", md.CurrentRSI7)
	}
	if md.Timestamp > 0 {
		lastUpdate := time.Unix(md.Timestamp, 0)
		if time.Since(lastUpdate) > 10*time.Minute {
			return fmt.Errorf("市场数据已过期 %.1f 分钟", time.Since(lastUpdate).Minutes())
		}
	}
	return nil
}

func (agent *PredictionAgent) calibrateProbability(pred *types.Prediction, ctx *PredictionContext) {
	if pred == nil || ctx == nil {
		return
	}

	// 🔧 关键修复：只有在样本量充足时才进行校准
	// 如果历史准确率 < 30%，说明：
	// 1) 样本量太小（如只有1-2条记录）
	// 2) 系统刚启动，数据不可信
	// 此时应该相信AI的原始判断，不进行校准
	if ctx.HistoricalPerf != nil && ctx.HistoricalPerf.AvgAccuracy >= 0.30 {
		calibrationFactor := ctx.HistoricalPerf.AvgAccuracy / 0.5
		if calibrationFactor <= 0 {
			calibrationFactor = 1
		}
		// 限制校准幅度，避免过度调整
		calibrationFactor = math.Max(0.8, math.Min(1.2, calibrationFactor))
		pred.Probability = math.Max(0.5, math.Min(1.0, pred.Probability*calibrationFactor))
	}

	if ctx.SharpeRatio < 0 {
		switch pred.Confidence {
		case "very_high":
			pred.Confidence = "high"
		case "high":
			pred.Confidence = "medium"
		case "medium":
			pred.Confidence = "medium"
		}
	}
}

func (agent *PredictionAgent) selectTimeframe(md *market.Data) string {
	if md == nil || md.CurrentPrice <= 0 || md.LongerTermContext == nil || md.LongerTermContext.ATR14 <= 0 {
		return "4h"
	}

	atrPct := (md.LongerTermContext.ATR14 / md.CurrentPrice) * 100

	// 🔧 调整阈值，增加1h和24h的使用
	switch {
	case atrPct > 4.0:  // 原来是3.0，提高阈值
		return "1h"     // 极高波动用1h（快速反应）
	case atrPct > 2.0:  // 新增中等波动区间
		return "4h"     // 中高波动用4h
	case atrPct < 0.8:  // 原来是1.0，降低阈值
		return "24h"    // 极低波动用24h（等待变盘）
	default:
		return "4h"     // 默认4h
	}
}

func (agent *PredictionAgent) validatePredictionEnhanced(pred *types.Prediction, md *market.Data) error {
	if pred == nil || md == nil {
		return nil
	}

	rsi := md.CurrentRSI7

	// 🔧 修正：只拒绝"逆势"的极端预测，允许"顺势"预测
	// RSI>85（超买）+ 预测down（做空）→ 可能错误（超买时应该会涨或横盘，不太会跌）
	// RSI<15（超卖）+ 预测up（做多）→ 可能错误（超卖时应该会跌或横盘，不太会涨）
	if pred.Direction == "down" && rsi > 85 && pred.Probability > 0.75 {
		return fmt.Errorf("RSI=%.2f 极度超买，高概率%.0f%%预测下跌可能错误（超买通常继续涨或盘整）",
			rsi, pred.Probability*100)
	}
	if pred.Direction == "up" && rsi < 15 && pred.Probability > 0.75 {
		return fmt.Errorf("RSI=%.2f 极度超卖，高概率%.0f%%预测上涨可能错误（超卖通常继续跌或盘整）",
			rsi, pred.Probability*100)
	}

	// 🆕 趋势一致性检查（仅检查明显逆势）
	if md.LongerTermContext != nil && md.LongerTermContext.EMA20 > 0 && md.LongerTermContext.EMA50 > 0 {
		price := md.CurrentPrice
		ema20 := md.LongerTermContext.EMA20
		ema50 := md.LongerTermContext.EMA50
		macd := md.CurrentMACD

		// 判断是否为明显的强趋势
		isStrongDowntrend := price < ema20*0.98 && ema20 < ema50 && macd < -0.0001
		isStrongUptrend := price > ema20*1.02 && ema20 > ema50 && macd > 0.0001

		// ⚠️  只在高概率逆势预测时才警告（允许低概率的逆势尝试）
		if isStrongDowntrend && pred.Direction == "up" && pred.Probability > 0.70 {
			return fmt.Errorf("明显下行趋势(价格<EMA20<EMA50且MACD<0)但高概率%.0f%%预测上涨 (建议降低概率或输出neutral)",
				pred.Probability*100)
		}

		if isStrongUptrend && pred.Direction == "down" && pred.Probability > 0.70 {
			return fmt.Errorf("明显上行趋势(价格>EMA20>EMA50且MACD>0)但高概率%.0f%%预测下跌 (建议降低概率或输出neutral)",
				pred.Probability*100)
		}
	}

	return nil
}

// truncateString 截断字符串到指定长度  
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

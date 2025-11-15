package decision

import (
	"encoding/json"
	"fmt"
	"log"
	"nofx/decision/agents"
	"nofx/market"
	"nofx/mcp"
	"nofx/pool"
	"strings"
	"sync"
	"time"
)

// PositionInfo 持仓信息
type PositionInfo struct {
	Symbol           string    `json:"symbol"`
	Side             string    `json:"side"` // "long" or "short"
	EntryPrice       float64   `json:"entry_price"`
	MarkPrice        float64   `json:"mark_price"`
	Quantity         float64   `json:"quantity"`
	Leverage         int       `json:"leverage"`
	UnrealizedPnL    float64   `json:"unrealized_pnl"`
	UnrealizedPnLPct float64   `json:"unrealized_pnl_pct"`
	LiquidationPrice float64   `json:"liquidation_price"`
	MarginUsed       float64   `json:"margin_used"`
	UpdateTime       int64     `json:"update_time"`  // 持仓更新时间戳（毫秒）
	OpenTime         time.Time `json:"open_time"`    // 🆕 开仓时间（用于判断持仓时长）
}

// AccountInfo 账户信息
type AccountInfo struct {
	TotalEquity      float64 `json:"total_equity"`      // 账户净值
	AvailableBalance float64 `json:"available_balance"` // 可用余额
	TotalPnL         float64 `json:"total_pnl"`         // 总盈亏
	TotalPnLPct      float64 `json:"total_pnl_pct"`     // 总盈亏百分比
	MarginUsed       float64 `json:"margin_used"`       // 已用保证金
	MarginUsedPct    float64 `json:"margin_used_pct"`   // 保证金使用率
	PositionCount    int     `json:"position_count"`    // 持仓数量
}

// CandidateCoin 候选币种（来自币种池）
type CandidateCoin struct {
	Symbol  string   `json:"symbol"`
	Sources []string `json:"sources"` // 来源: "ai500" 和/或 "oi_top"
}

// OITopData 持仓量增长Top数据（用于AI决策参考）
type OITopData struct {
	Rank              int     // OI Top排名
	OIDeltaPercent    float64 // 持仓量变化百分比（1小时）
	OIDeltaValue      float64 // 持仓量变化价值
	PriceDeltaPercent float64 // 价格变化百分比
	NetLong           float64 // 净多仓
	NetShort          float64 // 净空仓
}

// Context 交易上下文（传递给AI的完整信息）
type Context struct {
	CurrentTime     string                  `json:"current_time"`
	RuntimeMinutes  int                     `json:"runtime_minutes"`
	CallCount       int                     `json:"call_count"`
	Account         AccountInfo             `json:"account"`
	Positions       []PositionInfo          `json:"positions"`
	CandidateCoins  []CandidateCoin         `json:"candidate_coins"`
	MarketDataMap   map[string]*market.Data `json:"-"` // 不序列化，但内部使用
	OITopDataMap    map[string]*OITopData   `json:"-"` // OI Top数据映射
	Performance     interface{}             `json:"-"` // 历史表现分析（logger.PerformanceAnalysis）
	BTCETHLeverage  int                     `json:"-"` // BTC/ETH杠杆倍数（从配置读取）
	AltcoinLeverage int                     `json:"-"` // 山寨币杠杆倍数（从配置读取）
	MemoryPrompt    string                  `json:"-"` // 🧠 AI记忆提示（Sprint 1）
}

// Decision AI的交易决策
type Decision struct {
	Symbol          string  `json:"symbol"`
	Action          string  `json:"action"` // "open_long", "open_short", "close_long", "close_short", "hold", "wait"
	Leverage        int     `json:"leverage,omitempty"`
	PositionSizeUSD float64 `json:"position_size_usd,omitempty"`
	StopLoss        float64 `json:"stop_loss,omitempty"`
	TakeProfit      float64 `json:"take_profit,omitempty"`
	Confidence      int     `json:"confidence,omitempty"` // 信心度 (0-100)
	RiskUSD         float64 `json:"risk_usd,omitempty"`   // 最大美元风险
	Reasoning       string  `json:"reasoning"`
}

// FullDecision AI的完整决策（包含思维链）
type FullDecision struct {
	UserPrompt string     `json:"user_prompt"` // 发送给AI的输入prompt
	CoTTrace   string     `json:"cot_trace"`   // 思维链分析（AI输出）
	Decisions  []Decision `json:"decisions"`   // 具体决策列表
	Timestamp  time.Time  `json:"timestamp"`
}

// GetFullDecision 获取AI的完整交易决策（使用Multi-Agent架构）
func GetFullDecision(ctx *Context, mcpClient *mcp.Client) (*FullDecision, error) {
	// 1. 为所有币种获取市场数据
	if err := fetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("获取市场数据失败: %w", err)
	}

	// 2. 创建Multi-Agent决策协调器
	orchestrator := agents.NewDecisionOrchestrator(mcpClient, ctx.BTCETHLeverage, ctx.AltcoinLeverage)

	// 3. 转换Context为agents包的Context格式
	agentCtx := convertToAgentContext(ctx)

	// 4. 调用Multi-Agent系统获取决策
	agentDecision, err := orchestrator.GetFullDecision(agentCtx)
	if err != nil {
		return nil, fmt.Errorf("Multi-Agent决策失败: %w", err)
	}

	// 5. 转换agents.FullDecision为decision.FullDecision
	decision := &FullDecision{
		UserPrompt: "", // Multi-Agent不使用单一UserPrompt
		CoTTrace:   agentDecision.CoTTrace,
		Decisions:  convertAgentDecisions(agentDecision.Decisions),
		Timestamp:  time.Now(),
	}

	return decision, nil
}

// convertAgentDecisions 转换agents.Decision为decision.Decision
func convertAgentDecisions(agentDecisions []agents.Decision) []Decision {
	decisions := make([]Decision, len(agentDecisions))
	for i, ad := range agentDecisions {
		decisions[i] = Decision{
			Symbol:          ad.Symbol,
			Action:          ad.Action,
			Leverage:        ad.Leverage,
			PositionSizeUSD: ad.PositionSizeUSD,
			StopLoss:        ad.StopLoss,
			TakeProfit:      ad.TakeProfit,
			Confidence:      ad.Confidence,
			RiskUSD:         ad.RiskUSD,
			Reasoning:       ad.Reasoning,
		}
	}
	return decisions
}

// GetFullDecisionMonolithic 获取AI的完整交易决策（旧版单一prompt方式，保留作为备份）
// ⚠️ 注意：此函数当前未被使用，系统已切换到Multi-Agent架构（GetFullDecision）
// 保留此函数作为应急回退方案，如需切换回旧版，修改 trader/auto_trader.go:340
func GetFullDecisionMonolithic(ctx *Context, mcpClient *mcp.Client) (*FullDecision, error) {
	// 1. 为所有币种获取市场数据
	if err := fetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("获取市场数据失败: %w", err)
	}

	// 2. 构建 System Prompt（固定规则）和 User Prompt（动态数据）
	systemPrompt := buildSystemPrompt(ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage)
	userPrompt := buildUserPrompt(ctx)

	// 3. 调用AI API（使用 system + user prompt）
	aiResponse, err := mcpClient.CallWithMessages(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("调用AI API失败: %w", err)
	}

	// 4. 解析AI响应
	decision, err := parseFullDecisionResponse(aiResponse, ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage, ctx.MarketDataMap)
	if err != nil {
		return nil, fmt.Errorf("解析AI响应失败: %w", err)
	}

	decision.Timestamp = time.Now()
	decision.UserPrompt = userPrompt // 保存输入prompt
	return decision, nil
}

// convertToAgentContext 将decision.Context转换为agents.Context
func convertToAgentContext(ctx *Context) *agents.Context {
	// 转换持仓信息
	positions := make([]agents.PositionInfoInput, len(ctx.Positions))
	for i, pos := range ctx.Positions {
		positions[i] = agents.PositionInfoInput{
			Symbol:           pos.Symbol,
			Side:             pos.Side,
			EntryPrice:       pos.EntryPrice,
			MarkPrice:        pos.MarkPrice,
			Quantity:         pos.Quantity,
			Leverage:         pos.Leverage,
			UnrealizedPnL:    pos.UnrealizedPnL,
			UnrealizedPnLPct: pos.UnrealizedPnLPct,
			LiquidationPrice: pos.LiquidationPrice,
			MarginUsed:       pos.MarginUsed,
			UpdateTime:       pos.UpdateTime,
			OpenTime:         pos.OpenTime, // 🐛 修复：必须复制OpenTime，否则持仓时长计算错误
		}
	}

	// 转换候选币种
	candidates := make([]agents.CandidateCoin, len(ctx.CandidateCoins))
	for i, coin := range ctx.CandidateCoins {
		candidates[i] = agents.CandidateCoin{
			Symbol:  coin.Symbol,
			Sources: coin.Sources,
		}
	}

	// 转换账户信息
	account := agents.AccountInfo{
		TotalEquity:      ctx.Account.TotalEquity,
		AvailableBalance: ctx.Account.AvailableBalance,
		TotalPnL:         ctx.Account.TotalPnL,
		TotalPnLPct:      ctx.Account.TotalPnLPct,
		MarginUsed:       ctx.Account.MarginUsed,
		MarginUsedPct:    ctx.Account.MarginUsedPct,
		PositionCount:    ctx.Account.PositionCount,
	}

	return &agents.Context{
		CurrentTime:     ctx.CurrentTime,
		RuntimeMinutes:  ctx.RuntimeMinutes,
		CallCount:       ctx.CallCount,
		Account:         account,
		Positions:       positions,
		CandidateCoins:  candidates,
		MarketDataMap:   ctx.MarketDataMap,
		Performance:     ctx.Performance,
		BTCETHLeverage:  ctx.BTCETHLeverage,
		AltcoinLeverage: ctx.AltcoinLeverage,
		MemoryPrompt:    ctx.MemoryPrompt, // 🧠 传递AI记忆
	}
}

// fetchMarketDataForContext 为上下文中的所有币种获取市场数据和OI数据
func fetchMarketDataForContext(ctx *Context) error {
	ctx.MarketDataMap = make(map[string]*market.Data)
	ctx.OITopDataMap = make(map[string]*OITopData)

	// 收集所有需要获取数据的币种
	symbolSet := make(map[string]bool)

	// 1. 优先获取持仓币种的数据（这是必须的）
	for _, pos := range ctx.Positions {
		symbolSet[pos.Symbol] = true
	}

	// 2. 候选币种数量根据账户状态动态调整
	maxCandidates := calculateMaxCandidates(ctx)
	for i, coin := range ctx.CandidateCoins {
		if i >= maxCandidates {
			break
		}
		symbolSet[coin.Symbol] = true
	}

	// ✅ 优化：并发获取市场数据（大幅减少延迟）
	// 持仓币种集合（用于判断是否跳过OI检查）
	positionSymbols := make(map[string]bool)
	for _, pos := range ctx.Positions {
		positionSymbols[pos.Symbol] = true
	}

	// 使用goroutines并发获取数据
	var wg sync.WaitGroup
	var mu sync.Mutex // 保护 ctx.MarketDataMap 的并发写入

	for symbol := range symbolSet {
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()

			data, err := market.Get(sym)
			if err != nil {
				// 单个币种失败不影响整体，只记录错误
				log.Printf("⚠️  获取%s市场数据失败: %v", sym, err)
				return
			}

			// ⚠️ 流动性过滤：持仓价值低于15M USD的币种不做（多空都不做）
			// 持仓价值 = 持仓量 × 当前价格
			// 但现有持仓必须保留（需要决策是否平仓）
			isExistingPosition := positionSymbols[sym]
			if !isExistingPosition && data.OpenInterest != nil && data.CurrentPrice > 0 {
				// 计算持仓价值（USD）= 持仓量 × 当前价格
				oiValue := data.OpenInterest.Latest * data.CurrentPrice
				oiValueInMillions := oiValue / 1_000_000 // 转换为百万美元单位
				if oiValueInMillions < 15 {
					log.Printf("⚠️  %s 持仓价值过低(%.2fM USD < 15M)，跳过此币种 [持仓量:%.0f × 价格:%.4f]",
						sym, oiValueInMillions, data.OpenInterest.Latest, data.CurrentPrice)
					return
				}
			}

			// 并发安全地写入map
			mu.Lock()
			ctx.MarketDataMap[sym] = data
			mu.Unlock()
		}(symbol)
	}

	// 等待所有goroutines完成
	wg.Wait()

	// 加载OI Top数据（不影响主流程）
	oiPositions, err := pool.GetOITopPositions()
	if err == nil {
		for _, pos := range oiPositions {
			// 标准化符号匹配
			symbol := pos.Symbol
			ctx.OITopDataMap[symbol] = &OITopData{
				Rank:              pos.Rank,
				OIDeltaPercent:    pos.OIDeltaPercent,
				OIDeltaValue:      pos.OIDeltaValue,
				PriceDeltaPercent: pos.PriceDeltaPercent,
				NetLong:           pos.NetLong,
				NetShort:          pos.NetShort,
			}
		}
	}

	return nil
}

// calculateMaxCandidates 根据账户状态计算需要分析的候选币种数量
func calculateMaxCandidates(ctx *Context) int {
	// 直接返回候选池的全部币种数量
	// 因为候选池已经在 auto_trader.go 中筛选过了
	// 固定分析前20个评分最高的币种（来自AI500）
	return len(ctx.CandidateCoins)
}

// buildSystemPrompt 构建 System Prompt（固定规则，可缓存）
// ⚠️ 注意：此函数仅被GetFullDecisionMonolithic使用（旧版备份），当前系统不再调用
// Multi-Agent架构中，每个Agent有独立的prompt（见decision/agents/目录）
func buildSystemPrompt(accountEquity float64, btcEthLeverage, altcoinLeverage int) string {
	var sb strings.Builder

	// === 核心使命 ===
	sb.WriteString("你是专业的加密货币交易AI，在币安合约市场进行自主交易。\n\n")
	sb.WriteString("# 🎯 核心目标: 最大化夏普比率（Sharpe Ratio）\n\n")
	sb.WriteString("夏普比率 = 平均收益 / 收益波动率\n\n")
	sb.WriteString("**关键认知**: 系统每3分钟扫描一次，但不意味着每次都要交易！\n")
	sb.WriteString("大多数时候应该是 `wait` 或 `hold`，只在极佳机会时才开仓。\n\n")

	// === 决策流程 ===
	sb.WriteString("# 📋 决策流程（必须遵循）\n\n")
	sb.WriteString("1. **分析夏普比率**: 当前绩效(Sharpe)如何？（见用户Prompt末尾）\n")
	sb.WriteString("   - 遵循「夏普比率自我进化」部分的指导方针。\n\n")
	sb.WriteString("2. **执行量化体制分析**: 使用BTC/ETH的4h数据，**严格按照**「1. 量化市场体制」中的规则，确定大盘体制为 (A1), (A2), (B), 或 (C)。\n\n")
	sb.WriteString("3. **选择交易策略**: 根据体制选择策略。\n")
	sb.WriteString("   - **(A1) 上升趋势 / (A2) 下降趋势**: 严格顺势。只在趋势方向上寻找「回踩」信号。\n")
	sb.WriteString("   - **(B) 宽幅震荡**: 谨慎高抛低吸。使用RSI等摆动指标寻找「逆转」信号。\n")
	sb.WriteString("   - **(C) 窄幅盘整**: **🛑 禁止开仓 (WAIT)**。\n\n")
	sb.WriteString("4. **评估持仓**: 根据「市场体制」和「信号工具箱」重新评估持仓。\n\n")
	sb.WriteString("5. **寻找新机会**: 根据所选策略，在「信号工具箱」中寻找信号共振。\n")
	sb.WriteString("   - **禁止**：在(A)趋势市场中，使用(B)逆转信号（例如：(A1)牛市中仅因RSI超买而做空）。\n\n")
	sb.WriteString("6. **输出决策**: 详细说明你的分析（思维链 + JSON）。\n\n")

	// === 关键修改 V4.0: 大幅简化体制判断，强制验证输出 ===
	sb.WriteString("# 1. 🔬 市场体制判断（强制三步验证）\n\n")
	sb.WriteString("**⚠️ 警告：你必须在思维链中明确输出以下三步的计算结果，禁止跳过！**\n\n")

	sb.WriteString("**STEP 1: 计算BTC的4h ATR%**\n")
	sb.WriteString("```\n")
	sb.WriteString("ATR% = (4h ATR14 / 4h 当前价格) × 100%\n")
	sb.WriteString("```\n")
	sb.WriteString("在思维链中必须写：\"BTC 4h ATR% = X.XX%\"\n\n")

	sb.WriteString("**STEP 2: 判断波动率类型**\n")
	sb.WriteString("```\n")
	sb.WriteString("IF (ATR% < 1.0%):\n")
	sb.WriteString("    体制 = (C) 窄幅盘整\n")
	sb.WriteString("    策略 = 禁止开仓，WAIT\n")
	sb.WriteString("    停止判断，输出决策\n")
	sb.WriteString("ELSE:\n")
	sb.WriteString("    继续STEP 3\n")
	sb.WriteString("```\n")
	sb.WriteString("在思维链中必须写：\"ATR% X.XX% >= 1.0% → 有波动，继续判断趋势\" 或 \"ATR% X.XX% < 1.0% → (C)盘整，禁止开仓\"\n\n")

	sb.WriteString("**STEP 3: 判断趋势方向（仅当ATR%>=1.0%时执行）**\n")
	sb.WriteString("```\n")
	sb.WriteString("获取BTC 4h数据：\n")
	sb.WriteString("  - Price = 当前价格\n")
	sb.WriteString("  - EMA50 = 50周期EMA\n")
	sb.WriteString("  - EMA200 = 200周期EMA\n\n")
	sb.WriteString("IF (Price > EMA50) AND (EMA50 > EMA200):\n")
	sb.WriteString("    体制 = (A1) 上升趋势\n")
	sb.WriteString("    策略 = 顺势做多（回踩买入）\n")
	sb.WriteString("ELSE IF (Price < EMA50) AND (EMA50 < EMA200):\n")
	sb.WriteString("    体制 = (A2) 下降趋势\n")
	sb.WriteString("    策略 = 顺势做空（反弹卖出）\n")
	sb.WriteString("ELSE:\n")
	sb.WriteString("    体制 = (B) 宽幅震荡\n")
	sb.WriteString("    策略 = 谨慎高抛低吸（RSI超买超卖）\n")
	sb.WriteString("```\n")
	sb.WriteString("在思维链中必须写：\"Price X vs EMA50 Y → [满足/不满足] | EMA50 Y vs EMA200 Z → [满足/不满足] | 体制=(A1/A2/B)\"\n\n")

	sb.WriteString("**🚨 强制要求**：\n")
	sb.WriteString("1. 你必须在思维链中**逐行**输出STEP 1、2、3的计算结果\n")
	sb.WriteString("2. 你必须使用**精确数值**（不能说\"接近\"、\"大约\"）\n")
	sb.WriteString("3. 如果你跳过任何一步，或者逻辑矛盾，你的决策将被系统拒绝\n\n")

	sb.WriteString("**体制对应策略**：\n")
	sb.WriteString("- **(C) 窄幅盘整**: 🛑 禁止开仓。等待波动率放大。\n")
	sb.WriteString("- **(A1) 上升趋势**: ✅ 只做多，等价格回踩EMA20/EMA50支撑时买入。禁止做空。\n")
	sb.WriteString("- **(A2) 下降趋势**: ✅ 只做空，等价格反弹至EMA20/EMA50阻力时卖出。禁止做多。\n")
	sb.WriteString("- **(B) 宽幅震荡**: ⚠️ 谨慎高抛低吸，使用RSI超买(>70)做空、超卖(<30)做多。\n\n")
	// === 关键修改 V4.0 结束 ===

	// === 将信号与量化体制挂钩 ===
	sb.WriteString("# 2. 信号工具箱 (Signal Toolbox)\n\n")
	sb.WriteString("**以下信号的有效性取决于你在步骤1中分析的市场体制。**\n\n")
	sb.WriteString("**开仓必须同时满足≥3个独立维度信号**：\n\n")

	sb.WriteString("**做多信号**（至少3个同时成立）：\n")
	sb.WriteString("1. **体制/趋势**: 处于 **(A1) 上升趋势** (顺势回踩) **或** 处于 **(B) 震荡下轨** (逆势摸底)。\n")
	sb.WriteString("2. **动量**: 4h MACD > 0 且上升 或 1h RSI 从超卖区(30以下)反弹。\n")
	sb.WriteString("3. **位置**: 价格回踩EMA20支撑企稳 或 突破关键阻力位。\n")
	sb.WriteString("4. **资金**: 成交量放大(>20%) 或 OI增长(>10%)。\n")
	sb.WriteString("5. **情绪**: 资金费率<0（空头主导）且OI_Top显示净空仓高。\n\n")

	sb.WriteString("**做空信号**（至少3个同时成立）：\n")
	sb.WriteString("1. **体制/趋势**: 处于 **(A2) 下降趋势** (顺势反弹) **或** 处于 **(B) 震荡上轨** (逆势摸顶)。\n")
	sb.WriteString("2. **动量**: 4h MACD < 0 且下降 或 1h RSI 从超买区(70以上)回落。\n")
	sb.WriteString("3. **位置**: 价格反弹至EMA20阻力受阻 或 跌破关键支撑位。\n")
	sb.WriteString("4. **资金**: 成交量放大(>20%) 或 OI增长(>10%)。\n")
	sb.WriteString("5. **情绪**: 资金费率>0.01%（多头主导）且OI_Top显示净多仓高。\n\n")

	sb.WriteString("**❌ 禁止开仓情况**：\n")
	sb.WriteString("- **处于 (C) 窄幅盘整体制** (量化规则：4h ATR% < 1.0%)。\n")
	sb.WriteString("- **体制与信号冲突**（例如：(A1)上升趋势中，使用(B)逆转信号做空）。\n")
	sb.WriteString("- 指标矛盾（如MACD多头但价格已跌破EMA50）。\n\n")

	// === 关键修改 V4.0: 强化持仓管理，修复"呼吸空间"滥用 ===
	sb.WriteString("# 2.5. 💎 持仓管理（防止过早平仓 vs 及时止损）\n\n")
	sb.WriteString("**⚠️ 关键警告：区分\"呼吸空间\"和\"必须止损\"！**\n\n")

	sb.WriteString("### 🚨 强制止损信号（无论持仓时长，立即平仓）\n")
	sb.WriteString("以下情况**立即平仓**，不适用\"呼吸空间\"规则：\n\n")
	sb.WriteString("1. **极端反转信号**：\n")
	sb.WriteString("   - 空仓 + RSI(7) > 75 → 空头被轧空，立即平仓\n")
	sb.WriteString("   - 多仓 + RSI(7) < 25 → 多头被踩踏，立即平仓\n\n")
	sb.WriteString("2. **亏损扩大**：\n")
	sb.WriteString("   - 未实现盈亏 < -10% (基于保证金) → 入场错误，立即止损\n\n")
	sb.WriteString("3. **体制完全逆转**：\n")
	sb.WriteString("   - 空仓 + 体制从(A2)下降变为(A1)上升 → 趋势逆转，立即平仓\n")
	sb.WriteString("   - 多仓 + 体制从(A1)上升变为(A2)下降 → 趋势逆转，立即平仓\n\n")

	sb.WriteString("### 💎 呼吸空间规则（仅适用于无极端信号的仓位）\n")
	sb.WriteString("**前提**：持仓 < 30分钟 **且** 未触发上述强制止损信号\n\n")
	sb.WriteString("1. **默认动作**: HOLD（持有）\n")
	sb.WriteString("2. **禁止平仓理由**：\n")
	sb.WriteString("   - 利润很小（< +5%）\n")
	sb.WriteString("   - 价格小幅波动（< 2%）\n")
	sb.WriteString("   - RSI小幅变化（如从28涨到40）\n")
	sb.WriteString("   - 小周期(3m)指标背离\n\n")

	sb.WriteString("### 🔍 成熟仓位评估（持仓 > 30分钟）\n")
	sb.WriteString("1. **优先检查**：是否触发上述强制止损信号？如是，立即平仓。\n")
	sb.WriteString("2. **体制检查**：市场体制是否改变？\n")
	sb.WriteString("3. **信号检查**：原始开仓理由是否消失？\n")
	sb.WriteString("4. **目标检查**：是否接近止盈目标？\n")
	sb.WriteString("5. **原则**：只有在原始理由**完全消失**且**无极端信号**时，才考虑获利了结。\n\n")

	sb.WriteString("**🚨 示例（说明什么时候必须止损）**：\n")
	sb.WriteString("```\n")
	sb.WriteString("持仓：SOLUSDT空仓，入场价185，当前价187，持仓60分钟，亏损-10%\n")
	sb.WriteString("当前RSI(7) = 80.2（极度超买）\n\n")
	sb.WriteString("❌ 错误决策：\"持仓60分钟，给予呼吸空间，继续HOLD\"\n")
	sb.WriteString("✓ 正确决策：\"RSI 80.2 > 75 + 空仓亏损 → 触发强制止损信号 → 立即平仓\"\n")
	sb.WriteString("```\n\n")
	// === 关键修改 V4.0 结束 ===

	// === 关键修改：统一 R/R 规则 ===
	sb.WriteString("# 3. 硬约束（风险控制）\n\n")
	sb.WriteString("1. **风险回报比**: **最低必须 ≥ 1:2**。\n") // 统一R/R到1:2
	sb.WriteString("2. **最多持仓**: 3个币种（质量>数量）。\n")
	sb.WriteString(fmt.Sprintf("3. **单币仓位**: 山寨%.0f-%.0f U(%dx杠杆) | BTC/ETH %.0f-%.0f U(%dx杠杆)\n",
		accountEquity*0.8, accountEquity*1.5, altcoinLeverage, accountEquity*5, accountEquity*10, btcEthLeverage))
	sb.WriteString("4. **保证金**: 总使用率 ≤ 90%\n\n")

	// === 关键修改：将R/R与量化体制挂钩 ===
	sb.WriteString("# 4. 风险与杠杆（动态ATR矩阵）\n\n")
	sb.WriteString("**⚠️ 重要**: 必须根据ATR%动态调整杠杆和止损止盈！\n\n")
	sb.WriteString("**第一步：计算ATR%（波动率）** (使用你决策的币种的ATR%)\n")
	sb.WriteString("```\n")
	sb.WriteString("ATR% = (ATR14 / 当前价格) × 100%\n")
	sb.WriteString("```\n\n")
	sb.WriteString("**第二步：根据波动率确定基础倍数**\n")
	sb.WriteString("```\n")
	sb.WriteString("低波动: ATR% < 2%       → 杠杆系数 1.0 | 止损 4.0×ATR | 止盈基础 8.0×ATR\n")
	sb.WriteString("中波动: 2% ≤ ATR% < 4%  → 杠杆系数 0.8 | 止损 5.0×ATR | 止盈基础 10.0×ATR\n")
	sb.WriteString("高波动: ATR% ≥ 4%       → 杠杆系数 0.6 | 止损 6.0×ATR | 止盈基础 12.0×ATR\n")
	sb.WriteString("```\n\n")

	sb.WriteString("**第三步：根据市场体制调整止盈倍数（止损倍数不变）**\n")
	sb.WriteString("```\n")
	sb.WriteString("体制 (A) 趋势行情:\n")
	sb.WriteString("  - 可以提高止盈倍数：低波动→12-15x, 中波动→12-16x, 高波动→14-18x\n")
	sb.WriteString("  - 目的：让利润奔跑，追求更高的R/R比（2.5:1 ~ 3:1）\n")
	sb.WriteString("  - 示例：BNB ATR%=1.68%(低波动) + (A2)下降趋势 → 止损4x, 止盈12-15x\n\n")
	sb.WriteString("体制 (B) 震荡行情:\n")
	sb.WriteString("  - 使用基础止盈倍数（低波动8x, 中波动10x, 高波动12x）\n")
	sb.WriteString("  - 目的：快速获利了结，不贪心，标准R/R比（2:1）\n\n")
	sb.WriteString("体制 (C) 盘整行情:\n")
	sb.WriteString("  - 禁止交易\n")
	sb.WriteString("```\n\n")

	sb.WriteString("**第四步：计算止损止盈并验证R/R比（强制要求）**\n")
	sb.WriteString("```\n")
	sb.WriteString("⚠️ 关键原则：所有计算必须使用「精确市价」而不是圆整价格\n\n")
	sb.WriteString("1. 计算止损止盈价格（严格按照第二步和第三步确定的倍数）：\n")
	sb.WriteString("   做多: SL = 精确市价 - (ATR × 止损倍数), TP = 精确市价 + (ATR × 止盈倍数)\n")
	sb.WriteString("   做空: SL = 精确市价 + (ATR × 止损倍数), TP = 精确市价 - (ATR × 止盈倍数)\n")
	sb.WriteString("   \n")
	sb.WriteString("   例1：BNBUSDT做空，ATR%=1.68%(低波动)，市价1090.47, ATR=18.357\n")
	sb.WriteString("        体制(A)趋势 → 止损4x, 止盈12x（提高倍数让利润奔跑）\n")
	sb.WriteString("        SL = 1090.47+(18.357×4) = 1163.90\n")
	sb.WriteString("        TP = 1090.47-(18.357×12) = 870.19\n\n")
	sb.WriteString("   例2：DOGEUSDT做空，ATR%=2.1%(中波动)，市价0.1868, ATR=0.004\n")
	sb.WriteString("        体制(B)震荡 → 止损5x, 止盈10x（基础倍数）\n")
	sb.WriteString("        SL = 0.1868+(0.004×5) = 0.2068\n")
	sb.WriteString("        TP = 0.1868-(0.004×10) = 0.1468\n\n")
	sb.WriteString("2. 验证风险回报比（必须≥2.0:1）：\n")
	sb.WriteString("   做多: 风险% = (精确市价-SL)/精确市价×100, 收益% = (TP-精确市价)/精确市价×100\n")
	sb.WriteString("   做空: 风险% = (SL-精确市价)/精确市价×100, 收益% = (精确市价-TP)/精确市价×100\n")
	sb.WriteString("   R/R比 = 收益%/风险% ≥ 2.0\n")
	sb.WriteString("   \n")
	sb.WriteString("   例1：BNB做空，市价1090.47, SL=1163.90, TP=870.19\n")
	sb.WriteString("        风险%=(1163.90-1090.47)/1090.47×100=6.73%\n")
	sb.WriteString("        收益%=(1090.47-870.19)/1090.47×100=20.20%\n")
	sb.WriteString("        R/R=20.20/6.73=3.0:1 ✓ (趋势行情追求更高R/R)\n\n")
	sb.WriteString("   例2：DOGE做空，市价0.1868, SL=0.2068, TP=0.1468\n")
	sb.WriteString("        风险%=(0.2068-0.1868)/0.1868×100=10.71%\n")
	sb.WriteString("        收益%=(0.1868-0.1468)/0.1868×100=21.41%\n")
	sb.WriteString("        R/R=21.41/10.71=2.0:1 ✓ (震荡行情标准R/R)\n\n")
	sb.WriteString("2.5 🚨【强平价校验】（必须执行，防止止损失效）：\n")
	sb.WriteString("   ⚠️ 关键问题：如果止损价超过强平价，价格达到强平价时会直接强制平仓，止损单永远无法触发！\n\n")
	sb.WriteString("   **强平价计算公式：**\n")
	sb.WriteString("   做多: 强平价 = 入场价 × (1 - 0.95/杠杆)  // 留5%安全余量\n")
	sb.WriteString("   做空: 强平价 = 入场价 × (1 + 0.95/杠杆)  // 留5%安全余量\n\n")
	sb.WriteString("   **止损价必须在强平价安全范围内：**\n")
	sb.WriteString("   做多: 止损价 > 强平价 (止损在强平价之上)\n")
	sb.WriteString("   做空: 止损价 < 强平价 (止损在强平价之下)\n\n")
	sb.WriteString("   **示例1：HYPEUSDT做空 12x杠杆（反面教材）**\n")
	sb.WriteString("   入场价44.19, ATR=1.847, 高波动→止损6×ATR\n")
	sb.WriteString("   初步止损 = 44.19+(1.847×6) = 55.27\n")
	sb.WriteString("   强平价 = 44.19×(1+0.95/12) = 44.19×1.0792 = 47.69\n")
	sb.WriteString("   ❌ 止损55.27 > 强平47.69 → 强平价先触发，止损永远无法执行！\n")
	sb.WriteString("   ✓ 正确做法：降低止损倍数到3×ATR\n")
	sb.WriteString("     修正止损 = 44.19+(1.847×3) = 49.73 > 47.69但接近\n")
	sb.WriteString("     或者降低杠杆到8×: 强平价=44.19×(1+0.95/8)=49.43\n\n")
	sb.WriteString("   **示例2：BNBUSDT做空 20x杠杆（正确示例）**\n")
	sb.WriteString("   入场价1093.53, ATR=17.51, 低波动→止损4×ATR\n")
	sb.WriteString("   止损 = 1093.53+(17.51×4) = 1163.57\n")
	sb.WriteString("   强平价 = 1093.53×(1+0.95/20) = 1093.53×1.0475 = 1145.47\n")
	sb.WriteString("   ❌ 止损1163.57 > 强平1145.47 → 仍然失效！\n")
	sb.WriteString("   ✓ 修正：降低到3×ATR → 止损=1146.06，勉强在强平价之内\n\n")
	sb.WriteString("   **强制规则：**\n")
	sb.WriteString("   - 计算止损后，必须验证是否在强平价范围内\n")
	sb.WriteString("   - 如果超出，必须降低止损倍数（最低2×ATR）或降低杠杆\n")
	sb.WriteString("   - 如果2×ATR仍超出强平价，说明杠杆过高，必须降低杠杆或放弃交易\n")
	sb.WriteString("   - 在reasoning中必须写明：\"强平价=X.XX, 止损X.XX在强平价范围内✓\"\n\n")
	sb.WriteString("3. 如果R/R < 2.0:\n")
	sb.WriteString("   - 趋势行情(A): 继续提高止盈倍数直到R/R≥2.0（最多到18x）\n")
	sb.WriteString("   - 震荡行情(B): 放弃该交易，寻找更好机会\n\n")
	sb.WriteString("4. ⚠️ 严禁使用圆整价格：\n")
	sb.WriteString("   - 计算R/R时必须使用「精确市价」(如0.1868)，不能用圆整价(如0.19)\n")
	sb.WriteString("   - 止损止盈保留足够精度：价格<1用4位小数，1-100用2位小数，>100用1位小数\n")
	sb.WriteString("   - 错误示例：用0.19计算R/R却实际市价是0.1868 ❌\n")
	sb.WriteString("```\n\n")

	sb.WriteString("**第五步：计算实际杠杆**\n")
	sb.WriteString("```\n")
	sb.WriteString(fmt.Sprintf("当前配置：BTC/ETH基础杠杆=%dx | 山寨币基础杠杆=%dx\n\n", btcEthLeverage, altcoinLeverage))
	sb.WriteString("实际杠杆 = 基础杠杆 × 波动率系数（向下取整）\n")
	sb.WriteString(fmt.Sprintf("示例: BNB(山寨) ATR%%=2.1%%(中波动) → 杠杆 = %d × 0.8 = %d×\n", altcoinLeverage, int(float64(altcoinLeverage)*0.8)))
	sb.WriteString("```\n\n")

	sb.WriteString("**⚠️ 强制要求**：\n")
	sb.WriteString("1. 在reasoning中必须写明：\"大盘体制:(A/B/C)，依据:[量化证据]\"\n")
	sb.WriteString("2. 在reasoning中必须写明：\"ATR%%=X.X%%(波动等级)，杠杆系数=X.X，实际杠杆=X×\"\n")
	sb.WriteString("3. 在reasoning中必须写明：\"止损倍数=X.Xx，止盈倍数=X.Xx（基础/调整后）\"\n")
	sb.WriteString("4. 在reasoning中必须写明：\"精确市价=X.XXXX | 止损:计算过程 | 止盈:计算过程\"\n")
	sb.WriteString("5. 在reasoning中必须写明：\"R/R验证:风险%%=X.XX%%, 收益%%=X.XX%%, R/R=X.X:1✓\"（必须使用精确市价计算）\n")
	sb.WriteString("6. 🚨 在reasoning中必须写明：\"强平价=X.XX, 止损X.XX在强平价范围内✓\"（强平价校验是强制的，不能跳过）\n")
	sb.WriteString("7. 止损止盈必须使用ATR公式的精确计算值，禁止圆整到整数或心理价位\n")
	sb.WriteString("8. 在JSON的leverage字段中，必须使用计算后的实际杠杆。\n\n")

	// === [资金费率、冷却期、夏普比率部分保持不变] ===

	sb.WriteString("## 💰 资金费率与OI过滤\n\n")
	sb.WriteString("**禁止开仓条件**（逆向拥挤）：\n")
	sb.WriteString("1. **做多时**: 资金费率>0.05% 且 OI_Top显示净多仓>60%\n")
	sb.WriteString("2. **做空时**: 资金费率<-0.05% 且 OI_Top显示净空仓>60%\n\n")
	sb.WriteString("**优先开仓条件**（逆向机会）：\n")
	sb.WriteString("1. **做多时**: 资金费率<-0.01% 且 净空仓>50%\n")
	sb.WriteString("2. **做空时**: 资金费率>0.02% 且 净多仓>50%\n\n")

	sb.WriteString("## ⏳ 冷却期与交易频率控制\n\n")
	sb.WriteString("1. **同币种冷却**: 平仓后20分钟内不得重新开仓同一币种。\n")
	sb.WriteString("2. **小时限制**: 每小时最多开仓2次（避免过度交易）。\n\n")

	sb.WriteString("**综合信心度计算**：\n")
	sb.WriteString("```\n")
	sb.WriteString("基础分60分 + 满足条件加分：\n")
	sb.WriteString("+ 市场体制与信号完美匹配 (A/B) +20分\n")
	sb.WriteString("+ 多指标共振（3个+10分，4个+15分）\n")
	sb.WriteString("+ 资金费率逆向机会 +10分\n")
	sb.WriteString("+ AI500或OI_Top双重标记 +10分\n")
	sb.WriteString("最终≥80分才开仓\n")
	sb.WriteString("```\n\n")

	sb.WriteString("# 🧬 夏普比率自我进化\n\n")
	sb.WriteString("你必须根据收到的**夏普比率**反馈调整你的激进程度：\n\n")
	sb.WriteString("**夏普比率 < -0.5** (持续亏损):\n")
	sb.WriteString("  → 🛑 停止交易，连续观望至少6个周期（18分钟）。\n")
	sb.WriteString("  → 🔍 深度反思：是否违反了(C)盘整区禁止开仓的规则？是否在(A)趋势中逆势交易？\n\n")
	sb.WriteString("**夏普比率 -0.5 ~ 0** (轻微亏损):\n")
	sb.WriteString("  → ⚠️ 严格控制：只做信心度>85的交易。只做(A)趋势市场策略。\n\n")
	sb.WriteString("**夏普比率 0 ~ 0.7** (正收益):\n")
	sb.WriteString("  → ✅ 维持当前策略，(A)和(B)体制均可参与。\n\n")
	sb.WriteString("**夏普比率 > 0.7** (优异表现):\n")
	sb.WriteString("  → 🚀 可适度扩大仓位，(A)和(B)体制均可参与。\n\n")

	// === 关键修改：强化输出格式示例, 展示量化证据 ===
	sb.WriteString("# 📤 输出格式\n\n")
	sb.WriteString("**第一步: 思维链（纯文本）**\n")
	sb.WriteString("简洁分析你的思考过程，必须包括对「市场体制」的量化判断。\n\n")
	sb.WriteString("**第二步: JSON决策数组**\n\n")
	sb.WriteString("```json\n[\n")
	// 更新示例，强调包含强平价校验
	sb.WriteString(fmt.Sprintf("  {\"symbol\": \"BTCUSDT\", \"action\": \"open_long\", \"leverage\": %d, \"position_size_usd\": %.0f, \"stop_loss\": 104800.00, \"take_profit\": 117800.00, \"confidence\": 90, \"risk_usd\": 320, \"reasoning\": \"大盘体制:BTC 4h ATR%%=1.8%%(>=1.0%%), MA(P>50>200)=true -> (A1)上升趋势 | ATR=800, 精确市价108200.00 | 止损:108200-(800*4)=104800 | 止盈:108200+(800*12)=117800 | R/R验证:风险%%=(108200-104800)/108200*100=3.14%%, 收益%%=(117800-108200)/108200*100=8.87%%, R/R=8.87/3.14=2.82:1✓ | 强平价=108200*(1-0.95/%d)=106037, 止损104800在强平价范围内✓ | 杠杆:ATR%%=1.8%%(低),系数1.0,杠杆=%dx\"},\n", btcEthLeverage, accountEquity*5, btcEthLeverage, btcEthLeverage))
	sb.WriteString("  {\"symbol\": \"ETHUSDT\", \"action\": \"close_long\", \"reasoning\": \"大盘体制:ETH 4h MA均线缠绕 -> (B)震荡。RSI触及上轨，止盈离场\"}\n")
	sb.WriteString("  {\"symbol\": \"SOLUSDT\", \"action\": \"wait\", \"reasoning\": \"大盘体制:BTC 4h ATR%%=0.8%%(<1.0%%) -> (C)窄幅盘整。禁止开仓，等待波动。\"}\n")
	sb.WriteString("]\n```\n\n")

	sb.WriteString("---\n\n")
	sb.WriteString("**记住**: \n")
	sb.WriteString("- **体制为王 (Regime is King)**：严格执行量化体制分析，(C)不动 (A)顺势 (B)谨慎。\n")
	sb.WriteString("- **风险回报比 ≥ 2.0:1 是硬约束**：计算完止损止盈后，必须验证R/R比，不满足就调整止盈倍数或放弃交易。\n")
	sb.WriteString("- 🚨 **强平价校验是生死线**：止损价必须在强平价范围内，否则止损永远无法触发！这是最严重的风险！\n")
	sb.WriteString("- **禁止圆整价格**：止损止盈必须使用ATR公式的精确值，不要圆整到整数或心理价位。\n")
	sb.WriteString("- 目标是夏普比率，不是交易频率。\n")

	return sb.String()
}

// buildUserPrompt 构建 User Prompt（动态数据）
func buildUserPrompt(ctx *Context) string {
	var sb strings.Builder

	// 系统状态
	sb.WriteString(fmt.Sprintf("**时间**: %s | **周期**: #%d | **运行**: %d分钟\n\n",
		ctx.CurrentTime, ctx.CallCount, ctx.RuntimeMinutes))

	// BTC 市场
	if btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]; hasBTC {
		sb.WriteString(fmt.Sprintf("**BTC**: %.2f (1h: %+.2f%%, 4h: %+.2f%%) | MACD: %.4f | RSI: %.2f\n\n",
			btcData.CurrentPrice, btcData.PriceChange1h, btcData.PriceChange4h,
			btcData.CurrentMACD, btcData.CurrentRSI7))
	}

	// 账户
	sb.WriteString(fmt.Sprintf("**账户**: 净值%.2f | 余额%.2f (%.1f%%) | 盈亏%+.2f%% | 保证金%.1f%% | 持仓%d个\n\n",
		ctx.Account.TotalEquity,
		ctx.Account.AvailableBalance,
		(ctx.Account.AvailableBalance/ctx.Account.TotalEquity)*100,
		ctx.Account.TotalPnLPct,
		ctx.Account.MarginUsedPct,
		ctx.Account.PositionCount))

	// 持仓（完整市场数据）
	if len(ctx.Positions) > 0 {
		sb.WriteString("## 当前持仓\n")
		for i, pos := range ctx.Positions {
			// 计算持仓时长
			holdingDuration := ""
			if pos.UpdateTime > 0 {
				durationMs := time.Now().UnixMilli() - pos.UpdateTime
				durationMin := durationMs / (1000 * 60) // 转换为分钟
				if durationMin < 60 {
					holdingDuration = fmt.Sprintf(" | 持仓时长%d分钟", durationMin)
				} else {
					durationHour := durationMin / 60
					durationMinRemainder := durationMin % 60
					holdingDuration = fmt.Sprintf(" | 持仓时长%d小时%d分钟", durationHour, durationMinRemainder)
				}
			}

			sb.WriteString(fmt.Sprintf("%d. %s %s | 入场价%.4f 当前价%.4f | 盈亏%+.2f%% | 杠杆%dx | 保证金%.0f | 强平价%.4f%s\n\n",
				i+1, pos.Symbol, strings.ToUpper(pos.Side),
				pos.EntryPrice, pos.MarkPrice, pos.UnrealizedPnLPct,
				pos.Leverage, pos.MarginUsed, pos.LiquidationPrice, holdingDuration))

			// 使用FormatMarketData输出完整市场数据
			if marketData, ok := ctx.MarketDataMap[pos.Symbol]; ok {
				sb.WriteString(market.Format(marketData))
				sb.WriteString("\n")
			}
		}
	} else {
		sb.WriteString("**当前持仓**: 无\n\n")
	}

	// 候选币种（完整市场数据）
	sb.WriteString(fmt.Sprintf("## 候选币种 (%d个)\n\n", len(ctx.MarketDataMap)))
	displayedCount := 0
	for _, coin := range ctx.CandidateCoins {
		marketData, hasData := ctx.MarketDataMap[coin.Symbol]
		if !hasData {
			continue
		}
		displayedCount++

		sourceTags := ""
		if len(coin.Sources) > 1 {
			sourceTags = " (AI500+OI_Top双重信号)"
		} else if len(coin.Sources) == 1 && coin.Sources[0] == "oi_top" {
			sourceTags = " (OI_Top持仓增长)"
		}

		// 使用FormatMarketData输出完整市场数据
		sb.WriteString(fmt.Sprintf("### %d. %s%s\n\n", displayedCount, coin.Symbol, sourceTags))
		sb.WriteString(market.Format(marketData))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	// 夏普比率（直接传值，不要复杂格式化）
	if ctx.Performance != nil {
		// 直接从interface{}中提取SharpeRatio
		type PerformanceData struct {
			SharpeRatio float64 `json:"sharpe_ratio"`
		}
		var perfData PerformanceData
		if jsonData, err := json.Marshal(ctx.Performance); err == nil {
			if err := json.Unmarshal(jsonData, &perfData); err == nil {
				sb.WriteString(fmt.Sprintf("## 📊 夏普比率: %.2f\n\n", perfData.SharpeRatio))
			}
		}
	}

	sb.WriteString("---\n\n")
	sb.WriteString("现在请分析并输出决策（思维链 + JSON）\n")

	return sb.String()
}

// parseFullDecisionResponse 解析AI的完整决策响应
func parseFullDecisionResponse(aiResponse string, accountEquity float64, btcEthLeverage, altcoinLeverage int, marketDataMap map[string]*market.Data) (*FullDecision, error) {
	// 1. 提取思维链
	cotTrace := extractCoTTrace(aiResponse)

	// 2. 提取JSON决策列表
	decisions, err := extractDecisions(aiResponse)
	if err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: []Decision{},
		}, fmt.Errorf("提取决策失败: %w\n\n=== AI思维链分析 ===\n%s", err, cotTrace)
	}

	// 3. 验证决策
	if err := validateDecisions(decisions, accountEquity, btcEthLeverage, altcoinLeverage, marketDataMap); err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: decisions,
		}, fmt.Errorf("决策验证失败: %w\n\n=== AI思维链分析 ===\n%s", err, cotTrace)
	}

	return &FullDecision{
		CoTTrace:  cotTrace,
		Decisions: decisions,
	}, nil
}

// extractCoTTrace 提取思维链分析
func extractCoTTrace(response string) string {
	// 查找JSON数组的开始位置（更稳健：匹配换行符后的 [ 或行首的 [）
	jsonStart := findJSONArrayStart(response)

	if jsonStart > 0 {
		// 思维链是JSON数组之前的内容
		return strings.TrimSpace(response[:jsonStart])
	}

	// 如果找不到JSON，整个响应都是思维链
	return strings.TrimSpace(response)
}

// findJSONArrayStart 查找JSON数组的真实起始位置
// 避免误判思维链中的普通列表（如 [1.0%, 2.0%]）
func findJSONArrayStart(response string) int {
	// 策略1: 查找代码块中的JSON (```json\n[ 或 ```\n[)
	codeBlockPatterns := []string{"```json\n[", "```\n["}
	for _, pattern := range codeBlockPatterns {
		if idx := strings.Index(response, pattern); idx != -1 {
			return idx + len(pattern) - 1 // 返回 [ 的位置
		}
	}

	// 策略2: 查找独立成行的 [ (换行符后紧跟[，前面只有空格)
	lines := strings.Split(response, "\n")
	currentPos := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 如果这行只有一个 [ 或者以 [ 开头且下一个字符是换行/空格/{ (JSON对象开始)
		if trimmed == "[" || (len(trimmed) > 0 && trimmed[0] == '[' &&
			(len(trimmed) == 1 || trimmed[1] == '\n' || trimmed[1] == ' ' || trimmed[1] == '{')) {
			// 找到这个 [ 在原字符串中的位置
			return currentPos + strings.Index(line, "[")
		}
		currentPos += len(line) + 1 // +1 for newline
	}

	// 策略3: 回退到原始方法（找第一个[，但至少我们尝试了更好的方法）
	return strings.Index(response, "[")
}

// extractDecisions 提取JSON决策列表
func extractDecisions(response string) ([]Decision, error) {
	// 使用更稳健的方法查找JSON数组
	arrayStart := findJSONArrayStart(response)
	if arrayStart == -1 {
		return nil, fmt.Errorf("无法找到JSON数组起始")
	}

	// 从 [ 开始，匹配括号找到对应的 ]
	arrayEnd := findMatchingBracket(response, arrayStart)
	if arrayEnd == -1 {
		return nil, fmt.Errorf("无法找到JSON数组结束")
	}

	jsonContent := strings.TrimSpace(response[arrayStart : arrayEnd+1])

	// 🔧 修复常见的JSON格式错误：缺少引号的字段值
	// 匹配: "reasoning": 内容"}  或  "reasoning": 内容}  (没有引号)
	// 修复为: "reasoning": "内容"}
	// 使用简单的字符串扫描而不是正则表达式
	jsonContent = fixMissingQuotes(jsonContent)

	// 解析JSON
	var decisions []Decision
	if err := json.Unmarshal([]byte(jsonContent), &decisions); err != nil {
		return nil, fmt.Errorf("JSON解析失败: %w\nJSON内容: %s", err, jsonContent)
	}

	return decisions, nil
}

// fixMissingQuotes 修复JSON格式错误
func fixMissingQuotes(jsonStr string) string {
	// 1. 替换中文引号为英文引号
	jsonStr = strings.ReplaceAll(jsonStr, "\u201c", "\"") // "
	jsonStr = strings.ReplaceAll(jsonStr, "\u201d", "\"") // "
	jsonStr = strings.ReplaceAll(jsonStr, "\u2018", "'")  // '
	jsonStr = strings.ReplaceAll(jsonStr, "\u2019", "'")  // '

	// 2. 修复缺少引号的字段值（简化方法：逐行处理）
	// 问题示例: "reasoning":持仓仅6分钟... 应该是 "reasoning":"持仓仅6分钟..."
	lines := strings.Split(jsonStr, "\n")
	for i, line := range lines {
		// 查找模式: "字段名": 后面没有 "、{、[、数字、true、false、null
		// 使用简单的字符串查找
		idx := strings.Index(line, "\":")
		if idx == -1 {
			continue
		}

		// 从冒号后开始检查
		afterColon := idx + 2
		// 跳过空白
		for afterColon < len(line) && (line[afterColon] == ' ' || line[afterColon] == '\t') {
			afterColon++
		}

		if afterColon >= len(line) {
			continue
		}

		ch := line[afterColon]
		// 检查是否是合法的JSON值开始字符
		isValidStart := ch == '"' || ch == '{' || ch == '[' ||
			ch == 't' || ch == 'f' || ch == 'n' ||
			(ch >= '0' && ch <= '9') || ch == '-'

		if !isValidStart {
			// 找到非法开始，需要添加引号
			// 找到值的结束位置（, 或 } 或 "）
			valueEnd := afterColon
			for valueEnd < len(line) {
				if line[valueEnd] == ',' || line[valueEnd] == '}' || line[valueEnd] == '"' {
					break
				}
				valueEnd++
			}

			// 重构这一行
			before := line[:afterColon]
			value := strings.TrimSpace(line[afterColon:valueEnd])
			after := line[valueEnd:]

			// 转义值中的双引号
			value = strings.ReplaceAll(value, "\"", "\\\"")

			lines[i] = before + "\"" + value + "\"" + after
		}
	}

	return strings.Join(lines, "\n")
}

// validateDecisions 验证所有决策（需要账户信息、杠杆配置和市场数据）
func validateDecisions(decisions []Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int, marketDataMap map[string]*market.Data) error {
	for i, decision := range decisions {
		if err := validateDecision(&decision, accountEquity, btcEthLeverage, altcoinLeverage, marketDataMap); err != nil {
			return fmt.Errorf("决策 #%d 验证失败: %w", i+1, err)
		}
	}
	return nil
}

// findMatchingBracket 查找匹配的右括号
func findMatchingBracket(s string, start int) int {
	if start >= len(s) || s[start] != '[' {
		return -1
	}

	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}

// validateDecision 验证单个决策的有效性（使用真实市价计算R/R）
func validateDecision(d *Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int, marketDataMap map[string]*market.Data) error {
	// 验证action
	validActions := map[string]bool{
		"open_long":   true,
		"open_short":  true,
		"close_long":  true,
		"close_short": true,
		"hold":        true,
		"wait":        true,
	}

	if !validActions[d.Action] {
		return fmt.Errorf("无效的action: %s", d.Action)
	}

	// 开仓操作必须提供完整参数
	if d.Action == "open_long" || d.Action == "open_short" {
		// 根据币种使用配置的杠杆上限
		maxLeverage := altcoinLeverage          // 山寨币使用配置的杠杆
		maxPositionValue := accountEquity * 1.5 // 山寨币最多1.5倍账户净值
		if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
			maxLeverage = btcEthLeverage          // BTC和ETH使用配置的杠杆
			maxPositionValue = accountEquity * 10 // BTC/ETH最多10倍账户净值
		}

		if d.Leverage <= 0 || d.Leverage > maxLeverage {
			return fmt.Errorf("杠杆必须在1-%d之间（%s，当前配置上限%d倍）: %d", maxLeverage, d.Symbol, maxLeverage, d.Leverage)
		}
		if d.PositionSizeUSD <= 0 {
			return fmt.Errorf("仓位大小必须大于0: %.2f", d.PositionSizeUSD)
		}
		// 验证仓位价值上限（加1%容差以避免浮点数精度问题）
		tolerance := maxPositionValue * 0.01 // 1%容差
		if d.PositionSizeUSD > maxPositionValue+tolerance {
			if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
				return fmt.Errorf("BTC/ETH单币种仓位价值不能超过%.0f USDT（10倍账户净值），实际: %.0f", maxPositionValue, d.PositionSizeUSD)
			} else {
				return fmt.Errorf("山寨币单币种仓位价值不能超过%.0f USDT（1.5倍账户净值），实际: %.0f", maxPositionValue, d.PositionSizeUSD)
			}
		}
		if d.StopLoss <= 0 || d.TakeProfit <= 0 {
			return fmt.Errorf("止损和止盈必须大于0")
		}

		// 验证止损止盈的合理性
		if d.Action == "open_long" {
			if d.StopLoss >= d.TakeProfit {
				return fmt.Errorf("做多时止损价必须小于止盈价")
			}
		} else {
			if d.StopLoss <= d.TakeProfit {
				return fmt.Errorf("做空时止损价必须大于止盈价")
			}
		}

		// ✅ 验证风险回报比（必须≥1:2，使用真实市价）
		// 获取当前市价
		marketData, exists := marketDataMap[d.Symbol]
		if !exists || marketData.CurrentPrice <= 0 {
			return fmt.Errorf("无法获取%s的当前市价", d.Symbol)
		}
		currentPrice := marketData.CurrentPrice

		var riskPercent, rewardPercent, riskRewardRatio float64
		if d.Action == "open_long" {
			// 做多：风险 = 当前价 - 止损价，收益 = 止盈价 - 当前价
			riskPercent = (currentPrice - d.StopLoss) / currentPrice * 100
			rewardPercent = (d.TakeProfit - currentPrice) / currentPrice * 100
			if riskPercent > 0 {
				riskRewardRatio = rewardPercent / riskPercent
			}
		} else {
			// 做空：风险 = 止损价 - 当前价，收益 = 当前价 - 止盈价
			riskPercent = (d.StopLoss - currentPrice) / currentPrice * 100
			rewardPercent = (currentPrice - d.TakeProfit) / currentPrice * 100
			if riskPercent > 0 {
				riskRewardRatio = rewardPercent / riskPercent
			}
		}

		// 硬约束：风险回报比必须≥2.0（使用统一常量）
		// 由于浮点数精度问题和ATR计算的四舍五入，给予5%的容差
		minRiskRewardRatio := agents.MinRiskReward * (1.0 - agents.RRFloatTolerance) // 2.0 * 0.95 = 1.90
		if riskRewardRatio < minRiskRewardRatio {
			return fmt.Errorf("风险回报比过低(%.2f:1)，必须≥%.1f:1 [当前价:%.4f 风险:%.2f%% 收益:%.2f%%] [止损:%.2f 止盈:%.2f]",
				riskRewardRatio, agents.MinRiskReward, currentPrice, riskPercent, rewardPercent, d.StopLoss, d.TakeProfit)
		}

		// 🚨 硬约束：强平价校验（防止止损失效导致100%保证金损失）
		// 这是最关键的风控检查，必须在Go代码中独立验证，不能信任AI的计算
		var liquidationPrice float64
		// 使用统一的强平保证金率常量
		marginRate := agents.LiquidationMarginRate / float64(d.Leverage)

		if d.Action == "open_long" {
			// 做多: 强平价 = 入场价 * (1 - marginRate)
			liquidationPrice = currentPrice * (1.0 - marginRate)
			// 做多止损必须高于强平价，否则会先被强平而不是止损
			if d.StopLoss <= liquidationPrice {
				return fmt.Errorf("🚨 致命错误：做多止损价(%.4f)低于或等于估算的强平价(%.4f)，止损将失效，仓位会被强制平仓导致100%%保证金损失！[当前价:%.4f 杠杆:%dx]",
					d.StopLoss, liquidationPrice, currentPrice, d.Leverage)
			}
		} else if d.Action == "open_short" {
			// 做空: 强平价 = 入场价 * (1 + marginRate)
			liquidationPrice = currentPrice * (1.0 + marginRate)
			// 做空止损必须低于强平价，否则会先被强平而不是止损
			if d.StopLoss >= liquidationPrice {
				return fmt.Errorf("🚨 致命错误：做空止损价(%.4f)高于或等于估算的强平价(%.4f)，止损将失效，仓位会被强制平仓导致100%%保证金损失！[当前价:%.4f 杠杆:%dx]",
					d.StopLoss, liquidationPrice, currentPrice, d.Leverage)
			}
		}
	}

	return nil
}

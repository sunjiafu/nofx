package trader

import (
	"encoding/json"
	"fmt"
	"log"
	"nofx/decision"
	"nofx/logger"
	"nofx/market"
	"nofx/mcp"
	"nofx/memory"
	"nofx/pool"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AutoTraderConfig 自动交易配置（简化版 - AI全权决策）
type AutoTraderConfig struct {
	// Trader标识
	ID        string // Trader唯一标识（用于日志目录等）
	Name      string // Trader显示名称
	AIModel   string // AI模型: "qwen" 或 "deepseek"
	QwenModel string // Qwen模型具体版本（qwen-plus/qwen-max等）

	// 交易平台选择
	Exchange string // "binance", "hyperliquid" 或 "aster"

	// 币安API配置
	BinanceAPIKey    string
	BinanceSecretKey string
	BinanceTestnet   bool // 是否使用币安测试网

	// Hyperliquid配置
	HyperliquidPrivateKey string
	HyperliquidWalletAddr string
	HyperliquidTestnet    bool

	// Aster配置
	AsterUser       string // Aster主钱包地址
	AsterSigner     string // Aster API钱包地址
	AsterPrivateKey string // Aster API钱包私钥

	CoinPoolAPIURL string

	// AI配置
	UseQwen     bool
	DeepSeekKey string
	QwenKey     string

	// 自定义AI API配置
	CustomAPIURL    string
	CustomAPIKey    string
	CustomModelName string

	// 扫描配置
	ScanInterval time.Duration // 扫描间隔（建议3分钟）
	KlineInterval string        // K线周期（如 "5m", "10m", "15m"）

	// 账户配置
	InitialBalance float64 // 初始金额（用于计算盈亏，需手动设置）

	// 杠杆配置
	BTCETHLeverage  int // BTC和ETH的杠杆倍数
	AltcoinLeverage int // 山寨币的杠杆倍数

	// 风险控制（仅作为提示，AI可自主决定）
	MaxDailyLoss    float64       // 最大日亏损百分比（提示）
	MaxDrawdown     float64       // 最大回撤百分比（提示）
	StopTradingTime time.Duration // 触发风控后暂停时长
}

// AutoTrader 自动交易器
type AutoTrader struct {
	id                    string // Trader唯一标识
	name                  string // Trader显示名称
	aiModel               string // AI模型名称
	exchange              string // 交易平台名称
	config                AutoTraderConfig
	trader                Trader // 使用Trader接口（支持多平台）
	mcpClient             *mcp.Client
	decisionLogger        *logger.DecisionLogger // 决策日志记录器
	constraints           *TradingConstraints    // 交易硬约束管理器
	memoryManager         *memory.Manager        // 🧠 记忆管理器（Sprint 1）
	initialBalance        float64
	dailyPnL              float64
	lastResetTime         time.Time
	stopUntil             time.Time
	isRunning             bool
	startTime             time.Time        // 系统启动时间
	callCount             int              // AI调用次数
	positionFirstSeenTime map[string]int64 // 持仓首次出现时间 (symbol_side -> timestamp毫秒)
	lastPositionSnapshot  map[string]decision.PositionInfo
	manualCloseTracker    map[string]time.Time // 手动/程序主动平仓的时间戳，用于与止损触发区分

	// 山寨币异动扫描（WebSocket方案 - 只观察不交易）
	altcoinWSMonitor       *market.AltcoinWSMonitor
	altcoinScanner         *market.AltcoinScanner
	altcoinLogger          *market.AltcoinSignalLogger
	spotFuturesMonitor     *market.SpotFuturesMonitor  // 现货期货价差监控
	altcoinScanEnabled     bool // 是否启用山寨币扫描
}

// NewAutoTrader 创建自动交易器
func NewAutoTrader(config AutoTraderConfig) (*AutoTrader, error) {
	// 设置默认值
	if config.ID == "" {
		config.ID = "default_trader"
	}
	if config.Name == "" {
		config.Name = "Default Trader"
	}
	if config.AIModel == "" {
		if config.UseQwen {
			config.AIModel = "qwen"
		} else {
			config.AIModel = "deepseek"
		}
	}

	mcpClient := mcp.New()

	// 初始化AI
	if config.AIModel == "custom" {
		// 使用自定义API
		mcpClient.SetCustomAPI(config.CustomAPIURL, config.CustomAPIKey, config.CustomModelName)
		log.Printf("🤖 [%s] 使用自定义AI API: %s (模型: %s)", config.Name, config.CustomAPIURL, config.CustomModelName)
	} else if config.UseQwen || config.AIModel == "qwen" {
		// 使用Qwen
		mcpClient.SetQwenAPIKey(config.QwenKey, "")
		if config.QwenModel != "" {
			mcpClient.Model = config.QwenModel
		}
		log.Printf("🤖 [%s] 使用阿里云Qwen AI (模型: %s)", config.Name, mcpClient.Model)
	} else {
		// 默认使用DeepSeek
		mcpClient.SetDeepSeekAPIKey(config.DeepSeekKey)
		log.Printf("🤖 [%s] 使用DeepSeek AI", config.Name)
	}

	// 初始化币种池API
	if config.CoinPoolAPIURL != "" {
		pool.SetCoinPoolAPI(config.CoinPoolAPIURL)
	}

	// 设置默认交易平台
	if config.Exchange == "" {
		config.Exchange = "binance"
	}

	// 根据配置创建对应的交易器
	var trader Trader
	var err error

	switch config.Exchange {
	case "binance":
		log.Printf("🏦 [%s] 使用币安合约交易", config.Name)
		trader = NewFuturesTrader(config.BinanceAPIKey, config.BinanceSecretKey, config.BinanceTestnet)
	case "hyperliquid":
		log.Printf("🏦 [%s] 使用Hyperliquid交易", config.Name)
		trader, err = NewHyperliquidTrader(config.HyperliquidPrivateKey, config.HyperliquidWalletAddr, config.HyperliquidTestnet)
		if err != nil {
			return nil, fmt.Errorf("初始化Hyperliquid交易器失败: %w", err)
		}
	case "aster":
		log.Printf("🏦 [%s] 使用Aster交易", config.Name)
		trader, err = NewAsterTrader(config.AsterUser, config.AsterSigner, config.AsterPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("初始化Aster交易器失败: %w", err)
		}
	case "mock":
		log.Printf("🧪 [%s] 使用本地模拟交易（真实市场数据）", config.Name)
		trader = NewMockTrader(config.InitialBalance)
	default:
		return nil, fmt.Errorf("不支持的交易平台: %s", config.Exchange)
	}

	// 验证初始金额配置
	if config.InitialBalance <= 0 {
		return nil, fmt.Errorf("初始金额必须大于0，请在配置中设置InitialBalance")
	}

	// 初始化决策日志记录器（使用trader ID创建独立目录）
	logDir := fmt.Sprintf("decision_logs/%s", config.ID)
	decisionLogger := logger.NewDecisionLogger(logDir)

	// 初始化交易硬约束管理器
	constraints := NewTradingConstraints()
	log.Printf("🛡️ [%s] 硬约束已启用: 冷却期20分钟 | 日上限999次 | 时上限3次 | 最短持仓15分钟", config.Name)

	// 🧠 初始化AI记忆系统（Sprint 1）
	memoryManager, err := memory.NewManager(config.ID)
	if err != nil {
		return nil, fmt.Errorf("初始化记忆系统失败: %w", err)
	}

	// 🔧 从历史日志恢复周期编号（防止重启后周期编号混乱）
	lastCycleNumber := recoverLastCycleNumber(logDir)

	// 🔍 初始化山寨币异动扫描器（WebSocket方案 - 只观察不交易）
	var altcoinWSMonitor *market.AltcoinWSMonitor
	var altcoinScanner *market.AltcoinScanner
	var altcoinLogger *market.AltcoinSignalLogger
	var spotFuturesMonitor *market.SpotFuturesMonitor // 🆕 现货期货价差监控
	altcoinScanEnabled := true // 🚀 启用WebSocket方案

	if config.Exchange == "binance" && altcoinScanEnabled {
		// 获取Binance客户端
		if binanceTrader, ok := trader.(*FuturesTrader); ok {
			// 初始化WebSocket监控器（实时获取市场数据，不消耗REST API）
			altcoinWSMonitor = market.NewAltcoinWSMonitor()

			// 初始化扫描器（用于分析异动信号）
			altcoinScanner = market.NewAltcoinScanner(binanceTrader.client)

			// 创建山寨币信号日志目录
			altcoinLogDir := fmt.Sprintf("altcoin_logs/%s", config.ID)
			var err error
			altcoinLogger, err = market.NewAltcoinSignalLogger(altcoinLogDir)
			if err != nil {
				log.Printf("⚠️  创建山寨币日志失败: %v，将禁用扫描功能", err)
				altcoinScanEnabled = false
			} else {
				log.Printf("🔍 [%s] 山寨币异动扫描已启用 (WebSocket方案 - 零API消耗)", config.Name)

				// 🆕 初始化现货期货价差监控器（早期信号）
				spotFuturesMonitor = market.NewSpotFuturesMonitor(
					config.BinanceAPIKey,
					config.BinanceSecretKey,
					binanceTrader.client,
					altcoinWSMonitor,
				)
				log.Printf("📊 [%s] 现货期货价差监控已启用（捕捉DEX/现货先行信号）", config.Name)
			}
		}
	}

	// 🎯 设置全局K线周期（根据配置）
	market.SetDefaultInterval(config.KlineInterval)

	return &AutoTrader{
		id:                    config.ID,
		name:                  config.Name,
		aiModel:               config.AIModel,
		exchange:              config.Exchange,
		config:                config,
		trader:                trader,
		mcpClient:             mcpClient,
		decisionLogger:        decisionLogger,
		constraints:           constraints,
		memoryManager:         memoryManager, // 🧠 记忆系统
		initialBalance:        config.InitialBalance,
		lastResetTime:         time.Now(),
		startTime:             time.Now(),
		callCount:             lastCycleNumber, // 从历史日志恢复
		isRunning:             false,
		positionFirstSeenTime: make(map[string]int64),
		lastPositionSnapshot:  make(map[string]decision.PositionInfo),
		manualCloseTracker:    make(map[string]time.Time),
		altcoinWSMonitor:      altcoinWSMonitor,      // WebSocket监控器
		altcoinScanner:        altcoinScanner,        // 山寨币扫描器
		altcoinLogger:         altcoinLogger,         // 信号日志器
		spotFuturesMonitor:    spotFuturesMonitor,    // 🆕 现货期货价差监控
		altcoinScanEnabled:    altcoinScanEnabled,
	}, nil
}

// Run 运行自动交易主循环
func (at *AutoTrader) Run() error {
	at.isRunning = true
	log.Println("🚀 AI驱动自动交易系统启动")
	log.Printf("💰 初始余额: %.2f USDT", at.initialBalance)
	log.Printf("⚙️  扫描间隔: %v", at.config.ScanInterval)
	log.Println("🤖 AI将全权决定杠杆、仓位大小、止损止盈等参数")

	// 启动山寨币WebSocket监控器（独立运行，实时获取市场数据）
	if at.altcoinScanEnabled && at.altcoinWSMonitor != nil {
		log.Println("🔌 启动WebSocket监控器（实时追踪所有USDT合约）...")
		if err := at.altcoinWSMonitor.Start(); err != nil {
			log.Printf("⚠️  WebSocket启动失败: %v，将禁用扫描功能", err)
			at.altcoinScanEnabled = false
		}
	}

	// 启动山寨币异动扫描goroutine（独立运行，每30分钟扫描一次）
	if at.altcoinScanEnabled && at.altcoinScanner != nil {
		log.Println("🔍 启动山寨币异动扫描（每30分钟扫描一次WebSocket提供的Top50）...")
		go at.runAltcoinScanner()
	}

	ticker := time.NewTicker(at.config.ScanInterval)
	defer ticker.Stop()

	// 首次立即执行
	if err := at.runCycle(); err != nil {
		log.Printf("❌ 执行失败: %v", err)
	}

	for at.isRunning {
		select {
		case <-ticker.C:
			if err := at.runCycle(); err != nil {
				log.Printf("❌ 执行失败: %v", err)
			}
		}
	}

	// 关闭WebSocket监控器
	if at.altcoinWSMonitor != nil {
		at.altcoinWSMonitor.Stop()
	}

	// 关闭日志文件
	if at.altcoinLogger != nil {
		at.altcoinLogger.Close()
	}

	return nil
}

// Stop 停止自动交易
func (at *AutoTrader) Stop() {
	at.isRunning = false

	// 停止WebSocket监控器
	if at.altcoinWSMonitor != nil {
		at.altcoinWSMonitor.Stop()
	}

	log.Println("⏹ 自动交易系统停止")
}

// runCycle 运行一个交易周期（使用AI全权决策）
func (at *AutoTrader) runCycle() error {
	at.callCount++

	log.Print("\n" + strings.Repeat("=", 70))
	log.Printf("⏰ %s - AI决策周期 #%d", time.Now().Format("2006-01-02 15:04:05"), at.callCount)
	log.Print(strings.Repeat("=", 70))

	// 创建决策记录
	record := &logger.DecisionRecord{
		CycleNumber:  at.callCount, // 🔧 修复：使用callCount作为周期号，确保同一周期的多次日志记录使用相同的周期号
		ExecutionLog: []string{},
		Success:      true,
	}

	// 1. 检查是否需要停止交易
	if time.Now().Before(at.stopUntil) {
		remaining := at.stopUntil.Sub(time.Now())
		log.Printf("⏸ 风险控制：暂停交易中，剩余 %.0f 分钟", remaining.Minutes())
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("风险控制暂停中，剩余 %.0f 分钟", remaining.Minutes())
		at.decisionLogger.LogDecision(record)
		return nil
	}

	// 2. 重置日盈亏（每天重置）
	if time.Since(at.lastResetTime) > 24*time.Hour {
		at.dailyPnL = 0
		at.lastResetTime = time.Now()
		log.Println("📅 日盈亏已重置")
	}

	// 3. 收集交易上下文
	ctx, err := at.buildTradingContext()
	if err != nil {
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("构建交易上下文失败: %v", err)
		at.decisionLogger.LogDecision(record)
		return fmt.Errorf("构建交易上下文失败: %w", err)
	}

	// 🧠 注入AI记忆（Sprint 1）
	ctx.MemoryPrompt = at.memoryManager.GetContextPrompt()

	// 保存账户状态快照
	record.AccountState = logger.AccountSnapshot{
		TotalBalance:          ctx.Account.TotalEquity,
		AvailableBalance:      ctx.Account.AvailableBalance,
		TotalUnrealizedProfit: ctx.Account.TotalPnL,
		PositionCount:         ctx.Account.PositionCount,
		MarginUsedPct:         ctx.Account.MarginUsedPct,
	}

	// 保存持仓快照
	for _, pos := range ctx.Positions {
		record.Positions = append(record.Positions, logger.PositionSnapshot{
			Symbol:           pos.Symbol,
			Side:             pos.Side,
			PositionAmt:      pos.Quantity,
			EntryPrice:       pos.EntryPrice,
			MarkPrice:        pos.MarkPrice,
			UnrealizedProfit: pos.UnrealizedPnL,
			Leverage:         float64(pos.Leverage),
			LiquidationPrice: pos.LiquidationPrice,
		})
	}

	// 保存候选币种列表
	for _, coin := range ctx.CandidateCoins {
		record.CandidateCoins = append(record.CandidateCoins, coin.Symbol)
	}

	log.Printf("📊 账户净值: %.2f USDT | 可用: %.2f USDT | 持仓: %d",
		ctx.Account.TotalEquity, ctx.Account.AvailableBalance, ctx.Account.PositionCount)

	// ✅ 修复: 检查风险控制参数（MaxDailyLoss、MaxDrawdown）
	if at.config.MaxDailyLoss > 0 || at.config.MaxDrawdown > 0 {
		// 计算日盈亏百分比
		dailyPnLPct := 0.0
		if at.initialBalance > 0 {
			dailyPnLPct = (at.dailyPnL / at.initialBalance) * 100
		}

		// 计算最大回撤百分比
		drawdownPct := 0.0
		if at.initialBalance > 0 && ctx.Account.TotalEquity < at.initialBalance {
			drawdownPct = ((at.initialBalance - ctx.Account.TotalEquity) / at.initialBalance) * 100
		}

		log.Printf("📊 风险监控: 日盈亏%.2f%% (限制%.0f%%) | 回撤%.2f%% (限制%.0f%%)",
			dailyPnLPct, at.config.MaxDailyLoss, drawdownPct, at.config.MaxDrawdown)

		// 检查日亏损限制
		if at.config.MaxDailyLoss > 0 && dailyPnLPct < -at.config.MaxDailyLoss {
			at.stopUntil = time.Now().Add(at.config.StopTradingTime)
			log.Printf("🛑 风险控制触发: 日亏损%.2f%% 超过限制%.0f%%, 暂停交易%.0f分钟",
				dailyPnLPct, at.config.MaxDailyLoss, at.config.StopTradingTime.Minutes())
			record.Success = false
			record.ErrorMessage = fmt.Sprintf("日亏损%.2f%% 超限，暂停交易", dailyPnLPct)
			at.decisionLogger.LogDecision(record)
			return nil
		}

		// 检查最大回撤限制
		if at.config.MaxDrawdown > 0 && drawdownPct > at.config.MaxDrawdown {
			at.stopUntil = time.Now().Add(at.config.StopTradingTime)
			log.Printf("🛑 风险控制触发: 回撤%.2f%% 超过限制%.0f%%, 暂停交易%.0f分钟",
				drawdownPct, at.config.MaxDrawdown, at.config.StopTradingTime.Minutes())
			record.Success = false
			record.ErrorMessage = fmt.Sprintf("回撤%.2f%% 超限，暂停交易", drawdownPct)
			at.decisionLogger.LogDecision(record)
			return nil
		}
	}

	// 4. 调用AI获取完整决策
	log.Println("🤖 正在请求AI分析并决策...")
	decision, err := decision.GetFullDecision(ctx, at.mcpClient)

	// 即使有错误，也保存思维链、决策和输入prompt（用于debug）
	if decision != nil {
		record.InputPrompt = decision.UserPrompt
		record.CoTTrace = decision.CoTTrace
		if len(decision.Decisions) > 0 {
			decisionJSON, _ := json.MarshalIndent(decision.Decisions, "", "  ")
			record.DecisionJSON = string(decisionJSON)
		}
	}

	if err != nil {
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("获取AI决策失败: %v", err)

		// 打印AI思维链（即使有错误）
		if decision != nil && decision.CoTTrace != "" {
			log.Print("\n" + strings.Repeat("-", 70))
			log.Println("💭 AI思维链分析（错误情况）:")
			log.Println(strings.Repeat("-", 70))
			log.Println(decision.CoTTrace)
			log.Print(strings.Repeat("-", 70) + "\n")
		}

		at.decisionLogger.LogDecision(record)
		return fmt.Errorf("获取AI决策失败: %w", err)
	}

	// 5. 打印AI思维链
	log.Print("\n" + strings.Repeat("-", 70))
	log.Println("💭 AI思维链分析:")
	log.Println(strings.Repeat("-", 70))
	log.Println(decision.CoTTrace)
	log.Print(strings.Repeat("-", 70) + "\n")

	// 6. 打印AI决策
	log.Printf("📋 AI决策列表 (%d 个):\n", len(decision.Decisions))
	for i, d := range decision.Decisions {
		log.Printf("  [%d] %s: %s - %s", i+1, d.Symbol, d.Action, d.Reasoning)
		if d.Action == "open_long" || d.Action == "open_short" {
			log.Printf("      杠杆: %dx | 仓位: %.2f USDT | 止损: %.4f | 止盈: %.4f",
				d.Leverage, d.PositionSizeUSD, d.StopLoss, d.TakeProfit)
		}
	}
	log.Println()

	// 7. 对决策排序：确保先平仓后开仓（防止仓位叠加超限）
	sortedDecisions := sortDecisionsByPriority(decision.Decisions)

	log.Println("🔄 执行顺序（已优化）: 先平仓→后开仓")
	for i, d := range sortedDecisions {
		log.Printf("  [%d] %s %s", i+1, d.Symbol, d.Action)
	}
	log.Println()

	// 执行决策并记录结果
	for _, d := range sortedDecisions {
		actionRecord := logger.DecisionAction{
			Action:    d.Action,
			Symbol:    d.Symbol,
			Quantity:  0,
			Leverage:  d.Leverage,
			Price:     0,
			Timestamp: time.Now(),
			Success:   false,
			Reasoning: d.Reasoning, // ✅ NEW: 添加平仓原因
		}

		if err := at.executeDecisionWithRecord(&d, &actionRecord); err != nil {
			log.Printf("❌ 执行决策失败 (%s %s): %v", d.Symbol, d.Action, err)
			actionRecord.Error = err.Error()
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("❌ %s %s 失败: %v", d.Symbol, d.Action, err))
		} else {
			actionRecord.Success = true
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("✓ %s %s 成功", d.Symbol, d.Action))

			// 🧠 记录到AI记忆（Sprint 1）
			if d.Action != "hold" && d.Action != "wait" {
				tradeEntry := at.buildTradeEntry(&d, &actionRecord, ctx)
				if err := at.memoryManager.AddTrade(tradeEntry); err != nil {
					log.Printf("⚠️  记录交易到记忆失败: %v", err)
				}
			}

			// 成功执行后短暂延迟
			time.Sleep(1 * time.Second)
		}

		record.Decisions = append(record.Decisions, actionRecord)
	}

	// 8. 保存决策记录
	if err := at.decisionLogger.LogDecision(record); err != nil {
		log.Printf("⚠ 保存决策记录失败: %v", err)
	}

	return nil
}

// buildTradingContext 构建交易上下文
func (at *AutoTrader) buildTradingContext() (*decision.Context, error) {
	// 1. 获取账户信息
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("获取账户余额失败: %w", err)
	}

	// 获取账户字段
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Total Equity = 钱包余额 + 未实现盈亏
	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 2. 获取持仓信息
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	var positionInfos []decision.PositionInfo
	totalMarginUsed := 0.0

	// 当前持仓的key集合（用于清理已平仓的记录）
	currentPositionKeys := make(map[string]bool)

	newSnapshot := make(map[string]decision.PositionInfo)

	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity // 空仓数量为负，转为正数
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		// 计算占用保证金（估算）
		leverage := 10 // 默认值，实际应该从持仓信息获取
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed

		// 计算盈亏百分比
		pnlPct := 0.0
		if side == "long" {
			pnlPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			pnlPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		// 跟踪持仓首次出现时间
		posKey := symbol + "_" + side
		currentPositionKeys[posKey] = true
		if _, exists := at.positionFirstSeenTime[posKey]; !exists {
			// ⚠️ 检测到"新"持仓（可能是系统重启后的现有持仓）
			// 使用保守估计：假设已持仓60分钟（避免将旧持仓误判为"0分钟新持仓"）
			// 这样AI不会错误地应用"<30分钟必须HOLD"规则
			estimatedOpenTime := time.Now().Add(-60 * time.Minute).UnixMilli()
			at.positionFirstSeenTime[posKey] = estimatedOpenTime
			log.Printf("⚠️  [%s %s] 首次检测到此持仓，估算开仓时间为60分钟前（可能是系统重启）", symbol, side)
		}
		updateTime := at.positionFirstSeenTime[posKey]

		// 🆕 从TradingConstraints获取真实的开仓时间
		openTime := at.constraints.GetPositionOpenTime(symbol, side)
		if openTime.IsZero() {
			// 如果constraints中没有记录（可能是系统重启前的持仓），使用估算的时间
			openTime = time.UnixMilli(updateTime)
		}

		posInfo := decision.PositionInfo{
			Symbol:           symbol,
			Side:             side,
			EntryPrice:       entryPrice,
			MarkPrice:        markPrice,
			Quantity:         quantity,
			Leverage:         leverage,
			UnrealizedPnL:    unrealizedPnl,
			UnrealizedPnLPct: pnlPct,
			LiquidationPrice: liquidationPrice,
			MarginUsed:       marginUsed,
			UpdateTime:       updateTime,
			OpenTime:         openTime, // 🆕 开仓时间
		}

		positionInfos = append(positionInfos, posInfo)
		newSnapshot[posKey] = posInfo
	}

	// 检测已消失的持仓（例如止损/强平生效）
	for key, last := range at.lastPositionSnapshot {
		if !currentPositionKeys[key] {
			isManualClose := false
			if ts, ok := at.manualCloseTracker[key]; ok && time.Since(ts) < 2*time.Minute {
				log.Printf("📤 持仓已主动平仓: %s %s | 入场价 %.4f | 上次价格 %.4f | 未实现盈亏 %.2f%%",
					last.Symbol, strings.ToUpper(last.Side), last.EntryPrice, last.MarkPrice, last.UnrealizedPnLPct)
				delete(at.manualCloseTracker, key)
				isManualClose = true
			} else {
				log.Printf("🚨 检测到持仓消失，可能为止损/强平触发: %s %s | 入场价 %.4f | 上次价格 %.4f | 未实现盈亏 %.2f%%",
					last.Symbol, strings.ToUpper(last.Side), last.EntryPrice, last.MarkPrice, last.UnrealizedPnLPct)
			}

			// 🧠 记录止损/止盈到AI记忆
			if !isManualClose {
				// 构建交易记录
				holdMinutes := 0
				if !last.OpenTime.IsZero() {
					holdMinutes = int(time.Since(last.OpenTime).Minutes())
				}

				result := "break_even"
				if last.UnrealizedPnLPct > 0.1 {
					result = "win"
				} else if last.UnrealizedPnLPct < -0.1 {
					result = "loss"
				}

				// 推断止损还是止盈
				triggerType := "止损"
				if last.UnrealizedPnLPct > 0 {
					triggerType = "止盈"
				}

				tradeEntry := memory.TradeEntry{
					Cycle:       at.callCount,
					Timestamp:   time.Now(),
					Action:      "close",
					Symbol:      last.Symbol,
					Side:        last.Side,
					Signals:     []string{triggerType + "自动触发"},
					Reasoning:   fmt.Sprintf("%s自动触发（持仓消失，未经主动平仓决策）", triggerType),
					EntryPrice:  last.EntryPrice,
					ExitPrice:   last.MarkPrice,
					PositionPct: (last.MarginUsed / totalEquity) * 100,
					Leverage:    last.Leverage,
					HoldMinutes: holdMinutes,
					ReturnPct:   last.UnrealizedPnLPct,
					Result:      result,
				}

				if err := at.memoryManager.AddTrade(tradeEntry); err != nil {
					log.Printf("⚠️  记录止损/止盈到记忆失败: %v", err)
				} else {
					log.Printf("✅ 已记录%s到交易记忆：%s %s, 收益%.2f%%",
						triggerType, last.Symbol, last.Side, last.UnrealizedPnLPct)
				}
			}
		}
	}
	at.lastPositionSnapshot = newSnapshot

	// 清理已平仓的持仓记录
	for key := range at.positionFirstSeenTime {
		if !currentPositionKeys[key] {
			delete(at.positionFirstSeenTime, key)
		}
	}

	for key, ts := range at.manualCloseTracker {
		if time.Since(ts) > 10*time.Minute {
			delete(at.manualCloseTracker, key)
		}
	}

	// 3. 获取合并的候选币种池（AI500 + OI Top，去重）
	// 无论有没有持仓，都分析相同数量的币种（让AI看到所有好机会）
	// AI会根据保证金使用率和现有持仓情况，自己决定是否要换仓
	const ai500Limit = 20 // AI500取前20个评分最高的币种

	// 获取合并后的币种池（AI500 + OI Top）
	mergedPool, err := pool.GetMergedCoinPool(ai500Limit)
	if err != nil {
		return nil, fmt.Errorf("获取合并币种池失败: %w", err)
	}

	// 构建候选币种列表（包含来源信息）
	var candidateCoins []decision.CandidateCoin
	for _, symbol := range mergedPool.AllSymbols {
		sources := mergedPool.SymbolSources[symbol]
		candidateCoins = append(candidateCoins, decision.CandidateCoin{
			Symbol:  symbol,
			Sources: sources, // "ai500" 和/或 "oi_top"
		})
	}

	log.Printf("📋 合并币种池: AI500前%d + OI_Top20 = 总计%d个候选币种",
		ai500Limit, len(candidateCoins))

	// 4. 计算总盈亏
	totalPnL := totalEquity - at.initialBalance
	totalPnLPct := 0.0
	if at.initialBalance > 0 {
		totalPnLPct = (totalPnL / at.initialBalance) * 100
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	// 5. 分析历史表现（最近100个周期，避免长期持仓的交易记录丢失）
	// 假设每3分钟一个周期，100个周期 = 5小时，足够覆盖大部分交易
	performance, err := at.decisionLogger.AnalyzePerformance(100)
	if err != nil {
		log.Printf("⚠️  分析历史表现失败: %v", err)
		// 不影响主流程，继续执行（但设置performance为nil以避免传递错误数据）
		performance = nil
	}

	// 🧠 获取交易员记忆（实际交易历史）
	var memoryPrompt string
	if at.memoryManager != nil {
		memoryPrompt = at.memoryManager.GetContextPrompt()
	}

	// 6. 构建上下文
	ctx := &decision.Context{
		CurrentTime:     time.Now().Format("2006-01-02 15:04:05"),
		RuntimeMinutes:  int(time.Since(at.startTime).Minutes()),
		CallCount:       at.callCount,
		BTCETHLeverage:  at.config.BTCETHLeverage,  // 使用配置的杠杆倍数
		AltcoinLeverage: at.config.AltcoinLeverage, // 使用配置的杠杆倍数
		Account: decision.AccountInfo{
			TotalEquity:      totalEquity,
			AvailableBalance: availableBalance,
			TotalPnL:         totalPnL,
			TotalPnLPct:      totalPnLPct,
			MarginUsed:       totalMarginUsed,
			MarginUsedPct:    marginUsedPct,
			PositionCount:    len(positionInfos),
		},
		Positions:      positionInfos,
		CandidateCoins: candidateCoins,
		Performance:    performance,   // 添加历史表现分析
		MemoryPrompt:   memoryPrompt, // 🧠 注入交易员记忆
	}

	return ctx, nil
}

// executeDecisionWithRecord 执行AI决策并记录详细信息
func (at *AutoTrader) executeDecisionWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	switch decision.Action {
	case "open_long":
		return at.executeOpenLongWithRecord(decision, actionRecord)
	case "open_short":
		return at.executeOpenShortWithRecord(decision, actionRecord)
	case "close_long":
		return at.executeCloseLongWithRecord(decision, actionRecord)
	case "close_short":
		return at.executeCloseShortWithRecord(decision, actionRecord)
	case "hold", "wait":
		// 无需执行，仅记录
		return nil
	default:
		return fmt.Errorf("未知的action: %s", decision.Action)
	}
}

// executeOpenLongWithRecord 执行开多仓并记录详细信息
func (at *AutoTrader) executeOpenLongWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📈 开多仓: %s", decision.Symbol)

	// ⚠️ 先获取当前持仓信息（用于硬约束检查和防止仓位叠加）
	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("获取持仓失败: %w", err)
	}

	// 🛡️ 硬约束检查（冷却期、日交易上限、小时上限、最大持仓数量）
	if err := at.constraints.CanOpenPosition(decision.Symbol, len(positions)); err != nil {
		log.Printf("  ⚠️  硬约束拦截: %v", err)
		return fmt.Errorf("硬约束拦截: %w", err)
	}

	// ⚠️ 关键：检查是否已有同币种同方向持仓，如果有则拒绝开仓（防止仓位叠加超限）
	for _, pos := range positions {
		if pos["symbol"] == decision.Symbol && pos["side"] == "long" {
			return fmt.Errorf("❌ %s 已有多仓，拒绝开仓以防止仓位叠加超限。如需换仓，请先给出 close_long 决策", decision.Symbol)
		}
	}

	// ✅ 修复: 检查可用保证金是否充足 + 总保证金使用率
	balance, err := at.trader.GetBalance()
	if err != nil {
		return fmt.Errorf("获取账户余额失败: %w", err)
	}
	availableBalance := 0.0
	totalEquity := 0.0
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}
	if equity, ok := balance["totalWalletBalance"].(float64); ok {
		totalEquity = equity
	}

	// 计算当前总已用保证金（所有持仓的保证金之和）
	totalMarginUsed := 0.0
	for _, pos := range positions {
		// 获取持仓信息
		positionAmt := 0.0
		markPrice := 0.0
		leverage := 1

		if amt, ok := pos["positionAmt"].(float64); ok {
			positionAmt = amt
			if positionAmt < 0 {
				positionAmt = -positionAmt // 空仓取绝对值
			}
		}
		if price, ok := pos["markPrice"].(float64); ok {
			markPrice = price
		}
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		// 保证金 = (持仓价值) / 杠杆
		if leverage > 0 && markPrice > 0 {
			positionValue := positionAmt * markPrice
			marginForThisPosition := positionValue / float64(leverage)
			totalMarginUsed += marginForThisPosition
		}
	}

	// 计算所需保证金 = 仓位价值 / 杠杆
	requiredMargin := decision.PositionSizeUSD / float64(decision.Leverage)

	// 🚨 关键检查：总保证金使用率不能超过90%（硬约束）
	newTotalMarginUsed := totalMarginUsed + requiredMargin
	marginUtilizationRate := 0.0
	if totalEquity > 0 {
		marginUtilizationRate = (newTotalMarginUsed / totalEquity) * 100
	}

	if marginUtilizationRate > 90.0 {
		return fmt.Errorf("❌ 总保证金使用率将超过90%%限制: 当前%.2f%% + 新仓位%.2f USDT = %.2f%% (账户净值:%.2f USDT)",
			(totalMarginUsed/totalEquity)*100, requiredMargin, marginUtilizationRate, totalEquity)
	}

	// 检查可用保证金
	if requiredMargin > availableBalance {
		return fmt.Errorf("❌ 可用保证金不足: 需要%.2f USDT, 可用%.2f USDT", requiredMargin, availableBalance)
	}
	log.Printf("  💰 保证金检查通过: 需要%.2f USDT, 可用%.2f USDT, 总使用率%.1f%%", requiredMargin, availableBalance, marginUtilizationRate)

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}

	// 计算数量
	quantity := decision.PositionSizeUSD / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// 开仓
	order, err := at.trader.OpenLong(decision.Symbol, quantity, decision.Leverage)
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 开仓成功，订单ID: %v, 数量: %.4f", order["orderId"], quantity)

	// 🛡️ 记录开仓到硬约束管理器
	at.constraints.RecordOpenPosition(decision.Symbol, "long")

	// 记录开仓时间
	posKey := decision.Symbol + "_long"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// 设置止损止盈
	if err := at.trader.SetStopLoss(decision.Symbol, "LONG", quantity, decision.StopLoss); err != nil {
		log.Printf("  ⚠ 设置止损失败: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "LONG", quantity, decision.TakeProfit); err != nil {
		log.Printf("  ⚠ 设置止盈失败: %v", err)
	}

	return nil
}

// executeOpenShortWithRecord 执行开空仓并记录详细信息
func (at *AutoTrader) executeOpenShortWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📉 开空仓: %s", decision.Symbol)

	// ⚠️ 先获取当前持仓信息（用于硬约束检查和防止仓位叠加）
	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("获取持仓失败: %w", err)
	}

	// 🛡️ 硬约束检查（冷却期、日交易上限、小时上限、最大持仓数量）
	if err := at.constraints.CanOpenPosition(decision.Symbol, len(positions)); err != nil {
		log.Printf("  ⚠️  硬约束拦截: %v", err)
		return fmt.Errorf("硬约束拦截: %w", err)
	}

	// ⚠️ 关键：检查是否已有同币种同方向持仓，如果有则拒绝开仓（防止仓位叠加超限）
	for _, pos := range positions {
		if pos["symbol"] == decision.Symbol && pos["side"] == "short" {
			return fmt.Errorf("❌ %s 已有空仓，拒绝开仓以防止仓位叠加超限。如需换仓，请先给出 close_short 决策", decision.Symbol)
		}
	}

	// ✅ 修复: 检查可用保证金是否充足 + 总保证金使用率
	balance, err := at.trader.GetBalance()
	if err != nil {
		return fmt.Errorf("获取账户余额失败: %w", err)
	}
	availableBalance := 0.0
	totalEquity := 0.0
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}
	if equity, ok := balance["totalWalletBalance"].(float64); ok {
		totalEquity = equity
	}

	// 计算当前总已用保证金（所有持仓的保证金之和）
	totalMarginUsed := 0.0
	for _, pos := range positions {
		// 获取持仓信息
		positionAmt := 0.0
		markPrice := 0.0
		leverage := 1

		if amt, ok := pos["positionAmt"].(float64); ok {
			positionAmt = amt
			if positionAmt < 0 {
				positionAmt = -positionAmt // 空仓取绝对值
			}
		}
		if price, ok := pos["markPrice"].(float64); ok {
			markPrice = price
		}
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		// 保证金 = (持仓价值) / 杠杆
		if leverage > 0 && markPrice > 0 {
			positionValue := positionAmt * markPrice
			marginForThisPosition := positionValue / float64(leverage)
			totalMarginUsed += marginForThisPosition
		}
	}

	// 计算所需保证金 = 仓位价值 / 杠杆
	requiredMargin := decision.PositionSizeUSD / float64(decision.Leverage)

	// 🚨 关键检查：总保证金使用率不能超过90%（硬约束）
	newTotalMarginUsed := totalMarginUsed + requiredMargin
	marginUtilizationRate := 0.0
	if totalEquity > 0 {
		marginUtilizationRate = (newTotalMarginUsed / totalEquity) * 100
	}

	if marginUtilizationRate > 90.0 {
		return fmt.Errorf("❌ 总保证金使用率将超过90%%限制: 当前%.2f%% + 新仓位%.2f USDT = %.2f%% (账户净值:%.2f USDT)",
			(totalMarginUsed/totalEquity)*100, requiredMargin, marginUtilizationRate, totalEquity)
	}

	// 检查可用保证金
	if requiredMargin > availableBalance {
		return fmt.Errorf("❌ 可用保证金不足: 需要%.2f USDT, 可用%.2f USDT", requiredMargin, availableBalance)
	}
	log.Printf("  💰 保证金检查通过: 需要%.2f USDT, 可用%.2f USDT, 总使用率%.1f%%", requiredMargin, availableBalance, marginUtilizationRate)

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}

	// 计算数量
	quantity := decision.PositionSizeUSD / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// 开仓
	order, err := at.trader.OpenShort(decision.Symbol, quantity, decision.Leverage)
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 开仓成功，订单ID: %v, 数量: %.4f", order["orderId"], quantity)

	// 🛡️ 记录开仓到硬约束管理器
	at.constraints.RecordOpenPosition(decision.Symbol, "short")

	// 记录开仓时间
	posKey := decision.Symbol + "_short"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// 设置止损止盈
	if err := at.trader.SetStopLoss(decision.Symbol, "SHORT", quantity, decision.StopLoss); err != nil {
		log.Printf("  ⚠ 设置止损失败: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "SHORT", quantity, decision.TakeProfit); err != nil {
		log.Printf("  ⚠ 设置止盈失败: %v", err)
	}

	return nil
}

// executeCloseLongWithRecord 执行平多仓并记录详细信息
func (at *AutoTrader) executeCloseLongWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 平多仓: %s", decision.Symbol)

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 平仓
	order, err := at.trader.CloseLong(decision.Symbol, 0) // 0 = 全部平仓
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	// ✅ 修复: 更新日内盈亏
	if realizedPnL, ok := order["realized_pnl"].(float64); ok {
		at.dailyPnL += realizedPnL
		log.Printf("  💰 平仓盈亏: %+.2f USDT | 日内累计: %+.2f USDT", realizedPnL, at.dailyPnL)
	}

	log.Printf("  ✓ 平仓成功")

	// 🛡️ 记录平仓到硬约束管理器（设置冷却期）
	at.constraints.RecordClosePosition(decision.Symbol, "long")

	// 标记为手动/策略主动平仓，防止后续被误判为止损
	posKey := decision.Symbol + "_long"
	at.manualCloseTracker[posKey] = time.Now()

	return nil
}

// executeCloseShortWithRecord 执行平空仓并记录详细信息
func (at *AutoTrader) executeCloseShortWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 平空仓: %s", decision.Symbol)

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 平仓
	order, err := at.trader.CloseShort(decision.Symbol, 0) // 0 = 全部平仓
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	// ✅ 修复: 更新日内盈亏
	if realizedPnL, ok := order["realized_pnl"].(float64); ok {
		at.dailyPnL += realizedPnL
		log.Printf("  💰 平仓盈亏: %+.2f USDT | 日内累计: %+.2f USDT", realizedPnL, at.dailyPnL)
	}

	log.Printf("  ✓ 平仓成功")

	// 🛡️ 记录平仓到硬约束管理器（设置冷却期）
	at.constraints.RecordClosePosition(decision.Symbol, "short")

	// 标记为手动/策略主动平仓，防止后续被误判为止损
	posKey := decision.Symbol + "_short"
	at.manualCloseTracker[posKey] = time.Now()

	return nil
}

// GetID 获取trader ID
func (at *AutoTrader) GetID() string {
	return at.id
}

// GetName 获取trader名称
func (at *AutoTrader) GetName() string {
	return at.name
}

// GetAIModel 获取AI模型
func (at *AutoTrader) GetAIModel() string {
	return at.aiModel
}

// GetDecisionLogger 获取决策日志记录器
func (at *AutoTrader) GetDecisionLogger() *logger.DecisionLogger {
	return at.decisionLogger
}

// GetMemoryManager 获取记忆管理器
func (at *AutoTrader) GetMemoryManager() *memory.Manager {
	return at.memoryManager
}

// GetStatus 获取系统状态（用于API）
func (at *AutoTrader) GetStatus() map[string]interface{} {
	aiProvider := "DeepSeek"
	if at.config.UseQwen {
		aiProvider = "Qwen"
	}

	return map[string]interface{}{
		"trader_id":       at.id,
		"trader_name":     at.name,
		"ai_model":        at.aiModel,
		"exchange":        at.exchange,
		"is_running":      at.isRunning,
		"start_time":      at.startTime.Format(time.RFC3339),
		"runtime_minutes": int(time.Since(at.startTime).Minutes()),
		"call_count":      at.callCount,
		"initial_balance": at.initialBalance,
		"scan_interval":   at.config.ScanInterval.String(),
		"stop_until":      at.stopUntil.Format(time.RFC3339),
		"last_reset_time": at.lastResetTime.Format(time.RFC3339),
		"ai_provider":     aiProvider,
	}
}

// GetAccountInfo 获取账户信息（用于API）
func (at *AutoTrader) GetAccountInfo() (map[string]interface{}, error) {
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("获取余额失败: %w", err)
	}

	// 获取账户字段
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Total Equity = 钱包余额 + 未实现盈亏
	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 获取持仓计算总保证金
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	totalMarginUsed := 0.0
	totalUnrealizedPnL := 0.0
	for _, pos := range positions {
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		totalUnrealizedPnL += unrealizedPnl

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed
	}

	totalPnL := totalEquity - at.initialBalance
	totalPnLPct := 0.0
	if at.initialBalance > 0 {
		totalPnLPct = (totalPnL / at.initialBalance) * 100
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	return map[string]interface{}{
		// 核心字段
		"total_equity":      totalEquity,           // 账户净值 = wallet + unrealized
		"wallet_balance":    totalWalletBalance,    // 钱包余额（不含未实现盈亏）
		"unrealized_profit": totalUnrealizedProfit, // 未实现盈亏（从API）
		"available_balance": availableBalance,      // 可用余额

		// 盈亏统计
		"total_pnl":            totalPnL,           // 总盈亏 = equity - initial
		"total_pnl_pct":        totalPnLPct,        // 总盈亏百分比
		"total_unrealized_pnl": totalUnrealizedPnL, // 未实现盈亏（从持仓计算）
		"initial_balance":      at.initialBalance,  // 初始余额
		"daily_pnl":            at.dailyPnL,        // 日盈亏

		// 持仓信息
		"position_count":  len(positions),  // 持仓数量
		"margin_used":     totalMarginUsed, // 保证金占用
		"margin_used_pct": marginUsedPct,   // 保证金使用率
	}, nil
}

// GetPositions 获取持仓列表（用于API）
func (at *AutoTrader) GetPositions() ([]map[string]interface{}, error) {
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	var result []map[string]interface{}
	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		pnlPct := 0.0
		if side == "long" {
			pnlPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			pnlPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		marginUsed := (quantity * markPrice) / float64(leverage)

		result = append(result, map[string]interface{}{
			"symbol":             symbol,
			"side":               side,
			"entry_price":        entryPrice,
			"mark_price":         markPrice,
			"quantity":           quantity,
			"leverage":           leverage,
			"unrealized_pnl":     unrealizedPnl,
			"unrealized_pnl_pct": pnlPct,
			"liquidation_price":  liquidationPrice,
			"margin_used":        marginUsed,
		})
	}

	return result, nil
}

// sortDecisionsByPriority 对决策排序：先平仓，再开仓，最后hold/wait
// 这样可以避免换仓时仓位叠加超限
func sortDecisionsByPriority(decisions []decision.Decision) []decision.Decision {
	if len(decisions) <= 1 {
		return decisions
	}

	// 定义优先级
	getActionPriority := func(action string) int {
		switch action {
		case "close_long", "close_short":
			return 1 // 最高优先级：先平仓
		case "open_long", "open_short":
			return 2 // 次优先级：后开仓
		case "hold", "wait":
			return 3 // 最低优先级：观望
		default:
			return 999 // 未知动作放最后
		}
	}

	// 复制决策列表
	sorted := make([]decision.Decision, len(decisions))
	copy(sorted, decisions)

	// 按优先级排序
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if getActionPriority(sorted[i].Action) > getActionPriority(sorted[j].Action) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}

// recoverLastCycleNumber 从历史日志恢复最后的周期编号
// 读取日志目录中最新的决策日志文件，获取最大的 cycle_number
// 返回：最大周期编号（如果没有历史日志则返回0）
func recoverLastCycleNumber(logDir string) int {
	// 检查日志目录是否存在
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		return 0
	}

	// 读取日志目录中的所有文件
	files, err := os.ReadDir(logDir)
	if err != nil {
		log.Printf("⚠️  读取日志目录失败: %v，从周期 1 开始", err)
		return 0
	}

	// 遍历所有JSON文件，找到最大的 cycle_number
	maxCycleNumber := 0
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		// 读取JSON文件
		filePath := filepath.Join(logDir, file.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		// 解析JSON，提取 cycle_number
		var record struct {
			CycleNumber int `json:"cycle_number"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}

		if record.CycleNumber > maxCycleNumber {
			maxCycleNumber = record.CycleNumber
		}
	}

	if maxCycleNumber > 0 {
		log.Printf("📊 从历史日志恢复周期编号，继续从周期 %d 开始", maxCycleNumber+1)
	}

	return maxCycleNumber
}

// runAltcoinScanner 运行山寨币异动扫描循环（独立goroutine）
func (at *AutoTrader) runAltcoinScanner() {
	log.Printf("🔍 山寨币异动扫描器已启动")

	// 扫描间隔：30分钟（建议值，大幅降低API消耗）
	scanInterval := 30 * time.Minute
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()

	scanCount := 0

	// 首次延迟1分钟执行（等待WebSocket稳定和Top50列表初始化）
	time.Sleep(1 * time.Minute)

	for at.isRunning {
		scanCount++
		startTime := time.Now()

		// 从WebSocket获取Top50列表
		top50Symbols := at.altcoinWSMonitor.GetTop50Symbols()
		if len(top50Symbols) == 0 {
			log.Printf("⚠️ [扫描 #%d] Top50列表为空，跳过本次扫描（WebSocket可能尚未就绪）", scanCount)
			// 等待下次扫描
			select {
			case <-ticker.C:
				continue
			case <-time.After(scanInterval):
				if !at.isRunning {
					return
				}
				continue
			}
		}

		log.Printf("📊 [扫描 #%d] 使用WebSocket提供的Top%d币种", scanCount, len(top50Symbols))

		// 🆕 先扫描现货期货价差（早期信号 - 捕捉DEX/现货先行）
		if at.spotFuturesMonitor != nil {
			log.Printf("🔍 [扫描 #%d] 开始扫描现货期货价差...", scanCount)
			sfSignals, sfErr := at.spotFuturesMonitor.ScanPriceDifferences(top50Symbols)
			if sfErr != nil {
				log.Printf("⚠️  [扫描 #%d] 现货期货扫描失败: %v", scanCount, sfErr)
			} else if len(sfSignals) > 0 {
				log.Printf("✅ [扫描 #%d] 发现 %d 个现货期货价差信号（早期信号）", scanCount, len(sfSignals))
				for _, sfSignal := range sfSignals {
					// 格式化输出信号
					log.Printf("  🚨 %s | 现货$%.2f > 期货$%.2f (价差%.2f%%) | %d星 | %s",
						sfSignal.Symbol,
						sfSignal.SpotPrice,
						sfSignal.FuturesPrice,
						sfSignal.PriceDiffPct,
						sfSignal.Confidence,
						sfSignal.SuggestedAction,
					)
					log.Printf("      原因: %s", sfSignal.Reasoning)
				}
			} else {
				log.Printf("✅ [扫描 #%d] 未发现现货期货价差信号", scanCount)
			}
		}

		// 执行扫描（使用WebSocket提供的Top50列表）
		signals, err := at.altcoinScanner.ScanTop50(top50Symbols)
		if err != nil {
			log.Printf("❌ [扫描 #%d] 山寨币扫描失败: %v", scanCount, err)
		} else {
			// 记录每个信号
			for _, signal := range signals {
				at.altcoinLogger.LogSignal(signal)

				// 保存JSON（供后续分析）
				if err := at.altcoinLogger.SaveSignalJSON(signal); err != nil {
					log.Printf("⚠️  保存信号JSON失败: %v", err)
				}
			}

			// 记录扫描摘要
			duration := time.Since(startTime)
			scannedCount := at.altcoinScanner.GetLastScannedCount()
			at.altcoinLogger.LogScanSummary(scanCount, scannedCount, len(signals), duration)
		}

		// 每小时输出统计（30分钟 × 2 = 1小时）
		if scanCount%2 == 0 {
			stats := at.altcoinScanner.GetStatistics()
			at.altcoinLogger.LogHourlyStats(stats)
		}

		// 等待下次扫描
		select {
		case <-ticker.C:
			// 继续下一次扫描
		case <-time.After(scanInterval):
			// 超时保护
			if !at.isRunning {
				return
			}
		}
	}

	log.Printf("🛑 山寨币异动扫描器已停止")
}

// buildTradeEntry 构建交易记录条目（用于AI记忆系统）
func (at *AutoTrader) buildTradeEntry(
	decision *decision.Decision,
	actionRecord *logger.DecisionAction,
	ctx *decision.Context,
) memory.TradeEntry {
	// 确定action类型和side
	action := "hold"
	side := ""
	if decision.Action == "open_long" {
		action = "open"
		side = "long"
	} else if decision.Action == "open_short" {
		action = "open"
		side = "short"
	} else if decision.Action == "close_long" {
		action = "close"
		side = "long"
	} else if decision.Action == "close_short" {
		action = "close"
		side = "short"
	}

	// 获取市场体制（Sprint 1使用简化逻辑）
	marketRegime := "unknown"
	regimeStage := "mid" // 默认mid

	// 🔍 尝试从市场数据推断体制（简化版）
	if btcData, ok := ctx.MarketDataMap["BTCUSDT"]; ok && btcData != nil && btcData.LongerTermContext != nil {
		// 简单的趋势判断：价格 vs EMA50
		if btcData.CurrentPrice > btcData.LongerTermContext.EMA50 {
			if btcData.PriceChange4h > 2.0 {
				marketRegime = "markup" // 价格突破EMA50且4h涨幅>2% = 上涨阶段
			} else {
				marketRegime = "accumulation" // 价格在EMA50上方但涨幅不大 = 积累阶段
			}
		} else {
			if btcData.PriceChange4h < -2.0 {
				marketRegime = "markdown" // 价格跌破EMA50且4h跌幅>2% = 下跌阶段
			} else {
				marketRegime = "distribution" // 价格在EMA50下方但跌幅不大 = 分配阶段
			}
		}
	}

	// 提取持仓信息（如果有）
	var entryPrice, exitPrice, positionPct float64
	var holdMinutes int
	var returnPct float64
	var result string

	if action == "close" {
		// 平仓：从现有持仓中获取信息
		for _, pos := range ctx.Positions {
			if pos.Symbol == decision.Symbol && pos.Side == side {
				entryPrice = pos.EntryPrice
				exitPrice = actionRecord.Price
				positionPct = (pos.MarginUsed / ctx.Account.TotalEquity) * 100

				// 计算持仓时长（分钟）
				if !pos.OpenTime.IsZero() {
					holdMinutes = int(time.Since(pos.OpenTime).Minutes())
				}

				// 计算收益率和结果
				returnPct = pos.UnrealizedPnLPct
				if returnPct > 0 {
					result = "win"
				} else if returnPct < -0.1 { // 亏损>0.1%才算loss
					result = "loss"
				} else {
					result = "break_even"
				}
				break
			}
		}
	} else if action == "open" {
		// 开仓：记录开仓信息，结果为空（需要等待平仓）
		entryPrice = actionRecord.Price
		positionPct = (decision.PositionSizeUSD / float64(decision.Leverage)) / ctx.Account.TotalEquity * 100
	}

	// 提取信号（Sprint 1简化：从reasoning中提取关键词）
	signals := extractSignalsFromReasoning(decision.Reasoning)

	// 🔍 尝试从reasoning中提取预测信息
	predictedDirection := ""
	predictedProb := 0.0
	predictedMove := 0.0

	// 简单的预测提取：查找"预测"关键词
	if strings.Contains(decision.Reasoning, "预测: up") || strings.Contains(decision.Reasoning, "预测:up") {
		predictedDirection = "up"
		// 尝试提取概率（格式：概率65%）
		if idx := strings.Index(decision.Reasoning, "概率"); idx != -1 {
			var prob float64
			fmt.Sscanf(decision.Reasoning[idx:], "概率%f%%", &prob)
			predictedProb = prob / 100.0
		}
	} else if strings.Contains(decision.Reasoning, "预测: down") || strings.Contains(decision.Reasoning, "预测:down") {
		predictedDirection = "down"
		if idx := strings.Index(decision.Reasoning, "概率"); idx != -1 {
			var prob float64
			fmt.Sscanf(decision.Reasoning[idx:], "概率%f%%", &prob)
			predictedProb = prob / 100.0
		}
	}

	return memory.TradeEntry{
		Cycle:              at.callCount,
		Timestamp:          time.Now(),
		MarketRegime:       marketRegime,
		RegimeStage:        regimeStage,
		Action:             action,
		Symbol:             decision.Symbol,
		Side:               side,
		Signals:            signals,
		Reasoning:          decision.Reasoning,
		PredictedDirection: predictedDirection,
		PredictedProb:      predictedProb,
		PredictedMove:      predictedMove,
		EntryPrice:         entryPrice,
		ExitPrice:          exitPrice,
		PositionPct:        positionPct,
		Leverage:           decision.Leverage,
		HoldMinutes:        holdMinutes,
		ReturnPct:          returnPct,
		Result:             result,
	}
}

// extractSignalsFromReasoning 从reasoning中提取信号关键词
func extractSignalsFromReasoning(reasoning string) []string {
	signals := []string{}

	// 常见信号关键词
	keywords := []string{
		"MACD", "RSI", "EMA", "均线", "突破", "跌破",
		"金叉", "死叉", "超买", "超卖", "背离",
		"趋势", "震荡", "支撑", "阻力", "放量",
	}

	reasoningLower := strings.ToLower(reasoning)
	for _, keyword := range keywords {
		if strings.Contains(reasoningLower, strings.ToLower(keyword)) {
			signals = append(signals, keyword)
		}
	}

	// 最多保留5个信号
	if len(signals) > 5 {
		signals = signals[:5]
	}

	return signals
}

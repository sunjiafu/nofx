package agents

import (
	"encoding/json"
	"nofx/market"
	"nofx/mcp"
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
	MemoryPrompt    string // 🧠 AI记忆提示（Sprint 1）
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
	OpenTime         time.Time // 🆕 开仓时间（用于判断持仓时长）
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

	// 限价单相关字段
	IsLimitOrder bool    `json:"is_limit_order,omitempty"` // 是否是限价单
	LimitPrice   float64 `json:"limit_price,omitempty"`    // 限价单价格
	CurrentPrice float64 `json:"current_price,omitempty"`  // 当前价格（用于对比）
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
	mcpClient         *mcp.Client
	intelligenceAgent *MarketIntelligenceAgent // 市场情报Agent
	predictionAgent   *PredictionAgent         // 预测Agent
	btcEthLeverage    int
	altcoinLeverage   int
}

// NewDecisionOrchestrator 创建决策协调器
func NewDecisionOrchestrator(mcpClient *mcp.Client, btcEthLeverage, altcoinLeverage int) *DecisionOrchestrator {
	return &DecisionOrchestrator{
		mcpClient:         mcpClient,
		intelligenceAgent: NewMarketIntelligenceAgent(mcpClient),
		predictionAgent:   NewPredictionAgent(mcpClient),
		btcEthLeverage:    btcEthLeverage,
		altcoinLeverage:   altcoinLeverage,
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

// GetFullDecision 获取AI的完整交易决策（使用预测驱动模式）
func (o *DecisionOrchestrator) GetFullDecision(ctx *Context) (*FullDecision, error) {
	// 使用预测驱动模式（新架构）
	return o.GetFullDecisionPredictive(ctx)
}


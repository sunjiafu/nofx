package agents

import (
	"fmt"
	"log"
	"math"
)

// PortfolioRiskManager 组合风险管理器
// 检查整体仓位风险，防止过度集中和总风险过高
type PortfolioRiskManager struct {
	// 风控参数
	MaxTotalRiskPct      float64 // 总风险不超过账户净值的百分比（默认20%）
	MaxCorrelatedSymbols int     // 最多允许同方向相关币种数量（默认2）
	MaxSingleSymbolPct   float64 // 单币种风险不超过账户净值的百分比（默认10%）
}

// NewPortfolioRiskManager 创建组合风险管理器
func NewPortfolioRiskManager() *PortfolioRiskManager {
	return &PortfolioRiskManager{
		MaxTotalRiskPct:      20.0, // 总风险最多20%账户净值
		MaxCorrelatedSymbols: 2,    // 同方向最多2个相关币种
		MaxSingleSymbolPct:   10.0, // 单币种风险最多10%账户净值
	}
}

// CorrelationGroup 相关性分组
// 高相关性的币种会放在同一组
var CorrelationGroups = map[string][]string{
	"主流币": {"BTCUSDT", "ETHUSDT"},
	"L1公链": {"SOLUSDT", "AVAXUSDT", "NEARUSDT", "APTUSDT", "SUIUSDT"},
	"DeFi":  {"UNIUSDT", "AAVEUSDT", "MKRUSDT", "COMPUSDT"},
	"Meme":  {"DOGEUSDT", "SHIBUSDT", "PEPEUSDT", "FLOKIUSDT"},
	"L2":    {"ARBUSDT", "OPUSDT", "MATICUSDT"},
}

// ValidateNewPosition 验证新仓位是否符合组合风控要求
func (p *PortfolioRiskManager) ValidateNewPosition(
	existingPositions []PositionInfoInput,
	newSymbol string,
	newSide string, // "long" or "short"
	newRiskUSD float64,
	totalEquity float64,
) error {

	// 1. 检查单币种风险占比
	singleSymbolRiskPct := (newRiskUSD / totalEquity) * 100
	if singleSymbolRiskPct > p.MaxSingleSymbolPct {
		return fmt.Errorf("单币种风险过高: %s风险%.2f%% > %.2f%%上限",
			newSymbol, singleSymbolRiskPct, p.MaxSingleSymbolPct)
	}

	// 2. 计算总风险暴露（现有仓位 + 新仓位）
	totalRiskUSD := newRiskUSD
	for _, pos := range existingPositions {
		// 估算现有仓位的风险（使用保证金 * 20%作为风险估算）
		posRisk := pos.MarginUsed * 0.20
		totalRiskUSD += posRisk
	}

	totalRiskPct := (totalRiskUSD / totalEquity) * 100
	if totalRiskPct > p.MaxTotalRiskPct {
		return fmt.Errorf("总风险暴露过高: %.2f%% > %.2f%%上限 (现有仓位风险+新仓位风险)",
			totalRiskPct, p.MaxTotalRiskPct)
	}

	// 3. 检查相关性（同方向同组别的币种数量）
	newGroup := getCorrelationGroup(newSymbol)
	sameDirectionSameGroupCount := 0

	for _, pos := range existingPositions {
		posGroup := getCorrelationGroup(pos.Symbol)
		// 检查是否是同方向且同组
		if pos.Side == newSide && posGroup == newGroup && newGroup != "" {
			sameDirectionSameGroupCount++
		}
	}

	// 如果已经有同组同方向的仓位，警告但不阻止
	if sameDirectionSameGroupCount >= p.MaxCorrelatedSymbols {
		return fmt.Errorf("相关性风险: 已有%d个%s组%s仓位，建议分散投资",
			sameDirectionSameGroupCount, newGroup, newSide)
	}

	// 4. 检查极端情况：所有仓位同方向
	longCount := 0
	shortCount := 0
	for _, pos := range existingPositions {
		if pos.Side == "long" {
			longCount++
		} else {
			shortCount++
		}
	}

	if newSide == "long" {
		longCount++
	} else {
		shortCount++
	}

	totalPositions := longCount + shortCount
	if totalPositions >= 3 {
		// 如果3个或以上仓位全是同方向，警告
		if longCount == totalPositions {
			log.Printf("⚠️  [Portfolio风控] 所有%d个仓位都是多仓，市场下跌时风险极高", totalPositions)
		} else if shortCount == totalPositions {
			log.Printf("⚠️  [Portfolio风控] 所有%d个仓位都是空仓，市场上涨时风险极高", totalPositions)
		}
	}

	log.Printf("✅ [Portfolio风控] %s %s通过: 单币风险%.2f%% | 总风险%.2f%% | 同组%d个",
		newSymbol, newSide, singleSymbolRiskPct, totalRiskPct, sameDirectionSameGroupCount)

	return nil
}

// getCorrelationGroup 获取币种所属的相关性分组
func getCorrelationGroup(symbol string) string {
	for group, symbols := range CorrelationGroups {
		for _, s := range symbols {
			if s == symbol {
				return group
			}
		}
	}
	return "" // 不在任何已知组中
}

// CalculatePortfolioMetrics 计算组合指标
func (p *PortfolioRiskManager) CalculatePortfolioMetrics(
	positions []PositionInfoInput,
	totalEquity float64,
) map[string]interface{} {
	metrics := make(map[string]interface{})

	// 计算总风险
	totalRiskUSD := 0.0
	longExposure := 0.0
	shortExposure := 0.0

	for _, pos := range positions {
		posRisk := pos.MarginUsed * 0.20
		totalRiskUSD += posRisk

		if pos.Side == "long" {
			longExposure += pos.MarginUsed * float64(pos.Leverage)
		} else {
			shortExposure += pos.MarginUsed * float64(pos.Leverage)
		}
	}

	// 净敞口
	netExposure := longExposure - shortExposure

	metrics["total_risk_pct"] = (totalRiskUSD / totalEquity) * 100
	metrics["long_exposure"] = longExposure
	metrics["short_exposure"] = shortExposure
	metrics["net_exposure"] = netExposure
	metrics["net_exposure_pct"] = (netExposure / totalEquity) * 100
	metrics["position_count"] = len(positions)

	// 方向偏差（-1到1，0表示多空平衡）
	totalExposure := longExposure + shortExposure
	if totalExposure > 0 {
		metrics["direction_bias"] = netExposure / totalExposure
	} else {
		metrics["direction_bias"] = 0.0
	}

	return metrics
}

// EstimatePositionRisk 估算仓位风险（用于新仓位验证）
func EstimatePositionRisk(positionSizeUSD float64, stopLossPct float64) float64 {
	// 风险 = 仓位大小 * 止损百分比
	return positionSizeUSD * (math.Abs(stopLossPct) / 100.0)
}

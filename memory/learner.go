package memory

import (
	"fmt"
	"strings"
	"time"
)

// 🧠 Learner 自适应学习器
// 自动分析交易历史，生成学习总结

// 🚨 黑名单：这些是结果型关键词，不应统计为信号
var resultKeywordsBlacklist = []string{
	"止盈", "止损", "自动触发", "触发", "强平", "爆仓",
	"平仓", "获利", "亏损", "盈利", "收益",
}

// isResultKeyword 检查信号名是否为结果型关键词（应被过滤）
func isResultKeyword(signalName string) bool {
	nameLower := strings.ToLower(signalName)
	for _, blocked := range resultKeywordsBlacklist {
		if strings.Contains(nameLower, blocked) {
			return true
		}
	}
	return false
}

// UpdateLearningSummary 更新学习总结（每次添加交易后调用）
// ⚠️ 注意：此方法假设调用者已经持有锁，不再重复加锁
func (m *Manager) UpdateLearningSummary() error {
	// 🔧 修复死锁：不再加锁，因为调用者（AddTrade）已经持有锁
	// m.mu.Lock()
	// defer m.mu.Unlock()

	// 🔧 修正：只统计已完成的交易（有result的）
	completedTrades := 0
	for _, trade := range m.memory.RecentTrades {
		if trade.Result != "" {
			completedTrades++
		}
	}

	// 至少需要10笔已完成的交易才能开始学习
	if completedTrades < 10 {
		return nil
	}

	// 初始化学习总结
	if m.memory.LearningSummary == nil {
		m.memory.LearningSummary = &LearningSummary{
			SignalStats:       make(map[string]*SignalStat),
			FailurePatterns:   make([]string, 0),
			SuccessPatterns:   make([]string, 0),
			MarketPreferences: make(map[string]float64),
		}
	}

	summary := m.memory.LearningSummary
	summary.UpdatedAt = time.Now()

	// 1. 统计信号成功率
	m.analyzeSignals(summary)

	// 2. 识别失败模式
	m.identifyFailurePatterns(summary)

	// 3. 总结成功经验
	m.identifySuccessPatterns(summary)

	// 4. 分析市场环境偏好
	m.analyzeMarketPreferences(summary)

	return nil
}

// analyzeSignals 分析各类信号的成功率
func (m *Manager) analyzeSignals(summary *LearningSummary) {
	// 重置统计
	summary.SignalStats = make(map[string]*SignalStat)

	for _, trade := range m.memory.RecentTrades {
		if trade.Result == "" {
			continue // 跳过进行中的交易
		}

		// 统计每个信号
		for _, signal := range trade.Signals {
			if _, exists := summary.SignalStats[signal]; !exists {
				summary.SignalStats[signal] = &SignalStat{
					SignalName: signal,
				}
			}

			stat := summary.SignalStats[signal]
			stat.TotalCount++
			stat.LastUsed = trade.Timestamp

			if trade.Result == "win" {
				stat.WinCount++
			} else if trade.Result == "loss" {
				stat.LossCount++
			}

			// 计算胜率
			if stat.TotalCount > 0 {
				stat.WinRate = float64(stat.WinCount) / float64(stat.TotalCount)
			}
		}
	}
}

// identifyFailurePatterns 识别失败模式
func (m *Manager) identifyFailurePatterns(summary *LearningSummary) {
	summary.FailurePatterns = make([]string, 0)

	// 模式1：特定信号成功率低（样本量要求提高到10）
	for _, stat := range summary.SignalStats {
		// 🚨 过滤结果型伪信号
		if isResultKeyword(stat.SignalName) {
			continue
		}

		if stat.TotalCount >= 10 && stat.WinRate < 0.35 {
			pattern := fmt.Sprintf("⚠️ 信号\"%s\"成功率仅%.0f%%（%d胜%d负，样本:%d），建议降低权重",
				stat.SignalName, stat.WinRate*100, stat.WinCount, stat.LossCount, stat.TotalCount)
			summary.FailurePatterns = append(summary.FailurePatterns, pattern)
		}
	}

	// 模式2：高置信度预测反而失败（样本量要求提高到15）
	highConfFails := 0
	highConfTotal := 0
	for _, trade := range m.memory.RecentTrades {
		if trade.PredictedProb > 0.7 && trade.Result != "" {
			highConfTotal++
			if trade.Result == "loss" {
				highConfFails++
			}
		}
	}
	if highConfTotal >= 15 && float64(highConfFails)/float64(highConfTotal) > 0.5 {
		pattern := fmt.Sprintf("⚠️ 高置信度预测（>70%%）失败率%.0f%%（样本:%d），可能过度自信",
			float64(highConfFails)/float64(highConfTotal)*100, highConfTotal)
		summary.FailurePatterns = append(summary.FailurePatterns, pattern)
	}

	// 模式3：特定方向失败率高（样本量要求提高到15）
	longWins, longTotal := 0, 0
	shortWins, shortTotal := 0, 0
	for _, trade := range m.memory.RecentTrades {
		if trade.Result == "" {
			continue
		}
		if trade.Side == "long" {
			longTotal++
			if trade.Result == "win" {
				longWins++
			}
		} else if trade.Side == "short" {
			shortTotal++
			if trade.Result == "win" {
				shortWins++
			}
		}
	}

	if longTotal >= 15 && float64(longWins)/float64(longTotal) < 0.3 {
		pattern := fmt.Sprintf("⚠️ 做多成功率仅%.0f%%（%d/%d），当前市场可能不适合做多",
			float64(longWins)/float64(longTotal)*100, longWins, longTotal)
		summary.FailurePatterns = append(summary.FailurePatterns, pattern)
	}
	if shortTotal >= 15 && float64(shortWins)/float64(shortTotal) < 0.3 {
		pattern := fmt.Sprintf("⚠️ 做空成功率仅%.0f%%（%d/%d），当前市场可能不适合做空",
			float64(shortWins)/float64(shortTotal)*100, shortWins, shortTotal)
		summary.FailurePatterns = append(summary.FailurePatterns, pattern)
	}
}

// identifySuccessPatterns 总结成功经验
func (m *Manager) identifySuccessPatterns(summary *LearningSummary) {
	summary.SuccessPatterns = make([]string, 0)

	// 模式1：高成功率信号（样本量要求：至少20个，100%需要30个）
	for _, stat := range summary.SignalStats {
		// 🚨 过滤结果型伪信号
		if isResultKeyword(stat.SignalName) {
			continue
		}

		// 根据成功率要求不同的样本量
		minSamples := 20
		if stat.WinRate > 0.95 {
			minSamples = 30 // 100%成功率需要更多样本才可信
		}

		if stat.TotalCount >= minSamples && stat.WinRate > 0.65 {
			// 添加置信度标签
			confidence := "中等"
			if stat.TotalCount >= 50 {
				confidence = "高"
			} else if stat.TotalCount < 30 {
				confidence = "低"
			}

			pattern := fmt.Sprintf("✅ 信号\"%s\"成功率%.0f%%（%d胜%d负，样本:%d，置信度:%s）",
				stat.SignalName, stat.WinRate*100, stat.WinCount, stat.LossCount, stat.TotalCount, confidence)
			summary.SuccessPatterns = append(summary.SuccessPatterns, pattern)
		}
	}

	// 模式2：特定方向成功率高（样本量要求：至少20个）
	longWins, longTotal := 0, 0
	shortWins, shortTotal := 0, 0
	for _, trade := range m.memory.RecentTrades {
		if trade.Result == "" {
			continue
		}
		if trade.Side == "long" {
			longTotal++
			if trade.Result == "win" {
				longWins++
			}
		} else if trade.Side == "short" {
			shortTotal++
			if trade.Result == "win" {
				shortWins++
			}
		}
	}

	if longTotal >= 20 && float64(longWins)/float64(longTotal) > 0.65 {
		confidence := "中等"
		if longTotal >= 50 {
			confidence = "高"
		}
		pattern := fmt.Sprintf("✅ 做多成功率%.0f%%（%d/%d，置信度:%s），当前市场适合做多",
			float64(longWins)/float64(longTotal)*100, longWins, longTotal, confidence)
		summary.SuccessPatterns = append(summary.SuccessPatterns, pattern)
	}
	if shortTotal >= 20 && float64(shortWins)/float64(shortTotal) > 0.65 {
		confidence := "中等"
		if shortTotal >= 50 {
			confidence = "高"
		}
		pattern := fmt.Sprintf("✅ 做空成功率%.0f%%（%d/%d，置信度:%s），当前市场适合做空",
			float64(shortWins)/float64(shortTotal)*100, shortWins, shortTotal, confidence)
		summary.SuccessPatterns = append(summary.SuccessPatterns, pattern)
	}

	// 模式3：推理关键词分析（样本量要求提高到15，100%需要25）
	successReasons := make(map[string]int)
	failReasons := make(map[string]int)

	for _, trade := range m.memory.RecentTrades {
		if trade.Result == "" || trade.Reasoning == "" {
			continue
		}

		keywords := extractKeywords(trade.Reasoning)
		if trade.Result == "win" {
			for _, kw := range keywords {
				successReasons[kw]++
			}
		} else if trade.Result == "loss" {
			for _, kw := range keywords {
				failReasons[kw]++
			}
		}
	}

	// 找出成功率高的关键词
	for kw, successCount := range successReasons {
		failCount := failReasons[kw]
		total := successCount + failCount
		winRate := float64(successCount) / float64(total)

		// 根据成功率要求不同的样本量
		minSamples := 15
		if winRate > 0.95 {
			minSamples = 25 // 100%成功率需要更多样本
		}

		if total >= minSamples && winRate > 0.7 {
			confidence := "中等"
			if total >= 40 {
				confidence = "高"
			} else if total < 25 {
				confidence = "低"
			}

			pattern := fmt.Sprintf("✅ 推理包含\"%s\"时成功率%.0f%%（%d胜%d负，样本:%d，置信度:%s）",
				kw, winRate*100, successCount, failCount, total, confidence)
			summary.SuccessPatterns = append(summary.SuccessPatterns, pattern)
		}
	}
}

// analyzeMarketPreferences 分析市场环境偏好
func (m *Manager) analyzeMarketPreferences(summary *LearningSummary) {
	regimeStats := make(map[string]struct{ wins, total int })

	for _, trade := range m.memory.RecentTrades {
		if trade.Result == "" || trade.MarketRegime == "" {
			continue
		}

		stats := regimeStats[trade.MarketRegime]
		stats.total++
		if trade.Result == "win" {
			stats.wins++
		}
		regimeStats[trade.MarketRegime] = stats
	}

	summary.MarketPreferences = make(map[string]float64)
	for regime, stats := range regimeStats {
		if stats.total > 0 {
			winRate := float64(stats.wins) / float64(stats.total)
			summary.MarketPreferences[regime] = winRate
		}
	}
}

// extractKeywords 从推理文本中提取关键词
func extractKeywords(text string) []string {
	keywords := make([]string, 0)
	text = strings.ToLower(text)

	// 定义关键词列表
	keywordList := []string{
		"macd", "rsi", "ema", "趋势", "突破", "支撑", "阻力",
		"金叉", "死叉", "超买", "超卖", "背离", "震荡",
		"强势", "弱势", "回调", "反弹",
	}

	for _, kw := range keywordList {
		if strings.Contains(text, kw) {
			keywords = append(keywords, kw)
		}
	}

	return keywords
}

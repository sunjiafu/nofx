package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Manager 记忆管理器
type Manager struct {
	filepath string
	memory   *SimpleMemory
	mu       sync.RWMutex
}

// NewManager 创建或加载记忆管理器
func NewManager(traderID string) (*Manager, error) {
	// 确保目录存在
	dir := "trader_memory"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建目录失败: %w", err)
	}

	filepath := filepath.Join(dir, fmt.Sprintf("%s.json", traderID))

	m := &Manager{
		filepath: filepath,
	}

	// 尝试加载现有记忆
	if err := m.Load(); err != nil {
		if os.IsNotExist(err) {
			// 文件不存在，初始化新记忆
			m.memory = initializeMemory(traderID)
			if err := m.Save(); err != nil {
				return nil, fmt.Errorf("保存初始记忆失败: %w", err)
			}
			fmt.Printf("✨ 初始化新记忆: %s (空白学习模式)\n", traderID)
		} else {
			return nil, fmt.Errorf("加载记忆失败: %w", err)
		}
	} else {
		fmt.Printf("📚 加载现有记忆: %s (已有%d笔交易)\n", traderID, m.memory.TotalTrades)
	}

	return m, nil
}

// initializeMemory 初始化空白记忆（只有硬约束）
func initializeMemory(traderID string) *SimpleMemory {
	return &SimpleMemory{
		Version:      "1.0",
		TraderID:     traderID,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		TotalTrades:  0,
		Status:       "learning", // learning -> mature (>100 trades)
		RecentTrades: make([]TradeEntry, 0, 20),
		HardConstraints: []string{
			"单笔最大亏损不超过5%（系统constraints保证）",
			"日内最大回撤不超过10%",
			"最短持仓时间15分钟（避免频繁交易）",
			"冷却期20分钟（避免情绪化交易）",
		},
	}
}

// Load 从文件加载记忆
func (m *Manager) Load() error {
	data, err := os.ReadFile(m.filepath)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.memory = &SimpleMemory{}
	return json.Unmarshal(data, m.memory)
}

// Save 保存记忆到文件
func (m *Manager) Save() error {
	m.mu.RLock()
	data, err := json.MarshalIndent(m.memory, "", "  ")
	m.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	// 原子写入（先写临时文件，再重命名）
	tmpFile := m.filepath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := os.Rename(tmpFile, m.filepath); err != nil {
		return fmt.Errorf("重命名文件失败: %w", err)
	}

	return nil
}

// AddTrade 添加交易记录
func (m *Manager) AddTrade(entry TradeEntry) error {
	m.mu.Lock()

	// 分配TradeID
	entry.TradeID = m.memory.TotalTrades + 1

	// 添加到RecentTrades（只保留最近20笔）
	m.memory.RecentTrades = append(m.memory.RecentTrades, entry)
	if len(m.memory.RecentTrades) > 20 {
		m.memory.RecentTrades = m.memory.RecentTrades[1:]
	}

	m.memory.TotalTrades++
	m.memory.UpdatedAt = time.Now()

	// 100笔后进入mature状态
	if m.memory.TotalTrades >= 100 && m.memory.Status == "learning" {
		m.memory.Status = "mature"
		fmt.Printf("🎓 学习阶段完成！总共%d笔交易，进入成熟阶段\n", m.memory.TotalTrades)
	}

	// 🧠 自动更新学习总结（至少10笔交易后开始学习）
	if m.memory.TotalTrades >= 10 {
		if err := m.UpdateLearningSummary(); err != nil {
			fmt.Printf("⚠️  更新学习总结失败: %v\n", err)
			// 不影响主流程，继续执行
		}
	}

	// 🔧 修复死锁：在调用Save之前释放锁，因为Save内部也需要获取锁
	m.mu.Unlock()

	return m.Save()
}

// GetContextPrompt 生成上下文提示（供AI决策时使用）
func (m *Manager) GetContextPrompt() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.memory.TotalTrades == 0 {
		return `## 📝 你的记忆

这是你的第一次交易。你没有任何历史记录，从空白开始学习。
`
	}

	prompt := fmt.Sprintf("## 📝 你的最近决策（总共%d笔交易）\n\n", m.memory.TotalTrades)

	// 显示最近3笔（如果有的话）
	recent := m.memory.RecentTrades
	n := len(recent)
	start := n - 3
	if start < 0 {
		start = 0
	}

	for i := start; i < n; i++ {
		trade := recent[i]
		timeSince := time.Since(trade.Timestamp)

		prompt += fmt.Sprintf("**周期#%d** (%s前):\n", trade.Cycle, formatDuration(timeSince))
		prompt += fmt.Sprintf("  决策: %s %s %s\n", trade.Action, trade.Symbol, trade.Side)
		prompt += fmt.Sprintf("  推理: %s\n", trade.Reasoning)

		if trade.PredictedDirection != "" {
			prompt += fmt.Sprintf("  预测: %s %.0f%% 概率，预期%+.1f%%\n",
				trade.PredictedDirection, trade.PredictedProb*100, trade.PredictedMove)
		}

		if trade.Result != "" {
			emoji := "✅"
			if trade.Result == "loss" {
				emoji = "❌"
			} else if trade.Result == "break_even" {
				emoji = "➖"
			}
			prompt += fmt.Sprintf("  结果: %s %s %.2f%%\n", emoji, trade.Result, trade.ReturnPct)
		} else if trade.IsLimitOrder {
			// 🆕 限价单未成交：显示等待状态
			if trade.LimitPrice > 0 && trade.CurrentPrice > 0 {
				var direction string
				var distancePct float64
				if trade.Side == "long" {
					// 做多限价单：等待价格回调到限价
					direction = "⬇️"
					distancePct = ((trade.CurrentPrice - trade.LimitPrice) / trade.CurrentPrice) * 100
				} else {
					// 做空限价单：等待价格反弹到限价
					direction = "⬆️"
					distancePct = ((trade.LimitPrice - trade.CurrentPrice) / trade.CurrentPrice) * 100
				}
				prompt += fmt.Sprintf("  结果: ⏰ 等待限价单成交 (限价%.4f %s 距当前%.2f%%)\n",
					trade.LimitPrice, direction, distancePct)
			} else {
				prompt += "  结果: ⏰ 等待限价单成交\n"
			}
		} else {
			// 市价单已成交，持仓进行中
			prompt += "  结果: ⏳ 进行中\n"
		}
		prompt += "\n"
	}

	// 🧠 添加学习总结（如果有的话）
	if m.memory.LearningSummary != nil && m.memory.TotalTrades >= 10 {
		prompt += "\n## 🧠 你的学习总结（基于历史表现自动生成）\n\n"
		prompt += formatLearningSummary(m.memory.LearningSummary)
	}

	return prompt
}

// GetMemory 获取记忆（用于API）
func (m *Manager) GetMemory() *SimpleMemory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.memory
}

// GetOverallStats 计算整体统计
func (m *Manager) GetOverallStats() OverallStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := OverallStats{
		TotalTrades: m.memory.TotalTrades,
	}

	if len(m.memory.RecentTrades) == 0 {
		return stats
	}

	// 统计所有记录的交易
	var totalReturn float64
	for _, trade := range m.memory.RecentTrades {
		if trade.Result == "win" {
			stats.WinCount++
		} else if trade.Result == "loss" {
			stats.LossCount++
		}
		totalReturn += trade.ReturnPct
	}

	completed := stats.WinCount + stats.LossCount
	if completed > 0 {
		stats.WinRate = float64(stats.WinCount) / float64(completed)
		stats.AvgReturn = totalReturn / float64(completed)
		stats.TotalReturn = totalReturn
	}

	// 计算最近10笔胜率
	recentCount := 10
	if len(m.memory.RecentTrades) < recentCount {
		recentCount = len(m.memory.RecentTrades)
	}

	recentWins := 0
	recentCompleted := 0
	for i := len(m.memory.RecentTrades) - recentCount; i < len(m.memory.RecentTrades); i++ {
		trade := m.memory.RecentTrades[i]
		if trade.Result == "win" {
			recentWins++
			recentCompleted++
		} else if trade.Result == "loss" {
			recentCompleted++
		}
	}

	if recentCompleted > 0 {
		stats.RecentWinRate = float64(recentWins) / float64(recentCompleted)
	}

	return stats
}

// formatDuration 格式化时间间隔
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "刚才"
	} else if d < time.Hour {
		return fmt.Sprintf("%d分钟", int(d.Minutes()))
	} else if d < 24*time.Hour {
		return fmt.Sprintf("%d小时", int(d.Hours()))
	} else {
		return fmt.Sprintf("%d天", int(d.Hours()/24))
	}
}

// formatHardConstraints 格式化硬约束
func formatHardConstraints(constraints []string) string {
	result := ""
	for i, c := range constraints {
		result += fmt.Sprintf("%d. %s\n", i+1, c)
	}
	return result
}

// formatLearningSummary 格式化学习总结
func formatLearningSummary(summary *LearningSummary) string {
	var result string

	// 📊 统计可靠性警告（样本量不足时提醒）
	completedTrades := 0
	for signalName, stat := range summary.SignalStats {
		_ = signalName // 避免未使用变量警告
		if stat.TotalCount > 0 {
			completedTrades += stat.TotalCount
			break // 只需要知道有交易即可
		}
	}

	if completedTrades > 0 && completedTrades < 50 {
		result += "### ⚠️  统计可靠性提醒\n\n"
		result += fmt.Sprintf("当前总交易样本较少，统计结果仅供参考。\n")
		result += "建议积累至少50笔交易后，学习总结会更加可靠。\n\n"
	}

	// 1️⃣ 失败模式（优先显示）
	if len(summary.FailurePatterns) > 0 {
		result += "### ⚠️  识别到的失败模式\n\n"
		for _, pattern := range summary.FailurePatterns {
			result += fmt.Sprintf("- %s\n", pattern)
		}
		result += "\n"
	}

	// 2️⃣ 成功经验
	if len(summary.SuccessPatterns) > 0 {
		result += "### ✅ 总结的成功经验\n\n"
		for _, pattern := range summary.SuccessPatterns {
			result += fmt.Sprintf("- %s\n", pattern)
		}
		result += "\n"
	}

	// 3️⃣ 市场环境偏好
	if len(summary.MarketPreferences) > 0 {
		result += "### 📊 市场环境适应性\n\n"
		for regime, winRate := range summary.MarketPreferences {
			emoji := "✅"
			if winRate < 0.4 {
				emoji = "❌"
			} else if winRate < 0.5 {
				emoji = "⚠️"
			}
			result += fmt.Sprintf("- %s %s: 胜率 %.0f%%\n", emoji, regime, winRate*100)
		}
		result += "\n"
	}

	// 4️⃣ 信号统计（样本量≥20，显示置信度）
	if len(summary.SignalStats) > 0 {
		result += "### 🎯 关键信号成功率（样本≥20）\n\n"
		for _, stat := range summary.SignalStats {
			if stat.TotalCount >= 20 {
				emoji := "✅"
				if stat.WinRate < 0.4 {
					emoji = "❌"
				} else if stat.WinRate < 0.5 {
					emoji = "⚠️"
				}

				// 置信度标签
				confidence := "中等"
				if stat.TotalCount >= 50 {
					confidence = "高"
				} else if stat.TotalCount < 30 {
					confidence = "低"
				}

				result += fmt.Sprintf("- %s \"%s\": %.0f%% (%d胜/%d负，样本:%d，置信度:%s)\n",
					emoji, stat.SignalName, stat.WinRate*100, stat.WinCount, stat.LossCount, stat.TotalCount, confidence)
			}
		}
		result += "\n"
	}

	updateTime := time.Since(summary.UpdatedAt)
	result += fmt.Sprintf("*最后更新: %s前*\n", formatDuration(updateTime))

	return result
}


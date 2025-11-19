package tracker

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"nofx/decision/types"
	"nofx/market"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PredictionTracker 预测跟踪器
// 记录AI的每次预测，并在时间窗口后验证准确性
type PredictionTracker struct {
	dataDir string
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

var intervalDurations = map[string]time.Duration{
	"1m":  time.Minute,
	"3m":  3 * time.Minute,
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
}

// NewPredictionTracker 创建预测跟踪器
func NewPredictionTracker(dataDir string) *PredictionTracker {
	// 确保目录存在
	os.MkdirAll(dataDir, 0755)

	return &PredictionTracker{
		dataDir: dataDir,
	}
}

// PredictionRecord 预测记录
type PredictionRecord struct {
	ID            string            `json:"id"`
	Timestamp     time.Time         `json:"timestamp"`
	Symbol        string            `json:"symbol"`
	Prediction    *types.Prediction `json:"prediction"`
	EntryPrice    float64           `json:"entry_price"`    // 预测时的价格
	TargetTime    time.Time         `json:"target_time"`    // 预测目标时间
	Evaluated     bool              `json:"evaluated"`      // 是否已评估
	ActualMove    float64           `json:"actual_move"`    // 实际涨跌幅
	ActualHigh    float64           `json:"actual_high"`    // 期间最高价
	ActualLow     float64           `json:"actual_low"`     // 期间最低价
	IsCorrect     bool              `json:"is_correct"`     // 方向是否正确
	Accuracy      float64           `json:"accuracy"`       // 预测准确度(0-1)
	EvaluatedTime time.Time         `json:"evaluated_time"` // 评估时间

	// 🆕 记录所有预测（包括被拒绝的）
	Executed     bool   `json:"executed"`      // 是否实际开仓
	RejectReason string `json:"reject_reason"` // 拒绝原因（如果未执行）
}

// Record 记录一次预测（已执行的开仓）
func (pt *PredictionTracker) Record(prediction *types.Prediction, currentPrice float64) error {
	// 生成唯一ID
	id := fmt.Sprintf("%s_%d", prediction.Symbol, time.Now().Unix())

	// 计算目标时间
	targetTime := pt.calculateTargetTime(prediction.Timeframe)

	record := &PredictionRecord{
		ID:         id,
		Timestamp:  time.Now(),
		Symbol:     prediction.Symbol,
		Prediction: prediction,
		EntryPrice: currentPrice,
		TargetTime: targetTime,
		Evaluated:  false,
		Executed:   true, // 🆕 标记为已执行
	}

	// 保存到文件
	filename := filepath.Join(pt.dataDir, fmt.Sprintf("%s.json", id))
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}

	return ioutil.WriteFile(filename, data, 0644)
}

// RecordAll 记录所有预测（包括被拒绝的）
// 用于全面评估AI预测准确率
func (pt *PredictionTracker) RecordAll(prediction *types.Prediction, currentPrice float64, executed bool, rejectReason string) error {
	// 生成唯一ID（使用纳秒避免同一秒多个预测冲突）
	id := fmt.Sprintf("%s_%d_%d", prediction.Symbol, time.Now().Unix(), time.Now().Nanosecond())

	// 计算目标时间
	targetTime := pt.calculateTargetTime(prediction.Timeframe)

	record := &PredictionRecord{
		ID:           id,
		Timestamp:    time.Now(),
		Symbol:       prediction.Symbol,
		Prediction:   prediction,
		EntryPrice:   currentPrice,
		TargetTime:   targetTime,
		Evaluated:    false,
		Executed:     executed,
		RejectReason: rejectReason,
	}

	// 保存到文件
	filename := filepath.Join(pt.dataDir, fmt.Sprintf("%s.json", id))
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}

	return ioutil.WriteFile(filename, data, 0644)
}

// calculateTargetTime 计算预测目标时间
func (pt *PredictionTracker) calculateTargetTime(timeframe string) time.Time {
	now := time.Now()
	switch timeframe {
	case "1h":
		return now.Add(1 * time.Hour)
	case "4h":
		return now.Add(4 * time.Hour)
	case "24h":
		return now.Add(24 * time.Hour)
	default:
		return now.Add(4 * time.Hour) // 默认4小时
	}
}

// EvaluatePending 评估所有待评估的预测
func (pt *PredictionTracker) EvaluatePending() error {
	files, err := ioutil.ReadDir(pt.dataDir)
	if err != nil {
		return err
	}

	now := time.Now()

	for _, file := range files {
		if filepath.Ext(file.Name()) != ".json" {
			continue
		}

		// 读取记录
		fullPath := filepath.Join(pt.dataDir, file.Name())
		data, err := ioutil.ReadFile(fullPath)
		if err != nil {
			continue
		}

		var record PredictionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}

		// 跳过已评估的
		if record.Evaluated {
			continue
		}

		// 检查是否到达目标时间
		if now.Before(record.TargetTime) {
			continue // 还没到评估时间
		}

		// 获取实际价格数据
		actualData, err := pt.getActualPriceData(record.Symbol, record.Timestamp, record.TargetTime)
		if err != nil {
			fmt.Printf("⚠️  获取%s实际价格失败: %v\n", record.Symbol, err)
			continue
		}

		// 评估预测
		pt.evaluateRecord(&record, actualData)

		// 保存更新后的记录
		updatedData, _ := json.MarshalIndent(record, "", "  ")
		ioutil.WriteFile(fullPath, updatedData, 0644)
	}

	return nil
}

// ActualPriceData 实际价格数据
type ActualPriceData struct {
	FinalPrice float64
	HighPrice  float64
	LowPrice   float64
}

// getActualPriceData 获取实际价格数据
func (pt *PredictionTracker) getActualPriceData(symbol string, startTime, endTime time.Time) (*ActualPriceData, error) {
	klines, err := fetchHistoricalKlines(symbol, startTime, endTime)
	if err != nil || len(klines) == 0 {
		// 回退：无法获取历史数据时使用实时价，保持兼容
		marketData, err2 := market.Get(symbol)
		if err2 != nil {
			if err != nil {
				return nil, err
			}
			return nil, err2
		}
		return &ActualPriceData{
			FinalPrice: marketData.CurrentPrice,
			HighPrice:  marketData.CurrentPrice,
			LowPrice:   marketData.CurrentPrice,
		}, nil
	}

	final := klines[len(klines)-1].Close
	high := klines[0].High
	low := klines[0].Low
	for _, k := range klines {
		if k.High > high {
			high = k.High
		}
		if k.Low < low {
			low = k.Low
		}
	}

	return &ActualPriceData{
		FinalPrice: final,
		HighPrice:  high,
		LowPrice:   low,
	}, nil
}

// evaluateRecord 评估单条预测记录
func (pt *PredictionTracker) evaluateRecord(record *PredictionRecord, actualData *ActualPriceData) {
	// 计算实际涨跌幅
	record.ActualMove = ((actualData.FinalPrice - record.EntryPrice) / record.EntryPrice) * 100
	record.ActualHigh = actualData.HighPrice
	record.ActualLow = actualData.LowPrice

	// 判断方向是否正确
	pred := record.Prediction
	if pred.Direction == "up" && record.ActualMove > 0 {
		record.IsCorrect = true
	} else if pred.Direction == "down" && record.ActualMove < 0 {
		record.IsCorrect = true
	} else if pred.Direction == "neutral" && math.Abs(record.ActualMove) < 1.0 {
		record.IsCorrect = true
	} else {
		record.IsCorrect = false
	}

	// 计算准确度（预测幅度 vs 实际幅度）
	if pred.ExpectedMove != 0 {
		deviation := math.Abs(pred.ExpectedMove - record.ActualMove)
		record.Accuracy = 1.0 - math.Min(deviation/math.Abs(pred.ExpectedMove), 1.0)
	} else {
		record.Accuracy = 0.5 // 预测幅度为0时给中性评分
	}

	record.Evaluated = true
	record.EvaluatedTime = time.Now()
}

// GetPerformance 获取历史预测表现
func (pt *PredictionTracker) GetPerformance(symbol string) *types.HistoricalPerformance {
	files, err := ioutil.ReadDir(pt.dataDir)
	if err != nil {
		return &types.HistoricalPerformance{}
	}

	var allRecords []PredictionRecord
	var symbolRecords []PredictionRecord

	for _, file := range files {
		if filepath.Ext(file.Name()) != ".json" {
			continue
		}

		fullPath := filepath.Join(pt.dataDir, file.Name())
		data, err := ioutil.ReadFile(fullPath)
		if err != nil {
			continue
		}

		var record PredictionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}

		// 只统计已评估的记录
		if !record.Evaluated {
			continue
		}

		allRecords = append(allRecords, record)

		if record.Symbol == symbol {
			symbolRecords = append(symbolRecords, record)
		}
	}

	perf := &types.HistoricalPerformance{}

	// 计算总体胜率
	if len(allRecords) > 0 {
		correctCount := 0
		totalAccuracy := 0.0
		for _, r := range allRecords {
			if r.IsCorrect {
				correctCount++
			}
			totalAccuracy += r.Accuracy
		}
		perf.OverallWinRate = float64(correctCount) / float64(len(allRecords))
		perf.AvgAccuracy = totalAccuracy / float64(len(allRecords))
	}

	// 计算该币种胜率
	if len(symbolRecords) > 0 {
		correctCount := 0
		for _, r := range symbolRecords {
			if r.IsCorrect {
				correctCount++
			}
		}
		perf.SymbolWinRate = float64(correctCount) / float64(len(symbolRecords))
	}

	// 分析常见错误
	perf.CommonMistakes = pt.analyzeCommonMistakes(allRecords)

	return perf
}

// analyzeCommonMistakes 分析常见错误
func (pt *PredictionTracker) analyzeCommonMistakes(records []PredictionRecord) string {
	if len(records) < 10 {
		return "样本量不足，无法分析"
	}

	// 分析在不同市场条件下的表现
	// 例如：高波动时准确率如何、趋势市场vs震荡市场等

	// 简化实现：返回基本统计
	mistakes := make(map[string]int)
	for _, r := range records {
		if !r.IsCorrect {
			// 记录失败的预测特征
			if r.Prediction.Confidence == "very_high" {
				mistakes["过度自信"]++
			}
			if r.Prediction.RiskLevel == "low" && !r.IsCorrect {
				mistakes["低估风险"]++
			}
		}
	}

	if len(mistakes) == 0 {
		return "表现良好，无明显错误模式"
	}

	// 找出最常见的错误
	type kv struct {
		Key   string
		Value int
	}
	var sorted []kv
	for k, v := range mistakes {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Value > sorted[j].Value
	})

	return fmt.Sprintf("%s (发生%d次)", sorted[0].Key, sorted[0].Value)
}

// GetRecentPredictions 获取最近的预测记录（用于展示）
func (pt *PredictionTracker) GetRecentPredictions(limit int) []PredictionRecord {
	files, err := ioutil.ReadDir(pt.dataDir)
	if err != nil {
		return []PredictionRecord{}
	}

	var records []PredictionRecord

	for _, file := range files {
		if filepath.Ext(file.Name()) != ".json" {
			continue
		}

		fullPath := filepath.Join(pt.dataDir, file.Name())
		data, err := ioutil.ReadFile(fullPath)
		if err != nil {
			continue
		}

		var record PredictionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}

		records = append(records, record)
	}

	// 按时间排序（最新的在前）
	sort.Slice(records, func(i, j int) bool {
		return records[i].Timestamp.After(records[j].Timestamp)
	})

	// 限制数量
	if len(records) > limit {
		records = records[:limit]
	}

	return records
}

// GetRecentFeedback 获取最近预测的反馈摘要（用于AI自我学习）
func (pt *PredictionTracker) GetRecentFeedback(symbol string, limit int) string {
	// 获取所有最近的预测
	allRecords := pt.GetRecentPredictions(limit * 3) // 多获取一些，然后过滤

	// 过滤指定币种（如果提供）+ 只保留已评估的
	var records []PredictionRecord
	for _, rec := range allRecords {
		if !rec.Evaluated {
			continue
		}
		if symbol == "" || rec.Symbol == symbol {
			records = append(records, rec)
		}
		if len(records) >= limit {
			break
		}
	}

	if len(records) == 0 {
		return ""
	}

	// 统计准确率
	correct := 0
	successes := []string{}
	mistakes := []string{}

	for _, rec := range records {
		timeAgo := time.Since(rec.Timestamp)
		hoursAgo := int(timeAgo.Hours())
		minutesAgo := int(timeAgo.Minutes())

		var timeStr string
		if hoursAgo > 0 {
			timeStr = fmt.Sprintf("%dh ago", hoursAgo)
		} else {
			timeStr = fmt.Sprintf("%dm ago", minutesAgo)
		}

		if rec.IsCorrect {
			correct++
			// 记录成功案例
			success := fmt.Sprintf("%s %s: predicted %s %.1f%%, actually %.1f%% ✓",
				timeStr, rec.Symbol, rec.Prediction.Direction,
				rec.Prediction.ExpectedMove, rec.ActualMove)
			successes = append(successes, success)
		} else {
			// 记录错误案例
			mistake := fmt.Sprintf("%s %s: predicted %s %.1f%%, actually %.1f%%",
				timeStr, rec.Symbol, rec.Prediction.Direction,
				rec.Prediction.ExpectedMove, rec.ActualMove)
			mistakes = append(mistakes, mistake)
		}
	}

	// 构建反馈
	accuracy := float64(correct) / float64(len(records)) * 100
	var feedback strings.Builder

	feedback.WriteString(fmt.Sprintf("Recent Performance: %d/%d correct (%.0f%% accuracy)\n",
		correct, len(records), accuracy))

	// 🎯 关键改进：先显示成功，再显示错误（正向激励）
	if len(successes) > 0 {
		feedback.WriteString("\n✅ Recent Successes:\n")
		maxShow := 2
		if len(successes) < maxShow {
			maxShow = len(successes)
		}
		for i := 0; i < maxShow; i++ {
			feedback.WriteString(fmt.Sprintf("  • %s\n", successes[i]))
		}
	}

	// 显示错误（但不要过于负面）
	if len(mistakes) > 0 {
		feedback.WriteString("\n⚠️  Areas for Improvement:\n")
		maxShow := 2
		if len(mistakes) < maxShow {
			maxShow = len(mistakes)
		}
		for i := 0; i < maxShow; i++ {
			feedback.WriteString(fmt.Sprintf("  • %s\n", mistakes[i]))
		}

		// 尝试识别错误模式
		guidance := pt.analyzeErrorPattern(records)
		if guidance != "" {
			feedback.WriteString(fmt.Sprintf("\n💡 Insight: %s\n", guidance))
		}
	}

	// 🎯 根据表现给出平衡的建议
	if accuracy >= 70 {
		feedback.WriteString("\n✨ Good performance! Maintain your analytical approach.\n")
	} else if accuracy >= 50 {
		feedback.WriteString("\n📊 Moderate accuracy. Refine signals but keep predicting.\n")
	} else {
		feedback.WriteString("\n🔍 Review methodology. Focus on stronger confirmation signals.\n")
	}

	return feedback.String()
}

// analyzeErrorPattern 简单的错误模式识别
func (pt *PredictionTracker) analyzeErrorPattern(records []PredictionRecord) string {
	if len(records) < 5 {
		return ""
	}

	// 统计错误类型
	wrongDirection := 0
	overestimated := 0
	underestimated := 0

	for _, rec := range records {
		if !rec.IsCorrect {
			// 方向完全错误
			if (rec.Prediction.Direction == "up" && rec.ActualMove < 0) ||
				(rec.Prediction.Direction == "down" && rec.ActualMove > 0) {
				wrongDirection++
			}

			// 高估了幅度
			if math.Abs(rec.Prediction.ExpectedMove) > math.Abs(rec.ActualMove) {
				overestimated++
			}

			// 低估了幅度
			if math.Abs(rec.Prediction.ExpectedMove) < math.Abs(rec.ActualMove) {
				underestimated++
			}
		}
	}

	// 生成建议
	if wrongDirection >= 3 {
		return "Frequently wrong on direction. Require stronger trend confirmation signals."
	}
	if overestimated >= 3 {
		return "Often overestimate move size. Be more conservative on expected_move."
	}
	if underestimated >= 3 {
		return "Often underestimate moves. Consider stronger momentum when signals align."
	}

	return ""
}

// fetchHistoricalKlines 获取指定时间区间的历史K线
func fetchHistoricalKlines(symbol string, startTime, endTime time.Time) ([]market.Kline, error) {
	if endTime.Before(startTime) {
		endTime = startTime.Add(time.Hour)
	}

	duration := endTime.Sub(startTime)
	interval := chooseInterval(duration)
	intervalDuration := intervalDurations[interval]
	limit := int(duration/intervalDuration) + 5
	if limit < 10 {
		limit = 10
	}
	if limit > 1500 {
		limit = 1500
	}

	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/klines?symbol=%s&interval=%s&startTime=%d&endTime=%d&limit=%d",
		strings.ToUpper(symbol),
		interval,
		startTime.Add(-intervalDuration).UnixMilli(), // 向前扩展一根
		endTime.UnixMilli(),
		limit,
	)

	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("历史K线请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("历史K线HTTP %d: %s", resp.StatusCode, string(body))
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rawData [][]interface{}
	if err := json.Unmarshal(body, &rawData); err != nil {
		return nil, err
	}

	klines := make([]market.Kline, 0, len(rawData))
	for _, item := range rawData {
		if len(item) < 7 {
			continue
		}
		openTime := int64(item[0].(float64))
		open, _ := parseFloat(item[1])
		high, _ := parseFloat(item[2])
		low, _ := parseFloat(item[3])
		closeVal, _ := parseFloat(item[4])
		volume, _ := parseFloat(item[5])
		closeTime := int64(item[6].(float64))

		if time.UnixMilli(closeTime).Before(startTime) {
			continue
		}
		if time.UnixMilli(openTime).After(endTime) {
			break
		}

		klines = append(klines, market.Kline{
			OpenTime:  openTime,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     closeVal,
			Volume:    volume,
			CloseTime: closeTime,
		})
	}

	return klines, nil
}

func chooseInterval(duration time.Duration) string {
	switch {
	case duration <= 6*time.Hour:
		return "1m"
	case duration <= 24*time.Hour:
		return "5m"
	default:
		return "15m"
	}
}

func parseFloat(val interface{}) (float64, error) {
	switch v := val.(type) {
	case float64:
		return v, nil
	case string:
		return strconv.ParseFloat(v, 64)
	default:
		return 0, fmt.Errorf("无法解析浮点数: %v", val)
	}
}

// ==================== AI预测校准系统 ====================

// CalibrationData 校准数据
type CalibrationData struct {
	Symbol            string  // 币种
	SampleSize        int     // 样本数量
	CalibrationFactor float64 // 校准因子（实际准确率/预测置信度）
	OverconfidenceBias float64 // 过度自信偏差
	DirectionAccuracy float64 // 方向准确率
	MagnitudeAccuracy float64 // 幅度准确率
}

// GetCalibrationFactor 获取预测校准因子
// 基于历史预测的实际表现来校准AI的置信度
func (pt *PredictionTracker) GetCalibrationFactor(symbol string) *CalibrationData {
	files, err := ioutil.ReadDir(pt.dataDir)
	if err != nil {
		return &CalibrationData{Symbol: symbol, SampleSize: 0, CalibrationFactor: 1.0}
	}

	var records []PredictionRecord

	// 收集指定币种的历史记录
	for _, file := range files {
		if filepath.Ext(file.Name()) != ".json" {
			continue
		}

		fullPath := filepath.Join(pt.dataDir, file.Name())
		data, err := ioutil.ReadFile(fullPath)
		if err != nil {
			continue
		}

		var record PredictionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}

		// 只统计已评估的记录
		if !record.Evaluated {
			continue
		}

		// 如果指定了币种，只收集该币种的记录
		if symbol != "" && record.Symbol != symbol {
			continue
		}

		records = append(records, record)
	}

	// 样本不足时返回默认校准因子
	if len(records) < 5 {
		return &CalibrationData{
			Symbol:            symbol,
			SampleSize:        len(records),
			CalibrationFactor: 1.0,
			DirectionAccuracy: 0.5,
			MagnitudeAccuracy: 0.5,
		}
	}

	// 计算校准指标
	totalPredictedProb := 0.0
	totalActualCorrect := 0
	totalMagnitudeError := 0.0
	overconfidentCount := 0 // 高置信度预测失败次数

	for _, rec := range records {
		totalPredictedProb += rec.Prediction.Probability

		if rec.IsCorrect {
			totalActualCorrect++
		}

		// 幅度误差（预测vs实际）
		if rec.Prediction.ExpectedMove != 0 {
			magnitudeError := math.Abs(rec.Prediction.ExpectedMove-rec.ActualMove) / math.Abs(rec.Prediction.ExpectedMove)
			if magnitudeError > 1.0 {
				magnitudeError = 1.0
			}
			totalMagnitudeError += magnitudeError
		}

		// 过度自信检测：高置信度（>70%）预测失败
		if rec.Prediction.Probability > 0.70 && !rec.IsCorrect {
			overconfidentCount++
		}
	}

	avgPredictedProb := totalPredictedProb / float64(len(records))
	actualAccuracy := float64(totalActualCorrect) / float64(len(records))
	avgMagnitudeError := totalMagnitudeError / float64(len(records))

	// 校准因子 = 实际准确率 / 平均预测概率
	// 如果 AI 总是预测 70% 但只对 50%，校准因子 = 0.50/0.70 = 0.71
	// 这意味着下次 AI 预测 70% 时，实际应该打 70% * 0.71 = 50%
	calibrationFactor := 1.0
	if avgPredictedProb > 0.1 {
		calibrationFactor = actualAccuracy / avgPredictedProb
		// 限制校准因子范围 [0.5, 1.5]
		if calibrationFactor < 0.5 {
			calibrationFactor = 0.5
		}
		if calibrationFactor > 1.5 {
			calibrationFactor = 1.5
		}
	}

	// 过度自信偏差
	overconfidenceBias := float64(overconfidentCount) / float64(len(records))

	return &CalibrationData{
		Symbol:            symbol,
		SampleSize:        len(records),
		CalibrationFactor: calibrationFactor,
		OverconfidenceBias: overconfidenceBias,
		DirectionAccuracy: actualAccuracy,
		MagnitudeAccuracy: 1.0 - avgMagnitudeError,
	}
}

// CalibrateProbability 校准预测概率
// 根据历史表现调整AI给出的概率
func (pt *PredictionTracker) CalibrateProbability(symbol string, originalProb float64) float64 {
	calibration := pt.GetCalibrationFactor(symbol)

	// 样本不足时不校准
	if calibration.SampleSize < 5 {
		return originalProb
	}

	// 应用校准因子
	calibratedProb := originalProb * calibration.CalibrationFactor

	// 限制范围 [0.0, 1.0]
	if calibratedProb > 1.0 {
		calibratedProb = 1.0
	}
	if calibratedProb < 0.0 {
		calibratedProb = 0.0
	}

	return calibratedProb
}

// GetCalibrationSummary 获取校准摘要（用于日志和监控）
func (pt *PredictionTracker) GetCalibrationSummary() string {
	// 获取所有币种的校准数据
	symbols := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"} // 主要币种
	var sb strings.Builder

	sb.WriteString("=== AI预测校准报告 ===\n\n")

	for _, symbol := range symbols {
		cal := pt.GetCalibrationFactor(symbol)
		if cal.SampleSize < 3 {
			continue
		}

		sb.WriteString(fmt.Sprintf("%s (样本=%d):\n", symbol, cal.SampleSize))
		sb.WriteString(fmt.Sprintf("  校准因子: %.2f", cal.CalibrationFactor))

		if cal.CalibrationFactor < 0.8 {
			sb.WriteString(" ⚠️ 过度自信")
		} else if cal.CalibrationFactor > 1.2 {
			sb.WriteString(" 📈 保守预测")
		} else {
			sb.WriteString(" ✅ 校准良好")
		}

		sb.WriteString(fmt.Sprintf("\n  方向准确率: %.0f%%\n", cal.DirectionAccuracy*100))
		sb.WriteString(fmt.Sprintf("  幅度准确率: %.0f%%\n\n", cal.MagnitudeAccuracy*100))
	}

	// 整体校准
	overallCal := pt.GetCalibrationFactor("")
	if overallCal.SampleSize >= 5 {
		sb.WriteString(fmt.Sprintf("整体 (样本=%d): 校准因子=%.2f, 方向准确率=%.0f%%\n",
			overallCal.SampleSize, overallCal.CalibrationFactor, overallCal.DirectionAccuracy*100))
	}

	return sb.String()
}

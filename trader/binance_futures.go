package trader

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2/futures"
)

// FuturesTrader 币安合约交易器
type FuturesTrader struct {
	client *futures.Client

	// 余额缓存
	cachedBalance     map[string]interface{}
	balanceCacheTime  time.Time
	balanceCacheMutex sync.RWMutex

	// 持仓缓存
	cachedPositions     []map[string]interface{}
	positionsCacheTime  time.Time
	positionsCacheMutex sync.RWMutex

	// 冷却期管理：记录每个币种的最后平仓时间
	lastCloseTimes     map[string]time.Time
	closeTimeMutex     sync.RWMutex
	cooldownDuration   time.Duration // 冷却期时长（默认4小时）

	// 缓存有效期（60秒）- 防止API限流
	cacheDuration time.Duration
}

// NewFuturesTrader 创建合约交易器
func NewFuturesTrader(apiKey, secretKey string, useTestnet bool) *FuturesTrader {
	client := futures.NewClient(apiKey, secretKey)

	// 如果使用testnet，设置测试网URL
	if useTestnet {
		client.BaseURL = "https://testnet.binancefuture.com"
		log.Printf("🧪 使用Binance Futures Testnet: %s", client.BaseURL)
	} else {
		log.Printf("💰 使用Binance Futures主网")
	}

	return &FuturesTrader{
		client:           client,
		cacheDuration:    60 * time.Second,  // 60秒缓存（防止币安API限流封禁）
		lastCloseTimes:   make(map[string]time.Time), // 初始化冷却期记录
		cooldownDuration: 20 * time.Minute,  // 20分钟冷却期（与TradingConstraints统一）
	}
}

// GetBalance 获取账户余额（带缓存）
func (t *FuturesTrader) GetBalance() (map[string]interface{}, error) {
	// 先检查缓存是否有效
	t.balanceCacheMutex.RLock()
	if t.cachedBalance != nil && time.Since(t.balanceCacheTime) < t.cacheDuration {
		cacheAge := time.Since(t.balanceCacheTime)
		t.balanceCacheMutex.RUnlock()
		log.Printf("✓ 使用缓存的账户余额（缓存时间: %.1f秒前）", cacheAge.Seconds())
		return t.cachedBalance, nil
	}
	t.balanceCacheMutex.RUnlock()

	// 缓存过期或不存在，调用API
	log.Printf("🔄 缓存过期，正在调用币安API获取账户余额...")
	account, err := t.client.NewGetAccountService().Do(context.Background())
	if err != nil {
		log.Printf("❌ 币安API调用失败: %v", err)
		return nil, fmt.Errorf("获取账户信息失败: %w", err)
	}

	result := make(map[string]interface{})
	result["totalWalletBalance"], _ = strconv.ParseFloat(account.TotalWalletBalance, 64)
	result["availableBalance"], _ = strconv.ParseFloat(account.AvailableBalance, 64)
	result["totalUnrealizedProfit"], _ = strconv.ParseFloat(account.TotalUnrealizedProfit, 64)

	log.Printf("✓ 币安API返回: 总余额=%s, 可用=%s, 未实现盈亏=%s",
		account.TotalWalletBalance,
		account.AvailableBalance,
		account.TotalUnrealizedProfit)

	// 更新缓存
	t.balanceCacheMutex.Lock()
	t.cachedBalance = result
	t.balanceCacheTime = time.Now()
	t.balanceCacheMutex.Unlock()

	return result, nil
}

// GetPositions 获取所有持仓（带缓存）
func (t *FuturesTrader) GetPositions() ([]map[string]interface{}, error) {
	// 先检查缓存是否有效
	t.positionsCacheMutex.RLock()
	if t.cachedPositions != nil && time.Since(t.positionsCacheTime) < t.cacheDuration {
		cacheAge := time.Since(t.positionsCacheTime)
		t.positionsCacheMutex.RUnlock()
		log.Printf("✓ 使用缓存的持仓信息（缓存时间: %.1f秒前）", cacheAge.Seconds())
		return t.cachedPositions, nil
	}
	t.positionsCacheMutex.RUnlock()

	// 缓存过期或不存在，调用API
	log.Printf("🔄 缓存过期，正在调用币安API获取持仓信息...")
	positions, err := t.client.NewGetPositionRiskService().Do(context.Background())
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	var result []map[string]interface{}
	for _, pos := range positions {
		posAmt, _ := strconv.ParseFloat(pos.PositionAmt, 64)
		if posAmt == 0 {
			continue // 跳过无持仓的
		}

		posMap := make(map[string]interface{})
		posMap["symbol"] = pos.Symbol
		posMap["positionAmt"], _ = strconv.ParseFloat(pos.PositionAmt, 64)
		posMap["entryPrice"], _ = strconv.ParseFloat(pos.EntryPrice, 64)
		posMap["markPrice"], _ = strconv.ParseFloat(pos.MarkPrice, 64)
		posMap["unRealizedProfit"], _ = strconv.ParseFloat(pos.UnRealizedProfit, 64)
		posMap["leverage"], _ = strconv.ParseFloat(pos.Leverage, 64)
		posMap["liquidationPrice"], _ = strconv.ParseFloat(pos.LiquidationPrice, 64)

		// 判断方向
		if posAmt > 0 {
			posMap["side"] = "long"
		} else {
			posMap["side"] = "short"
		}

		result = append(result, posMap)
	}

	// 动态移动止损逻辑（在缓存更新前执行）
	for _, posMap := range result {
		symbol := posMap["symbol"].(string)
		side := posMap["side"].(string)
		entryPrice := posMap["entryPrice"].(float64) // 需要入场价用于保本保护
		markPrice := posMap["markPrice"].(float64)
		unRealizedProfit := posMap["unRealizedProfit"].(float64)
		leverage := int(posMap["leverage"].(float64))
		positionAmt := posMap["positionAmt"].(float64)

		// 🔧 修复：使用盈利百分比而不是价格变动百分比
		// 问题：之前使用价格变动（0.75%），但6倍杠杆时盈利是4.49%
		//       导致即使盈利4.49%，因为价格变动<2%而不触发移动止损
		// 修复：计算相对于保证金的盈利百分比

		// 计算保证金（仓位价值 / 杠杆）
		positionValue := math.Abs(positionAmt) * entryPrice
		margin := positionValue / float64(leverage)

		// 计算盈利百分比（盈利/保证金）
		var profitPct float64
		if margin > 0 {
			profitPct = (unRealizedProfit / margin) * 100
		}

		// 同时计算价格变动百分比（用于保护比例计算）
		var priceMovePct float64
		if side == "long" {
			priceMovePct = ((markPrice - entryPrice) / entryPrice) * 100
		} else {
			priceMovePct = ((entryPrice - markPrice) / entryPrice) * 100
		}

		// 【优化1】触发阈值：盈利≥5%时才触发移动止损
		// 说明：使用盈利百分比代替价格变动，统一适用于所有杠杆
		//       5%盈利对于6x-9x杠杆都是合理的保护阈值
		if profitPct < 5.0 {
			log.Printf("💤 [跳过移动止损] %s %s | 盈利%.2f%% < 5.0%% (阈值未达到)",
				symbol, side, profitPct)
			continue
		}

		// 【优化2】小额利润保护：绝对利润<1 USDT不移动止损
		absoluteProfit := unRealizedProfit
		if absoluteProfit < 0 {
			absoluteProfit = -absoluteProfit
		}
		if absoluteProfit < 1.0 {
			log.Printf("💰 [跳过移动止损] %s %s | 利润%.2f USDT < 1.0 USDT（太小，不移动）",
				symbol, side, absoluteProfit)
			continue
		}

		// 🔧 根据价格变动决定保护比例（不是触发条件）
		// 价格变动越大，保护比例越高
		//
		// 新策略：止损 = 入场价 + (当前价格 - 入场价) × 保护比例
		// 例如：价格涨3%，保护70%利润 → 止损在入场价+2.1%
		var newStopLoss float64
		var protectionRatio float64  // 利润保护比例

		if priceMovePct >= 10.0 {
			protectionRatio = 0.80  // 价格涨≥10%，保护80%利润
		} else if priceMovePct >= 7.0 {
			protectionRatio = 0.70  // 价格涨≥7%，保护70%利润
		} else if priceMovePct >= 5.0 {
			protectionRatio = 0.60  // 价格涨≥5%，保护60%利润
		} else if priceMovePct >= 3.0 {
			protectionRatio = 0.50  // 价格涨≥3%，保护50%利润
		} else {
			protectionRatio = 0.40  // 价格涨<3%，保护40%利润（最低保护）
		}

		if side == "long" {
			// 做多：止损 = 入场价 + (当前价 - 入场价) × 保护比例
			priceGain := markPrice - entryPrice
			newStopLoss = entryPrice + priceGain*protectionRatio
		} else {
			// 做空：止损 = 入场价 - (入场价 - 当前价) × 保护比例
			priceGain := entryPrice - markPrice
			newStopLoss = entryPrice - priceGain*protectionRatio
		}

		// 计算保本价
		var breakEvenPrice float64
		if side == "long" {
			breakEvenPrice = entryPrice * 1.001  // 保本价（入场价+0.1%手续费）
		} else {
			breakEvenPrice = entryPrice * 0.999  // 保本价（入场价-0.1%手续费）
		}

		// 获取当前止损订单
		currentStopLoss, err := t.getCurrentStopLoss(symbol, side)

		// 判断是否需要更新止损
		shouldUpdate := false
		var oldStopLoss float64

		if err != nil {
			// ✅ 如果没有找到当前止损单，直接设置新止损
			log.Printf("⚠️  [%s %s] 未找到现有止损单，将设置新止损", symbol, side)
			shouldUpdate = true
			oldStopLoss = 0 // 标记为没有旧止损

			// 🔒 第一次设置止损：使用保本保护
			if side == "long" && newStopLoss < breakEvenPrice {
				log.Printf("🔒 [保本保护] %s 止损从%.4f提升到保本价%.4f",
					symbol, newStopLoss, breakEvenPrice)
				newStopLoss = breakEvenPrice
			} else if side == "short" && newStopLoss > breakEvenPrice {
				log.Printf("🔒 [保本保护] %s 止损从%.4f降低到保本价%.4f",
					symbol, newStopLoss, breakEvenPrice)
				newStopLoss = breakEvenPrice
			}
		} else {
			// 有现有止损单，判断新止损是否更有利
			oldStopLoss = currentStopLoss

			// ✅ 修复：移动止损只能向有利方向移动
			if side == "long" {
				// 做多：新止损必须高于旧止损才更新（只升不降）
				if newStopLoss > currentStopLoss {
					shouldUpdate = true
					log.Printf("📈 [移动止损触发] %s LONG | 旧止损%.4f → 新止损%.4f (提高%.4f)",
						symbol, currentStopLoss, newStopLoss, newStopLoss-currentStopLoss)
				} else {
					log.Printf("💤 [移动止损跳过] %s LONG | 新止损%.4f ≤ 旧止损%.4f (不提高)",
						symbol, newStopLoss, currentStopLoss)
				}
			} else {
				// 做空：新止损必须低于旧止损才更新（只降不升）
				if newStopLoss < currentStopLoss {
					shouldUpdate = true
					log.Printf("📈 [移动止损触发] %s SHORT | 旧止损%.4f → 新止损%.4f (降低%.4f)",
						symbol, currentStopLoss, newStopLoss, currentStopLoss-newStopLoss)
				} else {
					log.Printf("💤 [移动止损跳过] %s SHORT | 新止损%.4f ≥ 旧止损%.4f (不降低)",
						symbol, newStopLoss, currentStopLoss)
				}
			}
		}

			if shouldUpdate {
				// 更新止损
				err := t.updateStopLoss(symbol, side, positionAmt, newStopLoss)
				if err != nil {
					log.Printf("⚠️  [移动止损失败] %s %s: %v", symbol, side, err)
				} else {
					if oldStopLoss > 0 {
						log.Printf("📈 [移动止损] %s %s | 盈利%.2f%% (价格变动%.2f%%) | 当前价%.4f | 止损 %.4f → %.4f | 保护%.0f%%利润",
							symbol, strings.ToUpper(side), profitPct, priceMovePct, markPrice, oldStopLoss, newStopLoss, protectionRatio*100)
					} else {
						log.Printf("📈 [设置止损] %s %s | 盈利%.2f%% (价格变动%.2f%%) | 当前价%.4f | 新止损 %.4f | 保护%.0f%%利润",
							symbol, strings.ToUpper(side), profitPct, priceMovePct, markPrice, newStopLoss, protectionRatio*100)
					}
				}
			}
	}

	// 更新缓存
	t.positionsCacheMutex.Lock()
	t.cachedPositions = result
	t.positionsCacheTime = time.Now()
	t.positionsCacheMutex.Unlock()

	return result, nil
}

// invalidateCache 清空缓存（在交易操作后调用，确保数据一致性）
func (t *FuturesTrader) invalidateCache() {
	t.balanceCacheMutex.Lock()
	t.cachedBalance = nil
	t.balanceCacheMutex.Unlock()

	t.positionsCacheMutex.Lock()
	t.cachedPositions = nil
	t.positionsCacheMutex.Unlock()

	log.Printf("  🔄 已清空缓存，下次查询将获取最新数据")
}

// SetLeverage 设置杠杆（智能判断+冷却期）
func (t *FuturesTrader) SetLeverage(symbol string, leverage int) error {
	// ✅ 修复API限流问题：不再强制清空缓存，使用现有缓存判断杠杆
	// 之前每次都清空缓存会导致频繁调用API，触发限流封禁

	// 先尝试获取当前杠杆（使用缓存的持仓信息）
	currentLeverage := 0
	positions, err := t.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == symbol {
				if lev, ok := pos["leverage"].(float64); ok {
					currentLeverage = int(lev)
					break
				}
			}
		}
	}

	// 如果当前杠杆已经是目标杠杆，跳过
	if currentLeverage == leverage && currentLeverage > 0 {
		log.Printf("  ✓ %s 杠杆已是 %dx，无需切换", symbol, leverage)
		return nil
	}

	// 切换杠杆
	_, err = t.client.NewChangeLeverageService().
		Symbol(symbol).
		Leverage(leverage).
		Do(context.Background())

	if err != nil {
		// 如果错误信息包含"No need to change"，说明杠杆已经是目标值
		if contains(err.Error(), "No need to change") {
			log.Printf("  ✓ %s 杠杆已是 %dx", symbol, leverage)
			return nil
		}
		return fmt.Errorf("设置杠杆失败: %w", err)
	}

	log.Printf("  ✓ %s 杠杆已切换为 %dx", symbol, leverage)

	// 切换杠杆后等待1秒（避免后续API调用过快）
	time.Sleep(1 * time.Second)

	return nil
}

// checkCooldown 检查币种是否在冷却期内
func (t *FuturesTrader) checkCooldown(symbol string) error {
	t.closeTimeMutex.RLock()
	lastCloseTime, exists := t.lastCloseTimes[symbol]
	t.closeTimeMutex.RUnlock()

	if !exists {
		// 从未平仓过，允许开仓
		return nil
	}

	elapsed := time.Since(lastCloseTime)
	if elapsed < t.cooldownDuration {
		remaining := t.cooldownDuration - elapsed
		return fmt.Errorf("%s在冷却期内（平仓后需等待%.0f分钟，已过%.0f分钟，还需%.0f分钟）",
			symbol,
			t.cooldownDuration.Minutes(),
			elapsed.Minutes(),
			remaining.Minutes())
	}

	return nil
}

// recordCloseTime 记录平仓时间
func (t *FuturesTrader) recordCloseTime(symbol string) {
	t.closeTimeMutex.Lock()
	t.lastCloseTimes[symbol] = time.Now()
	t.closeTimeMutex.Unlock()

	log.Printf("  🕐 已记录 %s 平仓时间，%.0f分钟内禁止再开仓",
		symbol, t.cooldownDuration.Minutes())
}

// SetMarginType 设置保证金模式
func (t *FuturesTrader) SetMarginType(symbol string, marginType futures.MarginType) error {
	err := t.client.NewChangeMarginTypeService().
		Symbol(symbol).
		MarginType(marginType).
		Do(context.Background())

	if err != nil {
		// 如果已经是该模式，不算错误
		if contains(err.Error(), "No need to change") {
			log.Printf("  ✓ %s 保证金模式已是 %s", symbol, marginType)
			return nil
		}
		// 如果是多资产模式冲突，跳过保证金模式设置
		if contains(err.Error(), "-4168") || contains(err.Error(), "Multi-Assets mode") {
			log.Printf("  ⚠ %s 检测到多资产模式，跳过保证金模式设置", symbol)
			return nil
		}
		return fmt.Errorf("设置保证金模式失败: %w", err)
	}

	log.Printf("  ✓ %s 保证金模式已切换为 %s", symbol, marginType)

	// 切换保证金模式后等待3秒（避免冷却期错误）
	log.Printf("  ⏱ 等待3秒冷却期...")
	time.Sleep(3 * time.Second)

	return nil
}

// OpenLong 开多仓
func (t *FuturesTrader) OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	// ✅ 冷却期检查：防止同币种频繁交易
	if err := t.checkCooldown(symbol); err != nil {
		return nil, err
	}

	// 先取消该币种的所有委托单（清理旧的止损止盈单）
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消旧委托单失败（可能没有委托单）: %v", err)
	}

	// 设置杠杆
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, err
	}

	// 设置逐仓模式
	if err := t.SetMarginType(symbol, futures.MarginTypeIsolated); err != nil {
		return nil, err
	}

	// 格式化数量到正确精度
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// ✅ 关键修复：验证格式化后的数量是否满足100 USDT最小名义价值
	// 格式化可能会截断精度，导致 quantity × price < 100
	formattedQty, _ := strconv.ParseFloat(quantityStr, 64)
	currentPrice, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, fmt.Errorf("获取市场价格失败: %w", err)
	}

	notionalValue := formattedQty * currentPrice
	if notionalValue < 100 {
		// 向上调整数量以满足最小值要求
		minQuantity := 100.0 / currentPrice
		// 获取精度以便正确舍入
		precision, _ := t.GetSymbolPrecision(symbol)
		factor := 1.0
		for i := 0; i < precision; i++ {
			factor *= 10
		}
		// 向上舍入
		adjustedQty := math.Ceil(minQuantity*factor) / factor
		quantityStr, _ = t.FormatQuantity(symbol, adjustedQty)

		log.Printf("  ⚠️ 调整数量以满足最小名义价值: %.8f (%.2f USDT) → %s (%.2f USDT)",
			formattedQty, notionalValue, quantityStr, adjustedQty*currentPrice)
	}

	// 创建市价买入订单
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeBuy).
		PositionSide(futures.PositionSideTypeLong).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("开多仓失败: %w", err)
	}

	log.Printf("✓ 开多仓成功: %s 数量: %s", symbol, quantityStr)
	log.Printf("  订单ID: %d", order.OrderID)

	// ✅ 修复: 交易后立即清空缓存，确保下次查询返回最新的余额和持仓
	t.invalidateCache()

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	return result, nil
}

// OpenShort 开空仓
func (t *FuturesTrader) OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	// ✅ 冷却期检查：防止同币种频繁交易
	if err := t.checkCooldown(symbol); err != nil {
		return nil, err
	}

	// 先取消该币种的所有委托单（清理旧的止损止盈单）
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消旧委托单失败（可能没有委托单）: %v", err)
	}

	// 设置杠杆
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, err
	}

	// 设置逐仓模式
	if err := t.SetMarginType(symbol, futures.MarginTypeIsolated); err != nil {
		return nil, err
	}

	// 格式化数量到正确精度
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// ✅ 关键修复：验证格式化后的数量是否满足100 USDT最小名义价值
	// 格式化可能会截断精度，导致 quantity × price < 100
	formattedQty, _ := strconv.ParseFloat(quantityStr, 64)
	currentPrice, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, fmt.Errorf("获取市场价格失败: %w", err)
	}

	notionalValue := formattedQty * currentPrice
	if notionalValue < 100 {
		// 向上调整数量以满足最小值要求
		minQuantity := 100.0 / currentPrice
		// 获取精度以便正确舍入
		precision, _ := t.GetSymbolPrecision(symbol)
		factor := 1.0
		for i := 0; i < precision; i++ {
			factor *= 10
		}
		// 向上舍入
		adjustedQty := math.Ceil(minQuantity*factor) / factor
		quantityStr, _ = t.FormatQuantity(symbol, adjustedQty)

		log.Printf("  ⚠️ 调整数量以满足最小名义价值: %.8f (%.2f USDT) → %s (%.2f USDT)",
			formattedQty, notionalValue, quantityStr, adjustedQty*currentPrice)
	}

	// 创建市价卖出订单
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeSell).
		PositionSide(futures.PositionSideTypeShort).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("开空仓失败: %w", err)
	}

	log.Printf("✓ 开空仓成功: %s 数量: %s", symbol, quantityStr)
	log.Printf("  订单ID: %d", order.OrderID)

	// ✅ 修复: 交易后立即清空缓存，确保下次查询返回最新的余额和持仓
	t.invalidateCache()

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	return result, nil
}

// CloseLong 平多仓
func (t *FuturesTrader) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
	// ✅ 修复: 平仓前获取持仓信息以计算realized_pnl
	var entryPrice float64
	var positionAmt float64

	// 如果数量为0，获取当前持仓数量
	if quantity == 0 {
		positions, err := t.GetPositions()
		if err != nil {
			return nil, err
		}

		for _, pos := range positions {
			if pos["symbol"] == symbol && pos["side"] == "long" {
				quantity = pos["positionAmt"].(float64)
				positionAmt = quantity
				entryPrice = pos["entryPrice"].(float64)
				break
			}
		}

		if quantity == 0 {
			return nil, fmt.Errorf("没有找到 %s 的多仓", symbol)
		}
	} else {
		// 如果指定了数量，也需要获取入场价
		positions, err := t.GetPositions()
		if err == nil {
			for _, pos := range positions {
				if pos["symbol"] == symbol && pos["side"] == "long" {
					entryPrice = pos["entryPrice"].(float64)
					positionAmt = quantity
					break
				}
			}
		}
	}

	// 格式化数量
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// 创建市价卖出订单（平多）
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeSell).
		PositionSide(futures.PositionSideTypeLong).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("平多仓失败: %w", err)
	}

	log.Printf("✓ 平多仓成功: %s 数量: %s", symbol, quantityStr)

	// ✅ 修复: 交易后立即清空缓存，确保下次查询返回最新的余额和持仓
	t.invalidateCache()

	// 平仓后取消该币种的所有挂单（止损止盈单）
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消挂单失败: %v", err)
	}

	// ✅ 修复: 查询订单详情获取成交均价，计算realized_pnl
	realizedPnL := 0.0
	if entryPrice > 0 && positionAmt > 0 {
		// 查询订单详情获取成交价
		orderDetail, err := t.client.NewGetOrderService().
			Symbol(symbol).
			OrderID(order.OrderID).
			Do(context.Background())

		if err == nil && orderDetail.AvgPrice != "" {
			avgPrice := 0.0
			fmt.Sscanf(orderDetail.AvgPrice, "%f", &avgPrice)
			// 做多平仓：realized_pnl = (平仓价 - 开仓价) × 数量
			realizedPnL = (avgPrice - entryPrice) * positionAmt
			log.Printf("  💰 平仓盈亏: 入场%.4f → 平仓%.4f | 盈亏%+.2f USDT", entryPrice, avgPrice, realizedPnL)
		}
	}

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	result["realized_pnl"] = realizedPnL // ✅ 添加realized_pnl字段

	// ✅ 记录平仓时间，启动冷却期
	t.recordCloseTime(symbol)

	return result, nil
}

// CloseShort 平空仓
func (t *FuturesTrader) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	// ✅ 修复: 平仓前获取持仓信息以计算realized_pnl
	var entryPrice float64
	var positionAmt float64

	// 如果数量为0，获取当前持仓数量
	if quantity == 0 {
		positions, err := t.GetPositions()
		if err != nil {
			return nil, err
		}

		for _, pos := range positions {
			if pos["symbol"] == symbol && pos["side"] == "short" {
				quantity = -pos["positionAmt"].(float64) // 空仓数量是负的，取绝对值
				positionAmt = quantity
				entryPrice = pos["entryPrice"].(float64)
				break
			}
		}

		if quantity == 0 {
			return nil, fmt.Errorf("没有找到 %s 的空仓", symbol)
		}
	} else {
		// 如果指定了数量，也需要获取入场价
		positions, err := t.GetPositions()
		if err == nil {
			for _, pos := range positions {
				if pos["symbol"] == symbol && pos["side"] == "short" {
					entryPrice = pos["entryPrice"].(float64)
					positionAmt = quantity
					break
				}
			}
		}
	}

	// 格式化数量
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// 创建市价买入订单（平空）
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeBuy).
		PositionSide(futures.PositionSideTypeShort).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("平空仓失败: %w", err)
	}

	log.Printf("✓ 平空仓成功: %s 数量: %s", symbol, quantityStr)

	// ✅ 修复: 交易后立即清空缓存，确保下次查询返回最新的余额和持仓
	t.invalidateCache()

	// 平仓后取消该币种的所有挂单（止损止盈单）
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消挂单失败: %v", err)
	}

	// ✅ 修复: 查询订单详情获取成交均价，计算realized_pnl
	realizedPnL := 0.0
	if entryPrice > 0 && positionAmt > 0 {
		// 查询订单详情获取成交价
		orderDetail, err := t.client.NewGetOrderService().
			Symbol(symbol).
			OrderID(order.OrderID).
			Do(context.Background())

		if err == nil && orderDetail.AvgPrice != "" {
			avgPrice := 0.0
			fmt.Sscanf(orderDetail.AvgPrice, "%f", &avgPrice)
			// 做空平仓：realized_pnl = (开仓价 - 平仓价) × 数量
			realizedPnL = (entryPrice - avgPrice) * positionAmt
			log.Printf("  💰 平仓盈亏: 入场%.4f → 平仓%.4f | 盈亏%+.2f USDT", entryPrice, avgPrice, realizedPnL)
		}
	}

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	result["realized_pnl"] = realizedPnL // ✅ 添加realized_pnl字段

	// ✅ 记录平仓时间，启动冷却期
	t.recordCloseTime(symbol)

	return result, nil
}

// CancelAllOrders 取消该币种的所有挂单
func (t *FuturesTrader) CancelAllOrders(symbol string) error {
	err := t.client.NewCancelAllOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err != nil {
		return fmt.Errorf("取消挂单失败: %w", err)
	}

	log.Printf("  ✓ 已取消 %s 的所有挂单", symbol)
	return nil
}

// GetMarketPrice 获取市场价格
func (t *FuturesTrader) GetMarketPrice(symbol string) (float64, error) {
	prices, err := t.client.NewListPricesService().Symbol(symbol).Do(context.Background())
	if err != nil {
		return 0, fmt.Errorf("获取价格失败: %w", err)
	}

	if len(prices) == 0 {
		return 0, fmt.Errorf("未找到价格")
	}

	price, err := strconv.ParseFloat(prices[0].Price, 64)
	if err != nil {
		return 0, err
	}

	return price, nil
}

// CalculatePositionSize 计算仓位大小
func (t *FuturesTrader) CalculatePositionSize(balance, riskPercent, price float64, leverage int) float64 {
	riskAmount := balance * (riskPercent / 100.0)
	positionValue := riskAmount * float64(leverage)
	quantity := positionValue / price
	return quantity
}

// SetStopLoss 设置止损单
func (t *FuturesTrader) SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error {
	var side futures.SideType
	var posSide futures.PositionSideType

	if positionSide == "LONG" {
		side = futures.SideTypeSell
		posSide = futures.PositionSideTypeLong
	} else {
		side = futures.SideTypeBuy
		posSide = futures.PositionSideTypeShort
	}

	// 格式化数量和价格
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return err
	}

	stopPriceStr, err := t.FormatPrice(symbol, stopPrice)
	if err != nil {
		return err
	}

	_, err = t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(side).
		PositionSide(posSide).
		Type(futures.OrderTypeStopMarket).
		StopPrice(stopPriceStr).
		Quantity(quantityStr).
		WorkingType(futures.WorkingTypeContractPrice).
		ClosePosition(true).
		Do(context.Background())

	if err != nil {
		return fmt.Errorf("设置止损失败: %w", err)
	}

	log.Printf("  止损价设置: %s", stopPriceStr)
	return nil
}

// SetTakeProfit 设置止盈单
func (t *FuturesTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	var side futures.SideType
	var posSide futures.PositionSideType

	if positionSide == "LONG" {
		side = futures.SideTypeSell
		posSide = futures.PositionSideTypeLong
	} else {
		side = futures.SideTypeBuy
		posSide = futures.PositionSideTypeShort
	}

	// 格式化数量和价格
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return err
	}

	takeProfitPriceStr, err := t.FormatPrice(symbol, takeProfitPrice)
	if err != nil {
		return err
	}

	_, err = t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(side).
		PositionSide(posSide).
		Type(futures.OrderTypeTakeProfitMarket).
		StopPrice(takeProfitPriceStr).
		Quantity(quantityStr).
		WorkingType(futures.WorkingTypeContractPrice).
		ClosePosition(true).
		Do(context.Background())

	if err != nil {
		return fmt.Errorf("设置止盈失败: %w", err)
	}

	log.Printf("  止盈价设置: %s", takeProfitPriceStr)
	return nil
}

// GetSymbolPrecision 获取交易对的数量精度
func (t *FuturesTrader) GetSymbolPrecision(symbol string) (int, error) {
	exchangeInfo, err := t.client.NewExchangeInfoService().Do(context.Background())
	if err != nil {
		return 0, fmt.Errorf("获取交易规则失败: %w", err)
	}

	for _, s := range exchangeInfo.Symbols {
		if s.Symbol == symbol {
			// 从LOT_SIZE filter获取精度
			for _, filter := range s.Filters {
				if filter["filterType"] == "LOT_SIZE" {
					stepSize := filter["stepSize"].(string)
					precision := calculatePrecision(stepSize)
					log.Printf("  %s 数量精度: %d (stepSize: %s)", symbol, precision, stepSize)
					return precision, nil
				}
			}
		}
	}

	log.Printf("  ⚠ %s 未找到精度信息，使用默认精度3", symbol)
	return 3, nil // 默认精度为3
}

// calculatePrecision 从stepSize计算精度
func calculatePrecision(stepSize string) int {
	// 去除尾部的0
	stepSize = trimTrailingZeros(stepSize)

	// 查找小数点
	dotIndex := -1
	for i := 0; i < len(stepSize); i++ {
		if stepSize[i] == '.' {
			dotIndex = i
			break
		}
	}

	// 如果没有小数点或小数点在最后，精度为0
	if dotIndex == -1 || dotIndex == len(stepSize)-1 {
		return 0
	}

	// 返回小数点后的位数
	return len(stepSize) - dotIndex - 1
}

// trimTrailingZeros 去除尾部的0
func trimTrailingZeros(s string) string {
	// 如果没有小数点，直接返回
	if !stringContains(s, ".") {
		return s
	}

	// 从后向前遍历，去除尾部的0
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}

	// 如果最后一位是小数点，也去掉
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}

	return s
}

// FormatQuantity 格式化数量到正确的精度
func (t *FuturesTrader) FormatQuantity(symbol string, quantity float64) (string, error) {
	precision, err := t.GetSymbolPrecision(symbol)
	if err != nil {
		// 如果获取失败，使用默认格式
		return fmt.Sprintf("%.3f", quantity), nil
	}

	format := fmt.Sprintf("%%.%df", precision)
	return fmt.Sprintf(format, quantity), nil
}

// FormatPrice 格式化价格到正确的精度
func (t *FuturesTrader) FormatPrice(symbol string, price float64) (string, error) {
	precision, err := t.GetSymbolPricePrecision(symbol)
	if err != nil {
		// 如果获取失败，使用默认格式
		return fmt.Sprintf("%.2f", price), nil
	}

	format := fmt.Sprintf("%%.%df", precision)
	return fmt.Sprintf(format, price), nil
}

// GetSymbolPricePrecision 获取交易对的价格精度
func (t *FuturesTrader) GetSymbolPricePrecision(symbol string) (int, error) {
	exchangeInfo, err := t.client.NewExchangeInfoService().Do(context.Background())
	if err != nil {
		return 0, fmt.Errorf("获取交易规则失败: %w", err)
	}

	for _, s := range exchangeInfo.Symbols {
		if s.Symbol == symbol {
			// 从PRICE_FILTER filter获取精度
			for _, filter := range s.Filters {
				if filter["filterType"] == "PRICE_FILTER" {
					tickSize := filter["tickSize"].(string)
					precision := calculatePrecision(tickSize)
					log.Printf("  %s 价格精度: %d (tickSize: %s)", symbol, precision, tickSize)
					return precision, nil
				}
			}
		}
	}

	log.Printf("  ⚠ %s 未找到价格精度信息，使用默认精度2", symbol)
	return 2, nil // 默认精度为2
}

// getCurrentStopLoss 获取当前止损订单的止损价格
func (t *FuturesTrader) getCurrentStopLoss(symbol string, side string) (float64, error) {
	// 获取该币种的所有挂单
	orders, err := t.client.NewListOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err != nil {
		return 0, fmt.Errorf("获取挂单失败: %w", err)
	}

	// 查找止损单
	var positionSide futures.PositionSideType
	if side == "long" {
		positionSide = futures.PositionSideTypeLong
	} else {
		positionSide = futures.PositionSideTypeShort
	}

	for _, order := range orders {
		if order.Type == futures.OrderTypeStopMarket && order.PositionSide == positionSide {
			stopPrice, err := strconv.ParseFloat(order.StopPrice, 64)
			if err != nil {
				continue
			}
			return stopPrice, nil
		}
	}

	// 如果没有找到止损单，返回错误
	return 0, fmt.Errorf("未找到止损单")
}

// updateStopLoss 更新止损价格（先验证参数，再取消旧的，最后设置新的）
func (t *FuturesTrader) updateStopLoss(symbol string, side string, positionAmt float64, newStopLoss float64) error {
	// ========================================
	// 第1步：先准备所有参数（避免取消旧止损后设置新止损失败）
	// ========================================
	var orderSide futures.SideType
	var posSide futures.PositionSideType

	if side == "long" {
		orderSide = futures.SideTypeSell
		posSide = futures.PositionSideTypeLong
	} else {
		orderSide = futures.SideTypeBuy
		posSide = futures.PositionSideTypeShort
	}

	// 格式化数量（在取消旧止损之前完成，确保参数正确）
	if positionAmt < 0 {
		positionAmt = -positionAmt // 空仓数量是负的，需要取绝对值
	}
	quantityStr, err := t.FormatQuantity(symbol, positionAmt)
	if err != nil {
		// ⚠️ 格式化失败，不要取消旧止损！直接返回错误
		return fmt.Errorf("格式化数量失败，保留旧止损: %w", err)
	}

	// 格式化止损价格（使用正确的价格精度）
	stopPriceStr, err := t.FormatPrice(symbol, newStopLoss)
	if err != nil {
		// ⚠️ 格式化失败，不要取消旧止损！直接返回错误
		return fmt.Errorf("格式化价格失败，保留旧止损: %w", err)
	}

	// ========================================
	// 第2步：取消旧止损（参数已验证，安全）
	// ========================================
	err = t.client.NewCancelAllOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err != nil {
		// 取消失败，保留旧止损
		return fmt.Errorf("取消旧止损单失败: %w", err)
	}

	// ========================================
	// 第3步：立即设置新止损（必须成功！）
	// ========================================
	_, err = t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(orderSide).
		PositionSide(posSide).
		Type(futures.OrderTypeStopMarket).
		StopPrice(stopPriceStr).
		Quantity(quantityStr).
		WorkingType(futures.WorkingTypeContractPrice).
		ClosePosition(true).
		Do(context.Background())

	if err != nil {
		// 🚨 严重错误：旧止损已取消，新止损设置失败！持仓无保护！
		log.Printf("🚨🚨🚨 严重错误：%s %s 旧止损已取消但新止损设置失败！持仓无保护！错误: %v", symbol, side, err)
		log.Printf("🚨 请立即手动设置止损！止损价: %s, 数量: %s", stopPriceStr, quantityStr)
		return fmt.Errorf("🚨 设置新止损失败（旧止损已取消）: %w", err)
	}

	log.Printf("  ✅ 止损已更新: %s %s | 新止损价: %s", symbol, side, stopPriceStr)
	return nil
}

// 辅助函数
func contains(s, substr string) bool {
	return len(s) >= len(substr) && stringContains(s, substr)
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ==================== 限价单功能 ====================

// PlaceLimitOrder 下限价单
func (t *FuturesTrader) PlaceLimitOrder(symbol string, side OrderSide, price, quantity float64, leverage int) (map[string]interface{}, error) {
	// ✅ 冷却期检查
	if err := t.checkCooldown(symbol); err != nil {
		return nil, err
	}

	// 先取消该币种的所有委托单（清理旧限价单）
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消旧委托单失败（可能没有委托单）: %v", err)
	}

	// 设置杠杆
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, err
	}

	// 设置逐仓模式
	if err := t.SetMarginType(symbol, futures.MarginTypeIsolated); err != nil {
		return nil, err
	}

	// 格式化价格和数量
	priceStr, err := t.FormatPrice(symbol, price)
	if err != nil {
		return nil, fmt.Errorf("格式化价格失败: %w", err)
	}

	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, fmt.Errorf("格式化数量失败: %w", err)
	}

	// 验证最小名义价值
	formattedQty, _ := strconv.ParseFloat(quantityStr, 64)
	notionalValue := formattedQty * price
	if notionalValue < 100 {
		return nil, fmt.Errorf("名义价值%.2f USDT < 100 USDT最小要求", notionalValue)
	}

	// 确定订单方向
	var orderSide futures.SideType
	var positionSide futures.PositionSideType

	if side == OrderSideBuy {
		orderSide = futures.SideTypeBuy
		positionSide = futures.PositionSideTypeLong
	} else {
		orderSide = futures.SideTypeSell
		positionSide = futures.PositionSideTypeShort
	}

	// 创建限价单
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(orderSide).
		PositionSide(positionSide).
		Type(futures.OrderTypeLimit).
		TimeInForce(futures.TimeInForceTypeGTC). // GTC: Good Till Cancel
		Quantity(quantityStr).
		Price(priceStr).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("下限价单失败: %w", err)
	}

	log.Printf("✅ 限价单已提交: %s %s @ %s (数量: %s, 订单ID: %d)",
		symbol, side, priceStr, quantityStr, order.OrderID)

	// 清空缓存
	t.invalidateCache()

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	result["price"] = price
	result["quantity"] = formattedQty

	return result, nil
}

// CancelLimitOrder 取消限价单
func (t *FuturesTrader) CancelLimitOrder(symbol string, orderID int64) error {
	_, err := t.client.NewCancelOrderService().
		Symbol(symbol).
		OrderID(orderID).
		Do(context.Background())

	if err != nil {
		return fmt.Errorf("取消订单失败: %w", err)
	}

	log.Printf("🗑️  已取消限价单: %s (订单ID: %d)", symbol, orderID)
	t.invalidateCache()
	return nil
}

// GetOrderStatus 查询订单状态
func (t *FuturesTrader) GetOrderStatus(symbol string, orderID int64) (map[string]interface{}, error) {
	order, err := t.client.NewGetOrderService().
		Symbol(symbol).
		OrderID(orderID).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("查询订单失败: %w", err)
	}

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = string(order.Status) // 🔧 修复：转换枚举为字符串
	result["side"] = string(order.Side)     // 🔧 修复：转换枚举为字符串
	result["type"] = string(order.Type)     // 🔧 修复：转换枚举为字符串
	result["price"], _ = strconv.ParseFloat(order.Price, 64)
	result["origQty"], _ = strconv.ParseFloat(order.OrigQuantity, 64)
	result["executedQty"], _ = strconv.ParseFloat(order.ExecutedQuantity, 64)
	result["avgPrice"], _ = strconv.ParseFloat(order.AvgPrice, 64)
	result["updateTime"] = order.UpdateTime

	return result, nil
}

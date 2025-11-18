package trader

import (
	"fmt"
	"log"
	"nofx/decision"
	"nofx/logger"
	"strconv"
	"time"
)

// executeOpenLimitOrderWithRecord 执行限价单开仓（智能管理已有订单）
func (at *AutoTrader) executeOpenLimitOrderWithRecord(d *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📝 限价单模式: %s @ %.4f (当前价 %.4f)",
		d.Symbol, d.LimitPrice, d.CurrentPrice)

	// 🛡️ 硬约束检查（冷却期、日交易上限、小时上限、最大持仓数量）
	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("获取持仓失败: %w", err)
	}

	if err := at.constraints.CanOpenPosition(d.Symbol, len(positions)); err != nil {
		log.Printf("  ⚠️  硬约束拦截: %v", err)
		return fmt.Errorf("硬约束拦截: %w", err)
	}

	// ⚠️ 检查是否已有同币种同方向持仓，如果有则拒绝（防止仓位叠加）
	targetSide := ""
	if d.Action == "open_long" {
		targetSide = "long"
	} else {
		targetSide = "short"
	}

	for _, pos := range positions {
		if pos["symbol"] == d.Symbol && pos["side"] == targetSide {
			return fmt.Errorf("❌ %s 已有%s仓，拒绝下限价单以防止仓位叠加", d.Symbol, targetSide)
		}
	}

	// ✅ 检查保证金是否充足
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

	// 计算当前总已用保证金
	totalMarginUsed := 0.0
	for _, pos := range positions {
		positionAmt := 0.0
		markPrice := 0.0
		leverage := 1

		if amt, ok := pos["positionAmt"].(float64); ok {
			positionAmt = amt
			if positionAmt < 0 {
				positionAmt = -positionAmt
			}
		}
		if price, ok := pos["markPrice"].(float64); ok {
			markPrice = price
		}
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		if leverage > 0 && markPrice > 0 {
			positionValue := positionAmt * markPrice
			marginForThisPosition := positionValue / float64(leverage)
			totalMarginUsed += marginForThisPosition
		}
	}

	requiredMargin := d.PositionSizeUSD / float64(d.Leverage)
	newTotalMarginUsed := totalMarginUsed + requiredMargin
	marginUtilizationRate := 0.0
	if totalEquity > 0 {
		marginUtilizationRate = (newTotalMarginUsed / totalEquity) * 100
	}

	if marginUtilizationRate > 90.0 {
		return fmt.Errorf("❌ 总保证金使用率将超过90%%限制: 当前%.2f%% + 新仓位%.2f USDT = %.2f%%",
			(totalMarginUsed/totalEquity)*100, requiredMargin, marginUtilizationRate)
	}

	if requiredMargin > availableBalance {
		return fmt.Errorf("❌ 可用保证金不足: 需要%.2f USDT, 可用%.2f USDT", requiredMargin, availableBalance)
	}
	log.Printf("  💰 保证金检查通过: 需要%.2f USDT, 可用%.2f USDT, 总使用率%.1f%%",
		requiredMargin, availableBalance, marginUtilizationRate)

	// 1️⃣ 检查是否已有限价单
	existingOrder, hasOrder := at.orderManager.GetOrder(d.Symbol)

	// 确定AI推荐方向
	aiDirection := ""
	if d.Action == "open_long" {
		aiDirection = "up"
	} else if d.Action == "open_short" {
		aiDirection = "down"
	}

	// 2️⃣ 如果已有限价单,检查是否需要更新
	if hasOrder {
		shouldUpdate, reason := at.orderManager.ShouldUpdatePrice(
			d.Symbol,
			d.LimitPrice,
			aiDirection,
		)

		if !shouldUpdate {
			log.Printf("  ℹ️  保持现有限价单: %s @ %.4f (原因: %s)",
				d.Symbol, existingOrder.Price, reason)
			return nil
		}

		// 需要更新：取消旧订单
		log.Printf("  🔄 限价单需要更新: %s (原因: %s)", d.Symbol, reason)

		binanceTrader, ok := at.trader.(*FuturesTrader)
		if !ok {
			return fmt.Errorf("限价单仅支持币安交易")
		}

		orderID, _ := strconv.ParseInt(existingOrder.OrderID, 10, 64)
		if err := binanceTrader.CancelLimitOrder(d.Symbol, orderID); err != nil {
			log.Printf("  ⚠️  取消旧限价单失败: %v (将继续下新单)", err)
		}

		at.orderManager.RemoveOrder(d.Symbol)
	}

	// 3️⃣ 下新的限价单
	binanceTrader, ok := at.trader.(*FuturesTrader)
	if !ok {
		return fmt.Errorf("限价单仅支持币安交易")
	}

	// 计算数量
	quantity := d.PositionSizeUSD / d.LimitPrice

	// 确定订单方向
	var side OrderSide
	if d.Action == "open_long" {
		side = OrderSideBuy
	} else {
		side = OrderSideSell
	}

	// 下单
	order, err := binanceTrader.PlaceLimitOrder(
		d.Symbol,
		side,
		d.LimitPrice,
		quantity,
		d.Leverage,
	)
	if err != nil {
		return fmt.Errorf("下限价单失败: %w", err)
	}

	// 4️⃣ 记录到订单管理器
	limitOrder := &LimitOrder{
		OrderID:     fmt.Sprintf("%v", order["orderId"]),
		Symbol:      d.Symbol,
		Side:        side,
		Price:       d.LimitPrice,
		Quantity:    quantity,
		Leverage:    d.Leverage,
		StopLoss:    d.StopLoss,
		TakeProfit:  d.TakeProfit,
		Status:      OrderStatusNew,
		CreateTime:  time.Now(),
		UpdateTime:  time.Now(),
		AIDirection: aiDirection,
		Reasoning:   d.Reasoning,
	}

	at.orderManager.AddOrder(limitOrder)

	// 5️⃣ 记录到日志
	actionRecord.Quantity = quantity
	actionRecord.Price = d.LimitPrice
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	// 计算回调百分比（限价相对当前价的偏离）
	pullbackPct := 0.0
	if d.Action == "open_long" {
		pullbackPct = (d.CurrentPrice - d.LimitPrice) / d.CurrentPrice * 100
	} else {
		pullbackPct = (d.LimitPrice - d.CurrentPrice) / d.CurrentPrice * 100
	}

	log.Printf("  ✅ 限价单已提交: %s %s @ %.4f (数量: %.4f, 回调: %.2f%%)",
		d.Symbol, side, d.LimitPrice, quantity, pullbackPct)

	return nil
}

// checkAndUpdateLimitOrders 每个周期检查并更新限价单状态
func (at *AutoTrader) checkAndUpdateLimitOrders() error {
	// 获取所有活跃的限价单
	activeOrders := at.orderManager.GetAllOrders()
	if len(activeOrders) == 0 {
		return nil
	}

	binanceTrader, ok := at.trader.(*FuturesTrader)
	if !ok {
		return fmt.Errorf("限价单仅支持币安交易")
	}

	for _, order := range activeOrders {
		// 查询订单状态
		orderID, err := strconv.ParseInt(order.OrderID, 10, 64)
		if err != nil {
			log.Printf("⚠️  解析订单ID失败: %s - %v", order.OrderID, err)
			continue
		}

		orderInfo, err := binanceTrader.GetOrderStatus(order.Symbol, orderID)
		if err != nil {
			log.Printf("⚠️  查询订单状态失败: %s %s - %v", order.Symbol, order.OrderID, err)
			continue
		}

		// 提取状态字段
		status, ok := orderInfo["status"].(string)
		if !ok {
			log.Printf("⚠️  订单状态格式错误: %s %s", order.Symbol, order.OrderID)
			continue
		}

		// 根据状态处理
		switch status {
		case "FILLED":
			// 订单已完全成交
			log.Printf("✅ 限价单成交: %s %s @ %.4f (数量: %.4f)",
				order.Symbol, order.Side, order.Price, order.Quantity)

			// 🛡️ 记录开仓到硬约束管理器
			side := "long"
			if order.Side == OrderSideSell {
				side = "short"
			}
			at.constraints.RecordOpenPosition(order.Symbol, side)

			// 记录开仓时间
			posKey := order.Symbol + "_" + side
			at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

			// 设置止损止盈
			if order.Side == OrderSideBuy {
				// 做多
				if err := at.trader.SetStopLoss(order.Symbol, "LONG", order.Quantity, order.StopLoss); err != nil {
					log.Printf("  ⚠️  设置止损失败: %v", err)
				}
				if err := at.trader.SetTakeProfit(order.Symbol, "LONG", order.Quantity, order.TakeProfit); err != nil {
					log.Printf("  ⚠️  设置止盈失败: %v", err)
				}
			} else {
				// 做空
				if err := at.trader.SetStopLoss(order.Symbol, "SHORT", order.Quantity, order.StopLoss); err != nil {
					log.Printf("  ⚠️  设置止损失败: %v", err)
				}
				if err := at.trader.SetTakeProfit(order.Symbol, "SHORT", order.Quantity, order.TakeProfit); err != nil {
					log.Printf("  ⚠️  设置止盈失败: %v", err)
				}
			}

			// 从订单管理器中移除
			at.orderManager.RemoveOrder(order.Symbol)

		case "PARTIALLY_FILLED":
			// 订单部分成交 - 取消剩余数量，管理已成交部分
			log.Printf("⚠️  限价单部分成交: %s %s @ %.4f (将取消剩余部分)",
				order.Symbol, order.Side, order.Price)

			// 取消剩余订单
			if err := binanceTrader.CancelLimitOrder(order.Symbol, orderID); err != nil {
				log.Printf("  ⚠️  取消剩余订单失败: %v", err)
			}

			// 🛡️ 记录开仓到硬约束管理器（部分成交也算开仓）
			side := "long"
			if order.Side == OrderSideSell {
				side = "short"
			}
			at.constraints.RecordOpenPosition(order.Symbol, side)

			// 记录开仓时间
			posKey := order.Symbol + "_" + side
			at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

			// 设置止损止盈（使用原计划的价格，系统会自动应用到实际持仓数量）
			if order.Side == OrderSideBuy {
				// 做多
				if err := at.trader.SetStopLoss(order.Symbol, "LONG", order.Quantity, order.StopLoss); err != nil {
					log.Printf("  ⚠️  设置止损失败: %v", err)
				}
				if err := at.trader.SetTakeProfit(order.Symbol, "LONG", order.Quantity, order.TakeProfit); err != nil {
					log.Printf("  ⚠️  设置止盈失败: %v", err)
				}
			} else {
				// 做空
				if err := at.trader.SetStopLoss(order.Symbol, "SHORT", order.Quantity, order.StopLoss); err != nil {
					log.Printf("  ⚠️  设置止损失败: %v", err)
				}
				if err := at.trader.SetTakeProfit(order.Symbol, "SHORT", order.Quantity, order.TakeProfit); err != nil {
					log.Printf("  ⚠️  设置止盈失败: %v", err)
				}
			}

			// 从订单管理器中移除
			at.orderManager.RemoveOrder(order.Symbol)

		case "NEW":
			// 订单仍在挂单中，无需操作
			// log.Printf("  ℹ️  限价单仍在挂单: %s %s @ %.4f", order.Symbol, order.Side, order.Price)

		case "CANCELED":
			// 订单已被取消（可能是手动取消或其他原因）
			log.Printf("ℹ️  限价单已取消: %s %s @ %.4f", order.Symbol, order.Side, order.Price)
			at.orderManager.RemoveOrder(order.Symbol)

		case "EXPIRED":
			// 订单已过期
			log.Printf("⏰ 限价单已过期: %s %s @ %.4f", order.Symbol, order.Side, order.Price)
			at.orderManager.RemoveOrder(order.Symbol)

		default:
			log.Printf("⚠️  未知订单状态: %s %s - 状态: %s", order.Symbol, order.OrderID, status)
		}
	}

	return nil
}

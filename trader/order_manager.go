package trader

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// OrderType 订单类型
type OrderType string

const (
	OrderTypeLimit  OrderType = "LIMIT"   // 限价单
	OrderTypeMarket OrderType = "MARKET"  // 市价单
)

// OrderStatus 订单状态
type OrderStatus string

const (
	OrderStatusNew             OrderStatus = "NEW"              // 新建订单
	OrderStatusPartiallyFilled OrderStatus = "PARTIALLY_FILLED" // 部分成交
	OrderStatusFilled          OrderStatus = "FILLED"           // 完全成交
	OrderStatusCanceled        OrderStatus = "CANCELED"         // 已取消
	OrderStatusExpired         OrderStatus = "EXPIRED"          // 已过期
)

// OrderSide 订单方向
type OrderSide string

const (
	OrderSideBuy  OrderSide = "BUY"  // 买入（做多）
	OrderSideSell OrderSide = "SELL" // 卖出（做空）
)

// LimitOrder 限价单信息
type LimitOrder struct {
	OrderID      string      `json:"order_id"`      // 交易所订单ID
	Symbol       string      `json:"symbol"`        // 交易对
	Side         OrderSide   `json:"side"`          // 方向（BUY/SELL）
	Price        float64     `json:"price"`         // 限价
	Quantity     float64     `json:"quantity"`      // 数量
	Leverage     int         `json:"leverage"`      // 杠杆
	StopLoss     float64     `json:"stop_loss"`     // 止损价
	TakeProfit   float64     `json:"take_profit"`   // 止盈价
	Status       OrderStatus `json:"status"`        // 订单状态
	FilledQty    float64     `json:"filled_qty"`    // 已成交数量
	AvgPrice     float64     `json:"avg_price"`     // 平均成交价
	CreateTime   time.Time   `json:"create_time"`   // 创建时间
	UpdateTime   time.Time   `json:"update_time"`   // 更新时间
	AIDirection  string      `json:"ai_direction"`  // AI推荐方向（up/down）
	Reasoning    string      `json:"reasoning"`     // 开仓理由
}

// OrderManager 订单管理器
type OrderManager struct {
	activeOrders map[string]*LimitOrder // symbol -> order
	mu           sync.RWMutex
}

// NewOrderManager 创建订单管理器
func NewOrderManager() *OrderManager {
	return &OrderManager{
		activeOrders: make(map[string]*LimitOrder),
	}
}

// AddOrder 添加限价单
func (om *OrderManager) AddOrder(order *LimitOrder) {
	om.mu.Lock()
	defer om.mu.Unlock()

	om.activeOrders[order.Symbol] = order
	log.Printf("📝 [OrderManager] 添加限价单: %s %s @ %.4f (订单ID: %s)",
		order.Symbol, order.Side, order.Price, order.OrderID)
}

// GetOrder 获取指定币种的订单
func (om *OrderManager) GetOrder(symbol string) (*LimitOrder, bool) {
	om.mu.RLock()
	defer om.mu.RUnlock()

	order, exists := om.activeOrders[symbol]
	return order, exists
}

// RemoveOrder 移除订单
func (om *OrderManager) RemoveOrder(symbol string) {
	om.mu.Lock()
	defer om.mu.Unlock()

	if order, exists := om.activeOrders[symbol]; exists {
		log.Printf("🗑️  [OrderManager] 移除订单: %s (订单ID: %s, 状态: %s)",
			symbol, order.OrderID, order.Status)
		delete(om.activeOrders, symbol)
	}
}

// UpdateOrderStatus 更新订单状态
func (om *OrderManager) UpdateOrderStatus(symbol string, status OrderStatus, filledQty, avgPrice float64) {
	om.mu.Lock()
	defer om.mu.Unlock()

	if order, exists := om.activeOrders[symbol]; exists {
		oldStatus := order.Status
		order.Status = status
		order.FilledQty = filledQty
		order.AvgPrice = avgPrice
		order.UpdateTime = time.Now()

		log.Printf("🔄 [OrderManager] 订单状态更新: %s %s → %s (成交: %.4f/%.4f @ %.4f)",
			symbol, oldStatus, status, filledQty, order.Quantity, avgPrice)
	}
}

// GetAllOrders 获取所有活跃订单
func (om *OrderManager) GetAllOrders() []*LimitOrder {
	om.mu.RLock()
	defer om.mu.RUnlock()

	orders := make([]*LimitOrder, 0, len(om.activeOrders))
	for _, order := range om.activeOrders {
		orders = append(orders, order)
	}
	return orders
}

// HasOrder 检查是否有指定币种的订单
func (om *OrderManager) HasOrder(symbol string) bool {
	om.mu.RLock()
	defer om.mu.RUnlock()

	_, exists := om.activeOrders[symbol]
	return exists
}

// ShouldUpdatePrice 判断是否需要更新限价
func (om *OrderManager) ShouldUpdatePrice(symbol string, newPrice float64, aiDirection string) (bool, string) {
	om.mu.RLock()
	defer om.mu.RUnlock()

	order, exists := om.activeOrders[symbol]
	if !exists {
		return false, "订单不存在"
	}

	// 检查AI方向是否改变
	if order.AIDirection != aiDirection {
		return true, fmt.Sprintf("AI方向改变: %s → %s", order.AIDirection, aiDirection)
	}

	// 检查价格偏离是否过大（>1%）
	priceDiff := (newPrice - order.Price) / order.Price * 100
	if priceDiff > 1.0 || priceDiff < -1.0 {
		return true, fmt.Sprintf("价格偏离%.2f%% > 1%%", priceDiff)
	}

	return false, ""
}

// GetOrderAge 获取订单存在时间
func (om *OrderManager) GetOrderAge(symbol string) time.Duration {
	om.mu.RLock()
	defer om.mu.RUnlock()

	if order, exists := om.activeOrders[symbol]; exists {
		return time.Since(order.CreateTime)
	}
	return 0
}

// ConvertSideToPositionSide 将订单方向转换为持仓方向
func ConvertSideToPositionSide(side OrderSide) string {
	if side == OrderSideBuy {
		return "long"
	}
	return "short"
}

// ConvertPositionSideToOrderSide 将持仓方向转换为订单方向
func ConvertPositionSideToOrderSide(positionSide string) OrderSide {
	if positionSide == "long" {
		return OrderSideBuy
	}
	return OrderSideSell
}

// ConvertAIDirectionToOrderSide 将AI方向转换为订单方向
func ConvertAIDirectionToOrderSide(direction string) OrderSide {
	if direction == "up" {
		return OrderSideBuy
	}
	return OrderSideSell
}

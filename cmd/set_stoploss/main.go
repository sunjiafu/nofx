package main

import (
	"fmt"
	"log"
	"nofx/trader"
	"os"
)

func main() {
	// 从环境变量读取币安API密钥
	apiKey := os.Getenv("BINANCE_API_KEY")
	secretKey := os.Getenv("BINANCE_SECRET_KEY")

	if apiKey == "" || secretKey == "" {
		log.Fatal("请设置环境变量: BINANCE_API_KEY, BINANCE_SECRET_KEY")
	}

	// 创建币安交易器
	ft := trader.NewFuturesTrader(apiKey, secretKey, false)

	// BTC SHORT 持仓参数
	symbol := "BTCUSDT"
	positionSide := "SHORT"
	quantity := 0.002
	stopLoss := 89295.07
	takeProfit := 83470.35

	fmt.Printf("正在为 %s %s (数量: %.3f) 设置止损止盈...\n", symbol, positionSide, quantity)
	fmt.Printf("止损价: %.2f\n", stopLoss)
	fmt.Printf("止盈价: %.2f\n", takeProfit)

	// 设置止损
	if err := ft.SetStopLoss(symbol, positionSide, quantity, stopLoss); err != nil {
		log.Printf("❌ 设置止损失败: %v", err)
	} else {
		log.Printf("✅ 止损设置成功: %.2f", stopLoss)
	}

	// 设置止盈
	if err := ft.SetTakeProfit(symbol, positionSide, quantity, takeProfit); err != nil {
		log.Printf("❌ 设置止盈失败: %v", err)
	} else {
		log.Printf("✅ 止盈设置成功: %.2f", takeProfit)
	}

	fmt.Println("完成!")
}

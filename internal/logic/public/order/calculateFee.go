package order

import "github.com/perfect-panel/server/internal/model/payment"

func calculateFee(amount int64, config *payment.Payment) int64 {
	switch config.FeeMode {
	case 0:
		return 0
	case 1:
		return amount * config.FeePercent / 100
	case 2:
		if amount > 0 {
			return config.FeeAmount
		}
		return 0
	case 3:
		return amount*config.FeePercent/100 + config.FeeAmount
	default:
		return 0
	}
}

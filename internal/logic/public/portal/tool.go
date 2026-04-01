package portal

import (
	"github.com/perfect-panel/server/internal/model/coupon"
	"github.com/perfect-panel/server/internal/model/payment"
	"github.com/perfect-panel/server/internal/types"
)

func getDiscount(discounts []types.SubscribeDiscount, inputMonths int64) float64 {
	var finalDiscount float64 = 100

	for _, discount := range discounts {
		if inputMonths >= discount.Quantity && discount.Discount < finalDiscount {
			finalDiscount = discount.Discount
		}
	}
	return finalDiscount / float64(100)
}

func calculateCoupon(amount int64, couponInfo *coupon.Coupon) int64 {
	if couponInfo.Type == 1 {
		return int64(float64(amount) * (float64(couponInfo.Discount) / float64(100)))
	} else {
		return min(couponInfo.Discount, amount)
	}
}

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

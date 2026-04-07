package order

import (
	"context"
	"encoding/json"
	"time"

	"github.com/perfect-panel/server/internal/model/log"
	"github.com/perfect-panel/server/internal/promo"
	"github.com/perfect-panel/server/pkg/constant"

	"gorm.io/gorm"

	"github.com/hibiken/asynq"
	"github.com/perfect-panel/server/internal/model/order"
	"github.com/perfect-panel/server/internal/model/user"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/xerr"
	queue "github.com/perfect-panel/server/queue/types"
	"github.com/pkg/errors"
)

type RenewalLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

// NewRenewalLogic creates a new renewal logic instance for subscription renewal operations
func NewRenewalLogic(ctx context.Context, svcCtx *svc.ServiceContext) *RenewalLogic {
	return &RenewalLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// Renewal processes subscription renewal orders including discount calculation,
// coupon validation, gift amount deduction, fee calculation, and order creation
func (l *RenewalLogic) Renewal(req *types.RenewalOrderRequest) (resp *types.RenewalOrderResponse, err error) {
	u, ok := l.ctx.Value(constant.CtxKeyUser).(*user.User)
	if !ok {
		logger.Error("current user is not found in context")
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidAccess), "Invalid Access")
	}
	if req.Quantity <= 0 {
		l.Debugf("[Renewal] Quantity is less than or equal to 0, setting to 1")
		req.Quantity = 1
	}

	// Validate quantity limit
	if req.Quantity > MaxQuantity {
		l.Errorw("[Renewal] Quantity exceeds maximum limit", logger.Field("quantity", req.Quantity), logger.Field("max", MaxQuantity))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidParams), "quantity exceeds maximum limit of %d", MaxQuantity)
	}

	orderNo := tool.GenerateTradeNo()
	// find user subscribe
	userSubscribe, err := l.svcCtx.UserModel.FindOneUserSubscribe(l.ctx, req.UserSubscribeID)
	if err != nil {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find user subscribe error: %v", err.Error())
	}
	// find subscription
	sub, err := l.svcCtx.SubscribeModel.FindOne(l.ctx, userSubscribe.SubscribeId)
	if err != nil {
		l.Errorw("[Renewal] Database query error", logger.Field("error", err.Error()), logger.Field("subscribe_id", userSubscribe.SubscribeId))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find subscribe error: %v", err.Error())
	}
	// check subscribe plan status
	if !*sub.Sell {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "subscribe not sell")
	}
	var discount float64 = 1
	if sub.Discount != "" {
		var dis []types.SubscribeDiscount
		_ = json.Unmarshal([]byte(sub.Discount), &dis)
		discount = getDiscount(dis, req.Quantity)
	}
	price := sub.UnitPrice * req.Quantity
	amount := int64(float64(price) * discount)
	discountAmount := price - amount

	// Validate amount to prevent overflow
	if amount > MaxOrderAmount {
		l.Errorw("[Renewal] Order amount exceeds maximum limit",
			logger.Field("amount", amount),
			logger.Field("max", MaxOrderAmount),
			logger.Field("user_id", u.Id),
			logger.Field("subscribe_id", sub.Id))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidParams), "order amount exceeds maximum limit")
	}

	var coupon int64 = 0
	if req.Coupon != "" {
		couponInfo, err := l.svcCtx.CouponModel.FindOneByCode(l.ctx, req.Coupon)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotExist), "coupon not found")
			}
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find coupon error: %v", err.Error())
		}
		if couponInfo.Count != 0 && couponInfo.Count <= couponInfo.UsedCount {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponInsufficientUsage), "coupon used")
		}
		couponSub := tool.StringToInt64Slice(couponInfo.Subscribe)

		if len(couponSub) > 0 && !tool.Contains(couponSub, sub.Id) {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotApplicable), "coupon not match")
		}
		var count int64
		err = l.svcCtx.DB.Transaction(func(tx *gorm.DB) error {
			return tx.Model(&order.Order{}).Where("user_id = ? and coupon = ?", u.Id, req.Coupon).Count(&count).Error
		})
		if err != nil {
			l.Errorw("[Renewal] Database query error", logger.Field("error", err.Error()), logger.Field("user_id", u.Id), logger.Field("coupon", req.Coupon))
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find coupon error: %v", err.Error())
		}
		if count >= couponInfo.UserLimit {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponInsufficientUsage), "coupon limit exceeded")
		}
		coupon = calculateCoupon(amount, couponInfo)
	}
	payment, err := l.svcCtx.PaymentModel.FindOne(l.ctx, req.Payment)
	if err != nil {
		l.Errorw("[Renewal] Database query error", logger.Field("error", err.Error()), logger.Field("payment", req.Payment))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find payment error: %v", err.Error())
	}
	baseAmount := amount - coupon

	if baseAmount > MaxOrderAmount {
		l.Errorw("[Renewal] Final order amount exceeds maximum limit after fee",
			logger.Field("amount", baseAmount),
			logger.Field("max", MaxOrderAmount),
			logger.Field("user_id", u.Id))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidParams), "order amount exceeds maximum limit")
	}

	// create order
	orderInfo := order.Order{
		UserId:         u.Id,
		ParentId:       userSubscribe.OrderId,
		OrderNo:        orderNo,
		Type:           2,
		Quantity:       req.Quantity,
		Price:          price,
		Amount:         0,
		GiftAmount:     0,
		Discount:       discountAmount,
		Coupon:         req.Coupon,
		CouponDiscount: coupon,
		PromoCampaignKey: "",
		PromoDiscount:    0,
		PaymentId:      payment.Id,
		Method:         payment.Platform,
		FeeAmount:      0,
		Status:         1,
		SubscribeId:    userSubscribe.SubscribeId,
		SubscribeToken: userSubscribe.Token,
	}
	promoService := promo.NewService(l.svcCtx)
	// Database transaction
	err = l.svcCtx.DB.Transaction(func(db *gorm.DB) error {
		lockedGrant, e := promoService.LockGrantForOrder(l.ctx, u.Id, orderInfo.Type, db)
		if e != nil {
			return e
		}
		promoAmount := int64(0)
		if lockedGrant != nil {
			promoAmount = min(baseAmount, lockedGrant.DiscountValue)
			orderInfo.PromoCampaignKey = lockedGrant.CampaignKey
			orderInfo.PromoDiscount = promoAmount
		} else {
			orderInfo.PromoCampaignKey = ""
			orderInfo.PromoDiscount = 0
		}

		amountAfterPromo := baseAmount - promoAmount
		feeAmount := int64(0)
		if amountAfterPromo > 0 {
			feeAmount = calculateFee(amountAfterPromo, payment)
		}
		finalAmount := amountAfterPromo + feeAmount
		if finalAmount > MaxOrderAmount {
			return errors.Wrapf(xerr.NewErrCode(xerr.InvalidParams), "order amount exceeds maximum limit")
		}

		deductionAmount := int64(0)
		if u.GiftAmount > 0 && finalAmount > 0 {
			if u.GiftAmount >= finalAmount {
				deductionAmount = finalAmount
				u.GiftAmount -= deductionAmount
				finalAmount = 0
			} else {
				deductionAmount = u.GiftAmount
				finalAmount -= u.GiftAmount
				u.GiftAmount = 0
			}
		}

		orderInfo.FeeAmount = feeAmount
		orderInfo.GiftAmount = deductionAmount
		orderInfo.Amount = finalAmount

		// update user deduction && Pre deduction ,Return after canceling the order
		if orderInfo.GiftAmount > 0 {
			// update user deduction && Pre deduction ,Return after canceling the order
			if err := l.svcCtx.UserModel.Update(l.ctx, u, db); err != nil {
				l.Errorw("[Renewal] Database update error", logger.Field("error", err.Error()), logger.Field("user", u))
				return err
			}
			// create deduction record
			giftLog := log.Gift{
				Type:        log.GiftTypeReduce,
				OrderNo:     orderInfo.OrderNo,
				SubscribeId: 0,
				Amount:      orderInfo.GiftAmount,
				Balance:     u.GiftAmount,
				Remark:      "Renewal order deduction",
				Timestamp:   time.Now().UnixMilli(),
			}
			content, _ := giftLog.Marshal()

			if err := db.Model(&log.SystemLog{}).Create(&log.SystemLog{
				Type:     log.TypeGift.Uint8(),
				Date:     time.Now().Format(time.DateOnly),
				ObjectID: u.Id,
				Content:  string(content),
			}).Error; err != nil {
				l.Errorw("[Renewal] Database insert error", logger.Field("error", err.Error()), logger.Field("deductionLog", giftLog))
				return err
			}
		}
		if err := db.Model(&order.Order{}).Create(&orderInfo).Error; err != nil {
			return err
		}
		if orderInfo.PromoDiscount > 0 {
			return promoService.BindGrantToOrder(l.ctx, lockedGrant, orderInfo.Id, db)
		}
		return nil
	})
	if err != nil {
		l.Errorw("[Renewal] Database insert error", logger.Field("error", err.Error()), logger.Field("order", orderInfo))
		return nil, errors.Wrapf(err, "insert order error: %v", err.Error())
	}
	// Deferred task
	payload := queue.DeferCloseOrderPayload{
		OrderNo: orderInfo.OrderNo,
	}
	val, err := json.Marshal(payload)
	if err != nil {
		l.Errorw("[Renewal] Marshal payload error", logger.Field("error", err.Error()), logger.Field("payload", payload))
	}
	task := asynq.NewTask(queue.DeferCloseOrder, val, asynq.MaxRetry(3))
	taskInfo, err := l.svcCtx.Queue.Enqueue(task, asynq.ProcessIn(CloseOrderTimeMinutes*time.Minute))
	if err != nil {
		l.Errorw("[Renewal] Enqueue task error", logger.Field("error", err.Error()), logger.Field("task", task))
	} else {
		l.Infow("[Renewal] Enqueue task success", logger.Field("TaskID", taskInfo.ID))
	}
	return &types.RenewalOrderResponse{
		OrderNo: orderInfo.OrderNo,
	}, nil
}

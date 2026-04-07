package order

import (
	"context"
	"encoding/json"
	"time"

	"github.com/perfect-panel/server/internal/model/log"
	"github.com/perfect-panel/server/internal/promo"
	"github.com/perfect-panel/server/pkg/constant"

	"github.com/hibiken/asynq"
	"github.com/perfect-panel/server/internal/model/order"
	"github.com/perfect-panel/server/internal/model/user"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/xerr"
	queue "github.com/perfect-panel/server/queue/types"
	"github.com/pkg/errors"
	"gorm.io/gorm"

	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/logger"
)

type PurchaseLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

const (
	CloseOrderTimeMinutes = 15
)

// NewPurchaseLogic creates a new purchase logic instance for subscription purchase operations.
// It initializes the logger with context and sets up the service context for database operations.
func NewPurchaseLogic(ctx context.Context, svcCtx *svc.ServiceContext) *PurchaseLogic {
	return &PurchaseLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// Purchase processes new subscription purchase orders including validation, discount calculation,
// coupon processing, gift amount deduction, fee calculation, and order creation with database transaction.
// It handles the complete purchase workflow from user validation to order creation and task scheduling.
func (l *PurchaseLogic) Purchase(req *types.PurchaseOrderRequest) (resp *types.PurchaseOrderResponse, err error) {

	u, ok := l.ctx.Value(constant.CtxKeyUser).(*user.User)
	if !ok {
		logger.Error("current user is not found in context")
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidAccess), "Invalid Access")
	}

	if req.Quantity <= 0 {
		l.Debugf("[Purchase] Quantity is less than or equal to 0, setting to 1")
		req.Quantity = 1
	}

	// Validate quantity limit
	if req.Quantity > MaxQuantity {
		l.Errorw("[Purchase] Quantity exceeds maximum limit", logger.Field("quantity", req.Quantity), logger.Field("max", MaxQuantity))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidParams), "quantity exceeds maximum limit of %d", MaxQuantity)
	}

	// find user subscription
	userSub, err := l.svcCtx.UserModel.QueryUserSubscribe(l.ctx, u.Id)
	if err != nil {
		l.Errorw("[Purchase] Database query error", logger.Field("error", err.Error()), logger.Field("user_id", u.Id))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find user subscription error: %v", err.Error())
	}
	if l.svcCtx.Config.Subscribe.SingleModel {
		if len(userSub) > 0 {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserSubscribeExist), "user has subscription")
		}
	}

	// find subscribe plan
	sub, err := l.svcCtx.SubscribeModel.FindOne(l.ctx, req.SubscribeId)

	if err != nil {
		l.Errorw("[Purchase] Database query error", logger.Field("error", err.Error()), logger.Field("subscribe_id", req.SubscribeId))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find subscribe error: %v", err.Error())
	}
	// check subscribe plan status
	if !*sub.Sell {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "subscribe not sell")
	}

	// check subscribe plan inventory
	if sub.Inventory == 0 {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.SubscribeOutOfStock), "subscribe out of stock")
	}

	var discount float64 = 1
	if sub.Discount != "" {
		var dis []types.SubscribeDiscount
		_ = json.Unmarshal([]byte(sub.Discount), &dis)
		discount = getDiscount(dis, req.Quantity)
	}
	price := sub.UnitPrice * req.Quantity
	// discount amount
	amount := int64(float64(price) * discount)
	discountAmount := price - amount

	// Validate amount to prevent overflow
	if amount > MaxOrderAmount {
		l.Errorw("[Purchase] Order amount exceeds maximum limit",
			logger.Field("amount", amount),
			logger.Field("max", MaxOrderAmount),
			logger.Field("user_id", u.Id),
			logger.Field("subscribe_id", req.SubscribeId))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidParams), "order amount exceeds maximum limit")
	}

	var coupon int64 = 0
	// Calculate the coupon deduction
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
		if len(couponSub) > 0 && !tool.Contains(couponSub, req.SubscribeId) {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotApplicable), "coupon not match")
		}
		var count int64
		err = l.svcCtx.DB.Transaction(func(tx *gorm.DB) error {
			return tx.Model(&order.Order{}).Where("user_id = ? and coupon = ?", u.Id, req.Coupon).Count(&count).Error
		})

		if err != nil {
			l.Errorw("[Purchase] Database query error", logger.Field("error", err.Error()), logger.Field("user_id", u.Id), logger.Field("coupon", req.Coupon))
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find coupon error: %v", err.Error())
		}
		if count >= couponInfo.UserLimit {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponInsufficientUsage), "coupon limit exceeded")
		}
		coupon = calculateCoupon(amount, couponInfo)
	}
	baseAmount := amount - coupon
	// find payment method
	payment, err := l.svcCtx.PaymentModel.FindOne(l.ctx, req.Payment)
	if err != nil {
		l.Errorw("[Purchase] Database query error", logger.Field("error", err.Error()), logger.Field("payment", req.Payment))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find payment method error: %v", err.Error())
	}
	// query user is new purchase or renewal
	isNew, err := l.svcCtx.OrderModel.IsUserEligibleForNewOrder(l.ctx, u.Id)
	if err != nil {
		l.Errorw("[Purchase] Database query error", logger.Field("error", err.Error()), logger.Field("user_id", u.Id))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find user order error: %v", err.Error())
	}
	// create order
	orderInfo := &order.Order{
		UserId:         u.Id,
		OrderNo:        tool.GenerateTradeNo(),
		Type:           1,
		Quantity:       req.Quantity,
		Price:          price,
		Amount:         0,
		Discount:       discountAmount,
		GiftAmount:     0,
		Coupon:         req.Coupon,
		CouponDiscount: coupon,
		PromoCampaignKey: "",
		PromoDiscount:    0,
		PaymentId:      payment.Id,
		Method:         payment.Platform,
		FeeAmount:      0,
		Status:         1,
		IsNew:          isNew,
		SubscribeId:    req.SubscribeId,
	}
	promoService := promo.NewService(l.svcCtx)
	// Database transaction
	err = l.svcCtx.DB.Transaction(func(db *gorm.DB) error {
		// check subscribe plan quota limit inside transaction to prevent race condition
		if sub.Quota > 0 {
			var currentUserSub []user.Subscribe
			if e := db.Model(&user.Subscribe{}).Where("user_id = ?", u.Id).Find(&currentUserSub).Error; e != nil {
				l.Errorw("[Purchase] Database query error", logger.Field("error", e.Error()), logger.Field("user_id", u.Id))
				return e
			}
			var count int64
			for _, v := range currentUserSub {
				if v.SubscribeId == req.SubscribeId {
					count++
				}
			}
			if count >= sub.Quota {
				return errors.Wrapf(xerr.NewErrCode(xerr.SubscribeQuotaLimit), "quota limit")
			}
		}

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
				finalAmount = 0
			} else {
				deductionAmount = u.GiftAmount
				finalAmount -= u.GiftAmount
			}
		}

		orderInfo.FeeAmount = feeAmount
		orderInfo.GiftAmount = deductionAmount
		orderInfo.Amount = finalAmount

		// update user gift amount and create deduction record
		if orderInfo.GiftAmount > 0 {
			// deduct gift amount from user
			u.GiftAmount -= orderInfo.GiftAmount
			if e := l.svcCtx.UserModel.Update(l.ctx, u, db); e != nil {
				l.Errorw("[Purchase] Database update error", logger.Field("error", e.Error()), logger.Field("user", u))
				return e
			}
			// create deduction record
			giftLog := log.Gift{
				Type:        log.GiftTypeReduce,
				OrderNo:     orderInfo.OrderNo,
				SubscribeId: 0,
				Amount:      orderInfo.GiftAmount,
				Balance:     u.GiftAmount,
				Remark:      "Purchase order deduction",
				Timestamp:   time.Now().UnixMilli(),
			}
			content, _ := giftLog.Marshal()

			if e := db.Model(&log.SystemLog{}).Create(&log.SystemLog{
				Type:     log.TypeGift.Uint8(),
				Date:     time.Now().Format(time.DateOnly),
				ObjectID: u.Id,
				Content:  string(content),
			}).Error; e != nil {
				l.Errorw("[Purchase] Database insert error",
					logger.Field("error", e.Error()),
					logger.Field("deductionLog", giftLog),
				)
				return e
			}
		}

		if sub.Inventory != -1 {
			// decrease subscribe plan stock
			sub.Inventory -= 1
			// update subscribe plan stock
			if err = l.svcCtx.SubscribeModel.Update(l.ctx, sub, db); err != nil {
				l.Errorw("[Purchase] Database update error", logger.Field("error", err.Error()), logger.Field("subscribe", sub))
				return err
			}
		}

		if err = db.WithContext(l.ctx).Model(&order.Order{}).Create(&orderInfo).Error; err != nil {
			return err
		}
		return promoService.BindGrantToOrder(l.ctx, lockedGrant, orderInfo.Id, db)
	})
	if err != nil {
		l.Errorw("[Purchase] Database insert error", logger.Field("error", err.Error()), logger.Field("orderInfo", orderInfo))

		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseInsertError), "insert order error: %v", err.Error())
	}
	// Deferred task
	payload := queue.DeferCloseOrderPayload{
		OrderNo: orderInfo.OrderNo,
	}
	val, err := json.Marshal(payload)
	if err != nil {
		l.Errorw("[Purchase] Marshal payload error", logger.Field("error", err.Error()), logger.Field("payload", payload))
	}
	task := asynq.NewTask(queue.DeferCloseOrder, val, asynq.MaxRetry(3))
	taskInfo, err := l.svcCtx.Queue.Enqueue(task, asynq.ProcessIn(CloseOrderTimeMinutes*time.Minute))
	if err != nil {
		l.Errorw("[Purchase] Enqueue task error", logger.Field("error", err.Error()), logger.Field("task", task))
	} else {
		l.Infow("[Purchase] Enqueue task success", logger.Field("TaskID", taskInfo.ID))
	}

	return &types.PurchaseOrderResponse{
		OrderNo: orderInfo.OrderNo,
	}, nil
}

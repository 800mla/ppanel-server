package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/perfect-panel/server/internal/model/order"
	"github.com/perfect-panel/server/internal/promo"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/constant"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/payment"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/xerr"
	queue "github.com/perfect-panel/server/queue/types"

	"github.com/hibiken/asynq"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

type PurchaseLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

// NewPurchaseLogic Purchase subscription
func NewPurchaseLogic(ctx context.Context, svcCtx *svc.ServiceContext) *PurchaseLogic {
	return &PurchaseLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

const (
	CloseOrderTimeMinutes = 15
)

func (l *PurchaseLogic) Purchase(req *types.PortalPurchaseRequest) (resp *types.PortalPurchaseResponse, err error) {
	req.AuthType = normalizePortalAuthType(req.AuthType)
	if _, err = requirePortalPurchaseIdentity(req.AuthType, req.Identifier); err != nil {
		return nil, err
	}

	// find subscribe plan
	sub, err := l.svcCtx.SubscribeModel.FindOne(l.ctx, req.SubscribeId)
	if err != nil {
		l.Errorw("[Purchase] Database query error", logger.Field("error", err.Error()), logger.Field("subscribe_id", req.SubscribeId))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find subscribe error: %v", err.Error())
	}

	// check subscribe plan stock
	if sub.Inventory == 0 {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.SubscribeOutOfStock), "subscribe out of stock")
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
	// discount amount
	amount := int64(float64(price) * discount)
	discountAmount := price - amount

	var couponAmount int64 = 0
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
		// Check expiration time
		expireTime := time.Unix(couponInfo.ExpireTime, 0)
		if time.Now().After(expireTime) {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponExpired), "coupon expired")
		}

		couponSub := tool.StringToInt64Slice(couponInfo.Subscribe)
		if len(couponSub) > 0 && !tool.Contains(couponSub, req.SubscribeId) {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotApplicable), "coupon not match")
		}

		couponAmount = calculateCoupon(amount, couponInfo)
	}
	// Calculate the handling fee
	amount -= couponAmount
	// find payment method
	paymentConfig, err := l.svcCtx.PaymentModel.FindOne(l.ctx, req.Payment)
	if err != nil {
		l.Logger.Error("[Purchase] Database query error", logger.Field("error", err.Error()), logger.Field("payment", req.Payment))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.PaymentMethodNotFound), "find payment method error: %v", err.Error())
	}

	if payment.ParsePlatform(paymentConfig.Platform) == payment.Balance {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.PaymentMethodNotFound), "balance error")
	}

	previewState, err := resolvePortalPreviewState(l.ctx, l.svcCtx, req.AuthType, req.Identifier, req.Password)
	if err != nil {
		return nil, err
	}

	var ticketPayload *portalVerificationTicketPayload
	var releaseTicketLock func()
	if requirement := resolvePortalVerificationRequirement(previewState); requirement != nil {
		if err = takePortalPurchaseIPLimit(l.ctx, l.svcCtx, req.IP); err != nil {
			return nil, err
		}
		if requirement.AccountMode == portalAccountModeNewEmail {
			if err = validatePortalEmailDomain(l.svcCtx, req.Identifier); err != nil {
				return nil, err
			}
		}
		ticketPayload, releaseTicketLock, err = l.preparePortalPurchaseTicket(req, requirement.AccountMode)
		if err != nil {
			return nil, err
		}
		defer func() {
			if releaseTicketLock != nil {
				releaseTicketLock()
			}
		}()
	} else if !previewState.CanPurchase {
		switch previewState.NextAction {
		case portalNextActionInputPassword:
			return nil, newPortalCheckoutPasswordRequiredErr()
		case portalNextActionVerifyEmail:
			return nil, newPortalCheckoutVerificationRequiredErr()
		default:
			if previewState.PurchaseBlockReason != "" {
				return nil, errors.Wrapf(xerr.NewErrCodeMsg(xerr.InvalidParams, previewState.PurchaseBlockReason), previewState.PurchaseBlockReason)
			}
		}
	}

	pendingOrder, err := findPortalPendingOrder(l.ctx, l.svcCtx, req.AuthType, req.Identifier)
	if err != nil {
		return nil, err
	}
	if pendingOrder != nil {
		return &types.PortalPurchaseResponse{
			OrderNo:       pendingOrder.OrderNo,
			PayableAmount: pendingOrder.Amount,
		}, nil
	}

	baseAmount := amount
	// create order
	orderInfo := &order.Order{
		UserId:         0,
		OrderNo:        tool.GenerateTradeNo(),
		Type:           1,
		Quantity:       req.Quantity,
		Price:          price,
		Amount:         0,
		Discount:       discountAmount,
		GiftAmount:     0,
		Coupon:         req.Coupon,
		CouponDiscount: couponAmount,
		PromoCampaignKey: "",
		PromoDiscount:    0,
		PaymentId:      req.Payment,
		Method:         paymentConfig.Platform,
		FeeAmount:      0,
		Status:         1,
		IsNew:          false,
		SubscribeId:    req.SubscribeId,
	}
	promoService := promo.NewService(l.svcCtx)
	// save order
	err = l.svcCtx.DB.Transaction(func(tx *gorm.DB) error {
		orderUser, err := resolvePortalOrderUserForPurchase(l.ctx, l.svcCtx, tx, req, ticketPayload)
		if err != nil {
			return err
		}
		orderInfo.UserId = orderUser.Id

		var paidCount int64
		if err = tx.Model(&order.Order{}).
			Where("user_id = ? AND status IN ?", orderUser.Id, []int64{2, 5}).
			Count(&paidCount).Error; err != nil {
			return err
		}
		orderInfo.IsNew = paidCount == 0

		lockedGrant, err := promoService.LockGrantForOrder(l.ctx, orderUser.Id, orderInfo.Type, tx)
		if err != nil {
			return err
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

		feeAmount := int64(0)
		finalAmount := baseAmount - promoAmount
		if finalAmount > 0 {
			feeAmount = calculateFee(finalAmount, paymentConfig)
			finalAmount += feeAmount
		}
		orderInfo.FeeAmount = feeAmount
		orderInfo.Amount = finalAmount

		// save guest order and user information
		tempOrder := constant.TemporaryOrderInfo{
			OrderNo:    orderInfo.OrderNo,
			Identifier: req.Identifier,
			AuthType:   req.AuthType,
			Password:   req.Password,
			InviteCode: req.InviteCode,
		}
		content, _ := tempOrder.Marshal()

		if _, err = l.svcCtx.Redis.Set(l.ctx, fmt.Sprintf(constant.TempOrderCacheKey, orderInfo.OrderNo), string(content), 24*time.Hour).Result(); err != nil {
			l.Errorw("[Purchase] Redis set error", logger.Field("error", err.Error()), logger.Field("order_no", orderInfo.OrderNo))
			return err
		}
		l.Infow("[Purchase] Guest order", logger.Field("order_no", orderInfo.OrderNo), logger.Field("identifier", req.Identifier))

		// Decrease subscribe plan stock
		if sub.Inventory != -1 {
			sub.Inventory--
			if e := l.svcCtx.SubscribeModel.Update(l.ctx, sub, tx); e != nil {
				l.Errorw("[Purchase] Database update error", logger.Field("error", e.Error()), logger.Field("subscribe_id", sub.Id))
				return e
			}
		}

		// save guest order
		if err = l.svcCtx.OrderModel.Insert(l.ctx, orderInfo, tx); err != nil {
			return err
		}
		return promoService.BindGrantToOrder(l.ctx, lockedGrant, orderInfo.Id, tx)
	})
	if err != nil {
		l.Errorw("[Purchase] Database transaction error", logger.Field("error", err.Error()))
		var codeErr *xerr.CodeError
		if errors.As(errors.Cause(err), &codeErr) {
			return nil, err
		}
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "transaction error: %v", err.Error())
	}
	// Deferred task
	payload := queue.DeferCloseOrderPayload{
		OrderNo: orderInfo.OrderNo,
	}
	val, err := json.Marshal(payload)
	if err != nil {
		l.Errorw("[CloseOrder Task] Marshal payload error", logger.Field("error", err.Error()), logger.Field("payload", payload))
	}
	task := asynq.NewTask(queue.DeferCloseOrder, val, asynq.MaxRetry(3))
	taskInfo, err := l.svcCtx.Queue.Enqueue(task, asynq.ProcessIn(CloseOrderTimeMinutes*time.Minute))
	if err != nil {
		l.Errorw("[CloseOrder Task] Enqueue task error", logger.Field("error", err.Error()), logger.Field("task", taskInfo))
	} else {
		l.Infow("[CloseOrder Task] Enqueue task success", logger.Field("TaskID", taskInfo.ID))
	}
		resp = &types.PortalPurchaseResponse{
		OrderNo:       orderInfo.OrderNo,
		PayableAmount: orderInfo.Amount,
	}
	if ticketPayload != nil && req.PortalVerificationTicket != "" {
		if err = consumePortalVerificationTicket(l.ctx, l.svcCtx, req.PortalVerificationTicket); err != nil {
			l.Errorw("[Purchase] Consume portal verification ticket error", logger.Field("error", err.Error()), logger.Field("identifier", req.Identifier))
		}
	}
	if err = storePortalPendingOrder(l.ctx, l.svcCtx, req.AuthType, req.Identifier, orderInfo.OrderNo); err != nil {
		l.Errorw("[Purchase] Store portal pending order error", logger.Field("error", err.Error()), logger.Field("identifier", req.Identifier), logger.Field("order_no", orderInfo.OrderNo))
	}
	return resp, nil
}

func (l *PurchaseLogic) preparePortalPurchaseTicket(req *types.PortalPurchaseRequest, expectedAccountMode string) (*portalVerificationTicketPayload, func(), error) {
	releaseLock, err := acquirePortalVerificationTicketLock(l.ctx, l.svcCtx, req.PortalVerificationTicket)
	if err != nil {
		return nil, nil, err
	}

	payload, err := loadPortalVerificationTicket(l.ctx, l.svcCtx, req.PortalVerificationTicket)
	if err != nil {
		releaseLock()
		return nil, nil, err
	}
	if err = validatePortalVerificationTicketPayload(payload, req.AuthType, req.Identifier, req.IP, req.UserAgent, expectedAccountMode); err != nil {
		releaseLock()
		return nil, nil, err
	}
	return payload, releaseLock, nil
}

package promo

import (
	"context"
	"time"

	"github.com/perfect-panel/server/internal/model/order"
	"github.com/perfect-panel/server/internal/model/userpromo"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	CampaignKeyNewUserFirstOrder = "new_user_first_order"
	DiscountTypeFixedAmount      = "fixed_amount"

	StatusPending   = "pending"
	StatusActive    = "active"
	StatusDismissed = "dismissed"
	StatusUsed      = "used"
	StatusExpired   = "expired"

	TitleNewUserFirstOrder       = "新人首单优惠"
	DescriptionNewUserFirstOrder = "注册成功后 2 小时内可用于首单，支付成功后立即失效"

	NewUserFirstOrderDiscountValue = int64(1000)
)

var NewUserFirstOrderPromoDuration = 2 * time.Hour

type Service struct {
	svcCtx *svc.ServiceContext
}

func NewService(svcCtx *svc.ServiceContext) *Service {
	return &Service{svcCtx: svcCtx}
}

func (s *Service) GrantSignupPromo(ctx context.Context, userID int64, tx ...*gorm.DB) error {
	db := s.db(ctx, tx...)

	var count int64
	if err := db.Model(&userpromo.UserPromoGrant{}).
		Where("user_id = ? AND campaign_key = ?", userID, CampaignKeyNewUserFirstOrder).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	now := time.Now()
	grant := &userpromo.UserPromoGrant{
		UserId:        userID,
		CampaignKey:   CampaignKeyNewUserFirstOrder,
		Status:        StatusPending,
		DiscountType:  DiscountTypeFixedAmount,
		DiscountValue: NewUserFirstOrderDiscountValue,
		ExpiresAt:     now.Add(NewUserFirstOrderPromoDuration),
	}
	return db.Create(grant).Error
}

func (s *Service) GetUserPromoStatus(ctx context.Context, userID int64) (*types.UserPromoStatus, error) {
	grant, err := s.getGrant(ctx, userID, false, nil)
	if err != nil {
		return nil, err
	}
	return s.toStatus(grant), nil
}

func (s *Service) DismissUserPromo(ctx context.Context, userID int64) error {
	return s.db(ctx).Transaction(func(tx *gorm.DB) error {
		grant, err := s.getGrant(ctx, userID, true, tx)
		if err != nil {
			return err
		}
		if grant == nil {
			return nil
		}
		if grant.Status == StatusUsed || grant.Status == StatusExpired {
			return nil
		}
		now := time.Now()
		grant.Status = StatusDismissed
		grant.DismissedAt = &now
		return tx.Save(grant).Error
	})
}

func (s *Service) PreviewDiscountForUser(ctx context.Context, userID int64, orderType uint8, amount int64) (int64, error) {
	grant, err := s.getGrant(ctx, userID, false, nil)
	if err != nil {
		return 0, err
	}
	if grant == nil || !supportsPromoOrderType(orderType) {
		return 0, nil
	}
	if grant.OrderId != 0 {
		available, err := s.isReservationAvailable(ctx, grant, 0, s.db(ctx))
		if err != nil {
			return 0, err
		}
		if !available {
			return 0, nil
		}
	}
	return calculateDiscount(amount, grant), nil
}

func (s *Service) PreviewDiscountForNewSignup(amount int64) int64 {
	grant := &userpromo.UserPromoGrant{
		DiscountType:  DiscountTypeFixedAmount,
		DiscountValue: NewUserFirstOrderDiscountValue,
	}
	return calculateDiscount(amount, grant)
}

func (s *Service) LockGrantForOrder(ctx context.Context, userID int64, orderType uint8, tx *gorm.DB) (*userpromo.UserPromoGrant, error) {
	if !supportsPromoOrderType(orderType) {
		return nil, nil
	}
	grant, err := s.getGrant(ctx, userID, true, tx)
	if err != nil || grant == nil {
		return grant, err
	}
	if grant.OrderId != 0 {
		available, err := s.isReservationAvailable(ctx, grant, 0, tx)
		if err != nil {
			return nil, err
		}
		if !available {
			return nil, nil
		}
	}
	return grant, nil
}

func (s *Service) BindGrantToOrder(ctx context.Context, grant *userpromo.UserPromoGrant, orderID int64, tx *gorm.DB) error {
	if grant == nil || orderID == 0 {
		return nil
	}
	grant.OrderId = orderID
	return tx.Save(grant).Error
}

func (s *Service) ConsumePromoByOrderID(ctx context.Context, orderID int64, tx *gorm.DB) error {
	if orderID == 0 {
		return nil
	}

	var grant userpromo.UserPromoGrant
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("order_id = ?", orderID).
		First(&grant).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}
	if grant.Status == StatusUsed {
		return nil
	}
	now := time.Now()
	grant.Status = StatusUsed
	grant.UsedAt = &now
	return tx.Save(&grant).Error
}

func (s *Service) ReleaseReservationByOrderID(ctx context.Context, orderID int64, tx ...*gorm.DB) error {
	if orderID == 0 {
		return nil
	}

	releaseFn := func(innerTx *gorm.DB) error {
		var grant userpromo.UserPromoGrant
		err := innerTx.
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("order_id = ?", orderID).
			First(&grant).Error
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		if grant.Status == StatusUsed {
			return nil
		}
		grant.OrderId = 0
		if err = s.normalizeGrant(ctx, &grant, innerTx, false); err != nil {
			return err
		}
		return innerTx.Save(&grant).Error
	}

	if len(tx) > 0 && tx[0] != nil {
		return releaseFn(tx[0].WithContext(ctx))
	}
	return s.db(ctx).Transaction(releaseFn)
}

func (s *Service) db(ctx context.Context, tx ...*gorm.DB) *gorm.DB {
	if len(tx) > 0 && tx[0] != nil {
		return tx[0].WithContext(ctx)
	}
	return s.svcCtx.DB.WithContext(ctx)
}

func (s *Service) getGrant(ctx context.Context, userID int64, forUpdate bool, tx *gorm.DB) (*userpromo.UserPromoGrant, error) {
	if userID == 0 {
		return nil, nil
	}

	db := s.db(ctx, tx)
	if forUpdate {
		db = db.Clauses(clause.Locking{Strength: "UPDATE"})
	}

	var grant userpromo.UserPromoGrant
	err := db.Where("user_id = ? AND campaign_key = ?", userID, CampaignKeyNewUserFirstOrder).First(&grant).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	if err = s.normalizeGrant(ctx, &grant, db, true); err != nil {
		return nil, err
	}
	if grant.OrderId != 0 && grant.Status != StatusUsed && grant.Status != StatusExpired {
		if _, err = s.isReservationAvailable(ctx, &grant, 0, db); err != nil {
			return nil, err
		}
	}
	return &grant, nil
}

func (s *Service) normalizeGrant(ctx context.Context, grant *userpromo.UserPromoGrant, db *gorm.DB, persist bool) error {
	if grant == nil {
		return nil
	}

	originalStatus := grant.Status
	originalOrderID := grant.OrderId
	now := time.Now()

	if grant.Status != StatusUsed {
		if !grant.ExpiresAt.After(now) || s.hasCompletedPaidOrder(ctx, grant.UserId, grant.OrderId, db) {
			grant.Status = StatusExpired
			if grant.OrderId != 0 {
				if available, err := s.isReservationAvailable(ctx, grant, 0, db); err != nil {
					return err
				} else if available {
					grant.OrderId = 0
				}
			}
		} else if grant.Status == StatusPending {
			if grant.DismissedAt != nil {
				grant.Status = StatusDismissed
			} else {
				grant.Status = StatusActive
			}
		} else if grant.Status == StatusActive && grant.DismissedAt != nil {
			grant.Status = StatusDismissed
		}
	}

	if persist && (originalStatus != grant.Status || originalOrderID != grant.OrderId) {
		return s.cleanDB(db).Save(grant).Error
	}
	return nil
}

func (s *Service) hasCompletedPaidOrder(ctx context.Context, userID, reservedOrderID int64, db *gorm.DB) bool {
	if userID == 0 {
		return false
	}

	query := s.cleanDB(db).Model(&order.Order{}).Where("user_id = ? AND status IN ?", userID, []int64{2, 5})
	if reservedOrderID != 0 {
		query = query.Where("id <> ?", reservedOrderID)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func (s *Service) isReservationAvailable(ctx context.Context, grant *userpromo.UserPromoGrant, currentOrderID int64, db *gorm.DB) (bool, error) {
	if grant == nil || grant.OrderId == 0 {
		return true, nil
	}
	if currentOrderID != 0 && grant.OrderId == currentOrderID {
		return true, nil
	}

	orderInfo, err := s.svcCtx.OrderModel.FindOne(ctx, grant.OrderId)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			grant.OrderId = 0
			if db != nil {
				if saveErr := s.cleanDB(db).Save(grant).Error; saveErr != nil {
					return false, saveErr
				}
			}
			return true, nil
		}
		return false, err
	}

	switch orderInfo.Status {
	case 3, 4:
		grant.OrderId = 0
		if db != nil {
			if err = s.cleanDB(db).Save(grant).Error; err != nil {
				return false, err
			}
		}
		return true, nil
	default:
		return false, nil
	}
}

func (s *Service) cleanDB(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true})
}

func (s *Service) toStatus(grant *userpromo.UserPromoGrant) *types.UserPromoStatus {
	status := &types.UserPromoStatus{
		Title:       TitleNewUserFirstOrder,
		Description: DescriptionNewUserFirstOrder,
	}
	if grant == nil {
		return status
	}

	status.Status = grant.Status
	status.ExpiresAt = grant.ExpiresAt.UnixMilli()
	status.Dismissed = grant.DismissedAt != nil
	if grant.Status == StatusPending || grant.Status == StatusActive || grant.Status == StatusDismissed {
		status.HasActivePromo = true
		remaining := time.Until(grant.ExpiresAt)
		if remaining > 0 {
			status.RemainingSeconds = int64(remaining / time.Second)
		}
	}
	return status
}

func calculateDiscount(amount int64, grant *userpromo.UserPromoGrant) int64 {
	if grant == nil || amount <= 0 {
		return 0
	}
	switch grant.DiscountType {
	case DiscountTypeFixedAmount:
		if grant.DiscountValue > amount {
			return amount
		}
		return grant.DiscountValue
	default:
		return 0
	}
}

func supportsPromoOrderType(orderType uint8) bool {
	return orderType == 1
}

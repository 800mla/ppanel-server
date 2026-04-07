package userpromo

import "time"

type UserPromoGrant struct {
	Id            int64      `gorm:"primaryKey"`
	UserId        int64      `gorm:"type:bigint;not null;uniqueIndex:uk_user_promo_campaign,priority:1;index:idx_user_promo_status,priority:1;comment:User ID"`
	CampaignKey   string     `gorm:"type:varchar(100);not null;uniqueIndex:uk_user_promo_campaign,priority:2;comment:Promo campaign key"`
	Status        string     `gorm:"type:varchar(20);not null;default:'pending';index:idx_user_promo_status,priority:2;comment:pending, active, dismissed, used, expired"`
	DiscountType  string     `gorm:"type:varchar(20);not null;default:'fixed_amount';comment:Discount type"`
	DiscountValue int64      `gorm:"type:int;not null;default:0;comment:Discount value in cents"`
	ExpiresAt     time.Time  `gorm:"type:datetime(3);not null;index:idx_user_promo_expires_at;comment:Promo expiration time"`
	DismissedAt   *time.Time `gorm:"type:datetime(3);default:null;comment:Popup dismissed time"`
	UsedAt        *time.Time `gorm:"type:datetime(3);default:null;comment:Promo used time"`
	OrderId       int64      `gorm:"type:bigint;not null;default:0;index:idx_user_promo_order_id;comment:Reserved or used order ID"`
	CreatedAt     time.Time  `gorm:"<-:create;comment:Create time"`
	UpdatedAt     time.Time  `gorm:"comment:Update time"`
}

func (UserPromoGrant) TableName() string {
	return "user_promo_grants"
}

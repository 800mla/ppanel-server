package userpromo

import (
	"context"

	"gorm.io/gorm"
)

type customUserPromoGrantLogicModel interface {
	FindOneByUserAndCampaign(ctx context.Context, userID int64, campaignKey string, tx ...*gorm.DB) (*UserPromoGrant, error)
	FindOneByOrderID(ctx context.Context, orderID int64, tx ...*gorm.DB) (*UserPromoGrant, error)
}

func NewModel(db *gorm.DB) Model {
	return &customUserPromoGrantModel{
		defaultUserPromoGrantModel: newUserPromoGrantModel(db),
	}
}

func (m *customUserPromoGrantModel) FindOneByUserAndCampaign(ctx context.Context, userID int64, campaignKey string, tx ...*gorm.DB) (*UserPromoGrant, error) {
	var grant UserPromoGrant
	db := m.db(ctx, tx...)
	err := db.Where("user_id = ? AND campaign_key = ?", userID, campaignKey).First(&grant).Error
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

func (m *customUserPromoGrantModel) FindOneByOrderID(ctx context.Context, orderID int64, tx ...*gorm.DB) (*UserPromoGrant, error) {
	var grant UserPromoGrant
	db := m.db(ctx, tx...)
	err := db.Where("order_id = ?", orderID).First(&grant).Error
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

package userpromo

import (
	"context"

	"gorm.io/gorm"
)

var _ Model = (*customUserPromoGrantModel)(nil)

type (
	Model interface {
		userPromoGrantModel
		customUserPromoGrantLogicModel
	}
	userPromoGrantModel interface {
		Insert(ctx context.Context, data *UserPromoGrant, tx ...*gorm.DB) error
		FindOne(ctx context.Context, id int64, tx ...*gorm.DB) (*UserPromoGrant, error)
		Update(ctx context.Context, data *UserPromoGrant, tx ...*gorm.DB) error
		Delete(ctx context.Context, id int64, tx ...*gorm.DB) error
		Transaction(ctx context.Context, fn func(db *gorm.DB) error) error
	}
	customUserPromoGrantModel struct {
		*defaultUserPromoGrantModel
	}
	defaultUserPromoGrantModel struct {
		*gorm.DB
	}
)

func newUserPromoGrantModel(db *gorm.DB) *defaultUserPromoGrantModel {
	return &defaultUserPromoGrantModel{DB: db}
}

func (m *defaultUserPromoGrantModel) db(ctx context.Context, tx ...*gorm.DB) *gorm.DB {
	if len(tx) > 0 {
		return tx[0].WithContext(ctx)
	}
	return m.WithContext(ctx)
}

func (m *defaultUserPromoGrantModel) Insert(ctx context.Context, data *UserPromoGrant, tx ...*gorm.DB) error {
	return m.db(ctx, tx...).Create(data).Error
}

func (m *defaultUserPromoGrantModel) FindOne(ctx context.Context, id int64, tx ...*gorm.DB) (*UserPromoGrant, error) {
	var grant UserPromoGrant
	err := m.db(ctx, tx...).Where("id = ?", id).First(&grant).Error
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

func (m *defaultUserPromoGrantModel) Update(ctx context.Context, data *UserPromoGrant, tx ...*gorm.DB) error {
	return m.db(ctx, tx...).Save(data).Error
}

func (m *defaultUserPromoGrantModel) Delete(ctx context.Context, id int64, tx ...*gorm.DB) error {
	return m.db(ctx, tx...).Delete(&UserPromoGrant{}, id).Error
}

func (m *defaultUserPromoGrantModel) Transaction(ctx context.Context, fn func(db *gorm.DB) error) error {
	return m.WithContext(ctx).Transaction(fn)
}

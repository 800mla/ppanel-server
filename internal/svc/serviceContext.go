package svc

import (
	"context"
	"time"

	"github.com/perfect-panel/server/internal/model/client"
	"github.com/perfect-panel/server/internal/model/node"
	"github.com/perfect-panel/server/internal/model/redemption"
	"github.com/perfect-panel/server/pkg/device"

	"github.com/perfect-panel/server/internal/config"
	"github.com/perfect-panel/server/internal/model/ads"
	"github.com/perfect-panel/server/internal/model/announcement"
	"github.com/perfect-panel/server/internal/model/auth"
	"github.com/perfect-panel/server/internal/model/coupon"
	"github.com/perfect-panel/server/internal/model/document"
	"github.com/perfect-panel/server/internal/model/log"
	"github.com/perfect-panel/server/internal/model/order"
	"github.com/perfect-panel/server/internal/model/payment"
	"github.com/perfect-panel/server/internal/model/subscribe"
	"github.com/perfect-panel/server/internal/model/system"
	"github.com/perfect-panel/server/internal/model/ticket"
	"github.com/perfect-panel/server/internal/model/traffic"
	"github.com/perfect-panel/server/internal/model/user"
	"github.com/perfect-panel/server/pkg/limit"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/nodeMultiplier"
	"github.com/perfect-panel/server/pkg/orm"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type ServiceContext struct {
	DB           *gorm.DB
	Redis        *redis.Client
	Config       config.Config
	Queue        *asynq.Client
	ExchangeRate float64
	GeoIP        *IPLocation

	//NodeCache   *cache.NodeCacheClient
	AuthModel   auth.Model
	AdsModel    ads.Model
	LogModel    log.Model
	NodeModel   node.Model
	UserModel   user.Model
	OrderModel  order.Model
	ClientModel client.Model
	TicketModel ticket.Model
	//ServerModel        server.Model
	SystemModel           system.Model
	CouponModel           coupon.Model
	RedemptionCodeModel   redemption.RedemptionCodeModel
	RedemptionRecordModel redemption.RedemptionRecordModel
	PaymentModel          payment.Model
	DocumentModel         document.Model
	SubscribeModel        subscribe.Model
	TrafficLogModel       traffic.Model
	AnnouncementModel     announcement.Model

	Restart               func() error
	TelegramBot           *tgbotapi.BotAPI
	NodeMultiplierManager *nodeMultiplier.Manager
	AuthLimiter           *limit.PeriodLimit
	DeviceManager         *device.DeviceManager
}

func NewServiceContext(c config.Config) *ServiceContext {
	// gorm initialize
	db, err := orm.ConnectMysql(orm.Mysql{
		Config: c.MySQL,
	})

	if err != nil {
		panic(err.Error())
	}

	// IP location initialize
	geoIP, err := NewIPLocation("./cache/GeoLite2-City.mmdb")
	if err != nil {
		logger.Errorf("[GeoIP] Initialize failed, IP location lookup will be unavailable: %s", err.Error())
	}

	rds := redis.NewClient(&redis.Options{
		Addr:            c.Redis.Host,
		Password:        c.Redis.Pass,
		DB:              c.Redis.DB,
		PoolSize:        c.Redis.PoolSize,                                  // 连接池大小：根据应用并发量调整，建议 100-500
		MinIdleConns:    c.Redis.MinIdleConns,                              // 最小空闲连接：保持一定数量的空闲连接，减少建立连接的开销
		MaxRetries:      c.Redis.MaxRetries,                                // 最大重试次数：网络抖动时自动重试
		PoolTimeout:     time.Second * time.Duration(c.Redis.PoolTimeout),  // 从连接池获取连接的超时时间
		ConnMaxIdleTime: time.Second * time.Duration(c.Redis.IdleTimeout),  // 空闲连接的超时时间，自动回收长时间空闲的连接
		ConnMaxLifetime: time.Second * time.Duration(c.Redis.MaxConnAge),   // 连接的最大生命周期，定期重建连接避免长时间使用的问题
		DialTimeout:     time.Second * time.Duration(c.Redis.DialTimeout),  // 建立新连接的超时时间
		ReadTimeout:     time.Second * time.Duration(c.Redis.ReadTimeout),  // 读操作超时时间
		WriteTimeout:    time.Second * time.Duration(c.Redis.WriteTimeout), // 写操作超时时间
	})
	err = rds.Ping(context.Background()).Err()
	if err != nil {
		panic(err.Error())
	}
	authLimiter := limit.NewPeriodLimit(86400, 15, rds, config.SendCountLimitKeyPrefix, limit.Align())
	srv := &ServiceContext{
		DB:           db,
		Redis:        rds,
		Config:       c,
		Queue:        NewAsynqClient(c),
		ExchangeRate: 0,
		GeoIP:        geoIP,
		//NodeCache:   cache.NewNodeCacheClient(rds),
		AuthLimiter: authLimiter,
		AdsModel:    ads.NewModel(db, rds),
		LogModel:    log.NewModel(db),
		NodeModel:   node.NewModel(db, rds),
		AuthModel:   auth.NewModel(db, rds),
		UserModel:   user.NewModel(db, rds),
		OrderModel:  order.NewModel(db, rds),
		ClientModel: client.NewSubscribeApplicationModel(db),
		TicketModel: ticket.NewModel(db, rds),
		//ServerModel:       server.NewModel(db, rds),
		SystemModel:           system.NewModel(db, rds),
		CouponModel:           coupon.NewModel(db, rds),
		RedemptionCodeModel:   redemption.NewRedemptionCodeModel(db, rds),
		RedemptionRecordModel: redemption.NewRedemptionRecordModel(db, rds),
		PaymentModel:          payment.NewModel(db, rds),
		DocumentModel:         document.NewModel(db, rds),
		SubscribeModel:        subscribe.NewModel(db, rds),
		TrafficLogModel:       traffic.NewModel(db),
		AnnouncementModel:     announcement.NewModel(db, rds),
	}
	srv.DeviceManager = NewDeviceManager(srv)
	return srv

}

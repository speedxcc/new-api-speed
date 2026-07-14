package model

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain（task_cas_test.go）的 AutoMigrate 列表未包含 SubscriptionPreConsumeRecord，
// 而 PreConsumeUserSubscription 会查询/写入该表，这里补建一次。
var bindGroupMigrateOnce sync.Once

func ensureBindGroupTables() {
	bindGroupMigrateOnce.Do(func() {
		_ = DB.AutoMigrate(&SubscriptionPreConsumeRecord{})
	})
}

// 辅助：seed 一个套餐 + 一个用户订阅实例。
func seedBindGroupPlan(t *testing.T, plan *SubscriptionPlan) {
	t.Helper()
	ensureBindGroupTables()
	// BeforeCreate 钩子会填充时间戳，这里走正常 Create
	require.NoError(t, DB.Create(plan).Error)
}

func seedBindGroupSub(t *testing.T, sub *UserSubscription) {
	t.Helper()
	require.NoError(t, DB.Create(sub).Error)
}

func loadBindGroupSub(t *testing.T, id int) UserSubscription {
	t.Helper()
	var sub UserSubscription
	require.NoError(t, DB.Where("id = ?", id).First(&sub).Error)
	return sub
}


// 场景 1：套餐绑定 vip 分组，请求用 vip 分组 → 命中订阅，扣订阅额度。
func TestPreConsumeUserSubscriptionBindGroupMatchesAndConsumes(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:            7101,
		Title:         "GPT4 Plan",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
		BindGroup:     "vip",
	}
	seedBindGroupPlan(t, plan)
	seedBindGroupSub(t, &UserSubscription{
		Id: 7201, UserId: 101, PlanId: plan.Id,
		AmountTotal: 1000, AmountUsed: 0,
		StartTime: now - 3600, EndTime: now + 30*24*3600, Status: "active",
		BindGroup: "vip",
	})

	res, err := PreConsumeUserSubscription("req-match-1", 101, "gpt-4", 0, 100, "vip")

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 7201, res.UserSubscriptionId)
	assert.EqualValues(t, 100, res.PreConsumed)
	assert.EqualValues(t, 1000, res.AmountTotal)
	assert.EqualValues(t, 0, res.AmountUsedBefore)
	assert.EqualValues(t, 100, res.AmountUsedAfter)

	sub := loadBindGroupSub(t, 7201)
	assert.EqualValues(t, 100, sub.AmountUsed, "vip 分组请求应扣订阅额度")
}

// 场景 2：套餐绑定 vip，请求用 default 分组 → 不匹配，返回错误（上层据此回退钱包）。
func TestPreConsumeUserSubscriptionBindGroupMismatchReturnsError(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:            7102,
		Title:         "VIP Only Plan",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
		BindGroup:     "vip",
	}
	seedBindGroupPlan(t, plan)
	seedBindGroupSub(t, &UserSubscription{
		Id: 7202, UserId: 102, PlanId: plan.Id,
		AmountTotal: 1000, AmountUsed: 0,
		StartTime: now - 3600, EndTime: now + 30*24*3600, Status: "active",
		BindGroup: "vip",
	})

	res, err := PreConsumeUserSubscription("req-mismatch-1", 102, "gpt-4", 0, 100, "default")

	require.Error(t, err, "default 分组不应命中绑定了 vip 的订阅")
	assert.Nil(t, res)
	// 错误信息需包含 "subscription quota insufficient"，以便上层 BillingSession 据此回退钱包
	assert.True(t, strings.Contains(err.Error(), "subscription quota insufficient") || strings.Contains(err.Error(), "no active subscription"),
		"错误应可被上层识别为额度不足以触发钱包回退, 实际: %s", err.Error())

	// 订阅额度不应被扣减
	sub := loadBindGroupSub(t, 7202)
	assert.EqualValues(t, 0, sub.AmountUsed, "不匹配时订阅额度不应变动")
}

// 场景 3：套餐未绑定分组（bind_group 为空），任何分组都命中 → 兼容旧行为。
func TestPreConsumeUserSubscriptionBindGroupEmptyMatchesAnyGroup(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:            7103,
		Title:         "Legacy Plan",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
		BindGroup:     "", // 未绑定，旧行为
	}
	seedBindGroupPlan(t, plan)
	seedBindGroupSub(t, &UserSubscription{
		Id: 7203, UserId: 103, PlanId: plan.Id,
		AmountTotal: 1000, AmountUsed: 0,
		StartTime: now - 3600, EndTime: now + 30*24*3600, Status: "active",
		BindGroup: "",
	})

	// 用 default 分组请求也应命中
	res, err := PreConsumeUserSubscription("req-legacy-1", 103, "gpt-4", 0, 50, "default")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 7203, res.UserSubscriptionId)

	sub := loadBindGroupSub(t, 7203)
	assert.EqualValues(t, 50, sub.AmountUsed, "未绑定分组的套餐应对任意分组生效")
}

// 场景 4：用户同时持有两个套餐（一个绑 vip，一个未绑定），优先匹配到绑 vip 的那个
// （因按 end_time asc 排序，让绑定的先到期以排在前面）。
func TestPreConsumeUserSubscriptionBindGroupSelectsMatchingPlan(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	// 绑定 vip 的套餐，先到期（end_time asc 会排在前面）
	bindPlan := &SubscriptionPlan{
		Id:            7104,
		Title:         "VIP Plan",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   5000,
		BindGroup:     "vip",
	}
	seedBindGroupPlan(t, bindPlan)
	// 未绑定分组的套餐，后到期
	legacyPlan := &SubscriptionPlan{
		Id:            7105,
		Title:         "Legacy Plan",
		PriceAmount:   5,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   9999,
		BindGroup:     "",
	}
	seedBindGroupPlan(t, legacyPlan)

	earlyEnd := now + 10*24*3600
	lateEnd := now + 30*24*3600
	seedBindGroupSub(t, &UserSubscription{
		Id: 7204, UserId: 104, PlanId: bindPlan.Id,
		AmountTotal: 5000, AmountUsed: 0,
		StartTime: now - 3600, EndTime: earlyEnd, Status: "active",
		BindGroup: "vip",
	})
	seedBindGroupSub(t, &UserSubscription{
		Id: 7205, UserId: 104, PlanId: legacyPlan.Id,
		AmountTotal: 9999, AmountUsed: 0,
		StartTime: now - 3600, EndTime: lateEnd, Status: "active",
		BindGroup: "",
	})

	// 用 vip 分组请求：应命中绑 vip 的订阅（7204），而不是未绑定的 7205
	res, err := PreConsumeUserSubscription("req-select-1", 104, "gpt-4", 0, 100, "vip")
	require.NoError(t, err)
	assert.Equal(t, 7204, res.UserSubscriptionId, "应优先命中绑定 vip 的订阅")
	assert.EqualValues(t, 5000, res.AmountTotal)

	// 用 default 分组请求：vip 订阅不匹配被跳过，应命中未绑定的 7205
	res2, err2 := PreConsumeUserSubscription("req-select-2", 104, "gpt-4", 0, 100, "default")
	require.NoError(t, err2)
	assert.Equal(t, 7205, res2.UserSubscriptionId, "default 分组应命中未绑定的订阅")
	assert.EqualValues(t, 9999, res2.AmountTotal)
}

// 场景 5：绑定 vip 但 vip 订阅额度不足 → 该订阅被跳过；若没有其他可匹配订阅，返回错误（触发回退钱包）。
func TestPreConsumeUserSubscriptionBindGroupInsufficientReturnsError(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:            7106,
		Title:         "Small VIP Plan",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   100,
		BindGroup:     "vip",
	}
	seedBindGroupPlan(t, plan)
	// 已用 80，剩余 20，但请求要扣 100
	seedBindGroupSub(t, &UserSubscription{
		Id: 7206, UserId: 105, PlanId: plan.Id,
		AmountTotal: 100, AmountUsed: 80,
		StartTime: now - 3600, EndTime: now + 30*24*3600, Status: "active",
		BindGroup: "vip",
	})

	res, err := PreConsumeUserSubscription("req-insufficient-1", 105, "gpt-4", 0, 100, "vip")

	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "subscription quota insufficient")
	// 额度不应变动
	sub := loadBindGroupSub(t, 7206)
	assert.EqualValues(t, 80, sub.AmountUsed)
}

// 场景 6（边界）：usingGroup 为空 → 不做分组过滤，等同于旧行为（绑定 vip 的也能命中）。
// 防御性：万一上游 UsingGroup 为空，不应误跳过已绑定分组的订阅。
func TestPreConsumeUserSubscriptionEmptyUsingGroupDisablesFilter(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:            7107,
		Title:         "VIP Plan",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
		BindGroup:     "vip",
	}
	seedBindGroupPlan(t, plan)
	seedBindGroupSub(t, &UserSubscription{
		Id: 7207, UserId: 106, PlanId: plan.Id,
		AmountTotal: 1000, AmountUsed: 0,
		StartTime: now - 3600, EndTime: now + 30*24*3600, Status: "active",
		BindGroup: "vip",
	})

	// usingGroup="" 应命中（过滤条件中 usingGroup != "" 为 false，不会跳过）
	res, err := PreConsumeUserSubscription("req-empty-group-1", 106, "gpt-4", 0, 100, "")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 7207, res.UserSubscriptionId)
}

// 场景 7：幂等性 —— 同一 requestId 再次预扣，返回首次结果，不重复扣费。
// 覆盖 BindGroup 场景下的幂等记录查询路径。
func TestPreConsumeUserSubscriptionBindGroupIdempotent(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:            7108,
		Title:         "VIP Plan",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
		BindGroup:     "vip",
	}
	seedBindGroupPlan(t, plan)
	seedBindGroupSub(t, &UserSubscription{
		Id: 7208, UserId: 107, PlanId: plan.Id,
		AmountTotal: 1000, AmountUsed: 0,
		StartTime: now - 3600, EndTime: now + 30*24*3600, Status: "active",
		BindGroup: "vip",
	})

	// 第一次预扣
	res1, err1 := PreConsumeUserSubscription("req-idempotent-1", 107, "gpt-4", 0, 100, "vip")
	require.NoError(t, err1)
	assert.EqualValues(t, 100, res1.PreConsumed)

	// 同一 requestId 再次预扣 → 返回首次记录，不重复扣
	res2, err2 := PreConsumeUserSubscription("req-idempotent-1", 107, "gpt-4", 0, 100, "vip")
	require.NoError(t, err2)
	assert.Equal(t, res1.UserSubscriptionId, res2.UserSubscriptionId)
	assert.EqualValues(t, res1.PreConsumed, res2.PreConsumed)

	// 只扣了一次
	sub := loadBindGroupSub(t, 7208)
	assert.EqualValues(t, 100, sub.AmountUsed, "幂等预扣不应重复扣费")
}

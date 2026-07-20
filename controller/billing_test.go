package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// openBillingTestDB spins up an in-memory SQLite database and points the model
// package at it. Each test gets an isolated DB (shared-cache keyed by test name).
func openBillingTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	// The legacy billing branch reads token/user quota when
	// DisplayTokenStatEnabled is true; default it off so the fallback path is
	// deterministic and exercises the user-quota branch.
	common.DisplayTokenStatEnabled = false
	// Subscription-first is the production default; each test may override it.
	common.SubscriptionBalanceEnabled = true

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	model.DB = db
	model.LOG_DB = db

	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	if err := db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSubscription{}); err != nil {
		t.Fatalf("failed to migrate tables: %v", err)
	}
	return db
}

// seedUserAndToken creates a user + an (unlimited) token and returns the
// userId to attach to the gin context, mimicking what middleware.TokenAuth sets.
func seedUserAndToken(t *testing.T, db *gorm.DB, userQuota, usedQuota int) int {
	t.Helper()
	user := model.User{
		Id:        1,
		Username:  "billing-tester",
		Role:      common.RoleCommonUser,
		Quota:     userQuota,
		UsedQuota: usedQuota,
		Status:    common.UserStatusEnabled,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user.Id
}

// seedActiveSubscription inserts a single active UserSubscription for the user.
func seedActiveSubscription(t *testing.T, db *gorm.DB, userId int, amountTotal, amountUsed int64, endTime int64) {
	t.Helper()
	sub := model.UserSubscription{
		UserId:      userId,
		PlanId:      1,
		AmountTotal: amountTotal,
		AmountUsed:  amountUsed,
		StartTime:   1700000000,
		EndTime:     endTime,
		Status:      "active",
	}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}
}

// newBillingContext builds a gin test context with the userId injected, like the
// TokenAuth middleware does for these routes. Returns the recorder so the caller
// can read the response body.
func newBillingContext(userId int) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("id", userId)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	return c, w
}

// ---- Tests ----

// TestBillingGetSubscriptionWithActiveSubscription verifies that when the user
// owns an active subscription plan, /dashboard/billing/subscription reports the
// plan's total quota (hard_limit_usd) and remaining quota (soft_limit_usd),
// plus the plan's end time (access_until). This is what CC Switch reads.
func TestBillingGetSubscriptionWithActiveSubscription(t *testing.T) {
	db := openBillingTestDB(t)
	userId := seedUserAndToken(t, db, /*quota*/ 0, /*used*/ 0)

	// A $10 plan (10 * QuotaPerUnit internal quota) with $3 used.
	total := int64(10 * common.QuotaPerUnit)
	used := int64(3 * common.QuotaPerUnit)
	endTime := int64(1800000000)
	seedActiveSubscription(t, db, userId, total, used, endTime)

	c, w := newBillingContext(userId)
	GetSubscription(c)

	require.Equal(t, http.StatusOK, w.Code)

	var resp OpenAISubscriptionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, "billing_subscription", resp.Object)
	// hard_limit reflects the plan total ($10).
	assert.InDelta(t, 10.0, resp.HardLimitUSD, 0.0001)
	assert.InDelta(t, 10.0, resp.SystemHardLimitUSD, 0.0001)
	// soft_limit is the remaining amount ($10 - $3 = $7).
	assert.InDelta(t, 7.0, resp.SoftLimitUSD, 0.0001)
	// access_until reflects the plan end time.
	assert.Equal(t, endTime, resp.AccessUntil)
}

// TestBillingGetUsageWithActiveSubscription verifies that
// /dashboard/billing/usage reports the plan's used quota.
func TestBillingGetUsageWithActiveSubscription(t *testing.T) {
	db := openBillingTestDB(t)
	userId := seedUserAndToken(t, db, 0, 0)

	used := int64(3 * common.QuotaPerUnit) // $3 used
	endTime := int64(1800000000)
	seedActiveSubscription(t, db, userId, int64(10*common.QuotaPerUnit), used, endTime)

	c, w := newBillingContext(userId)
	GetUsage(c)

	require.Equal(t, http.StatusOK, w.Code)

	var resp OpenAIUsageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, "list", resp.Object)
	// total_usage is in cents (×100), so $3 -> 300.
	assert.InDelta(t, 300.0, resp.TotalUsage, 0.01)
}

// TestBillingGetSubscriptionFallbackWithoutSubscription verifies that without
// an active subscription the endpoint falls back to legacy user-quota numbers.
func TestBillingGetSubscriptionFallbackWithoutSubscription(t *testing.T) {
	db := openBillingTestDB(t)
	// user owns $5 remain + $2 used = $7 total hard limit.
	userId := seedUserAndToken(t, db, 5*int(common.QuotaPerUnit), 2*int(common.QuotaPerUnit))

	c, w := newBillingContext(userId)
	GetSubscription(c)

	require.Equal(t, http.StatusOK, w.Code)

	var resp OpenAISubscriptionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Legacy branch: hard_limit = remain + used = $7.
	assert.InDelta(t, 7.0, resp.HardLimitUSD, 0.0001)
	// Legacy branch keeps soft == hard (existing behaviour preserved).
	assert.InDelta(t, 7.0, resp.SoftLimitUSD, 0.0001)
}

// TestBillingSoftLimitNeverNegative guards against a plan whose used quota
// exceeds its total (over-spent): soft_limit must clamp to 0, not go negative.
func TestBillingSoftLimitNeverNegative(t *testing.T) {
	db := openBillingTestDB(t)
	userId := seedUserAndToken(t, db, 0, 0)

	// $5 total but $8 used -> over-spent.
	seedActiveSubscription(t, db, userId,
		int64(5*common.QuotaPerUnit),
		int64(8*common.QuotaPerUnit),
		int64(1800000000))

	c, w := newBillingContext(userId)
	GetSubscription(c)

	require.Equal(t, http.StatusOK, w.Code)

	var resp OpenAISubscriptionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.InDelta(t, 5.0, resp.HardLimitUSD, 0.0001)
	assert.GreaterOrEqual(t, resp.SoftLimitUSD, 0.0)
	assert.InDelta(t, 0.0, resp.SoftLimitUSD, 0.0001)
}

// TestBillingSubscriptionDisabledFallsBackToQuota verifies that when the
// SubscriptionBalanceEnabled switch is off, the endpoints ignore any active
// subscription and report the legacy token/user quota instead.
func TestBillingSubscriptionDisabledFallsBackToQuota(t *testing.T) {
	db := openBillingTestDB(t)
	// User owns $3 remain + $2 used = $5 legacy quota.
	userId := seedUserAndToken(t, db, 3*int(common.QuotaPerUnit), 2*int(common.QuotaPerUnit))
	// ...but also has an active subscription ($50 total / $10 used) which must
	// be ignored when the switch is off.
	seedActiveSubscription(t, db, userId,
		int64(50*common.QuotaPerUnit),
		int64(10*common.QuotaPerUnit),
		int64(1800000000))

	prev := common.SubscriptionBalanceEnabled
	common.SubscriptionBalanceEnabled = false
	t.Cleanup(func() { common.SubscriptionBalanceEnabled = prev })

	c, w := newBillingContext(userId)
	GetSubscription(c)

	require.Equal(t, http.StatusOK, w.Code)

	var subResp OpenAISubscriptionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &subResp))
	// Legacy user quota: $3 + $2 = $5, NOT the subscription's $50.
	assert.InDelta(t, 5.0, subResp.HardLimitUSD, 0.0001)

	c2, w2 := newBillingContext(userId)
	GetUsage(c2)
	require.Equal(t, http.StatusOK, w2.Code)

	var usageResp OpenAIUsageResponse
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &usageResp))
	// Legacy user used quota: $2 -> 200 cents, NOT the subscription's $10.
	assert.InDelta(t, 200.0, usageResp.TotalUsage, 0.01)
}

// TestBillingResolveActiveSubscriptionPicksLatestExpiry ensures that when a
// user has multiple active subscriptions, the longest-valid one wins — i.e. the
// one with the latest end_time (GetAllActiveUserSubscriptions orders by
// end_time desc, so [0] is the latest-expiring plan). This matches the
// intuition of showing the user's most enduring active balance.
func TestBillingResolveActiveSubscriptionPicksLatestExpiry(t *testing.T) {
	db := openBillingTestDB(t)
	userId := seedUserAndToken(t, db, 0, 0)

	sooner := int64(1800000000) // expires first
	later := int64(1900000000)  // expires last -> should be picked
	// Insert "sooner" first to prove ordering isn't just insertion order.
	seedActiveSubscription(t, db, userId, int64(10*common.QuotaPerUnit), 0, sooner)
	// Vary the plan id so both rows persist (no unique constraint, but keeps it clear).
	sub2 := model.UserSubscription{
		UserId: userId, PlanId: 2,
		AmountTotal: int64(20 * common.QuotaPerUnit),
		EndTime:     later, Status: "active",
	}
	require.NoError(t, db.Create(&sub2).Error)

	sub := resolveActiveSubscription(userId)
	require.NotNil(t, sub)
	assert.Equal(t, later, sub.EndTime, "should pick latest-expiring subscription")
	assert.Equal(t, int64(20*common.QuotaPerUnit), sub.AmountTotal)
}

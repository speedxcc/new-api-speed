package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// quotaToDisplayAmount converts an internal quota integer to the display amount
// (USD / CNY / TOKENS) using the site-wide QuotaDisplayType, matching the units
// returned by the OpenAI-compatible billing endpoints.
func quotaToDisplayAmount(quota int) float64 {
	amount := float64(quota)
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		amount = amount / common.QuotaPerUnit * operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// amount keeps raw token count
	default:
		amount = amount / common.QuotaPerUnit
	}
	return amount
}

// resolveActiveSubscription returns the user's latest-expiring active
// subscription (GetAllActiveUserSubscriptions orders by end_time desc, so [0]
// is the longest-valid plan), or nil if the user has none. It never returns an
// error so callers can treat the subscription path as purely opportunistic:
// any failure simply falls back to the legacy token/user quota numbers.
func resolveActiveSubscription(userId int) *model.UserSubscription {
	if userId <= 0 {
		return nil
	}
	subs, err := model.GetAllActiveUserSubscriptions(userId)
	if err != nil || len(subs) == 0 {
		return nil
	}
	return subs[0].Subscription
}

func GetSubscription(c *gin.Context) {
	userId := c.GetInt("id")

	// Subscription-first: when SubscriptionBalanceEnabled is on and the user
	// owns an active subscription plan, expose that plan's total quota / used
	// quota so that CC Switch (and other OpenAI-compatible clients) display the
	// subscription balance without extra configuration. Falls through to legacy
	// token/user quota otherwise.
	if common.SubscriptionBalanceEnabled {
		if sub := resolveActiveSubscription(userId); sub != nil {
			amountTotal := quotaToDisplayAmount(int(sub.AmountTotal))
			amountUsed := quotaToDisplayAmount(int(sub.AmountUsed))
			// Keep soft < hard in the OpenAI sense while letting clients read the
			// remaining quota directly from soft_limit_usd.
			remaining := amountTotal - amountUsed
			if remaining < 0 {
				remaining = 0
			}
			subscription := OpenAISubscriptionResponse{
				Object:             "billing_subscription",
				HasPaymentMethod:   true,
				SoftLimitUSD:       remaining,
				HardLimitUSD:       amountTotal,
				SystemHardLimitUSD: amountTotal,
				AccessUntil:        sub.EndTime,
			}
			c.JSON(200, subscription)
			return
		}
	}

	var remainQuota int
	var usedQuota int
	var err error
	var token *model.Token
	var expiredTime int64
	if common.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		expiredTime = token.ExpiredTime
		remainQuota = token.RemainQuota
		usedQuota = token.UsedQuota
	} else {
		remainQuota, err = model.GetUserQuota(userId, false)
		usedQuota, err = model.GetUserUsedQuota(userId)
	}
	if expiredTime <= 0 {
		expiredTime = 0
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "upstream_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	quota := remainQuota + usedQuota
	amount := float64(quota)
	// OpenAI 兼容接口中的 *_USD 字段含义保持“额度单位”对应值：
	// 我们将其解释为以“站点展示类型”为准：
	// - USD: 直接除以 QuotaPerUnit
	// - CNY: 先转 USD 再乘汇率
	// - TOKENS: 直接使用 tokens 数量
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		amount = amount / common.QuotaPerUnit * operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// amount 保持 tokens 数值
	default:
		amount = amount / common.QuotaPerUnit
	}
	if token != nil && token.UnlimitedQuota {
		amount = 100000000
	}
	subscription := OpenAISubscriptionResponse{
		Object:             "billing_subscription",
		HasPaymentMethod:   true,
		SoftLimitUSD:       amount,
		HardLimitUSD:       amount,
		SystemHardLimitUSD: amount,
		AccessUntil:        expiredTime,
	}
	c.JSON(200, subscription)
	return
}

func GetUsage(c *gin.Context) {
	userId := c.GetInt("id")

	// Subscription-first: mirror GetSubscription and report the active
	// subscription's used quota when one exists and SubscriptionBalanceEnabled
	// is on.
	if common.SubscriptionBalanceEnabled {
		if sub := resolveActiveSubscription(userId); sub != nil {
			usage := OpenAIUsageResponse{
				Object:     "list",
				TotalUsage: quotaToDisplayAmount(int(sub.AmountUsed)) * 100,
			}
			c.JSON(200, usage)
			return
		}
	}

	var quota int
	var err error
	var token *model.Token
	if common.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		quota = token.UsedQuota
	} else {
		userId := c.GetInt("id")
		quota, err = model.GetUserUsedQuota(userId)
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "new_api_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	amount := float64(quota)
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		amount = amount / common.QuotaPerUnit * operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// tokens 保持原值
	default:
		amount = amount / common.QuotaPerUnit
	}
	usage := OpenAIUsageResponse{
		Object:     "list",
		TotalUsage: amount * 100,
	}
	c.JSON(200, usage)
	return
}

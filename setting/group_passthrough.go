package setting

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// groupPassThrough 记录开启了"上游错误直接透传(不重试)"的分组。
// key=分组名,value=true 表示该分组的上游 API 错误直接透传给客户端、不再重试。
// 未出现的分组默认走正常重试逻辑（兼容旧行为）。
// 典型场景：GLM 的 421 五小时限制、GPT 的某些限流，重试无意义，直接透传即可。
var groupPassThrough = map[string]bool{}

// IsGroupPassThrough 返回该分组是否开启了错误透传（不重试）。
func IsGroupPassThrough(group string) bool {
	return groupPassThrough[group]
}

func UpdateGroupPassThroughByJsonString(jsonString string) error {
	groupPassThrough = make(map[string]bool)
	if strings.TrimSpace(jsonString) == "" {
		return nil
	}
	return common.Unmarshal([]byte(jsonString), &groupPassThrough)
}

func GroupPassThrough2JsonString() string {
	jsonBytes, err := common.Marshal(groupPassThrough)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

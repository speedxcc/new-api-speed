package model

import "testing"

func TestCacheWriteTokensFromOther(t *testing.T) {
	tests := []struct {
		name  string
		other logUsageOtherFields
		want  int
	}{
		{
			name:  "cache_write_tokens wins when present",
			other: logUsageOtherFields{CacheWriteTokens: 100, CacheCreationTokens: 80},
			want:  100,
		},
		{
			name:  "falls back to cache_creation_tokens",
			other: logUsageOtherFields{CacheCreationTokens: 50},
			want:  50,
		},
		{
			name: "split 5m/1h sums when larger",
			other: logUsageOtherFields{
				CacheCreationTokens:   30,
				CacheCreationTokens5m: 20,
				CacheCreationTokens1h: 30,
			},
			want: 50,
		},
		{
			name: "creation total wins when larger than split",
			other: logUsageOtherFields{
				CacheCreationTokens:   90,
				CacheCreationTokens5m: 20,
				CacheCreationTokens1h: 30,
			},
			want: 90,
		},
		{
			name:  "no cache write fields",
			other: logUsageOtherFields{},
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cacheWriteTokensFromOther(tt.other); got != tt.want {
				t.Errorf("cacheWriteTokensFromOther() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAddLogUsageRow(t *testing.T) {
	tests := []struct {
		name       string
		prompt     int
		completion int
		otherRaw   string
		wantTotal  int64
		wantCache  int64
	}{
		{
			name:       "openai semantic: prompt already includes cached tokens",
			prompt:     1000,
			completion: 200,
			otherRaw:   `{"cache_tokens":800,"cache_ratio":0.1}`,
			wantTotal:  1200,
			wantCache:  800,
		},
		{
			name:       "anthropic semantic: cache read and write added back",
			prompt:     1000,
			completion: 200,
			otherRaw:   `{"usage_semantic":"anthropic","cache_tokens":5000,"cache_write_tokens":2000}`,
			wantTotal:  8200,
			wantCache:  5000,
		},
		{
			name:       "anthropic via billing path marker",
			prompt:     100,
			completion: 0,
			otherRaw:   `{"admin_info":{"usage_billing_path":"billing-usage-anthropic"},"cache_tokens":300,"cache_creation_tokens":100}`,
			wantTotal:  500,
			wantCache:  300,
		},
		{
			name:       "input_tokens_total takes precedence",
			prompt:     1000,
			completion: 200,
			otherRaw:   `{"input_tokens_total":9000,"usage_semantic":"anthropic","cache_tokens":5000}`,
			wantTotal:  9200,
			wantCache:  5000,
		},
		{
			name:       "no other: base only",
			prompt:     100,
			completion: 50,
			otherRaw:   "",
			wantTotal:  150,
			wantCache:  0,
		},
		{
			name:       "invalid json: base only",
			prompt:     100,
			completion: 50,
			otherRaw:   `{invalid`,
			wantTotal:  150,
			wantCache:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stat := &TokenUsageStat{}
			stat.addLogUsageRow(tt.prompt, tt.completion, tt.otherRaw)
			if stat.TotalTokens != int64(tt.prompt+tt.completion) {
				t.Errorf("TotalTokens = %v, want %v", stat.TotalTokens, tt.prompt+tt.completion)
			}
			if stat.TotalTokensInclCache != tt.wantTotal {
				t.Errorf("TotalTokensInclCache = %v, want %v", stat.TotalTokensInclCache, tt.wantTotal)
			}
			if stat.CacheReadTokens != tt.wantCache {
				t.Errorf("CacheReadTokens = %v, want %v", stat.CacheReadTokens, tt.wantCache)
			}
		})
	}
}

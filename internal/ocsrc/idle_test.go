package ocsrc

import (
	"testing"
	"time"
)

// 產品預設值必須符合 spec 的「45 秒內偵測無聲斷線」：不超過 45 秒，
// 且容得下至少兩個 15 秒心跳間隔（避免一次延遲就誤判）。
func TestDefaultIdleTimeoutMeetsThe45SecondTarget(t *testing.T) {
	const heartbeat = 15 * time.Second
	if DefaultIdleTimeout > 45*time.Second {
		t.Fatalf("預設無聲逾時 %v 超過 45 秒目標", DefaultIdleTimeout)
	}
	if DefaultIdleTimeout < 2*heartbeat {
		t.Fatalf("預設無聲逾時 %v 容不下兩個心跳間隔", DefaultIdleTimeout)
	}
}

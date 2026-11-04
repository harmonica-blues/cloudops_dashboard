package main

import "testing"

func TestWAFWebACLARN(t *testing.T) {
	cfg := WAFConfig{WebACLID: "abc-123", WebACLName: "webacl", Region: "ap-northeast-1"}
	if got, want := wafWebACLARN(cfg, "111122223333"), "arn:aws:wafv2:us-east-1:111122223333:global/webacl/webacl/abc-123"; got != want {
		t.Fatalf("CloudFront ARN = %s, want %s", got, want)
	}
	cfg.Scope = "regional"
	if got, want := wafWebACLARN(cfg, "111122223333"), "arn:aws:wafv2:ap-northeast-1:111122223333:regional/webacl/webacl/abc-123"; got != want {
		t.Fatalf("regional ARN = %s, want %s", got, want)
	}
}

func TestWAFLabels(t *testing.T) {
	for action, want := range map[string]string{"BLOCK": "ブロック", "ALLOW": "許可", "EXCLUDED_AS_COUNT": "カウント", "CAPTCHA": "CAPTCHA", "OTHER": "OTHER"} {
		if got := wafActionLabel(action); got != want {
			t.Fatalf("wafActionLabel(%s) = %s, want %s", action, got, want)
		}
	}
	if got := wafRuleLabel("AWS-AWSManagedRulesCommonRuleSet", "SizeRestrictions_BODY"); got != "AWS-AWSManagedRulesCommonRuleSet / SizeRestrictions_BODY" {
		t.Fatalf("rule group label = %s", got)
	}
	if got := wafRuleLabel(wafDefaultActionRule, ""); got != "既定のアクション" {
		t.Fatalf("default action label = %s", got)
	}
}

func TestSortEventsKeepsNewestFirst(t *testing.T) {
	events := sortEvents([]WAFEvent{{Time: "2026-10-28T01:00:00Z"}, {Time: "2026-10-28T03:00:00Z"}, {Time: "2026-10-28T02:00:00Z"}}, 2)
	if len(events) != 2 || events[0].Time != "2026-10-28T03:00:00Z" || events[1].Time != "2026-10-28T02:00:00Z" {
		t.Fatalf("unexpected order: %+v", events)
	}
}

func TestRankRuleCounts(t *testing.T) {
	ranked := rankRuleCounts([]WAFRuleCount{{Rule: "a", Blocked: 5}, {Rule: "b", Blocked: 0}, {Rule: "c", Blocked: 20}})
	if len(ranked) != 2 || ranked[0].Rule != "c" || ranked[1].Rule != "a" {
		t.Fatalf("unexpected ranking: %+v", ranked)
	}
}

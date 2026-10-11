package engine

import "testing"

func TestDeriveKeyPrecedence(t *testing.T) {
	home := "/Users/t"
	cases := []struct {
		name string
		it   Item
		want string
	}{
		{"arg wins", Item{Arg: "AAA-111", Path: "/Users/t/x", Label: "iPhone"}, "AAA-111"},
		{"path tilde", Item{Path: "/Users/t/Library/Caches/go-build"}, "~/Library/Caches/go-build"},
		{"path outside home stays absolute", Item{Path: "/Library/Updates"}, "/Library/Updates"},
		{"path equal to home", Item{Path: "/Users/t"}, "~"},
		{"label fallback", Item{Label: "com.apple.TimeMachine.x.local"}, "com.apple.TimeMachine.x.local"},
		{"anonymous", Item{}, ""},
	}
	for _, c := range cases {
		if got := c.it.DeriveKey(home); got != c.want {
			t.Errorf("%s: DeriveKey = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestItemIDRoundTrip(t *testing.T) {
	id := ItemID("xcode-archives", "~/Library/Developer/Xcode/Archives/2026/App.xcarchive")
	rule, key, isItem := SplitItemID(id)
	if !isItem || rule != "xcode-archives" || key != "~/Library/Developer/Xcode/Archives/2026/App.xcarchive" {
		t.Fatalf("round trip broke: %q %q %v", rule, key, isItem)
	}
	rule, _, isItem = SplitItemID("go-build-cache")
	if isItem || rule != "go-build-cache" {
		t.Fatalf("bare rule atom misparsed: %q %v", rule, isItem)
	}
}

func TestFillItemKeys(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "r"},
		Items: []Item{
			{Arg: "UDID-1"},
			{Path: "/Users/t/a"},
			{}, // anonymous → positional
			{Key: "preset"},
		},
	}
	f.FillItemKeys("/Users/t")
	want := []string{"UDID-1", "~/a", "#3", "preset"}
	for i, w := range want {
		if f.Items[i].Key != w {
			t.Errorf("item %d key = %q, want %q", i, f.Items[i].Key, w)
		}
	}
}

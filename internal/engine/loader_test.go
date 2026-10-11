package engine

import (
	"strings"
	"testing"
	"testing/fstest"
)

func ruleYAML(id string) string {
	return `
id: ` + id + `
title: Test rule
category: dev-caches
risk: safe
paths:
  darwin:
    - ~/Library/Caches/` + id + `
regen:
  story: comes back
  cost: none
`
}

func TestLoadFS(t *testing.T) {
	fsys := fstest.MapFS{
		"b-rule.yaml": {Data: []byte(ruleYAML("b-rule"))},
		"a-rule.yaml": {Data: []byte(ruleYAML("a-rule"))},
		"README.md":   {Data: []byte("ignored")},
	}
	catalog, err := LoadFS(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 || catalog[0].ID != "a-rule" || catalog[1].ID != "b-rule" {
		t.Fatalf("catalog wrong: %+v", catalog)
	}
}

func TestLoadFSRejectsDuplicateIDs(t *testing.T) {
	fsys := fstest.MapFS{
		"one.yaml": {Data: []byte(ruleYAML("same-id"))},
		"two.yaml": {Data: []byte(ruleYAML("same-id"))},
	}
	_, err := LoadFS(fsys)
	if err == nil || !strings.Contains(err.Error(), "duplicate rule id") {
		t.Fatalf("want duplicate-id error, got %v", err)
	}
}

func TestLoadFSRejectsUnknownFields(t *testing.T) {
	fsys := fstest.MapFS{
		"typo.yaml": {Data: []byte(ruleYAML("typo-rule") + "native_comand: oops\n")},
	}
	_, err := LoadFS(fsys)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want strict-decode error, got %v", err)
	}
}

func TestLoadFSRejectsInvalidRule(t *testing.T) {
	fsys := fstest.MapFS{
		"bad.yaml": {Data: []byte("id: bad\ntitle: Bad\ncategory: x\nrisk: nope\npaths:\n  darwin: [~/x]\n")},
	}
	if _, err := LoadFS(fsys); err == nil {
		t.Fatal("want validation error")
	}
}

// TestEmbeddedCatalogLoads is the guard that keeps every shipped rule
// parseable and valid: a bad community PR fails here.
func TestEmbeddedCatalogLoads(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) == 0 {
		t.Fatal("embedded catalog is empty")
	}
}

func TestWithoutBeta(t *testing.T) {
	catalog := []Rule{{ID: "a"}, {ID: "b", Beta: true}, {ID: "c"}}
	got := WithoutBeta(catalog)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("WithoutBeta = %+v", got)
	}
}

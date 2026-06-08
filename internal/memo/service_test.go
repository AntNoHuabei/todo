package memo

import (
	"path/filepath"
	"testing"
	"time"
)

func TestServiceCreateSearchUpdateDelete(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := NewStore(filepath.Join(t.TempDir(), "memos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, loc)
	svc.SetNow(func() time.Time { return time.Date(2026, 6, 8, 10, 0, 0, 0, loc) })

	item, err := svc.Create(CreateInput{
		Title:   "Phison 项目资料",
		Content: "项目地址是 https://example.com/phison ，账号说明见页面。",
		Tags:    []string{"Phison", "项目"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.ID == "" || item.Status != StatusActive {
		t.Fatalf("unexpected created memo: %#v", item)
	}
	if len(item.Links) != 1 || item.Links[0] != "https://example.com/phison" {
		t.Fatalf("link was not extracted: %#v", item.Links)
	}

	reopened, err := NewStore(filepath.Join(filepath.Dir(store.path), "memos.json"))
	if err != nil {
		t.Fatal(err)
	}
	reopenedSvc := NewService(reopened, loc)
	got, err := reopenedSvc.List(Filter{Query: "phison链接", Tags: []string{"项目"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Phison 项目资料" {
		t.Fatalf("unexpected search result: %#v", got)
	}

	content := "新链接 https://example.com/phison/v2"
	updated, err := reopenedSvc.Update(item.ID[:6], UpdateInput{Content: &content})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Links) != 2 {
		t.Fatalf("update did not merge extracted links: %#v", updated.Links)
	}

	if _, err := reopenedSvc.Delete(item.ID[:6]); err != nil {
		t.Fatal(err)
	}
	active, err := reopenedSvc.List(Filter{Query: "phison"})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("deleted memo should be hidden: %#v", active)
	}
	all, err := reopenedSvc.List(Filter{Query: "phison", IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Status != StatusDeleted {
		t.Fatalf("deleted memo should be visible with IncludeDeleted: %#v", all)
	}
}

package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestBlogPosts is #121: staff write, schedule, edit and delete posts on
// the LAN; the site shows due posts only; a scheduled post goes out with a
// republish by itself, once.
func TestBlogPosts(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	ctx := context.Background()
	desk := webSignIn(t, h, pool, seed.OfficeEmail, seed.OfficePassword)
	var tenant uuid.UUID
	_ = pool.QueryRow(ctx, `select id from tenants where name = $1`, seed.DemoTenant).Scan(&tenant)

	post := func(title string, at time.Time, draft bool) string {
		t.Helper()
		code, out := desk.do("POST", "/site/posts", map[string]any{"title": title, "body": "Text for " + title,
			"publish_at": at.Format(time.RFC3339), "draft": draft})
		if code != http.StatusCreated {
			t.Fatalf("post %q: %d %v", title, code, out)
		}
		return out["id"].(string)
	}
	now := time.Now()
	live := post("Flu shots are here", now.Add(-time.Hour), false)
	post("Holiday hours", now.Add(2*time.Hour), false) // scheduled
	post("Half-written idea", now.Add(-time.Hour), true)

	// Validation and access.
	if code, _ := desk.do("POST", "/site/posts", map[string]any{"title": "", "body": "x"}); code != http.StatusBadRequest {
		t.Errorf("empty title: %d, want 400", code)
	}
	if code, _ := desk.do("POST", "/site/posts", map[string]any{"title": "x", "body": "x", "publish_at": "tomorrow"}); code != http.StatusBadRequest {
		t.Errorf("bad date: %d, want 400", code)
	}
	relay := &client{t: t, h: h, relay: true, cookies: desk.cookies, csrf: desk.csrf}
	if code, _ := relay.do("GET", "/site/posts", nil); code != http.StatusNotFound {
		t.Errorf("posts through the Relay: %d, want 404", code)
	}
	code, out := desk.do("GET", "/site/posts", nil)
	status := map[string]string{}
	for _, p := range out["posts"].([]any) {
		p := p.(map[string]any)
		status[p["title"].(string)] = p["status"].(string)
	}
	if code != http.StatusOK || status["Flu shots are here"] != "live" || status["Holiday hours"] != "scheduled" || status["Half-written idea"] != "draft" {
		t.Errorf("list: %d %v", code, status)
	}

	// The site shows only what is due.
	text, names := bundleText(t, s, tenant)
	if !strings.Contains(text, "Flu shots are here") || strings.Contains(text, "Holiday hours") || strings.Contains(text, "Half-written") {
		t.Error("site shows a scheduled or draft post, or misses the live one")
	}
	if !strings.Contains(strings.Join(names, " "), "blog.html") {
		t.Errorf("no blog page: %v", names)
	}

	// Edit and delete, audited.
	if code, _ := desk.do("PUT", "/site/posts/"+live, map[string]any{"title": "Flu shots are in", "body": "Walk in any weekday.",
		"publish_at": now.Add(-time.Hour).Format(time.RFC3339)}); code != http.StatusOK {
		t.Errorf("edit: %d", code)
	}
	if code, _ := desk.do("PUT", "/site/posts/"+uuid.NewString(), map[string]any{"title": "x", "body": "x"}); code != http.StatusNotFound {
		t.Errorf("edit unknown post: %d, want 404", code)
	}
	var audited int
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where action like 'site_post.%'`).Scan(&audited)
	if audited != 4 {
		t.Errorf("%d site_post events, want 4", audited)
	}

	// Scheduled publishing: nothing before the site was first published.
	fsp := &fakeSitePublisher{}
	s.SetSitePublisher(fsp)
	comeDue := func() { // time passes: the scheduled post's moment arrives
		t.Helper()
		if _, err := pool.Exec(ctx, `update site_posts set publish_at = coalesce(
			(select published_at from site_profiles where tenant_id = site_posts.tenant_id), now()) + interval '1 millisecond'
			where title = 'Holiday hours'`); err != nil {
			t.Fatal(err)
		}
	}
	comeDue()
	if n, err := s.publishDuePosts(ctx, time.Now()); err != nil || n != 0 {
		t.Errorf("never-published site republished: %d %v", n, err)
	}
	if _, err := pool.Exec(ctx, `update site_posts set publish_at = now() + interval '2 hours' where title = 'Holiday hours'`); err != nil {
		t.Fatal(err)
	}
	if code, _ := desk.do("POST", "/site/publish", nil); code != http.StatusOK {
		t.Fatalf("publish: %d", code)
	}
	if n, _ := s.publishDuePosts(ctx, time.Now()); n != 0 {
		t.Errorf("republished with nothing newly due: %d", n)
	}
	comeDue()
	if n, err := s.publishDuePosts(ctx, time.Now().Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("scheduled post due: republished %d (%v), want 1", n, err)
	}
	if !strings.Contains(untar(t, fsp.bundles[len(fsp.bundles)-1])["blog.html"], "Holiday hours") {
		t.Error("the scheduled publish lacks the post that came due")
	}
	if n, _ := s.publishDuePosts(ctx, time.Now()); n != 0 {
		t.Errorf("republished twice for the same post: %d", n)
	}
	var scheduled int
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where action = 'site.publish' and actor_id is null`).Scan(&scheduled)
	if scheduled != 1 {
		t.Errorf("%d scheduled publishes audited, want 1", scheduled)
	}
}

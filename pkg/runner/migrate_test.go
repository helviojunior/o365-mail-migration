package runner

import "testing"

func TestMatchPath(t *testing.T) {
	cases := []struct {
		name      string
		lowerPath string
		filter    string
		want      bool
	}{
		{"exact", "inbox", "Inbox", true},
		{"child", "inbox/projects", "inbox", true},
		{"deep child", "inbox/projects/2024", "Inbox/Projects", true},
		{"parent is not child", "inbox", "inbox/projects", false},
		{"prefix is not a segment", "archive", "arch", false},
		{"sibling", "junk email", "inbox", false},
		{"trailing slash tolerated", "inbox/projects", "inbox/", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchPath(c.lowerPath, c.filter); got != c.want {
				t.Fatalf("matchPath(%q, %q) = %v, want %v", c.lowerPath, c.filter, got, c.want)
			}
		})
	}
}

func TestFolderSelected(t *testing.T) {
	newMig := func(include, exclude []string) *Migrator {
		m := &Migrator{}
		m.opts.Migrate.IncludeFolders = include
		m.opts.Migrate.ExcludeFolders = exclude
		return m
	}

	t.Run("no filters selects everything", func(t *testing.T) {
		m := newMig(nil, nil)
		if !m.folderSelected("Inbox/Anything") {
			t.Fatal("expected folder to be selected when no filters are set")
		}
	})

	t.Run("include limits to matching paths", func(t *testing.T) {
		m := newMig([]string{"Inbox", "Archive"}, nil)
		if !m.folderSelected("Inbox/Projects") {
			t.Fatal("Inbox/Projects should match include 'Inbox'")
		}
		if m.folderSelected("Sent Items") {
			t.Fatal("Sent Items should not match includes")
		}
	})

	t.Run("exclude wins over include", func(t *testing.T) {
		m := newMig([]string{"Inbox"}, []string{"Inbox/Spam"})
		if m.folderSelected("Inbox/Spam") {
			t.Fatal("excluded folder must not be selected")
		}
		if !m.folderSelected("Inbox/Keep") {
			t.Fatal("non-excluded child of Inbox should still be selected")
		}
	})
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Fatalf("short string changed: %q", got)
	}
	if got := truncate("hello world", 5); got != "hello" {
		t.Fatalf("truncate = %q, want %q", got, "hello")
	}
	if got := truncate("", 5); got != "" {
		t.Fatalf("empty string changed: %q", got)
	}
}

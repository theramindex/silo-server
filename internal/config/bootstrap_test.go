package config

import "testing"

func TestLoadDatabaseURLNeedsNoSecretKey(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/silo")
	t.Setenv("SECRET_KEY", "")
	url, err := LoadDatabaseURL("")
	if err != nil || url != "postgres://example.invalid/silo" {
		t.Fatalf("LoadDatabaseURL = %q, %v", url, err)
	}
	// The server itself still refuses to start without the key.
	if _, err := LoadBootstrap(""); err == nil {
		t.Fatal("LoadBootstrap accepted a missing SECRET_KEY")
	}

	t.Setenv("DATABASE_URL", "")
	if _, err := LoadDatabaseURL(""); err == nil {
		t.Fatal("LoadDatabaseURL accepted a missing DATABASE_URL")
	}
}

func TestLoadBootstrapReadsPreferredPathPrefixes(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/silo")
	t.Setenv("SECRET_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("SILO_PREFERRED_PATH_PREFIXES", "/mnt/ceph,/mnt/cloud")

	bc, err := LoadBootstrap("")
	if err != nil {
		t.Fatalf("LoadBootstrap: %v", err)
	}
	if bc.PreferredPathPrefixes != "/mnt/ceph,/mnt/cloud" {
		t.Fatalf("PreferredPathPrefixes = %q", bc.PreferredPathPrefixes)
	}

	t.Setenv("SILO_PREFERRED_PATH_PREFIXES", "")
	bc, err = LoadBootstrap("")
	if err != nil {
		t.Fatalf("LoadBootstrap: %v", err)
	}
	if bc.PreferredPathPrefixes != "" {
		t.Fatalf("PreferredPathPrefixes = %q, want empty when unset", bc.PreferredPathPrefixes)
	}
}

func TestAudiobookshelfCompatWithBootstrapListen(t *testing.T) {
	cases := []struct {
		name, listen, bootstrap, want string
	}{
		{"ABS_PORT moves an enabled listener", ":13378", ":13379", ":13379"},
		{"a disabled listener stays disabled", "", ":13379", ""},
		{"no bootstrap address keeps the listener", ":13378", "", ":13378"},
	}
	for _, tc := range cases {
		got := AudiobookshelfCompatConfig{Listen: tc.listen}.WithBootstrapListen(tc.bootstrap)
		if got.Listen != tc.want {
			t.Errorf("%s: Listen = %q, want %q", tc.name, got.Listen, tc.want)
		}
	}
}
